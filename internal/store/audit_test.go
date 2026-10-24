package store_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/NumerR/SOS/internal/database"
	"github.com/NumerR/SOS/internal/store"
)

// Тесты журнала действий (T1.2) на уровне хранилища: что реально попадает в
// строку, что из неё читается и как она ведёт себя при удалении учётки.
// Обработчики проверяются своими тестами (internal/handlers/audit_test.go),
// здесь — только SQL-слой.

func newAuditDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("открыть базу: %v", err)
	}
	if err := database.Migrate(db); err != nil {
		t.Fatalf("базовая схема: %v", err)
	}
	// Миграция модуля audit живёт в самом модуле, а модуль импортирует
	// handlers; здесь достаточно её текста — он тот же, что применяет сервер.
	if _, err := db.Exec(store.AuditEventsMigration); err != nil {
		t.Fatalf("таблица аудита: %v", err)
	}

	t.Cleanup(func() { _ = db.Close() })

	return db
}

func addUser(t *testing.T, db *sql.DB, username, role string) int64 {
	t.Helper()

	result, err := db.ExecContext(context.Background(),
		`INSERT INTO users (username, email, full_name, password_hash, role) VALUES (?, ?, '', 'x', ?)`,
		username, username+"@example.test", role,
	)
	if err != nil {
		t.Fatalf("создать пользователя %s: %v", username, err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("id пользователя: %v", err)
	}

	return id
}

func record(t *testing.T, audit *store.AuditStore, event store.AuditEvent) {
	t.Helper()

	if err := audit.Record(context.Background(), event); err != nil {
		t.Fatalf("записать событие %s: %v", event.Action, err)
	}
}

func pointer(value int64) *int64 { return &value }

// TestRecordAndListRoundTrip — поля доезжают до базы и обратно.
//
// Отдельно проверяем NULL автора: это не «пустая строка», а «действие без
// установившегося автора» (неудачный вход), и смешивать эти два смысла нельзя.
func TestRecordAndListRoundTrip(t *testing.T) {
	db := newAuditDB(t)
	audit := store.NewAuditStore(db)
	userID := addUser(t, db, "roundtrip", "student")

	record(t, audit, store.AuditEvent{
		At:          time.Now().Add(-time.Hour).Unix(),
		ActorUserID: pointer(userID),
		Action:      string(store.AuditLogin),
		TargetType:  "user",
		TargetID:    "42",
		Detail:      "revoked_sessions=3",
		OK:          true,
		IP:          "203.0.113.7",
		UserAgent:   "curl/8",
	})

	record(t, audit, store.AuditEvent{
		At:         time.Now().Unix(),
		Action:     string(store.AuditLogin),
		TargetType: "identifier",
		Detail:     "invalid credentials identifier=ghost",
	})

	events, err := audit.List(context.Background(), store.AuditFilter{Limit: 10})
	if err != nil {
		t.Fatalf("прочитать журнал: %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("в журнале %d записей, ожидалось 2", len(events))
	}

	// ORDER BY at DESC: свежая строка обязана идти первой.
	newest, oldest := events[0], events[1]

	if oldest.ActorUserID == nil || *oldest.ActorUserID != userID {
		t.Errorf("автор неудачного входа: %v, ожидалось id %d", oldest.ActorUserID, userID)
	}
	if oldest.Detail != "revoked_sessions=3" || oldest.IP != "203.0.113.7" || oldest.UserAgent != "curl/8" {
		t.Errorf("поля потеряны: %+v", oldest)
	}
	if !oldest.OK {
		t.Error("ok=true записан как false")
	}
	if oldest.ActorUsername != "roundtrip" {
		t.Errorf("имя автора не пришло из JOIN: %q", oldest.ActorUsername)
	}

	if newest.ActorUserID != nil {
		t.Errorf("у события без автора actor_user_id = %d, ожидалось NULL", *newest.ActorUserID)
	}
	if newest.ActorUsername != "" {
		t.Errorf("у события без автора имя = %q, ожидалась пустая строка", newest.ActorUsername)
	}
	if newest.OK {
		t.Error("ok=false записан как true")
	}
}

// TestListAtDefaultsToNow — Record без явного времени ставит текущее.
func TestListAtDefaultsToNow(t *testing.T) {
	db := newAuditDB(t)
	audit := store.NewAuditStore(db)

	before := time.Now().Add(-time.Minute).Unix()
	record(t, audit, store.AuditEvent{Action: string(store.AuditRegister)})
	after := time.Now().Add(time.Minute).Unix()

	events, err := audit.List(context.Background(), store.AuditFilter{Limit: 5})
	if err != nil || len(events) != 1 {
		t.Fatalf("журнал после одной записи: %d строк, ошибка %v", len(events), err)
	}

	if events[0].At < before || events[0].At > after {
		t.Errorf("at = %d вне окна %d…%d — время события не проставляется", events[0].At, before, after)
	}
}

// TestListFilters — фильтр по действию и по автору.
func TestListFilters(t *testing.T) {
	db := newAuditDB(t)
	audit := store.NewAuditStore(db)

	first := addUser(t, db, "filterfirst", "student")
	second := addUser(t, db, "filtersecond", "teacher")

	events := []store.AuditEvent{
		{ActorUserID: pointer(first), Action: string(store.AuditLogin), OK: true},
		{ActorUserID: pointer(first), Action: string(store.AuditLogout), OK: true},
		{ActorUserID: pointer(second), Action: string(store.AuditLogin), OK: false},
	}

	for i := range events {
		events[i].At = time.Now().Add(time.Duration(i) * time.Second).Unix()
		record(t, audit, events[i])
	}

	ctx := context.Background()

	logins, err := audit.List(ctx, store.AuditFilter{Action: string(store.AuditLogin), Limit: 10})
	if err != nil {
		t.Fatalf("фильтр по действию: %v", err)
	}
	if len(logins) != 2 {
		t.Errorf("auth.login: %d записей, ожидалось 2", len(logins))
	}

	mine, err := audit.List(ctx, store.AuditFilter{ActorID: second, Limit: 10})
	if err != nil {
		t.Fatalf("фильтр по автору: %v", err)
	}
	if len(mine) != 1 || mine[0].Action != string(store.AuditLogin) || mine[0].OK {
		t.Errorf("журнал автора %d: %+v, ожидалась одна неудачная запись входа", second, mine)
	}

	both, err := audit.List(ctx, store.AuditFilter{Action: string(store.AuditLogin), ActorID: second, Limit: 10})
	if err != nil || len(both) != 1 {
		t.Errorf("два фильтра сразу: %d записей (%v), ожидалось 1", len(both), err)
	}
}

// TestListPaginationIsStable — страница не теряет и не дублирует строки.
//
// Все события пишутся с одним и тем же `at`: секундное разрешение такое
// допускает, и без тай-брейка по id порядок внутри такта зависит от плана
// запроса.
func TestListPaginationIsStable(t *testing.T) {
	db := newAuditDB(t)
	audit := store.NewAuditStore(db)

	at := time.Now().Unix()
	for i := 0; i < 5; i++ {
		record(t, audit, store.AuditEvent{
			At:     at,
			Action: string(store.AuditGrade),
			Detail: "index=" + string(rune('a'+i)),
		})
	}

	ctx := context.Background()

	seen := map[int64]bool{}

	for page := 0; page < 3; page++ {
		events, err := audit.List(ctx, store.AuditFilter{Limit: 2, Offset: page * 2})
		if err != nil {
			t.Fatalf("страница %d: %v", page, err)
		}

		for _, event := range events {
			if seen[event.ID] {
				t.Fatalf("страница %d повторила запись %d", page, event.ID)
			}
			seen[event.ID] = true
		}
	}

	if len(seen) != 5 {
		t.Errorf("за три страницы собрано %d уникальных записей из 5", len(seen))
	}
}

// TestListNonPositiveLimit — ноль и минус не превращаются в «всю таблицу».
func TestListNonPositiveLimit(t *testing.T) {
	db := newAuditDB(t)
	audit := store.NewAuditStore(db)

	for i := 0; i < 3; i++ {
		record(t, audit, store.AuditEvent{Action: string(store.AuditLogout)})
	}

	events, err := audit.List(context.Background(), store.AuditFilter{})
	if err != nil {
		t.Fatalf("чтение без лимита: %v", err)
	}
	if len(events) > 1 {
		t.Errorf("без лимита вернулось %d строк — хранилище обязано резать, а не выгружать всё", len(events))
	}
}

// TestRecordCutsOversizedFields — длинные входные значения обрезаются, а не
// отклоняются и не режут строку пополам.
func TestRecordCutsOversizedFields(t *testing.T) {
	db := newAuditDB(t)
	audit := store.NewAuditStore(db)

	long := strings.Repeat("a", 5000)
	// Русская строка: байтовая граница разрезала бы её посреди UTF-8-последо-
	// вательности, и JSON-ответ страницы админки стал бы невалидным.
	cyrillic := strings.Repeat("оценка", 500)

	record(t, audit, store.AuditEvent{Action: string(store.AuditGrade), UserAgent: long, Detail: cyrillic, IP: long})
	record(t, audit, store.AuditEvent{Action: string(store.AuditRoleChange), TargetType: long, TargetID: long})

	events, err := audit.List(context.Background(), store.AuditFilter{Limit: 5})
	if err != nil || len(events) != 2 {
		t.Fatalf("журнал: %d строк, ошибка %v", len(events), err)
	}

	for _, event := range events {
		// Считаем символы, а не байты: кириллический detail в 200 символах
		// занимает 400 байт, и байтовая проверка ругалась бы на корректную
		// обрезку.
		if n := utf8.RuneCountInString(event.UserAgent); n > 200 {
			t.Errorf("user_agent не обрезан: %d символов", n)
		}
		if n := utf8.RuneCountInString(event.IP); n > 45 {
			t.Errorf("ip не обрезан: %d символов", n)
		}
		if n := utf8.RuneCountInString(event.Detail); n > 200 {
			t.Errorf("detail не обрезан: %d символов", n)
		}
		if n := utf8.RuneCountInString(event.TargetType); n > 64 {
			t.Errorf("target_type не обрезан: %d символов", n)
		}
		if n := utf8.RuneCountInString(event.TargetID); n > 64 {
			t.Errorf("target_id не обрезан: %d символов", n)
		}
		if strings.ContainsRune(event.Detail, '\uFFFD') {
			t.Error("после обрезки в detail есть битый символ — резали по байту")
		}
	}
}

// TestAuditSurvivesUserDeletion — ON DELETE SET NULL, а не CASCADE.
//
// Это единственная ссылка в схеме, где удаление первичной записи не уносит
// историю: журнал, исчезающий вместе с учёткой, бесполезен при разборе того,
// что человек сделал до удаления.
func TestAuditSurvivesUserDeletion(t *testing.T) {
	db := newAuditDB(t)
	audit := store.NewAuditStore(db)
	userID := addUser(t, db, "deleted", "teacher")

	record(t, audit, store.AuditEvent{
		ActorUserID: pointer(userID),
		Action:      string(store.AuditRoleChange),
		Detail:      "from=teacher to=student",
		OK:          true,
	})

	if _, err := db.Exec(`DELETE FROM users WHERE id = ?`, userID); err != nil {
		t.Fatalf("удалить пользователя: %v", err)
	}

	events, err := audit.List(context.Background(), store.AuditFilter{Limit: 5})
	if err != nil {
		t.Fatalf("чтение после удаления автора: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("записей %d, ожидалось 1: журнал не должен исчезать вместе с учёткой", len(events))
	}
	if events[0].ActorUserID != nil {
		t.Errorf("actor_user_id = %d после удаления пользователя, ожидалось NULL", *events[0].ActorUserID)
	}
	if events[0].Detail != "from=teacher to=student" {
		t.Errorf("detail потерян: %q", events[0].Detail)
	}
}

// TestValidAuditAction — список действий замкнут, а случайная строка в него не
// проходит: фильтр по несуществующему действию обработчик обязан отвергать.
func TestValidAuditAction(t *testing.T) {
	for _, action := range store.AuditActions() {
		if !store.ValidAuditAction(action) {
			t.Errorf("действие %s не проходит собственную проверку", action)
		}
	}

	for _, bogus := range []string{"", "login", "auth.", "AUTH.LOGIN", "auth.login "} {
		if store.ValidAuditAction(bogus) {
			t.Errorf("значение %q признано известным действием", bogus)
		}
	}
}
