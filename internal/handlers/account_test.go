package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/NumerR/SOS/internal/store"
)

// Тесты остатка T0.2: «выйти везде», смена пароля, отзыв чужих сессий и
// очистка протухших. Хелперы (request, login, csrfFrom, newTestHandlers)
// общие с auth_test.go.

// callAsUser повторяет цепочку middleware из main.go для защищённых POST
// эндпоинтов аккаунта: RequireAuth -> RequireCSRF -> обработчик.
func callAsUser(h *Handlers, handler http.Handler, target, body string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	wrapped := h.RequireAuth(h.RequireCSRF(handler))

	req := request(http.MethodPost, target, body, "10.0.9.9:1", cookies)
	if token := csrfFrom(cookies); token != "" {
		req.Header.Set(csrfHeaderName, token)
	}

	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	return rec
}

func sessionCookieOf(cookies []*http.Cookie) string {
	for _, c := range cookies {
		if c.Name == sessionCookieName {
			return c.Value
		}
	}
	return ""
}

func TestLogoutAllRevokesEverySession(t *testing.T) {
	h := newTestHandlers(t)
	createUser(t, h, "multi", "devicepass1", "student")

	first := login(t, h, "multi", "devicepass1", "10.0.9.1:1")
	second := login(t, h, "multi", "devicepass1", "10.0.9.2:1")

	if sessionCookieOf(first) == "" || sessionCookieOf(second) == "" {
		t.Fatal("вход не вернул gs_session — тест не о чём")
	}

	rec := callAsUser(h, http.HandlerFunc(h.LogoutAll), "/api/auth/logout-all", `{}`, first)
	if rec.Code != http.StatusOK {
		t.Fatalf("ожидался 200, получили %d: %s", rec.Code, rec.Body.String())
	}

	ctx := context.Background()

	// Проверяем обе сессии, а не только ту, с которой пришёл запрос: смысл
	// кнопки «выйти везде» в чужих устройствах.
	for name, cookies := range map[string][]*http.Cookie{"текущая": first, "другое устройство": second} {
		if _, err := h.Sessions.Get(ctx, sessionCookieOf(cookies)); err != store.ErrNotFound {
			t.Fatalf("%s: сессия жива после logout-all (ошибка %v)", name, err)
		}
	}

	// Кука снимается и в ответе: иначе фронтенд ещё один запрос сделает
	// с мёртвой сессией.
	cleared := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("ответ не снял gs_session (MaxAge < 0)")
	}
}

func TestLogoutAllRequiresAuth(t *testing.T) {
	h := newTestHandlers(t)

	handler := h.RequireAuth(h.RequireCSRF(http.HandlerFunc(h.LogoutAll)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, request(http.MethodPost, "/api/auth/logout-all", `{}`, "10.0.10.1:1", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("без сессии ожидался 401, получили %d", rec.Code)
	}
}

func TestChangePasswordKeepsCurrentSessionAndRevokesOthers(t *testing.T) {
	h := newTestHandlers(t)
	createUser(t, h, "rotator", "oldpassword1", "student")

	current := login(t, h, "rotator", "oldpassword1", "10.0.11.1:1")
	other := login(t, h, "rotator", "oldpassword1", "10.0.11.2:1")

	body := `{"current_password":"oldpassword1","new_password":"newpassword2"}`
	rec := callAsUser(h, http.HandlerFunc(h.ChangePassword), "/api/auth/change-password", body, current)
	if rec.Code != http.StatusOK {
		t.Fatalf("ожидался 200, получили %d: %s", rec.Code, rec.Body.String())
	}

	var answer struct {
		Status          string `json:"status"`
		RevokedSessions int64  `json:"revoked_sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatalf("разобрать ответ: %v (%s)", err, rec.Body.String())
	}
	if answer.RevokedSessions != 1 {
		t.Fatalf("отозвано сессий %d, ожидался 1 (текущая сохраняется)", answer.RevokedSessions)
	}

	ctx := context.Background()
	if _, err := h.Sessions.Get(ctx, sessionCookieOf(current)); err != nil {
		t.Fatalf("текущая сессия отозвана: %v", err)
	}
	if _, err := h.Sessions.Get(ctx, sessionCookieOf(other)); err != store.ErrNotFound {
		t.Fatalf("чужая сессия жива после смены пароля (ошибка %v)", err)
	}

	// Старый пароль больше не работает, новый — работает.
	if rec := callLogin(h, `{"identifier":"rotator","password":"oldpassword1"}`, "10.0.11.3:1", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("старый пароль принял вход, код %d", rec.Code)
	}
	if rec := callLogin(h, `{"identifier":"rotator","password":"newpassword2"}`, "10.0.11.4:1", nil); rec.Code != http.StatusOK {
		t.Fatalf("новый пароль не принял вход, код %d: %s", rec.Code, rec.Body.String())
	}
}

func TestChangePasswordRejectsWrongCurrentPassword(t *testing.T) {
	h := newTestHandlers(t)
	createUser(t, h, "guarded", "rightpassword1", "student")
	cookies := login(t, h, "guarded", "rightpassword1", "10.0.12.1:1")

	body := `{"current_password":"guess","new_password":"attackerpass1"}`
	rec := callAsUser(h, http.HandlerFunc(h.ChangePassword), "/api/auth/change-password", body, cookies)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("ожидался 401, получили %d: %s", rec.Code, rec.Body.String())
	}

	if _, err := h.Sessions.Get(context.Background(), sessionCookieOf(cookies)); err != nil {
		t.Fatalf("неудачная попытка должна была оставить сессию: %v", err)
	}

	// Пароль остался прежним.
	if rec := callLogin(h, `{"identifier":"guarded","password":"rightpassword1"}`, "10.0.12.2:1", nil); rec.Code != http.StatusOK {
		t.Fatalf("старый пароль перестал работать после неудачной смены, код %d", rec.Code)
	}
}

func TestChangePasswordValidatesInput(t *testing.T) {
	h := newTestHandlers(t)
	createUser(t, h, "picky", "oldpassword1", "student")
	cookies := login(t, h, "picky", "oldpassword1", "10.0.13.1:1")

	cases := []struct {
		name string
		body string
		code int
	}{
		{"новый короче 8 символов", `{"current_password":"oldpassword1","new_password":"short"}`, http.StatusUnprocessableEntity},
		{"совпадает со старым", `{"current_password":"oldpassword1","new_password":"oldpassword1"}`, http.StatusUnprocessableEntity},
		{"пустой текущий", `{"current_password":"","new_password":"brandnewpass1"}`, http.StatusUnauthorized},
		{"лишнее поле", `{"current_password":"oldpassword1","new_password":"brandnewpass1","role":"admin"}`, http.StatusBadRequest},
		{"не разбирать тело", `{`, http.StatusBadRequest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := callAsUser(h, http.HandlerFunc(h.ChangePassword), "/api/auth/change-password", tc.body, cookies)
			if rec.Code != tc.code {
				t.Fatalf("ожидался %d, получили %d: %s", tc.code, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestChangePasswordRequiresCSRF(t *testing.T) {
	h := newTestHandlers(t)
	createUser(t, h, "csrfcase", "oldpassword1", "student")
	cookies := login(t, h, "csrfcase", "oldpassword1", "10.0.14.1:1")

	handler := h.RequireAuth(h.RequireCSRF(http.HandlerFunc(h.ChangePassword)))

	req := request(http.MethodPost, "/api/auth/change-password", `{"current_password":"oldpassword1","new_password":"brandnewpass1"}`, "10.0.14.1:1", cookies)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("без X-CSRF-Token ожидался 403, получили %d", rec.Code)
	}
}

func TestClientIPDependsOnTrustProxy(t *testing.T) {
	defer SetTrustProxy(false)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	req.Header.Set("X-Forwarded-For", "198.51.100.9")

	SetTrustProxy(false)
	if got := ClientIP(req); got != "203.0.113.7" {
		t.Fatalf("без доверия прокси ключ должен браться из TCP, получили %q", got)
	}

	SetTrustProxy(true)
	if got := ClientIP(req); got != "198.51.100.9" {
		t.Fatalf("с TRUST_PROXY ключ должен браться из X-Forwarded-For, получили %q", got)
	}

	// Мусор в заголовке не должен превращать всех в один бакет:
	// «unknown», пустая строка и не-IP игнорируются, остаётся TCP-адрес.
	for _, bad := range []string{"unknown", "", "not-an-ip", ", ,198.51.100.9"} {
		req.Header.Set("X-Forwarded-For", bad)
		if got := ClientIP(req); got != "203.0.113.7" {
			t.Fatalf("X-Forwarded-For=%q: ожидался откат к TCP, получили %q", bad, got)
		}
	}

	// Цепочка «клиент, прокси1, прокси2» — берём первого.
	req.Header.Set("X-Forwarded-For", "198.51.100.9, 203.0.113.33")
	if got := ClientIP(req); got != "198.51.100.9" {
		t.Fatalf("из цепочки должен браться первый адрес, получили %q", got)
	}
}

func TestExpiredSessionsAreDeleted(t *testing.T) {
	h, db := newTestEnv(t)
	user := createUser(t, h, "expiry", "password1", "student")
	ctx := context.Background()

	alive, err := h.Sessions.Create(ctx, user.ID)
	if err != nil {
		t.Fatalf("создать сессию: %v", err)
	}

	// Просроченную сессию подвигаем назад прямым запросом: двигать срок
	// умением хранилища делать нельзя.
	expired, err := h.Sessions.Create(ctx, user.ID)
	if err != nil {
		t.Fatalf("создать вторую сессию: %v", err)
	}

	result, err := db.ExecContext(
		ctx,
		`UPDATE sessions SET expires_at = ? WHERE id = ?`,
		time.Now().Add(-time.Hour).Unix(),
		expired,
	)
	if err != nil {
		t.Fatalf("сдвинуть срок: %v", err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		t.Fatalf("срок сдвинут на %d строчках, ожидался 1", n)
	}

	deleted, err := h.Sessions.DeleteExpired(ctx)
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("удалено %d сессий, ожидался 1", deleted)
	}

	if _, err := h.Sessions.Get(ctx, alive); err != nil {
		t.Fatalf("живая сессия пострадала: %v", err)
	}

	active, err := h.Sessions.CountActive(ctx)
	if err != nil {
		t.Fatalf("CountActive: %v", err)
	}
	if active != 1 {
		t.Fatalf("активных %d, ожидался 1", active)
	}

	// Повторный запуск очистки ничего не удаляет и не падает: именно так
	// поведёт себя тикер, который вызовут второй раз.
	again, err := h.Sessions.DeleteExpired(ctx)
	if err != nil {
		t.Fatalf("повторный DeleteExpired: %v", err)
	}
	if again != 0 {
		t.Fatalf("повтор удалил %d сессий", again)
	}
}
