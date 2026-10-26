package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ADR-0031: сбой хранилища на пути проверки входа обязан быть 500, а не 401.
//
// RequireAuth до правки спрашивал у `authenticatedUser` один bool, и в него
// склеивались три разных исхода: куки нет, сессия истекла, база не ответила.
// Два первых — «входа нет», и 401 для них верный; третий проходил той же
// веткой, и человек с жалобой «меня не пускает» получал страницу входа, а
// разбирающий лог — строку доступа со status=401 вместо отказа сервера.
//
// Второй набор проверок держит прежний 401 на всех видах «входа нет»: иначе
// правка свелась бы к замене одного неверного кода другим — тот же порядок,
// что у ADR-0029.

// requireAuthProbe — RequireAuth поверх обработчика-иголки: 204 означает, что
// middleware дошёл до дела, а не отверг запрос.
func requireAuthProbe(h *Handlers) http.Handler {
	return h.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
}

// callRequireAuth — вызов через LogRequests: у отказа обязана быть строка с
// request_id ровно в том виде, в каком её ищет человек, разбирающий жалобу.
func callRequireAuth(handler http.Handler, cookies []*http.Cookie) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	LogRequests(handler).ServeHTTP(rec, request(http.MethodGet, "/api/courses", "", "10.0.31.1:1", cookies))
	return rec
}

func sessionCookie(value string) []*http.Cookie {
	return []*http.Cookie{{Name: sessionCookieName, Value: value}}
}

func TestRequireAuthOnStoreFailureAnswersFiveHundred(t *testing.T) {
	h, db := newFullEnv(t)
	createUser(t, h, "s_owner", "password123", "student")
	cookies := login(t, h, "s_owner", "password123", "10.0.31.1:1")
	handler := requireAuthProbe(h)

	// На целой базе тот же запрос проходит — иначе проверка ниже ничего не
	// доказывает: 204 должен быть достижим.
	if rec := callRequireAuth(handler, cookies); rec.Code != http.StatusNoContent {
		t.Fatalf("целая база: middleware ответил %d, ожидался 204", rec.Code)
	}

	breakTable(t, db, "sessions")
	buffer := captureLogs(t)

	rec := callRequireAuth(handler, cookies)
	expectStoreFailure(t, "RequireAuth/чтение сессии", rec)

	if got := decodeError(t, rec).Message; got != sessionFailureReason {
		t.Fatalf("клиенту ушёл текст %q, ожидался %q", got, sessionFailureReason)
	}

	logged := buffer.String()
	for _, want := range []string{
		`msg="отказ сервера"`,
		`reason="failed to verify session"`,
		"no such table: sessions",
		"request_id=" + rec.Header().Get(requestIDHeader),
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("в логе нет %q:\n%s", want, logged)
		}
	}

	// Отказ один: строка «отказ сервера» плюс поднятая до Error строка доступа.
	if got := strings.Count(logged, "level=ERROR"); got != 2 {
		t.Fatalf("Error-строк %d, ожидалось 2 (отказ + строка доступа):\n%s", got, logged)
	}
}

// TestRequireAuthDuringOutageKeepsAnonymousAtFourZeroOne держит ограничение
// правки: сбой касается только тех, кто пришёл с кукой. Запрос без сессии
// обязан остаться честным «не вошёл» и не плодить отказы сервера там, где база
// ни при чём.
func TestRequireAuthDuringOutageKeepsAnonymousAtFourZeroOne(t *testing.T) {
	h, db := newFullEnv(t)
	breakTable(t, db, "sessions")
	buffer := captureLogs(t)

	rec := callRequireAuth(requireAuthProbe(h), nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("аноним при сломанной базе: код %d, ожидался 401: %s", rec.Code, rec.Body.String())
	}
	if body := decodeError(t, rec); body.Code != "unauthorized" {
		t.Fatalf("аноним при сломанной базе: код конверта %q, ожидался %q", body.Code, "unauthorized")
	}
	if strings.Contains(buffer.String(), `msg="отказ сервера"`) {
		t.Fatalf("отсутствие куки записано отказом сервера:\n%s", buffer.String())
	}
}

// TestRequireAuthKeepsStaleSessionAtFourZeroOne — три вида «входа нет» на
// живой базе: истёкшая сессия, неизвестная и сессия без пользователя.
func TestRequireAuthKeepsStaleSessionAtFourZeroOne(t *testing.T) {
	h, db := newFullEnv(t)
	user := createUser(t, h, "s_stale", "password123", "student")
	handler := requireAuthProbe(h)

	_, err := db.Exec(`INSERT INTO sessions (id, user_id, expires_at) VALUES ('expired-session', ?, ?)`,
		user.ID, time.Now().Add(-time.Minute).Unix())
	if err != nil {
		t.Fatalf("завести истёкшую сессию: %v", err)
	}

	// Так выглядит аккаунт, удалённый мимо внешних ключей (ручная чистка или
	// частичный restore): каскад обычно уносит и сессию, поэтому ветку
	// подставляют снятием ограничения — на одном подключении это детерминировано.
	// Ответ обязан остаться 401: превратить удалённый аккаунт в «сервер сломан»
	// значило бы завести новый баг вместо старого.
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("выключить внешние ключи: %v", err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`PRAGMA foreign_keys = ON`) })

	if _, err := db.Exec(`INSERT INTO sessions (id, user_id, expires_at) VALUES ('ownerless-session', ?, ?)`,
		user.ID, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatalf("завести сессию без пользователя: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM users WHERE id = ?`, user.ID); err != nil {
		t.Fatalf("удалить пользователя: %v", err)
	}

	for _, probe := range []struct {
		what  string
		value string
	}{
		{"истёкшая сессия", "expired-session"},
		{"неизвестная сессия", "нет-такой-сессии"},
		{"сессия без пользователя", "ownerless-session"},
	} {
		rec := callRequireAuth(handler, sessionCookie(probe.value))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: код %d, ожидался 401: %s", probe.what, rec.Code, rec.Body.String())
		}
	}
}

// TestDashboardPageOnStoreFailureAnswersFiveHundred — то же различие на пути
// HTML-страниц: прежний redirect на /login при сломанной базе прятал отказ и
// отправлял человека на форму входа, где его ждал тот же сбой — уже без причины.
func TestDashboardPageOnStoreFailureAnswersFiveHundred(t *testing.T) {
	h, db := newFullEnv(t)
	createUser(t, h, "s_page", "password123", "student")
	cookies := login(t, h, "s_page", "password123", "10.0.31.2:1")

	page := LogRequests(http.HandlerFunc(h.DashboardPage))

	// На целой базе guard не отвергает запрос. Код здесь не 200: web/ лежит на
	// уровне репозитория, а тест идёт из internal/handlers, поэтому ServeFile
	// отдаёт 404. Важно, что до сбоя ни redirect, ни 500 нет.
	rec := callPage(page, cookies)
	if rec.Code == http.StatusFound || rec.Code == http.StatusInternalServerError {
		t.Fatalf("целая база: /dashboard отвечен %d, guard сработал как отказ", rec.Code)
	}

	breakTable(t, db, "sessions")
	buffer := captureLogs(t)

	rec = callPage(page, cookies)
	expectStoreFailure(t, "DashboardPage/чтение сессии", rec)

	if got := rec.Header().Get("Location"); got != "" {
		t.Fatalf("сбой базы спрятан за redirect на %q", got)
	}
	logged := buffer.String()
	for _, want := range []string{`msg="отказ сервера"`, `reason="failed to verify session"`, "no such table: sessions"} {
		if !strings.Contains(logged, want) {
			t.Fatalf("в логе нет %q:\n%s", want, logged)
		}
	}

	// Анониму redirect остаётся: он и без базы знает, что не вошёл.
	if rec := callPage(page, nil); rec.Code != http.StatusFound {
		t.Fatalf("аноним: код %d, ожидался 302", rec.Code)
	} else if got := rec.Header().Get("Location"); got != "/login" {
		t.Fatalf("аноним: redirect на %q, ожидался /login", got)
	}
}

func callPage(handler http.Handler, cookies []*http.Cookie) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, request(http.MethodGet, "/dashboard", "", "10.0.31.2:1", cookies))
	return rec
}

// TestAuthenticatedUserDistinguishesFailureFromAbsence смотрит на саму границу:
// helper возвращает ошибку тогда и только тогда, когда хранилище не ответило.
// Через снятие таблицы users это надо делать с выключенными ключами: DROP
// родительской таблицы в SQLite исполняет каскад, сессия уносится вместе с
// ней, и проверяемый шаг (чтение пользователя) просто не наступает.
func TestAuthenticatedUserDistinguishesFailureFromAbsence(t *testing.T) {
	h, db := newFullEnv(t)
	user := createUser(t, h, "s_direct", "password123", "student")
	cookies := login(t, h, "s_direct", "password123", "10.0.31.3:1")

	found, err := h.authenticatedUser(request(http.MethodGet, "/dashboard", "", "10.0.31.3:1", cookies))
	if err != nil {
		t.Fatalf("на целой базе: %v", err)
	}
	if found == nil || found.ID != user.ID {
		t.Fatalf("на целой базе возвращён %+v, ожидался пользователь %d", found, user.ID)
	}

	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("выключить внешние ключи: %v", err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`PRAGMA foreign_keys = ON`) })

	breakTable(t, db, "users")

	got, err := h.authenticatedUser(request(http.MethodGet, "/dashboard", "", "10.0.31.3:1", cookies))
	if err == nil {
		t.Fatalf("после снятия таблицы users ошибки нет (пользователь: %+v)", got)
	}
	if got != nil {
		t.Fatalf("при сбое возвращён пользователь: %+v", got)
	}
	for _, want := range []string{"no such table: users", "чтение пользователя сессии"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("в тексте ошибки %q нет %q — middleware теряет, какой шаг не ответил", err.Error(), want)
		}
	}
}
