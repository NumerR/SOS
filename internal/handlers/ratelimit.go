package handlers

import (
	"log"
	"net"
	"net/http"
	"strconv"
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
}

const (
	loginIPCapacity      = 20
	loginIPWindow        = 10 * time.Minute
	loginAccountCapacity = 5
	loginAccountWindow   = 15 * time.Minute
	registerIPCapacity   = 10
	registerIPWindow     = time.Hour
	loginAccountPenalty  = 2 // неудачная проверка пароля стоит дороже одного запроса
)

// NewLimiters собирает наборы ограничений со значениями по умолчанию.
func NewLimiters() Limiters {
	return Limiters{
		Login:        ratelimit.New(loginIPCapacity, loginIPWindow),
		LoginAccount: ratelimit.New(loginAccountCapacity, loginAccountWindow),
		Register:     ratelimit.New(registerIPCapacity, registerIPWindow),
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

// LimitByIP — ключом служит адрес соединения.
//
// За реверс-прокси это адрес прокси, и тогда нужен X-Forwarded-For + явный
// список доверенных прокси; пока приложение наружу не публикуется, оставляем
// честное «берём то, что даёт TCP» вместо ложного доверия заголовку.
func LimitByIP(r *http.Request) string {
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
