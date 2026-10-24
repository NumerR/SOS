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
//     группам гостям, и персональные данные в этих ответах отсутствуют: только
//     предмет, время, аудитория, преподаватель. Право смотреть расписание группы
//     не зависит от того, кто смотрит;
//   - управление источником (/api/schedule/source/*) — право
//     authz.ScheduleSourceManage, то есть администратор. Именно эти маршруты
//     ходят к сайту вуза с токеном из окружения, и частоту обращений к чужому
//     API должен контролировать один человек, а не каждый гость страницы.
//
// Ответы источника в базу пишутся разобранными (см. internal/store), поэтому
// ни здесь, ни на странице никто не пересчитывает подгруппы и не вырезает HTML.

// maxScheduleImportMonths — сколько месяцев за один запрос можно импортировать.
//
// Ограничение нужно не для экономии: каждый месяц — отдельный запрос к чужому
// сайту, а админ-панель стоит за обычным браузером, где случайная опечатка в
// параметре не должна превращаться в сотни обращений к источнику.
const maxScheduleImportMonths = 3

// maxScheduleGroupLimit — сколько групп отдаём на страницу за раз.
const maxScheduleGroupLimit = 500

// ScheduleGroupsAPI — GET /api/schedule/groups?q=&limit=
//
// Список групп из уже синхронизированного каталога. Пустой каталог — не ошибка:
// страница тогда честно говорит «расписание ещё не импортировано».
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
	limit := queryInt(r, "limit", 200)

	groups, err := h.ScheduleSource.ListGroups(r.Context(), apeks.Source, user, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load group catalogue")
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
		writeError(w, http.StatusUnprocessableEntity, "group is required")
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

	lessons, err := h.ScheduleSource.ListLessons(r.Context(), apeks.Source, group.ExternalID, fromKey, toKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load schedule")
		return
	}

	importedAt, err := h.ScheduleSource.LastImportAt(r.Context(), apeks.Source, group.ExternalID)
	if err != nil {
		requestLogger(r).Warn("расписание: время импорта не прочитано",
			slog.String("group", group.ExternalID),
			slog.Any("error", err))
		importedAt = 0
	}

	months, err := h.ScheduleSource.ImportedMonths(r.Context(), apeks.Source, group.ExternalID)
	if err != nil {
		requestLogger(r).Warn("расписание: список импортированных месяцев не прочитан",
			slog.String("group", group.ExternalID),
			slog.Any("error", err))
		months = []store.MonthKey{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"source":          apeks.Source,
		"source_name":     apeks.DisplayName,
		"group_id":        group.ExternalID,
		"group_name":      group.Name,
		"department":      group.Department,
		"range":           map[string]int{"from": fromKey, "to": toKey},
		"updated_at":      importedAt,
		"days":            groupLessonsByDay(lessons),
		"imported_months": months,
		"source_page_url": apeks.SourcePageURL,
	})
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
		writeError(w, http.StatusInternalServerError, "failed to read source status")
		return
	}

	// Настроен ли источник: админская панель по этому полю показывает либо
	// кнопки импорта, либо подсказку про APEKS_TOKEN. Состояние «не настроен» —
	// не ошибка запроса, а конфигурация, поэтому оно часть ответа, а не код 503.
	writeJSON(w, http.StatusOK, map[string]any{
		"source":         apeks.Source,
		"source_name":    apeks.DisplayName,
		"configured":     h.Apeks != nil,
		"missing_config": "APEKS_TOKEN",
		"stats":          stats,
	})
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
		requestLogger(r).Error("расписание: каталог групп не сохранён", slog.Any("error", err))
		h.recordAudit(r, auditWrite{
			action:     store.AuditScheduleGroupsSync,
			actor:      user,
			targetType: "schedule_source",
			detail:     fmt.Sprintf("source=%s fetched=%d", apeks.Source, len(rows)),
			ok:         false,
		})
		writeError(w, http.StatusInternalServerError, "failed to store group catalogue")
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
		writeError(w, http.StatusUnprocessableEntity, "group is required")
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

	imported := make([]store.MonthKey, 0, len(months))

	for _, slot := range months {
		month, err := h.Apeks.GetMonth(r.Context(), group.ExternalID, slot.month, slot.year)
		if err != nil {
			h.scheduleSourceFailure(w, r, user, fmt.Sprintf("пары %s %02d.%d", group.Name, slot.month, slot.year), err)
			return
		}

		rows := make([]store.SourceLesson, 0, len(month.Lessons))
		for _, lesson := range month.Lessons {
			rows = append(rows, store.SourceLesson{
				ExternalID:   lesson.ExternalID,
				GroupExtID:   group.ExternalID,
				Month:        slot.month,
				Year:         slot.year,
				DayKey:       lesson.DayKey,
				Date:         lesson.Date,
				Weekday:      lesson.Weekday,
				LessonNumber: lesson.LessonNumber,
				Time:         lesson.Time,
				Start:        lesson.Start,
				End:          lesson.End,
				Group:        lesson.Group,
				Subgroup:     lesson.Subgroup,
				Discipline:   lesson.Discipline,
				Type:         lesson.Type,
				Room:         lesson.Room,
				Teacher:      lesson.Teacher,
			})
		}

		stored, err := h.ScheduleSource.ReplaceMonth(r.Context(), apeks.Source, group.ExternalID, slot.month, slot.year, rows)
		if err != nil {
			requestLogger(r).Error("расписание: импорт месяца не сохранён",
				slog.String("group", group.ExternalID),
				slog.Any("error", err))
			h.recordAudit(r, auditWrite{
				action:     store.AuditScheduleImport,
				actor:      user,
				targetType: "schedule_group",
				targetID:   group.ExternalID,
				detail:     fmt.Sprintf("month=%02d.%d parsed=%d", slot.year, slot.month, len(rows)),
				ok:         false,
			})
			writeError(w, http.StatusInternalServerError, "failed to store imported schedule")
			return
		}

		imported = append(imported, store.MonthKey{
			Month:      slot.month,
			Year:       slot.year,
			Lessons:    stored,
			ImportedAt: time.Now().Unix(),
		})

		h.recordAudit(r, auditWrite{
			action:     store.AuditScheduleImport,
			actor:      user,
			targetType: "schedule_group",
			targetID:   group.ExternalID,
			detail:     fmt.Sprintf("%s: %02d.%d parsed=%d stored=%d", group.Name, slot.month, slot.year, len(rows), stored),
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
		slots = append(slots, scheduleMonth{month: int(cursor.Month()), year: cursor.Year()})
		cursor = cursor.AddDate(0, 1, 0)
	}

	return slots, nil
}

type scheduleMonth struct {
	month int
	year  int
}

// scheduleDayRange читает диапазон дней из запроса.
//
// Поддержаны два способа задать период: from/to в unix-секундах (тот же
// контракт, что у ленты /api/schedule) и month/year (страница переключает
// месяцы, и считать границы месяца на клиенте — ещё одно место, где бывает
// ошибка с длиной февраля).
func scheduleDayRange(r *http.Request) (int, int, error) {
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

		first := year*10000 + month*100 + 1
		last := year*10000 + month*100 + daysInMonth(year, time.Month(month))
		return first, last, nil
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

	return dayKey(start), dayKey(end), nil
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
		writeError(w, http.StatusNotFound, fmt.Sprintf("%s not found in the group catalogue: sync it first", field))
	case errors.Is(err, store.ErrAmbiguousGroup):
		// Текст собран в store и уже содержит список id: он для человека,
		// который будет выбирать группу вручную.
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	default:
		requestLogger(r).Error("расписание: ошибка чтения каталога групп", slog.Any("error", err))
		writeError(w, http.StatusInternalServerError, "failed to read group catalogue")
	}
}

// scheduleSourceNotConfigured — источник не настроен (APEKS_TOKEN пуст).
func (h *Handlers) scheduleSourceNotConfigured(w http.ResponseWriter, r *http.Request, user *models.User) {
	requestLogger(r).Warn("расписание: импорт запрошен при ненастроенном источнике",
		slog.String("user", user.Username),
		slog.String("missing", "APEKS_TOKEN"),
	)

	h.recordAudit(r, auditWrite{
		action:     store.AuditScheduleImport,
		actor:      user,
		targetType: "schedule_source",
		detail:     "source=apeks reason=not_configured",
		ok:         false,
	})

	writeError(w, http.StatusServiceUnavailable,
		"источник расписания не настроен: задайте APEKS_TOKEN на сервере")
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
