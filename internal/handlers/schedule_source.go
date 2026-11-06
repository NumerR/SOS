package handlers

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/NumerR/SOS/internal/apeks"
	"github.com/NumerR/SOS/internal/models"
	"github.com/NumerR/SOS/internal/store"
)

// Внешний источник расписания (T4.1): чтение импортированных пар и запуск
// импорта.
//
// Разделение ролей маршрутов здесь принципиальное:
//
//   - чтение (/api/schedule/groups, /api/schedule/lessons) — публичное, как и
//     сама страница /schedule. Витрина показывает официальное расписание
//     группам и гостям, и персональные данные в этих ответах отсутствуют: только
//     предмет, время, аудитория, преподаватель. Право смотреть расписание группы
//     не зависит от того, кто смотрит;
//   - управление источником (/api/schedule/source/*) — право
//     authz.ScheduleSourceManage, то есть администратор. Здесь импорт запускается
//     человеком и частота обращений к чужому API — его решение.
//
// Что изменилось с ADR-0042 и что осталось неизменным. Публичное чтение больше
// не обязано ждать админской кнопки: если месяца группы в базе нет,
// /api/schedule/lessons приводит его сам (schedule_readthrough.go). Не изменилось
// главное — браузер токена не видит. Исходящий запрос делает сервер, он же
// дедуплицирует его по (группа, месяц), ограничивает параллельность и бюджет, а
// наружу отдаёт копию из базы и поле source_state о том, свежая ли это копия или
// архив. Ответы источника пишутся в базу разобранными (см. internal/store),
// поэтому ни здесь, ни на странице никто не пересчитывает подгруппы и не вырезает
// HTML.

// maxScheduleImportMonths — сколько месяцев за один запрос можно импортировать.
//
// Ограничение нужно не для экономии: каждый месяц — отдельный запрос к чужому
// сайту, а админ-панель стоит за обычным браузером, где случайная опечатка в
// параметре не должна превращаться в сотни обращений к источнику.
const maxScheduleImportMonths = 3

// maxScheduleGroupLimit — потолок явного limit, тот же, что у хранилища.
const maxScheduleGroupLimit = 2000

// ScheduleGroupsAPI — GET /api/schedule/groups?q=&limit=
//
// Список групп из уже синхронизированного каталога. Пустой каталог — не ошибка:
// страница тогда честно говорит «расписание ещё не импортировано».
//
// Без limit отдаётся весь каталог: страница строит из ответа datalist и
// подпись «В каталоге N групп», а каталог — это несколько сотен строк, которые
// хранилище всё равно читает целиком. Первые 200 из 588 делали подсказку
// неполной и врали о размере каталога.
func (h *Handlers) ScheduleGroupsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if h.ScheduleSource == nil {
		writeError(w, http.StatusServiceUnavailable, "schedule source storage is not connected")
		return
	}

	user := r.URL.Query().Get("q")
	// 0 — весь каталог; queryInt отдаёт fallback и на неразобранное значение.
	limit := queryInt(r, "limit", 0)

	groups, err := h.ScheduleSource.ListGroups(r.Context(), apeks.Source, user, limit)
	if err != nil {
		internalError(w, r, "failed to load group catalogue", err)
		return
	}

	syncedAt, err := h.ScheduleSource.LastGroupSync(r.Context(), apeks.Source)
	if err != nil {
		// Метка времени — украшение списка: отказывать запрос из-за неё нельзя,
		// но и врать про «обновлено только что» не будем — отдаём 0.
		requestLogger(r).Warn("расписание: время синхронизации групп не прочитано",
			slog.Any("error", err))
		syncedAt = 0
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"source":          apeks.Source,
		"source_name":     apeks.DisplayName,
		"synced_at":       syncedAt,
		"count":           len(groups),
		"groups":          groups,
		"source_page_url": apeks.SourcePageURL,
	})
}

// scheduleDayResponse — один день расписания.
type scheduleDayResponse struct {
	DayKey  int                  `json:"day_key"`
	Date    string               `json:"date"`
	Weekday string               `json:"weekday"`
	Lessons []store.SourceLesson `json:"lessons"`
}

// ScheduleLessonsAPI — GET /api/schedule/lessons?group=&from=&to=
//
// Диапазон — unix-секунды (как у ленты /api/schedule), но внутри фильтр идёт по
// дням источника (YYYYMMDD): учебный день задаёт вуз, и «сегодня» в ленте и
// «сегодня» в расписании обязаны совпадать даже если сервер живёт в UTC.
func (h *Handlers) ScheduleLessonsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if h.ScheduleSource == nil {
		writeError(w, http.StatusServiceUnavailable, "schedule source storage is not connected")
		return
	}

	groupRef := strings.TrimSpace(r.URL.Query().Get("group"))
	if groupRef == "" {
		writeFieldError(w, http.StatusUnprocessableEntity, "group", "group is required")
		return
	}

	group, err := h.ScheduleSource.ResolveGroup(r.Context(), apeks.Source, groupRef)
	if err != nil {
		h.writeScheduleSourceError(w, r, "group", err)
		return
	}

	fromKey, toKey, err := scheduleDayRange(r)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	// Покрытие читается до пар, и отказ здесь — 500, а не «пустой список»:
	// по этому же снимку решается, ходить ли в АПЭКС. Сваливать ошибку базы в
	// «расписания нет» значило бы нарушить ADR-0029 и начать слать запросы к
	// источнику по ложной причине.
	coverage, err := h.ScheduleSource.MonthCoverage(r.Context(), apeks.Source, group.ExternalID)
	if err != nil {
		internalError(w, r, "failed to read schedule coverage", err,
			slog.String("group", group.ExternalID))
		return
	}

	// Сквозное чтение (ADR-0042): месяц, о котором база ничего не знает,
	// приводится из источника этим же запросом. Отказ источника сюда не обязан
	// становиться ошибкой страницы — показывается то, что лежит, а о причине
	// говорит source_state.
	outcome := h.scheduleEnsureMonths(r, *group, fromKey, toKey, coverage)
	if outcome.fetched > 0 {
		// Снимок устарел ровно на привезённые месяцы, и перечитывается только
		// ради них: без этого подпись «обновлено» отстала бы от показанных пар.
		refreshed, err := h.ScheduleSource.MonthCoverage(r.Context(), apeks.Source, group.ExternalID)
		if err != nil {
			requestLogger(r).Warn("расписание: покрытие после закачки не перечитано",
				slog.String("group", group.ExternalID),
				slog.Any("error", err))
		} else {
			coverage = refreshed
		}
	}

	lessons, err := h.ScheduleSource.ListLessons(r.Context(), apeks.Source, group.ExternalID, fromKey, toKey)
	if err != nil {
		internalError(w, r, "failed to load schedule", err)
		return
	}

	months := scheduleImportedMonths(coverage)

	updated := scheduleLastRefresh(coverage)
	if outcome.lastFetched > updated {
		updated = outcome.lastFetched
	}

	// Что за корпус стоит в каждой строке аудитории (ADR-0045). Сбой
	// справочника здесь не ошибка страницы: пары показаны правильно, просто без
	// уточнения, что корпус 1 — это Главный. Но и молчать нельзя, поэтому
	// причина уходит в лог, а не в пустоту.
	roomBuildings := map[string]scheduleRoomBuilding{}
	if h.Places != nil {
		places, err := h.Places.List(r.Context(), false)
		if err != nil {
			requestLogger(r).Warn("расписание: справочник мест не прочитан",
				slog.String("group", group.ExternalID), slog.Any("error", err))
		} else {
			roomBuildings = scheduleRoomBuildings(lessons, places)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"source":          apeks.Source,
		"source_name":     apeks.DisplayName,
		"group_id":        group.ExternalID,
		"group_name":      group.Name,
		"department":      group.Department,
		"course":          group.Course,
		"range":           map[string]int{"from": fromKey, "to": toKey},
		"range_imported":  scheduleHasCopy(coverage, fromKey, toKey),
		"updated_at":      updated,
		"days":            groupLessonsByDay(lessons),
		"imported_months": months,
		"source_state":    outcome.sourceState(),
		// Настроен ли источник. Одного source_state для строки состояния
		// страницы не хватает: «нечего показать» бывает и когда токена нет
		// вовсе, и когда вуз не ответил, а сказать гостю «сайт вуза молчит»,
		// когда молчать ему нечем, — соврать.
		"source_configured": h.Apeks != nil,
		"source_page_url":   apeks.SourcePageURL,
		// Строка аудитории → корпус: его номер и его имя из справочника.
		// Отдельное поле, а не колонка в строке пары: пар за неделю больше, чем
		// уникальных аудиторий, и одно и то же название кочевало бы по всему
		// ответу. Пустой объект, а не null: страница читает поле до первой
		// проверки, и `null` превратился бы в ошибку на сборке без справочника.
		"room_buildings": roomBuildings,
	})
}

// scheduleRoomBuilding — чем подписать строку аудитории.
//
// Номер корпуса в ответе лежит не для красоты: по нему страница карты находит
// маркер того корпуса, куда надо перелететь, и именно по нему пары собираются
// в слои (schedule_map.go). Имя же — ровно то, что человек написал в
// справочнике: когда его нет, подписи нет, а переход остаётся возможным.
type scheduleRoomBuilding struct {
	Building string `json:"building"`
	Name     string `json:"name,omitempty"`
}

// scheduleRoomBuildings — какие аудитории этого периода называют корпус, у
// которого есть строка в справочнике.
//
// Разбор строки аудитории делает ровно тот же код, что и слой карты
// (apeks.BuildingsFromRoom): если бы подпись в расписании считала корпус своим
// правилом, две страницы одного и того же занятия разошлись бы на глазах, и
// карта стала бы отвечать за одно, а расписание — за другое.
//
// Ключом остаётся исходная строка источника, без нормализации: страница ищет
// по той строке, которую ей показали в поле room, и любое «улучшение» ключа на
// одной стороне превратилось бы в молчаливую потерю подписи на другой.
//
// Пары с нулём или двумя корпусами остаются без подписи — ровно как на карте,
// где они уходят в «не показано». Это не упущение: угадать корпус для «201»
// значило бы подписать занятие там, где его могло не быть.
func scheduleRoomBuildings(lessons []store.SourceLesson, places []store.Place) map[string]scheduleRoomBuilding {
	directory := placesByBuilding(places)
	names := make(map[string]scheduleRoomBuilding, 8)

	for _, lesson := range lessons {
		if _, seen := names[lesson.Room]; seen {
			continue
		}

		buildings := apeks.BuildingsFromRoom(lesson.Room)
		if len(buildings) != 1 {
			continue
		}

		place := directory[buildings[0]]
		if place == nil {
			continue
		}
		names[lesson.Room] = scheduleRoomBuilding{
			Building: buildings[0],
			Name:     strings.TrimSpace(place.Name),
		}
	}

	return names
}

// groupLessonsByDay собирает плоский список пар по дням.
//
// Дни берутся только из строк: пустого дня в ответе быть не должно — иначе
// интерфейс рисует «понедельник: пусто» для семи дней подряд, а это не
// расписание, а таблица-заглушка.
func groupLessonsByDay(lessons []store.SourceLesson) []scheduleDayResponse {
	days := make([]scheduleDayResponse, 0, 8)
	index := make(map[int]int, 8)

	for _, lesson := range lessons {
		at, ok := index[lesson.DayKey]
		if !ok {
			at = len(days)
			index[lesson.DayKey] = at
			days = append(days, scheduleDayResponse{
				DayKey:  lesson.DayKey,
				Date:    lesson.Date,
				Weekday: lesson.Weekday,
				Lessons: []store.SourceLesson{},
			})
		}
		days[at].Lessons = append(days[at].Lessons, lesson)
	}

	return days
}

// ScheduleStatusAPI — GET /api/schedule/source/status (право у маршрутизатора).
//
// Отдельный маршрут, а не поля в ответе списка групп: здесь цифры всего
// источника, и спрашивать их должен админ, а не каждый, кто открыл страницу.
func (h *Handlers) ScheduleStatusAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if h.ScheduleSource == nil {
		writeError(w, http.StatusServiceUnavailable, "schedule source storage is not connected")
		return
	}

	stats, err := h.ScheduleSource.Stats(r.Context(), apeks.Source)
	if err != nil {
		internalError(w, r, "failed to read source status", err)
		return
	}

	// Настроен ли источник: админская панель по этому полю показывает либо
	// кнопки импорта, либо подсказку о причине. Состояние «не настроен» —
	// не ошибка запроса, а конфигурация, поэтому оно часть ответа, а не код 503.
	//
	// engine — каким кодом разобраны привезённые пары («go» / «python»).
	// Поле нужно не панели, а человеку, который разбирается, почему у двух
	// одинаковых импортов разные строки: при двух движках это единственный
	// способ увидеть, какой из них работал.
	//
	// missing_config и hint перестали быть константами после ADR-0021: с двумя
	// движками путь «токен задан, а парсер не запускается» стал возможен, и
	// совет завести APEKS_TOKEN человеку, у которого он задан, — это не
	// подсказка, а ложный след. Значения приходят из состояния сборки сервера
	// (cmd/server.newScheduleSourceClient), а текст ошибки моста в ответ не
	// попадает: там абсолютные пути процесса.
	writeJSON(w, http.StatusOK, map[string]any{
		"source":         apeks.Source,
		"source_name":    apeks.DisplayName,
		"configured":     h.Apeks != nil,
		"engine":         scheduleSourceEngine(h.Apeks),
		"missing_config": h.scheduleSourceMissing(),
		"hint":           h.scheduleSourceHint(),
		"stats":          stats,
	})
}

// scheduleSourceMissing — имя переменной окружения, из-за которой источника
// нет. Поднявшийся источник не называет ничего: «чего не хватает» при
// работающем клиенте не бывает, и ответ `configured: true` с
// `missing_config: "APEKS_TOKEN"` — это поле, противоречащее первому (замечено
// живым прогоном с чистого клона после ADR-0023, где так и было).
//
// Пустое поле Deps при отсутствии клиента означает «причину не передали» (тест
// и любая другая сборка могут собрать Deps без неё), поэтому ответ — прежний
// общий вариант, а не пустая строка: панель обязана иметь слово всегда.
func (h *Handlers) scheduleSourceMissing() string {
	if h.Apeks != nil {
		return ""
	}
	if name := strings.TrimSpace(h.ScheduleSourceMissing); name != "" {
		return name
	}
	return "APEKS_TOKEN"
}

// scheduleSourceHint — фрагмент подсказки для панели и для текста 503, без
// точки на конце (обе стороны дописывают свою фразу). Поднявшийся источник
// подсказки не имеет в принципе: «причина, по которой импорта нет» при
// работающем клиенте — это уже не про конфигурацию.
//
// Пустая строка — «причину не передали», и тогда текст формулирует вызывающая
// сторона: панель своим прежним предложением, 503 — общим советом про
// APEKS_TOKEN.
func (h *Handlers) scheduleSourceHint() string {
	if h.Apeks != nil {
		return ""
	}
	return strings.TrimSpace(h.ScheduleSourceHint)
}

// scheduleSourceEngine — имя движка источника для админского статуса.
//
// Пустая строка при nil: «источник не настроен» — это не «движок go» и не
// «движок python», и выдумывать одно из двух значило бы показать админу
// рабочий путь там, где его нет.
func scheduleSourceEngine(client apeks.Backend) string {
	if client == nil {
		return ""
	}
	return client.Engine()
}

// ScheduleSyncGroupsAPI — POST /api/schedule/source/groups.
//
// Тянет каталог групп целиком (один запрос источника) и дополняет базу.
func (h *Handlers) ScheduleSyncGroupsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}

	user := h.currentUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if h.ScheduleSource == nil {
		writeError(w, http.StatusServiceUnavailable, "schedule source storage is not connected")
		return
	}
	if h.Apeks == nil {
		h.scheduleSourceNotConfigured(w, r, user)
		return
	}

	groups, err := h.Apeks.GetGroups(r.Context())
	if err != nil {
		h.scheduleSourceFailure(w, r, user, "группы", err)
		return
	}

	rows := make([]store.SourceGroup, 0, len(groups))
	for _, group := range groups {
		rows = append(rows, store.SourceGroup{
			ExternalID: group.ID,
			Name:       group.Name,
			Department: group.Department,
			Course:     group.Course,
		})
	}

	added, err := h.ScheduleSource.UpsertGroups(r.Context(), apeks.Source, rows)
	if err != nil {
		h.recordAudit(r, auditWrite{
			action:     store.AuditScheduleGroupsSync,
			actor:      user,
			targetType: "schedule_source",
			detail:     fmt.Sprintf("source=%s fetched=%d", apeks.Source, len(rows)),
			ok:         false,
		})
		internalError(w, r, "failed to store group catalogue", err)
		return
	}

	h.recordAudit(r, auditWrite{
		action:     store.AuditScheduleGroupsSync,
		actor:      user,
		targetType: "schedule_source",
		detail:     fmt.Sprintf("source=%s fetched=%d added=%d", apeks.Source, len(rows), added),
		ok:         true,
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"source":  apeks.Source,
		"fetched": len(rows),
		"added":   added,
	})
}

// ScheduleSourceImportRequest — тело POST /api/schedule/source/lessons.
type ScheduleImportRequest struct {
	Group string `json:"group"`
	Month int    `json:"month"`
	Year  int    `json:"year"`
	Count int    `json:"count"`
}

// ScheduleImportAPI — POST /api/schedule/source/lessons.
//
// Импортирует один месяц (или несколько подряд, count) выбранной группы:
// запрос к источнику → разбор → замена пакета в базе.
//
// Месяцы идут последовательно в одном запросе, а не параллельно: источник —
// чужой сайт, и бить по нему одновременно пятью запросами ради удобства
// админской кнопки неправильно.
func (h *Handlers) ScheduleImportAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}

	user := h.currentUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if h.ScheduleSource == nil {
		writeError(w, http.StatusServiceUnavailable, "schedule source storage is not connected")
		return
	}
	if h.Apeks == nil {
		h.scheduleSourceNotConfigured(w, r, user)
		return
	}

	var req ScheduleImportRequest
	if !readJSON(w, r, &req) {
		return
	}

	groupRef := strings.TrimSpace(req.Group)
	if groupRef == "" {
		writeFieldError(w, http.StatusUnprocessableEntity, "group", "group is required")
		return
	}

	group, err := h.ScheduleSource.ResolveGroup(r.Context(), apeks.Source, groupRef)
	if err != nil {
		h.writeScheduleSourceError(w, r, "group", err)
		return
	}

	months, err := scheduleImportMonths(req.Month, req.Year, req.Count)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	imported := make([]store.MonthState, 0, len(months))

	for _, slot := range months {
		stored, err := h.importGroupMonth(r.Context(), *group, slot.Month, slot.Year)
		if err != nil {
			label := fmt.Sprintf("пары %s %02d.%d", group.Name, slot.Month, slot.Year)
			if errors.Is(err, errScheduleImportStore) {
				// Разобранное не записано: это наша ошибка, а не отказ вуза.
				h.recordAudit(r, auditWrite{
					action:     store.AuditScheduleImport,
					actor:      user,
					targetType: "schedule_group",
					targetID:   group.ExternalID,
					detail:     fmt.Sprintf("month=%02d.%d", slot.Month, slot.Year),
					ok:         false,
				})
				internalError(w, r, "failed to store imported schedule", err,
					slog.String("group", group.ExternalID),
					slog.String("month", fmt.Sprintf("%02d.%d", slot.Month, slot.Year)))
				return
			}

			h.scheduleSourceFailure(w, r, user, label, err)
			return
		}

		at := time.Now().Unix()
		imported = append(imported, store.MonthState{
			Month:       slot.Month,
			Year:        slot.Year,
			Lessons:     stored,
			OK:          true,
			RefreshedAt: at,
			AttemptedAt: at,
		})

		h.recordAudit(r, auditWrite{
			action:     store.AuditScheduleImport,
			actor:      user,
			targetType: "schedule_group",
			targetID:   group.ExternalID,
			detail:     fmt.Sprintf("%s: %02d.%d stored=%d", group.Name, slot.Month, slot.Year, stored),
			ok:         true,
		})
	}

	total := 0
	for _, month := range imported {
		total += month.Lessons
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"source":     apeks.Source,
		"group_id":   group.ExternalID,
		"group_name": group.Name,
		"months":     imported,
		"lessons":    total,
	})
}

// scheduleImportMonths — список месяцев импорта.
//
// Нулевой month/year означает «текущий месяц»: админская кнопка «импортировать
// это» без полей даты должна работать, а требовать от человека ввести месяц —
// значит дать ему повод ввести его неправильно.
func scheduleImportMonths(month, year, count int) ([]scheduleMonth, error) {
	now := time.Now()
	if month == 0 || year == 0 {
		if month == 0 && year == 0 {
			month, year = int(now.Month()), now.Year()
		} else {
			return nil, errors.New("month and year must be set together")
		}
	}

	if month < 1 || month > 12 {
		return nil, errors.New("month must be between 1 and 12")
	}
	if year < 2000 || year > 2100 {
		return nil, errors.New("year looks wrong: expected 2000..2100")
	}
	if count <= 0 {
		count = 1
	}
	if count > maxScheduleImportMonths {
		return nil, fmt.Errorf("count must be between 1 and %d", maxScheduleImportMonths)
	}

	slots := make([]scheduleMonth, 0, count)
	cursor := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)

	for range count {
		slots = append(slots, scheduleMonth{Month: int(cursor.Month()), Year: cursor.Year()})
		cursor = cursor.AddDate(0, 1, 0)
	}

	return slots, nil
}

// scheduleMonth — один месяц периода импорта.
//
// Поля записаны с большой буквы не для стиля: список месяцев отдаётся наружу в
// снимке фоновой задачи (scheduleImportView), а encoding/json видит только
// экспортированные поля. С тех же полей одиночный импорт считает свой ответ.
type scheduleMonth struct {
	Month int `json:"month"`
	Year  int `json:"year"`
}

// scheduleDayRange читает диапазон дней из запроса — тот же период, что
// scheduleTimeRange, но в ключах источника (YYYYMMDD).
func scheduleDayRange(r *http.Request) (int, int, error) {
	start, end, err := scheduleTimeRange(r)
	if err != nil {
		return 0, 0, err
	}
	return dayKey(start), dayKey(end), nil
}

// scheduleTimeRange читает период запроса в unix-секундах [start, end]
// включительно.
//
// Помощник один на оба слоя карты: парам нужны ключи дней, событиям —
// unix-секунды (у них дата в такой форме и лежит), а человек задаёт период
// одинаково, на каком бы слое его ни открыл. Два независимых разбора означали бы
// два набора текстов отказа и две версии правила «что считается месяцем».
//
// Поддержаны два способа задать период: from/to в unix-секундах (тот же
// контракт, что у ленты /api/schedule) и month/year (страница переключает
// месяцы, и считать границы месяца на клиенте — ещё одно место, где бывает
// ошибка с длиной февраля).
func scheduleTimeRange(r *http.Request) (int64, int64, error) {
	query := r.URL.Query()

	if rawMonth, rawYear := query.Get("month"), query.Get("year"); rawMonth != "" || rawYear != "" {
		month, err := strconv.Atoi(strings.TrimSpace(rawMonth))
		if err != nil {
			return 0, 0, errors.New("month must be a number")
		}
		year, err := strconv.Atoi(strings.TrimSpace(rawYear))
		if err != nil {
			return 0, 0, errors.New("year must be a number")
		}
		if month < 1 || month > 12 || year < 2000 || year > 2100 {
			return 0, 0, errors.New("month and year are out of range")
		}

		first := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.Local)
		last := time.Date(year, time.Month(month), daysInMonth(year, time.Month(month)), 23, 59, 59, 0, time.Local)
		return first.Unix(), last.Unix(), nil
	}

	start, end := defaultWeekRange()
	if value := query.Get("from"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed <= 0 {
			return 0, 0, errors.New("from must be a unix timestamp")
		}
		start = parsed
	}
	if value := query.Get("to"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed <= 0 {
			return 0, 0, errors.New("to must be a unix timestamp")
		}
		end = parsed
	}
	if end < start {
		start, end = end, start
	}

	return start, end, nil
}

// dayKey — unix-секунда -> день источника в форме YYYYMMDD.
//
// Считается по локальной зоне сервера: пользователь смотрит на «эту неделю» в
// своём времени, и привязка к зоне сервера — то же правило, по которому лента
// уже считает начало дня (defaultWeekRange).
func dayKey(unix int64) int {
	t := time.Unix(unix, 0).Local()
	return t.Year()*10000 + int(t.Month())*100 + t.Day()
}

func daysInMonth(year int, month time.Month) int {
	// День перед первым числом следующего месяца — ровно последнее число
	// нужного, включая 29 февраля.
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func queryInt(r *http.Request, name string, fallback int) int {
	value := strings.TrimSpace(r.URL.Query().Get(name))
	if value == "" {
		return fallback
	}

	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	if parsed > maxScheduleGroupLimit {
		return maxScheduleGroupLimit
	}
	return parsed
}

// writeScheduleSourceError отвечает на ошибку поиска группы.
//
// Неоднозначность и «не найдено» — разные тексты: в первом случае админ должен
// увидеть варианты и выбрать id, во втором — понять, что каталог не
// синхронизирован.
func (h *Handlers) writeScheduleSourceError(w http.ResponseWriter, r *http.Request, field string, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeFieldError(w, http.StatusNotFound, "group",
			fmt.Sprintf("%s not found in the group catalogue: sync it first", field))
	case errors.Is(err, store.ErrAmbiguousGroup):
		// Текст собран в store и уже содержит список id: он для человека,
		// который будет выбирать группу вручную.
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	default:
		internalError(w, r, "failed to read group catalogue", err, slog.String("field", field))
	}
}

// scheduleSourceNotConfigured — источник не настроен: клиента нет, и импорта
// физически не может быть.
//
// Причину говорит сборка сервера (Deps.ScheduleSourceMissing/…Hint), а не эта
// функция: с ADR-0021 «не настроен» перестало быть синонимом пустого
// APEKS_TOKEN — токен может быть задан, а парсер недоступен, и совет завести
// токен в этом случае уводит админа ровно не туда.
func (h *Handlers) scheduleSourceNotConfigured(w http.ResponseWriter, r *http.Request, user *models.User) {
	reason := "задайте APEKS_TOKEN на сервере"
	if hint := h.scheduleSourceHint(); hint != "" {
		reason = hint
	}

	requestLogger(r).Warn("расписание: импорт запрошен при ненастроенном источнике",
		slog.String("user", user.Username),
		slog.String("missing", h.scheduleSourceMissing()),
	)

	h.recordAudit(r, auditWrite{
		action:     store.AuditScheduleImport,
		actor:      user,
		targetType: "schedule_source",
		// Имя переменной в деталях, а не только «not_configured»: журнал читают
		// через страницу /admin, и админ, который уже завёл токен и получил
		// отказ, обязан увидеть в истории ту же причину, что и в панели.
		detail: fmt.Sprintf("source=apeks reason=not_configured missing=%s", h.scheduleSourceMissing()),
		ok:     false,
	})

	writeError(w, http.StatusServiceUnavailable,
		"источник расписания не настроен: "+reason)
}

// scheduleSourceFailure — источник не ответил (сеть, 403, неизвестный формат).
//
// Текст клиенту — общий, в лог — настоящая причина. Причина разделения: ошибки
// apeks уже гарантированно без токена (см. Client.scrub), но показывать их
// браузеру смысла нет, а админу они нужны в логе процесса, где их и так видно
// по request id.
func (h *Handlers) scheduleSourceFailure(w http.ResponseWriter, r *http.Request, user *models.User, what string, err error) {
	requestLogger(r).Error("расписание: источник не ответил",
		slog.String("what", what),
		slog.String("user", user.Username),
		slog.Any("error", err),
	)

	h.recordAudit(r, auditWrite{
		action:     store.AuditScheduleImport,
		actor:      user,
		targetType: "schedule_source",
		detail:     fmt.Sprintf("source=%s what=%s", apeks.Source, what),
		ok:         false,
	})

	writeError(w, http.StatusBadGateway, fmt.Sprintf("Источник %s не ответил (%s). Проверьте APEKS_TOKEN и попробуйте позже.", apeks.DisplayName, what))
}
