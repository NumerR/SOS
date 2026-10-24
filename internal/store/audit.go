package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// AuditStore пишет и читает журнал действий (audit_events).
//
// Зачем отдельная таблица, если есть структурированный лог (ADR-0008). Лог
// живёт на хосте, который отдаёт его ops-команде, и ротация его короче, чем
// жизнь аккаунта: через неделю «кто оценил эту работу» уже не восстановить.
// Аудит — часть данных приложения: его спрашивает администратор через
// интерфейс, и он обязан пережить перезапуск и выгрузку базы. Поэтому в лог
// пишутся метаданные запроса, а в базу — факты о действиях.
//
// Что сюда попадает и что нет — правило, которое важно не нарушать:
//
//   - пароли, хэши, токены сессий и csrf — никогда. Ни в detail, ни в
//     target_id: строка аудита читается людьми через админку, а идентификатор
//     сессии — это чужой вход в систему.
//   - идентификатор учётки из неудачного входа — попадает: это ровно то, что
//     нужно админу, чтобы увидеть перебор. Он не секрет (логин виден в списке
//     пользователей), и он уже уходит в лог процесса в замаскированном виде —
//     здесь же нужен полный.
//
// Границы длин применяются здесь, а не в вызывающем коде: аудиту запрещены
// ошибки «запись не влезла» (она стоила бы потерянного события), поэтому
// Record обрезает вход, а не отвергает его.

// AuditEventsMigration — DDL таблицы audit_events.
//
// Текст объявлен здесь, а не внутри пакета модуля, по одной практической
// причине: модуль (internal/modules/audit) импортирует handlers, handlers
// импортируют store. Если бы DDL жил в модуле, тестам хранилища и обработчиков
// пришлось бы импортировать пакет выше себя — Go запретил бы это как цикл в
// тесте, и единственным выходом было бы копировать SQL во второй раз.
// Копия хуже: схема и запросы к ней разошлись бы молча.
//
// Граница при этом не смазана: право объявления миграции остаётся у модуля —
// именно он перечисляет эту строку в Migrations(), а store по-прежнему не
// применяет миграции сам.
//
// Колонки времени — Unix-секунды (INTEGER), как во всей текущей схеме:
// `timestamptz` появится вместе с переездом на PostgreSQL в T2.1, там же
// уедут и плейсхолдеры «?».
//
// FK на users — с ON DELETE SET NULL, и это единственное место в схеме, где
// удаление ссылку рвёт, а не уносит строку. Причина: журнал, который исчезает
// вместе с учёткой, нельзя показать при разборе «что этот человек натворил,
// пока его не удалили». Событие остаётся, виновник становится NULL.
const AuditEventsMigration = `
CREATE TABLE IF NOT EXISTS audit_events (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    at            INTEGER NOT NULL,
    actor_user_id INTEGER,
    action        TEXT    NOT NULL,
    target_type   TEXT    NOT NULL DEFAULT '',
    target_id     TEXT    NOT NULL DEFAULT '',
    detail        TEXT    NOT NULL DEFAULT '',
    ok            INTEGER NOT NULL DEFAULT 1,
    ip            TEXT    NOT NULL DEFAULT '',
    user_agent    TEXT    NOT NULL DEFAULT '',
    FOREIGN KEY(actor_user_id) REFERENCES users(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_audit_events_at ON audit_events(at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_events_actor ON audit_events(actor_user_id, at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_events_action ON audit_events(action, at DESC);
`

// AuditAction — имя действия в колонке action.
//
// Имена с префиксом модуля ("auth.", "admin.", "submission."): список остаётся
// читаемым, когда аудита начнут требовать другие модули, а фильтр по префиксу
// не требует знать все имена наперёд.
type AuditAction string

const (
	AuditLogin          AuditAction = "auth.login"           // вход — удачный и нет
	AuditLogout         AuditAction = "auth.logout"          // выход с одного устройства
	AuditLogoutAll      AuditAction = "auth.logout_all"      // «выйти везде»
	AuditPasswordChange AuditAction = "auth.password_change" // смена пароля
	AuditRegister       AuditAction = "auth.register"        // саморегистрация
	AuditRoleChange     AuditAction = "admin.role_change"    // админ назначил роль
	AuditGrade          AuditAction = "submission.grade"     // выставлена оценка
)

// auditActions — все известные действия. Нужно фильтру чтения: опечатка в
// ?action= не должна выглядеть как «в журнале пусто».
var auditActions = []AuditAction{
	AuditLogin,
	AuditLogout,
	AuditLogoutAll,
	AuditPasswordChange,
	AuditRegister,
	AuditRoleChange,
	AuditGrade,
}

// ValidAuditAction — объявлено ли такое действие в системе.
func ValidAuditAction(action string) bool {
	for _, known := range auditActions {
		if string(known) == action {
			return true
		}
	}
	return false
}

// AuditActions — список для подсказки в тексте ошибки и документации.
func AuditActions() []string {
	out := make([]string, 0, len(auditActions))
	for _, action := range auditActions {
		out = append(out, string(action))
	}
	return out
}

// Границы полей. В audit_events пишут значения, которые пришли из заголовков
// запроса и от обработчиков, — то есть извне.
const (
	maxAuditDetailLength    = 200
	maxAuditTargetLength    = 64
	maxAuditIPLength        = 45 // IPv6 в текстовой форме — максимум 45 символов
	maxAuditUserAgentLength = 200
)

// AuditEvent — одна строка журнала.
//
// ActorUserID указывает на пользователя, который действовал. nil — «не
// установлено»: неудачный вход несуществующей учётки. Именно nil, а не 0:
// 0 — это значение, которое кто-нибудь прочитает как «пользователь с id 0».
//
// TargetID — строка, а не число, потому что объектами аудита бывают и то, что
// числовым id не описывается (имя учётки при неудачном входе), и будущий
// источник расписания с внешним идентификатором.
//
// ActorUsername заполняется только чтением: LEFT JOIN по users. Роль автора
// события не отдаём намеренно — она могла измениться после события, и «был
// админом» в журнале стало бы неправдой; в detail роль записана там, где она
// суть события (смена роли).
type AuditEvent struct {
	ID            int64  `json:"id"`
	At            int64  `json:"at"`
	ActorUserID   *int64 `json:"actor_user_id"`
	ActorUsername string `json:"actor_username"`
	Action        string `json:"action"`
	TargetType    string `json:"target_type"`
	TargetID      string `json:"target_id"`
	Detail        string `json:"detail"`
	OK            bool   `json:"ok"`
	IP            string `json:"ip"`
	UserAgent     string `json:"user_agent"`
}

// AuditFilter — параметры чтения журнала.
//
// nil-указатели и нули здесь означают «без ограничения», поэтому отдельный
// флаг для «фильтра нет» не нужен.
type AuditFilter struct {
	Action  string
	ActorID int64
	Limit   int
	Offset  int
}

// AuditStore работает с таблицей audit_events.
type AuditStore struct {
	db *sql.DB
}

// NewAuditStore создаёт хранилище журнала.
func NewAuditStore(db *sql.DB) *AuditStore {
	return &AuditStore{db: db}
}

// Record добавляет событие.
//
// Отвергать вход здесь специально нельзя: строка аудита не должна срываться
// из-за длины, иначе потерянное событие дороже битого поля. Поэтому значения
// укорачиваются, а не отклоняются. Ошибку возвращаем, чтобы вызывающий мог
// отлогировать сбой базы, — но ради неё отменять вход пользователя нельзя
// (см. handlers.recordAudit).
func (s *AuditStore) Record(ctx context.Context, event AuditEvent) error {
	if s == nil || s.db == nil {
		return sql.ErrConnDone
	}

	if event.At == 0 {
		event.At = time.Now().Unix()
	}

	_, err := s.db.ExecContext(
		ctx,
		`
		INSERT INTO audit_events (
			at,
			actor_user_id,
			action,
			target_type,
			target_id,
			detail,
			ok,
			ip,
			user_agent
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
		event.At,
		actorColumn(event.ActorUserID),
		auditCut(event.Action, maxAuditTargetLength),
		auditCut(event.TargetType, maxAuditTargetLength),
		auditCut(event.TargetID, maxAuditTargetLength),
		auditCut(event.Detail, maxAuditDetailLength),
		event.OK,
		auditCut(event.IP, maxAuditIPLength),
		auditCut(event.UserAgent, maxAuditUserAgentLength),
	)

	return err
}

// List читает журнал свежими записями вперёд.
//
// ORDER BY at DESC, id DESC: один и тот же такт часов у двух событий реален
// (секундное разрешение), и без id-тай-брейка страница могла бы показать одну
// и ту же запись дважды или потерять её при пагинации.
//
// LEFT JOIN, а не INNER: строка с actor_user_id IS NULL (неудачный вход) или с
// разорванной ссылкой (пользователь удалён, ON DELETE SET NULL) обязана
// остаться видимой — иначе журнал начал бы терять события ровно в тех случаях,
// ради которых его и ведут.
//
// Значение LIMIT обязано быть положительным: в SQLite LIMIT NULL означает
// «без ограничения», а вызывающий без параметра получил бы выгрузку всей
// таблицы. Разумную границу задаёт обработчик (handlers.AdminAudit), здесь —
// только защита от нуля.
func (s *AuditStore) List(ctx context.Context, filter AuditFilter) ([]AuditEvent, error) {
	if s == nil || s.db == nil {
		return nil, sql.ErrConnDone
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = 1
	}

	query := `
		SELECT ae.id, ae.at, ae.actor_user_id, ae.action, ae.target_type,
		       ae.target_id, ae.detail, ae.ok, ae.ip, ae.user_agent,
		       COALESCE(u.username, '')
		FROM audit_events ae
		LEFT JOIN users u ON u.id = ae.actor_user_id
		WHERE 1 = 1`
	args := make([]any, 0, 3)

	if filter.Action != "" {
		query += ` AND ae.action = ?`
		args = append(args, filter.Action)
	}

	if filter.ActorID > 0 {
		query += ` AND ae.actor_user_id = ?`
		args = append(args, filter.ActorID)
	}

	query += `
		ORDER BY ae.at DESC, ae.id DESC
		LIMIT ? OFFSET ?`
	args = append(args, limit, filter.Offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]AuditEvent, 0, limit)

	for rows.Next() {
		var event AuditEvent
		var ok int
		var actor sql.NullInt64

		if err := rows.Scan(
			&event.ID,
			&event.At,
			&actor,
			&event.Action,
			&event.TargetType,
			&event.TargetID,
			&event.Detail,
			&ok,
			&event.IP,
			&event.UserAgent,
			&event.ActorUsername,
		); err != nil {
			return nil, err
		}

		event.OK = ok != 0
		if actor.Valid {
			value := actor.Int64
			event.ActorUserID = &value
		}

		events = append(events, event)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return events, nil
}

// actorColumn переводит *int64 в параметр запроса: nil обязан стать NULL, а
// не нулём — иначе журнал утверждал бы, что действовал пользователь с id 0.
func actorColumn(actor *int64) any {
	if actor == nil {
		return nil
	}
	return *actor
}

// auditCut обрезает по символам, а не по байтам: UTF-8-строка, разрезанная по
// байту, превращается в битый текст, который не отрендерит ни JSON-энкодер,
// ни таблица админки.
func auditCut(value string, limit int) string {
	if limit <= 0 {
		return ""
	}

	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= limit {
		return string(runes)
	}

	return string(runes[:limit])
}
