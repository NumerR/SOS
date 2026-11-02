package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/NumerR/SOS/internal/apeks"
	"github.com/NumerR/SOS/internal/models"
	"github.com/NumerR/SOS/internal/store"
)

// Массовый импорт расписания: все группы каталога за выбранный период
// (запрос владельца — «парситься должны все группы и все даты»).
//
// Одиночный импорт /api/schedule/source/lessons качает одну группу. Для кнопки
// «импортировать это» это верно, но из 588 групп каталога расписание имели
// ровно 1 группа: чтобы показать другую, админ обязан был выбрать её и нажать
// импорт вручную — и так 588 раз.
//
// Почему задача фоновая, а не один долгий запрос: 588 групп × месяц — это 588
// обращений к чужому сайту, десятки минут. Держать под ними HTTP-соединение
// нельзя: браузер рвёт его раньше, а обрыв вкладки оборвал бы импорт на
// середине, и узнать, что именно уже лежит, было бы нечем. Поэтому POST только
// запускает задачу и отвечает сразу, а прогресс читается отдельным запросом.
//
// Один поток, а не несколько: источник — чужой сайт вуза, и пять параллельных
// соединений ради скорости были бы стуком в дверь, которую открыли под ручное
// чтение. Месяцы одной группы и так шли последовательно (ScheduleImportAPI),
// та же дисциплина здесь расширяется на каталог.
//
// Идемпотентность: шаг задачи — store.ReplaceMonth, он заменяет пакет
// (источник, группа, месяц) целиком по ключу (source, group_ext_id,
// external_id). Повторный запуск дозаполняет пропуски и не плодит дублей,
// поэтому прерванную задачу не «докачивают», а запускают заново.
//
// Что живёт только в памяти процесса: сама задача и её прогресс. Данные лежат
// в базе, и перезапуск сервера импорт не теряет — он теряет строку прогресса,
// и панель показывает «задача не запускалась», пока её снова не запустят.

// errScheduleImportStore — месяц разобран, но не записан.
//
// Разделяет две причины отказа, отвечающие по-разному: источник не ответил
// (502, вина сети и чужого сайта) и база не приняла строки (500, наша вина).
// Без метки обработчик одиночного импорта не отличил бы одно от другого, а
// массовая задача не могла бы решить «пропустить группу» вместо «уронить
// весь каталог».
var errScheduleImportStore = errors.New("месяц не записан в базу")

// scheduleImportDeadline — потолок одной проходки каталога.
//
// Число следует из размера прогона, а не из «красивой» цифры: 588 групп по
// месяцу при замеренных ~1 с на ответ — около 10 минут, три месяца — около 30.
// Запас нужен не для скорости, а чтобы висящий источник не оставил задачу в
// состоянии «выполняется» до самого перезапуска сервера.
const scheduleImportDeadline = 90 * time.Minute

// scheduleImportJob — состояние фоновой задачи.
//
// Счётчики пишет goroutine проходки, читает их HTTP-обработчик прогресса на
// каждом опросе панели, поэтому всё под одним мьютексом.
type scheduleImportJob struct {
	mu sync.Mutex

	actor      string
	startedAt  int64
	finishedAt int64
	months     []scheduleMonth
	groups     int
	total      int
	done       int
	lessons    int
	failed     int
	current    string
	failedLast string
}

// scheduleImportView — снимок задачи для панели.
//
// Свободного текста ошибок в ответе нет намеренно: у Python-движка в тексте
// бывают абсолютные пути процесса (см. ADR-0023 про missing_config), и наружу
// уходит только имя группы, на которой шаг не удался. Настоящая причина — в логе.
type scheduleImportView struct {
	Running    bool            `json:"running"`
	Actor      string          `json:"actor"`
	StartedAt  int64           `json:"started_at"`
	FinishedAt int64           `json:"finished_at"`
	Months     []scheduleMonth `json:"months"`
	Groups     int             `json:"groups"`
	Total      int             `json:"steps"`
	Done       int             `json:"done"`
	Lessons    int             `json:"lessons"`
	Failed     int             `json:"failed"`
	Current    string          `json:"current"`
	FailedLast string          `json:"failed_last_group"`
}

// snapshot собирает снимок под мьютексом. Список месяцев отдаётся копией:
// браузер разбирает JSON параллельно с тем, как задача идёт по тому же срезу.
func (j *scheduleImportJob) snapshot() scheduleImportView {
	j.mu.Lock()
	defer j.mu.Unlock()

	return scheduleImportView{
		Running:    j.finishedAt == 0,
		Actor:      j.actor,
		StartedAt:  j.startedAt,
		FinishedAt: j.finishedAt,
		Months:     append([]scheduleMonth{}, j.months...),
		Groups:     j.groups,
		Total:      j.total,
		Done:       j.done,
		Lessons:    j.lessons,
		Failed:     j.failed,
		Current:    j.current,
		FailedLast: j.failedLast,
	}
}

func (j *scheduleImportJob) setCurrent(value string) {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.current = value
}

// stepDone закрывает один шаг: «месяц группы обработан, столько-то пар».
func (j *scheduleImportJob) stepDone(lessons int, failed bool, where string) {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.done++
	j.lessons += lessons
	j.current = where
	if failed {
		j.failed++
		j.failedLast = where
	}
}

func (j *scheduleImportJob) finish() {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.current = ""
	j.finishedAt = time.Now().Unix()
}

// ScheduleAllImportRequest — тело POST /api/schedule/source/import-all.
//
// Группы здесь нет: задача именно про весь каталог. Появись поле «группа»,
// у него появился бы соблазн запустить импорт одной группы в обход её id и
// покривить счётчики прогресса, а для одной группы уже существует
// /api/schedule/source/lessons.
type ScheduleAllImportRequest struct {
	Month int `json:"month"`
	Year  int `json:"year"`
	Count int `json:"count"`
}

// ScheduleImportAllAPI — POST /api/schedule/source/import-all.
//
// Запускает обход всего каталога за период {month, year, count}. Период тот же
// формат, что и у одиночного импорта: «месяцев подряд» должно означать одно и
// то же на обеих кнопках, иначе на панели появятся две шкалы.
func (h *Handlers) ScheduleImportAllAPI(w http.ResponseWriter, r *http.Request) {
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

	var req ScheduleAllImportRequest
	if !readJSON(w, r, &req) {
		return
	}

	months, err := scheduleImportMonths(req.Month, req.Year, req.Count)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	groups, err := h.ScheduleSource.ListGroups(r.Context(), apeks.Source, "", 0)
	if err != nil {
		internalError(w, r, "failed to load group catalogue for mass import", err)
		return
	}
	if len(groups) == 0 {
		// Не 503 и не тихий ноль: пустой каталог — это пропущенный шаг
		// «синхронизировать группы», и админ обязан увидеть именно это.
		writeError(w, http.StatusUnprocessableEntity,
			"каталог групп пуст: сначала синхронизируйте группы")
		return
	}

	job, started := h.startScheduleImport(user.Username, months, groups)
	if !started {
		// Второй щелчок при уже идущей задаче — не ошибка админа, а двойной
		// клик. 409 с живым прогрессом в теле: панель показывает те же цифры,
		// что и обычный опрос, и человек видит идущий импорт, а не поломку.
		h.recordAudit(r, auditWrite{
			action:     store.AuditScheduleImport,
			actor:      user,
			targetType: "schedule_source",
			detail:     fmt.Sprintf("source=%s all reason=already_running", apeks.Source),
			ok:         false,
		})

		view, _ := h.snapshotScheduleImport()
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "массовый импорт уже выполняется",
			"job":   view,
		})
		return
	}

	go h.runScheduleImport(job, groups, months)

	h.recordAudit(r, auditWrite{
		action:     store.AuditScheduleImport,
		actor:      user,
		targetType: "schedule_source",
		detail: fmt.Sprintf("source=%s all start groups=%d steps=%d months=%s",
			apeks.Source, len(groups), len(groups)*len(months), scheduleMonthSpan(months)),
		ok: true,
	})
	logScheduleImportStart(user, groups, months)

	writeJSON(w, http.StatusAccepted, map[string]any{
		"source": apeks.Source,
		"job":    job.snapshot(),
	})
}

// ScheduleImportAllStatusAPI — GET /api/schedule/source/import-all.
//
// Прогресс задачи. Пустое состояние — не 404: «массовый импорт ещё не
// запускался» — рабочее состояние источника, а не ошибка запроса, и панель
// обязана уметь его показать без разбора кода.
func (h *Handlers) ScheduleImportAllStatusAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if h.ScheduleSource == nil {
		writeError(w, http.StatusServiceUnavailable, "schedule source storage is not connected")
		return
	}

	view, found := h.snapshotScheduleImport()
	if !found {
		view = scheduleImportView{Months: []scheduleMonth{}}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"source": apeks.Source,
		"job":    view,
	})
}

// startScheduleImport атомарно решает, можно ли запустить задачу, и заводит её.
//
// Отдельной функцией из-за гонки двух щелчков: проверка «уже выполняется» и
// запись нового состояния обязаны быть одним шагом под той же блокировкой,
// которой читается прогресс. Без общей блокировки два запроса стартовали бы
// одновременно и качали бы каталог дважды.
func (h *Handlers) startScheduleImport(actor string, months []scheduleMonth,
	groups []store.SourceGroup) (*scheduleImportJob, bool) {
	h.scheduleImportMu.Lock()
	defer h.scheduleImportMu.Unlock()

	if previous := h.scheduleImport; previous != nil && previous.snapshot().Running {
		return previous, false
	}

	job := &scheduleImportJob{
		actor:     actor,
		startedAt: time.Now().Unix(),
		months:    append([]scheduleMonth{}, months...),
		groups:    len(groups),
		total:     len(groups) * len(months),
	}
	h.scheduleImport = job

	return job, true
}

// snapshotScheduleImport читает текущую задачу; false — задач не было вовсе.
func (h *Handlers) snapshotScheduleImport() (scheduleImportView, bool) {
	h.scheduleImportMu.Lock()
	defer h.scheduleImportMu.Unlock()

	if h.scheduleImport == nil {
		return scheduleImportView{}, false
	}
	return h.scheduleImport.snapshot(), true
}

// runScheduleImport обходит каталог и пишет прогресс в задачу.
//
// Контекст отвязан от запроса-инициатора: POST отвечает сразу, и его ctx был бы
// снят в ту же секунду — задача умерла бы на первом шаге.
func (h *Handlers) runScheduleImport(job *scheduleImportJob, groups []store.SourceGroup, months []scheduleMonth) {
	ctx, cancel := context.WithTimeout(context.Background(), scheduleImportDeadline)
	defer cancel()
	defer job.finish()
	defer func() { logScheduleImportFinish(job.snapshot()) }()

	for _, group := range groups {
		for _, slot := range months {
			where := fmt.Sprintf("%s · %02d.%d", group.Name, slot.Month, slot.Year)
			job.setCurrent(where)

			stored, err := h.importGroupMonth(ctx, group, slot.Month, slot.Year)
			if err != nil {
				logScheduleImportStep(group, slot, err)
			}
			job.stepDone(stored, err != nil, where)
		}

		if ctx.Err() != nil {
			// Источник встал или срок проходки истёк: тянуть остальные группы
			// бессмысленно, каждая следующая ошибка будет той же самой.
			return
		}
	}
}

// importGroupMonth — один шаг импорта: запрос месяца, сборка строк, замена
// пакета в базе.
//
// Общий для одиночной кнопки (ScheduleImportAPI) и массовой задачи: две копии
// разошлись бы при первой же правке поля пары, и один импорт начал бы терять
// колонку, которую второй уже пишет. Ошибка записи помечается
// errScheduleImportStore: вызывающая сторона по метке выбирает 502 или 500, а
// массовая задача — пропустить группу вместо того, чтобы уронить весь каталог.
//
// Группа — значение, а не указатель: у одиночного импорта её даёт ResolveGroup
// (указатель), у массового — ListGroups (срез значений). Любая единая форма
// заставила бы в одном из путей либо копировать структуру, либо разыменовывать
// её на каждом шаге, а helper нужен ровно её external_id.
func (h *Handlers) importGroupMonth(ctx context.Context, group store.SourceGroup, month, year int) (int, error) {
	data, err := h.Apeks.GetMonth(ctx, group.ExternalID, month, year)
	if err != nil {
		return 0, err
	}

	rows := make([]store.SourceLesson, 0, len(data.Lessons))
	for _, lesson := range data.Lessons {
		rows = append(rows, store.SourceLesson{
			ExternalID:   lesson.ExternalID,
			GroupExtID:   group.ExternalID,
			Month:        month,
			Year:         year,
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

	stored, err := h.ScheduleSource.ReplaceMonth(ctx, apeks.Source, group.ExternalID, month, year, rows)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", errScheduleImportStore, err)
	}
	return stored, nil
}

// scheduleMonthSpan — подпись периода для аудита и лога: «11.2026+12.2026».
func scheduleMonthSpan(months []scheduleMonth) string {
	parts := make([]string, 0, len(months))
	for _, slot := range months {
		parts = append(parts, fmt.Sprintf("%02d.%d", slot.Month, slot.Year))
	}
	return strings.Join(parts, "+")
}

func logScheduleImportStart(user *models.User, groups []store.SourceGroup, months []scheduleMonth) {
	slog.Info("расписание: запущен массовый импорт",
		slog.String("user", user.Username),
		slog.Int("groups", len(groups)),
		slog.Int("steps", len(groups)*len(months)),
		slog.String("months", scheduleMonthSpan(months)),
	)
}

func logScheduleImportFinish(view scheduleImportView) {
	slog.Info("расписание: массовый импорт завершён",
		slog.Int("groups", view.Groups),
		slog.Int("steps", view.Total),
		slog.Int("done", view.Done),
		slog.Int("lessons", view.Lessons),
		slog.Int("failed", view.Failed),
		slog.String("failed_last", view.FailedLast),
	)
}

// logScheduleImportStep — настоящая причина сбоя одного месяца, только в лог.
func logScheduleImportStep(group store.SourceGroup, slot scheduleMonth, err error) {
	slog.Warn("расписание: массовый импорт пропустил группу",
		slog.String("group", group.Name),
		slog.String("external_id", group.ExternalID),
		slog.String("month", fmt.Sprintf("%02d.%d", slot.Month, slot.Year)),
		slog.Any("error", err),
	)
}
