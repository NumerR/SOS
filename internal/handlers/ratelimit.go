package handlers

import (
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/NumerR/SOS/internal/ratelimit"
)

// Limiters — наборы ограничений для публичных эндпоинтов аутентификации.
// Пороги — константы ниже, чтобы их было видно в одном месте и можно было
// вынести в конфиг, когда появится конфигурация.
type Limiters struct {
	// Login — грубая защита от флуда по IP: проверяется до разбора тела запроса.
	Login *ratelimit.Limiter
	// LoginAccount — то же, но по идентификатору учётки: перебор пароля к одному
	// аккаунту с разных IP упирается сюда. Списание — внутри Login.
	LoginAccount *ratelimit.Limiter
	// Register — по IP: не даём набить базу мусорными аккаунтами.
	Register *ratelimit.Limiter
	// Password — по IP на POST /api/auth/change-password: попытка подобрать
	// текущий пароль внутри чужой открытой сессии так же ограничена, как вход.
	Password *ratelimit.Limiter
}

const (
	loginIPCapacity      = 20
	loginIPWindow        = 10 * time.Minute
	loginAccountCapacity = 5
	loginAccountWindow   = 15 * time.Minute
	registerIPCapacity   = 10
	registerIPWindow     = time.Hour
	passwordIPCapacity   = 6
	passwordIPWindow     = 15 * time.Minute
)

// NewLimiters собирает наборы ограничений со значениями по умолчанию.
func NewLimiters() Limiters {
	return Limiters{
		Login:        ratelimit.New(loginIPCapacity, loginIPWindow),
		LoginAccount: ratelimit.New(loginAccountCapacity, loginAccountWindow),
		Register:     ratelimit.New(registerIPCapacity, registerIPWindow),
		Password:     ratelimit.New(passwordIPCapacity, passwordIPWindow),
	}
}

// RateLimit — middleware: пропускает запрос, пока у ключа есть разрешение.
// Ответ 429 сопровождается Retry-After, чтобы клиент не повторял наугад.
func (h *Handlers) RateLimit(limiter *ratelimit.Limiter, name string, keyOf func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := keyOf(r)
			if key == "" {
				// Без ключа ограничивать нечего — не роняем вход, но и не делаем вид,
				// что защита работает: пишем в лог.
				log.Printf("ratelimit %s: не удалось определить ключ запроса, лимит пропущен", name)
				next.ServeHTTP(w, r)
				return
			}

			if !limiter.Allow(key) {
				retry := limiter.RetryAfter(key)
				if retry < time.Second {
					retry = time.Second
				}
				w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())))
				writeError(w, http.StatusTooManyRequests, "too many requests, try again later")
				log.Printf("ratelimit %s: превышен лимит для ключа %s", name, redactKey(key))
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// trustProxy decides whether X-Forwarded-For may be believed. It is set from
// TRUST_PROXY at startup in main.go — see SetTrustProxy.
var trustProxy bool

// SetTrustProxy включает доверие X-Forwarded-For и X-Forwarded-Proto.
//
// По умолчанию выключено: за заголовком может стоять кто угодно, и «доверять
// всегда» означало бы, что переборщик обходит лимиты одной строчкой в curl.
// Включать нужно только когда приложение гарантированно стоит за своим
// прокси (или когда известна сеть прокси — тогда её надо сравнивать с RemoteAddr).
func SetTrustProxy(on bool) { trustProxy = on }

// ClientIP — адрес клиента для логов и ключа лимитера.
//
// Без TRUST_PROXY это адрес TCP-соединения. С TRUST_PROXY берётся первое
// значение из X-Forwarded-For (клиент, а не промежуточные узлы) — и только
// валидное: пустой, не-IP или «unknown» из-за прокси-заглушки не должны
// превращаться в один общий бакет на всех.
func ClientIP(r *http.Request) string {
	if trustProxy {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			candidate := strings.TrimSpace(strings.Split(forwarded, ",")[0])
			if ip := net.ParseIP(candidate); ip != nil {
				return ip.String()
			}
		}
	}

	if ip, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return ip
	}

	return r.RemoteAddr
}

// redactKey не даёт логам собирать список активных логинов: оставляем
// адрес целиком (он и так в логах HTTP) и маскируем идентификатор учётки.
func redactKey(key string) string {
	if len(key) <= 4 {
		return "****"
	}
	return key[:2] + "***" + key[len(key)-2:]
}
