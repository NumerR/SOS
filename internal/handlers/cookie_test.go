package handlers

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/NumerR/SOS/internal/store"
)

// Тесты имени сессионной куки (T0.3): на HTTPS — с префиксом __Host-,
// на локальном HTTP — обычное имя.

func httpsRequest() *http.Request {
	req := request(http.MethodPost, "/api/auth/login", "", "10.0.5.5:1", nil)
	req.TLS = &tls.ConnectionState{}
	return req
}

func cookiesOf(rec *httptest.ResponseRecorder) []*http.Cookie {
	return rec.Result().Cookies()
}

func findCookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, cookie := range cookiesOf(rec) {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

func TestSetSessionCookieUsesHostPrefixOnHTTPS(t *testing.T) {
	rec := httptest.NewRecorder()

	setSessionCookie(rec, httpsRequest(), "sess-1")

	cookie := findCookie(rec, secureSessionCookieName)
	if cookie == nil {
		t.Fatalf("на HTTPS кука должна выпускаться как %s, получили %v", secureSessionCookieName, cookiesOf(rec))
	}
	if findCookie(rec, sessionCookieName) != nil {
		t.Fatal("на HTTPS не должно оставаться незащищённого имени gs_session")
	}

	// Три требования браузера к префиксу __Host- — проверяем все, иначе
	// браузер тихо выбросит куку и «working locally» кончится на деплое.
	if !cookie.Secure {
		t.Error("__Host- кука обязана быть Secure")
	}
	if cookie.Path != "/" {
		t.Errorf("Path должен быть /, получился %q", cookie.Path)
	}
	if cookie.Domain != "" {
		t.Errorf("Domain должен быть пустым, получился %q", cookie.Domain)
	}
	if !cookie.HttpOnly {
		t.Error("сессионная кука должна быть HttpOnly")
	}
	if cookie.MaxAge <= 0 {
		t.Errorf("MaxAge должен быть положительным, получился %d", cookie.MaxAge)
	}
	if cookie.MaxAge != int(store.SessionLifetime.Seconds()) {
		t.Errorf("MaxAge %d не равен времени жизни сессии %v", cookie.MaxAge, store.SessionLifetime)
	}
}

func TestSetSessionCookieKeepsPlainNameOnHTTP(t *testing.T) {
	rec := httptest.NewRecorder()

	setSessionCookie(rec, request(http.MethodPost, "/api/auth/login", "", "10.0.5.6:1", nil), "sess-2")

	cookie := findCookie(rec, sessionCookieName)
	if cookie == nil {
		t.Fatalf("на HTTP ожидалась %s", sessionCookieName)
	}
	if cookie.Secure {
		t.Error("без HTTPS кука не может быть Secure — браузер её не примет")
	}
}

func TestClearSessionCookieRemovesBothNames(t *testing.T) {
	rec := httptest.NewRecorder()

	clearSessionCookie(rec, httpsRequest())

	for _, name := range sessionCookieNames() {
		cookie := findCookie(rec, name)
		if cookie == nil {
			t.Fatalf("кука %s не снята", name)
		}
		if cookie.MaxAge >= 0 {
			t.Errorf("%s: MaxAge %d, ожидалось снятие", name, cookie.MaxAge)
		}
	}
}

func TestSessionIDFromRequestPrefersHostPrefixed(t *testing.T) {
	req := request(http.MethodGet, "/api/auth/me", "", "10.0.5.7:1", []*http.Cookie{
		{Name: sessionCookieName, Value: "plain"},
		{Name: secureSessionCookieName, Value: "host-prefixed"},
	})

	if got := sessionIDFromRequest(req); got != "host-prefixed" {
		t.Fatalf("ожидалось значение из %s, получили %q", secureSessionCookieName, got)
	}

	empty := request(http.MethodGet, "/api/auth/me", "", "10.0.5.8:1", nil)
	if got := sessionIDFromRequest(empty); got != "" {
		t.Fatalf("без куки ожидалась пустая строка, получили %q", got)
	}
}

func TestIsSecureRequestNeedsTrustForForwardedProto(t *testing.T) {
	SetTrustProxy(false)
	t.Cleanup(func() { SetTrustProxy(false) })

	forwarded := request(http.MethodGet, "/api/auth/me", "", "10.0.5.9:1", nil)
	forwarded.Header.Set("X-Forwarded-Proto", "https")

	if isSecureRequest(forwarded) {
		t.Fatal("без TRUST_PROXY заголовок X-Forwarded-Proto не должен давать Secure")
	}

	SetTrustProxy(true)
	if !isSecureRequest(forwarded) {
		t.Fatal("с TRUST_PROXY заголовок должен давать Secure — за прокси TLS терминируется там")
	}
}
