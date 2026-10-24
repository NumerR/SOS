package handlers

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"

	"github.com/NumerR/SOS/internal/store"
)

const (
	csrfCookieName = "csrf_token"
	csrfHeaderName = "X-CSRF-Token"
)

// generateCSRFToken создаёт криптостойкий токен (32 байта -> 64 hex-символа).
func generateCSRFToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// getCSRFCookie читает токен из cookie запроса (пустая строка, если её нет).
func getCSRFCookie(r *http.Request) string {
	c, err := r.Cookie(csrfCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// ensureCSRFCookie выпускает csrf-cookie, если в запросе её ещё нет.
// Вызывается из RequireAuth: так покрываются и пользователи, чьи сессии
// созданы до включения CSRF (сессия есть — токена нет).
func ensureCSRFCookie(w http.ResponseWriter, r *http.Request) {
	if getCSRFCookie(r) != "" {
		return
	}

	token, err := generateCSRFToken()
	if err != nil {
		// Не роняем авторизованный запрос из-за сбоя энтропии:
		// RequireCSRF на mutate-роуте просто вернёт 403, пока токен не появится.
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: false, // читается JS для заголовка — в этом суть double-submit cookie
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(store.SessionLifetime.Seconds()),
	})
}

// RequireCSRF — middleware double-submit cookie для mutate-запросов.
// Безопасные методы (GET/HEAD/OPTIONS) пропускаются без проверки: у нас нет
// GET-мутаций, поэтому вешать проверку на них = мёртвый код.
func (h *Handlers) RequireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}

		cookieToken := getCSRFCookie(r)
		headerToken := r.Header.Get(csrfHeaderName)

		if cookieToken == "" {
			writeError(w, http.StatusForbidden, "csrf token missing")
			return
		}
		if headerToken == "" || !constantTimeEqual(headerToken, cookieToken) {
			writeError(w, http.StatusForbidden, "invalid csrf token")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// clearCSRFCookie снимает csrf-cookie — вместе с отзывом сессий она больше
// не должна留在 браузере.
func clearCSRFCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: false,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// constantTimeEqual сравнивает строки за фиксированное время,
// чтобы не утекать длину/содержимое по таймингу.
func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
