package handlers

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/NumerR/SOS/internal/models"
	"github.com/NumerR/SOS/internal/store"
)

// Журнал действий (T1.2): что пишется в audit_events и как оно читается.
//
// Правило, из которого вырос весь этот файл: событие пишется в том обработчике,
// где оно произошло, а не в middleware поверх транспорта. Middleware умеет
// увидеть «POST /api/admin/users/role вернул 200», но не знает, какая роль
// была до и кто остался без сессий — а это ровно то, о чём спрашивает
// администратор.
//
// Второе правило: аудит не имеет права испортить запрос. Ошибка записи — это
// warn в лог процесса и продолжение: вход пользователя, оценка работы, смена
// роли уже состоялись в базе, и откатывать их из-за журнала абсурдно. Теряем
// строку аудита, а не доступ.

// auditWrite — одно событие в формулировке вызывающего кода.
//
// Поля разведены по именам, а не идут позиционными аргументами: перепутать
// target_type с target_id компилятор не заставит, а журнал с перепутанными
// колонками читается как правдоподобная неправда.
type auditWrite struct {
	action store.AuditAction

	// actor — тот, кто действовал. nil означает «не установлен» и в базе
	// становится NULL (неудачный вход несуществующей учётки).
	actor *models.User

	targetType string
	targetID   string

	// detail — что именно изменилось: «score=8», «from=student to=admin».
	// Паролей, хэшей и идентификаторов сессий здесь быть не должно — это
	// проверяет тест TestAuditStoresNoCredentials.
	detail string

	ok bool
}

// recordAudit пишет событие, доставая IP и User-Agent из запроса.
//
// IP берётся через ClientIP, то есть с тем же условием доверия
// X-Forwarded-For (TRUST_PROXY), что и у лимитеров: иначе журнал мог бы
// утверждать, что вход был с адреса, который подставил клиент.
func (h *Handlers) recordAudit(r *http.Request, event auditWrite) {
	if h.Audit == nil {
		// Хранилище не подключено: бывает в unit-тестах пакетов, где журнал не
		// проверяют. Молчание здесь безопасно именно потому, что запись —
		// побочный эффект, а не условие доступа.
		return
	}

	record := store.AuditEvent{
		Action:     string(event.action),
		TargetType: event.targetType,
		TargetID:   event.targetID,
		Detail:     event.detail,
		OK:         event.ok,
		IP:         ClientIP(r),
		UserAgent:  r.UserAgent(),
	}

	if event.actor != nil {
		actor := event.actor.ID
		record.ActorUserID = &actor
	}

	if err := h.Audit.Record(r.Context(), record); err != nil {
		requestLogger(r).Warn("аудит: событие не записано",
			slog.String("action", string(event.action)),
			slog.Any("error", err),
		)
	}
}

// auditTargetID оформляет числовой идентификатор объекта для колонки target_id.
func auditTargetID(id int64) string {
	return strconv.FormatInt(id, 10)
}

// Границы чтения журнала.
const (
	defaultAuditLimit = 50
	maxAuditLimit     = 200
)

// AdminAudit читает журнал: GET /api/admin/audit.
//
// Право audit.view проверяет маршрутизатор, поэтому здесь остаётся только
// разобрать фильтры. Публичного пути к журналу нет намеренно: строки содержат
// IP, User-Agent и имена учётных записей из неудачных входов.
//
// Параметры: action (один из store.AuditActions()), actor_user_id, limit,
// offset. Отдаёт массив событий свежими вперёд без общего счётчика — счётчик
// потребовал бы второго запроса и отдельного решения о том, считается ли он с
// фильтром; клиент узнаёт «есть ещё» по длине страницы (len == limit).
func (h *Handlers) AdminAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}

	if h.Audit == nil {
		writeError(w, http.StatusInternalServerError, "audit log is not available")
		return
	}

	query := r.URL.Query()

	filter := store.AuditFilter{Limit: defaultAuditLimit}

	if action := strings.TrimSpace(query.Get("action")); action != "" {
		if !store.ValidAuditAction(action) {
			writeError(w, http.StatusUnprocessableEntity,
				"action must be one of: "+strings.Join(store.AuditActions(), ", "))
			return
		}
		filter.Action = action
	}

	if raw := strings.TrimSpace(query.Get("actor_user_id")); raw != "" {
		actorID, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || actorID <= 0 {
			writeError(w, http.StatusUnprocessableEntity, "actor_user_id must be a positive number")
			return
		}
		filter.ActorID = actorID
	}

	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit <= 0 {
			writeError(w, http.StatusUnprocessableEntity, "limit must be a positive number")
			return
		}
		if limit > maxAuditLimit {
			limit = maxAuditLimit
		}
		filter.Limit = limit
	}

	if raw := strings.TrimSpace(query.Get("offset")); raw != "" {
		offset, err := strconv.Atoi(raw)
		if err != nil || offset < 0 {
			writeError(w, http.StatusUnprocessableEntity, "offset must be 0 or greater")
			return
		}
		filter.Offset = offset
	}

	events, err := h.Audit.List(r.Context(), filter)
	if err != nil {
		requestLogger(r).Error("аудит: не удалось прочитать журнал", slog.Any("error", err))
		writeError(w, http.StatusInternalServerError, "failed to load audit log")
		return
	}

	writeJSON(w, http.StatusOK, events)
}
