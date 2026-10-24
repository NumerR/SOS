package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NumerR/SOS/internal/authz"
	"github.com/NumerR/SOS/internal/database"
	"github.com/NumerR/SOS/internal/models"
	"github.com/NumerR/SOS/internal/ratelimit"
	"github.com/NumerR/SOS/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// Вспомогательное окружение тестов обработки аутентификации:
// in-memory SQLite, те же middleware, что и в main.go, и bcrypt минимальной
// стоимости — тесты проверяют логику, а не скорость хэширования.

func newTestHandlers(t *testing.T) *Handlers {
	t.Helper()

	h, _ := newTestEnv(t)
	return h
}

// newTestEnv — то же, но возвращает и *sql.DB: часть проверок двигает
// expires_at у сессии прямым запросом, а такого метода у хранилища нет —
// и быть не должно, наружу «подделать срок» не должен уметь никто.
func newTestEnv(t *testing.T) (*Handlers, *sql.DB) {
	t.Helper()

	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("открыть базу: %v", err)
	}
	if err := database.Migrate(db); err != nil {
		t.Fatalf("миграции: %v", err)
	}

	// Таблица журнала: в живом сервере её создаёт миграция модуля audit, а
	// здесь модуля нет — иначе тесты обработчиков зависели бы от пакета,
	// который зависит от них.
	if _, err := db.Exec(store.AuditEventsMigration); err != nil {
		t.Fatalf("таблица аудита: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	return New(Deps{
		Users:    store.NewUserStore(db),
		Sessions: store.NewSessionStore(db),
		Audit:    store.NewAuditStore(db),
		Limits:   NewLimiters(),
	}), db
}

func createUser(t *testing.T, h *Handlers, username, password, role string) *models.User {
	t.Helper()

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("хэш пароля: %v", err)
	}

	user, err := h.Users.Create(context.Background(), username, username+"@example.test", username+" Test", string(hash), role)
	if err != nil {
		t.Fatalf("создать пользователя %s: %v", username, err)
	}
	return user
}

// request — единая точка формирования HTTP-запроса в тестах.
func request(method, target, body, remoteAddr string, cookies []*http.Cookie) *http.Request {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}

	req := httptest.NewRequest(method, target, reader)
	if reader != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	return req
}

// call — вызов обработчика с теми же middleware, что навешаны на маршрут в main.go.
func callLogin(h *Handlers, body, remoteAddr string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	handler := h.RateLimit(h.Limits.Login, "login", ClientIP)(http.HandlerFunc(h.Login))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, request(http.MethodPost, "/api/auth/login", body, remoteAddr, cookies))
	return rec
}

func callRegister(h *Handlers, body, remoteAddr string) *httptest.ResponseRecorder {
	handler := h.RateLimit(h.Limits.Register, "register", ClientIP)(http.HandlerFunc(h.Register))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, request(http.MethodPost, "/api/auth/register", body, remoteAddr, nil))
	return rec
}

func callAdminSetRole(h *Handlers, body, remoteAddr string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	handler := h.RequirePerm(authz.UserRoleManage, "only administrators can assign roles",
		h.RequireCSRF(http.HandlerFunc(h.AdminSetRole)))

	req := request(http.MethodPost, "/api/admin/users/role", body, remoteAddr, cookies)
	// Double-submit: токен едет и в cookie, и в заголовке.
	if token := csrfFrom(cookies); token != "" {
		req.Header.Set(csrfHeaderName, token)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func decodeUser(t *testing.T, rec *httptest.ResponseRecorder) models.User {
	t.Helper()

	var user models.User
	if err := json.Unmarshal(rec.Body.Bytes(), &user); err != nil {
		t.Fatalf("разобрать ответ: %v (тело: %s)", err, rec.Body.String())
	}
	return user
}

func login(t *testing.T, h *Handlers, identifier, password, remoteAddr string) []*http.Cookie {
	t.Helper()

	body, err := json.Marshal(map[string]string{"identifier": identifier, "password": password})
	if err != nil {
		t.Fatalf("собрать запрос: %v", err)
	}

	rec := callLogin(h, string(body), remoteAddr, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("вход %s: код %d, тело %s", identifier, rec.Code, rec.Body.String())
	}
	return rec.Result().Cookies()
}

// csrfFrom — токен из cookie, который сервер теперь выдаёт вместе с сессией.
func csrfFrom(cookies []*http.Cookie) string {
	for _, c := range cookies {
		if c.Name == csrfCookieName {
			return c.Value
		}
	}
	return ""
}

func TestRegisterCreatesStudentAndIssuesCSRF(t *testing.T) {
	h := newTestHandlers(t)

	rec := callRegister(h, `{"username":"nova","email":"nova@example.test","full_name":"Nova T","password":"secret12345","role":"student"}`, "10.0.0.1:1234")
	if rec.Code != http.StatusCreated {
		t.Fatalf("ожидался 201, получили %d: %s", rec.Code, rec.Body.String())
	}

	user := decodeUser(t, rec)
	if user.Role != "student" {
		t.Fatalf("роль после публичной регистрации %q, ожидался student", user.Role)
	}

	// Дефект из docs/04: без csrf-cookie первый POST после регистрации давал 403.
	if csrfFrom(rec.Result().Cookies()) == "" {
		t.Fatal("вместе с сессией не выдан csrf_token — первый POST вернёт 403")
	}
}

func TestRegisterCannotPickTeacherRole(t *testing.T) {
	h := newTestHandlers(t)

	rec := callRegister(h, `{"username":"mallory","email":"mallory@example.test","full_name":"Mallory","password":"secret12345","role":"teacher"}`, "10.0.0.2:1234")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("роль из публичной регистрации должна отклоняться, код %d: %s", rec.Code, rec.Body.String())
	}

	if _, err := h.Users.GetByIdentifier(context.Background(), "mallory"); err == nil {
		t.Fatal("пользователь с ролью teacher всё-таки создан")
	}
}

func TestRegisterWithoutRoleDefaultsToStudent(t *testing.T) {
	h := newTestHandlers(t)

	rec := callRegister(h, `{"username":"quiet","email":"quiet@example.test","full_name":"Quiet","password":"secret12345"}`, "10.0.0.3:1234")
	if rec.Code != http.StatusCreated {
		t.Fatalf("ожидался 201, получили %d: %s", rec.Code, rec.Body.String())
	}
	if got := decodeUser(t, rec).Role; got != "student" {
		t.Fatalf("роль по умолчанию %q", got)
	}
}

func TestLoginRejectsUnknownUserAndWrongPassword(t *testing.T) {
	h := newTestHandlers(t)
	createUser(t, h, "alice", "rightpass1", "student")

	rec := callLogin(h, `{"identifier":"nosuchuser","password":"rightpass1"}`, "10.0.1.1:1", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("неизвестный пользователь: ожидался 401, получили %d", rec.Code)
	}

	body := `{"identifier":"alice","password":"wrongpass1"}`
	rec = callLogin(h, body, "10.0.1.2:1", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("неверный пароль: ожидался 401, получили %d", rec.Code)
	}

	rec = callLogin(h, `{"identifier":"alice","password":"rightpass1"}`, "10.0.1.3:1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("верный пароль: ожидался 200, получили %d: %s", rec.Code, rec.Body.String())
	}
	if csrfFrom(rec.Result().Cookies()) == "" {
		t.Fatal("после входа не выдан csrf_token")
	}
}

func TestLoginBruteForceIsLockedPerAccount(t *testing.T) {
	h := newTestHandlers(t)
	// Маленький бакет, чтобы не гонять пять bcrypt-сравнений ради теста.
	h.Limits.LoginAccount = ratelimit.New(3, time.Hour)
	createUser(t, h, "bob", "rightpass1", "student")

	const addr = "10.0.2.9:1" // один и тот же IP: лимит должен сработать по аккаунту
	body := `{"identifier":"bob","password":"wrongpass1"}`

	for i := 1; i <= 3; i++ {
		rec := callLogin(h, body, addr, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("попытка %d: ожидался 401, получили %d", i, rec.Code)
		}
	}

	rec := callLogin(h, body, addr, nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("после исчерпания бакета ожидался 429, получили %d", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("429 обязан сопровождаться Retry-After")
	}

	// Верный пароль тоже не должен проходить, пока аккаунт под блокировкой.
	rec = callLogin(h, `{"identifier":"bob","password":"rightpass1"}`, addr, nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("блокировка обходится верным паролем: код %d", rec.Code)
	}
}

func TestSuccessfulLoginResetsFailedAttempts(t *testing.T) {
	h := newTestHandlers(t)
	h.Limits.LoginAccount = ratelimit.New(3, time.Hour)
	createUser(t, h, "carol", "rightpass1", "student")

	const addr = "10.0.3.9:1"
	wrong := `{"identifier":"carol","password":"wrongpass1"}`

	callLogin(h, wrong, addr, nil)
	callLogin(h, wrong, addr, nil)
	login(t, h, "carol", "rightpass1", addr)

	// После успешного входа бакет обнулён: три неудачи снова дают 401, а не 429.
	for i := 1; i <= 3; i++ {
		if rec := callLogin(h, wrong, addr, nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("попытка %d после успешного входа: ожидался 401, получили %d", i, rec.Code)
		}
	}
}

func TestLoginRateLimitPerIP(t *testing.T) {
	h := newTestHandlers(t)
	h.Limits.Login = ratelimit.New(3, time.Hour)

	// Разные идентификаторы — чтобы сработал именно лимит по IP, а не по аккаунту.
	for i, id := range []string{"x1", "x2", "x3"} {
		rec := callLogin(h, `{"identifier":"`+id+`","password":"whatever1"}`, "10.0.4.4:1", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("запрос %d: ожидался 401, получили %d", i+1, rec.Code)
		}
	}

	rec := callLogin(h, `{"identifier":"x4","password":"whatever1"}`, "10.0.4.4:1", nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("превышение лимита по IP: ожидался 429, получили %d", rec.Code)
	}

	// Другой IP не должен страдать за чужого.
	rec = callLogin(h, `{"identifier":"x4","password":"whatever1"}`, "10.0.5.5:1", nil)
	if rec.Code == http.StatusTooManyRequests {
		t.Fatal("лимит по IP применяется к чужому адресу")
	}
}

func TestAdminSetRolePermissionsAndRevocation(t *testing.T) {
	h := newTestHandlers(t)
	createUser(t, h, "root", "adminpass1", "admin")
	student := createUser(t, h, "dave", "studentp1", "student")

	adminCookies := login(t, h, "root", "adminpass1", "10.0.6.1:1")
	studentCookies := login(t, h, "dave", "studentp1", "10.0.6.2:1")

	// Студент — не админ.
	rec := callAdminSetRole(h, `{"user_id":`+itoa(student.ID)+`,"role":"teacher"}`, "10.0.6.2:1", studentCookies)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("студенту роль выдаётся: код %d", rec.Code)
	}

	// Админ — может, и с корректным CSRF-токеном из cookie.
	rec = callAdminSetRole(h, `{"user_id":`+itoa(student.ID)+`,"role":"teacher"}`, "10.0.6.1:1", adminCookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("админу отказано: код %d, тело %s", rec.Code, rec.Body.String())
	}
	if got := decodeUser(t, rec).Role; got != "teacher" {
		t.Fatalf("в ответе роль %q", got)
	}

	// Сессия «повышенного» отозвана — иначе права живут до истечения куки.
	me := h.RequireAuth(http.HandlerFunc(h.Me))
	rec = httptest.NewRecorder()
	me.ServeHTTP(rec, request(http.MethodGet, "/api/auth/me", "", "10.0.6.2:1", studentCookies))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("старая сессия после смены роли жива: код %d", rec.Code)
	}
}

func TestAdminSetRoleValidatesInput(t *testing.T) {
	h := newTestHandlers(t)
	createUser(t, h, "root2", "adminpass1", "admin")
	target := createUser(t, h, "erin", "studentp1", "student")
	cookies := login(t, h, "root2", "adminpass1", "10.0.7.1:1")

	cases := []struct {
		name string
		body string
		code int
	}{
		{"неизвестная роль", `{"user_id":` + itoa(target.ID) + `,"role":"dean"}`, http.StatusUnprocessableEntity},
		{"роль без user_id", `{"user_id":0,"role":"teacher"}`, http.StatusUnprocessableEntity},
		{"нет такого пользователя", `{"user_id":999999,"role":"teacher"}`, http.StatusNotFound},
		{"сам себе", `{"user_id":` + itoa(adminIDOf(t, h, "root2")) + `,"role":"student"}`, http.StatusConflict},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := callAdminSetRole(h, tc.body, "10.0.7.1:1", cookies)
			if rec.Code != tc.code {
				t.Fatalf("ожидался %d, получили %d: %s", tc.code, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestAdminSetRoleRequiresCSRF — проверка, что маршрут действительно закрыт
// double-submit'ом: без заголовка запрос от админа отклоняется.
func TestAdminSetRoleRequiresCSRF(t *testing.T) {
	h := newTestHandlers(t)
	createUser(t, h, "root3", "adminpass1", "admin")
	target := createUser(t, h, "fred", "studentp1", "student")
	cookies := login(t, h, "root3", "adminpass1", "10.0.8.1:1")

	handler := h.RequirePerm(authz.UserRoleManage, "only administrators can assign roles",
		h.RequireCSRF(http.HandlerFunc(h.AdminSetRole)))

	req := request(http.MethodPost, "/api/admin/users/role", `{"user_id":`+itoa(target.ID)+`,"role":"teacher"}`, "10.0.8.1:1", cookies)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("без X-CSRF-Token ожидался 403, получили %d", rec.Code)
	}
}

func adminIDOf(t *testing.T, h *Handlers, username string) int64 {
	t.Helper()

	user, err := h.Users.GetByIdentifier(context.Background(), username)
	if err != nil {
		t.Fatalf("найти %s: %v", username, err)
	}
	return user.ID
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
