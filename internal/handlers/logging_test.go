package handlers

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Тесты middleware T0.3: request id + строка доступа, и дедлайн на обращения
// к базе.

// captureLogs подменяет глобальный slog-логгер буфером и возвращает его.
// Возврат предыдущего логгера — через t.Cleanup, иначе тесты, которые идут
// позже, пишутся в чужой буфер.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()

	buffer := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	return buffer
}

func TestLogRequestsIssuesRequestID(t *testing.T) {
	buffer := captureLogs(t)

	var fromContext string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fromContext = RequestID(r.Context())
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("нет"))
	})

	rec := httptest.NewRecorder()
	LogRequests(inner).ServeHTTP(rec, request(http.MethodGet, "/api/auth/me", "", "10.0.0.5:1", nil))

	headerID := rec.Header().Get(requestIDHeader)
	if headerID == "" {
		t.Fatal("ответ не содержит X-Request-Id")
	}
	if fromContext != headerID {
		t.Fatalf("request id в контексте (%q) не совпадает с заголовком (%q)", fromContext, headerID)
	}

	// «нет» — 3 символа, но 6 байт: счётчик байтовый, как и Content-Length.
	logged := buffer.String()
	for _, want := range []string{"request_id=" + headerID, "status=404", "path=/api/auth/me", "method=GET", "bytes=6"} {
		if !strings.Contains(logged, want) {
			t.Fatalf("в строке доступа нет %q:\n%s", want, logged)
		}
	}

	// 4xx — Warn: такие строки должны быть видны даже на уровне info.
	if !strings.Contains(logged, "level=WARN") {
		t.Fatalf("ошибка 4xx не залогирована как warn:\n%s", logged)
	}
}

func TestLogRequestsIgnoresUntrustedRequestID(t *testing.T) {
	captureLogs(t)
	SetTrustProxy(false)
	t.Cleanup(func() { SetTrustProxy(false) })

	rec := httptest.NewRecorder()
	req := request(http.MethodGet, "/api/health", "", "10.0.0.6:1", nil)
	req.Header.Set(requestIDHeader, "id-iz-zagolovka")

	LogRequests(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})).ServeHTTP(rec, req)

	if got := rec.Header().Get(requestIDHeader); got == "id-iz-zagolovka" {
		t.Fatal("прокси-заголовок подменён клиентом: без TRUST_PROXY id должен генерироваться")
	}
}

func TestLogRequestsReusesTrustedProxyRequestID(t *testing.T) {
	captureLogs(t)
	SetTrustProxy(true)
	t.Cleanup(func() { SetTrustProxy(false) })

	rec := httptest.NewRecorder()
	req := request(http.MethodGet, "/api/health", "", "10.0.0.7:1", nil)
	req.Header.Set(requestIDHeader, "upstream-42")

	var fromContext string
	LogRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fromContext = RequestID(r.Context())
	})).ServeHTTP(rec, req)

	if rec.Header().Get(requestIDHeader) != "upstream-42" || fromContext != "upstream-42" {
		t.Fatalf("за задоверенным прокси id не подхватился: заголовок %q, контекст %q",
			rec.Header().Get(requestIDHeader), fromContext)
	}
}

func TestLogRequestsRejectsHostileProxyRequestID(t *testing.T) {
	captureLogs(t)
	SetTrustProxy(true)
	t.Cleanup(func() { SetTrustProxy(false) })

	// Перевод строки в id — это лог-инъекция: в JSON-логе она бы разбила
	// запись, в текстовом — добавила фальшивую строку.
	for _, hostile := range []string{"a\nfake log line", strings.Repeat("x", maxRequestIDLength+1)} {
		rec := httptest.NewRecorder()
		req := request(http.MethodGet, "/api/health", "", "10.0.0.8:1", nil)
		// httptest.NewRequest не даёт перевод строки в заголовке (ValueError),
		// поэтому ставим напрямую — именно так выглядит «сырой» заголовок от
		// клиента, который прошёл через слабый прокси.
		req.Header[requestIDHeader] = []string{hostile}

		LogRequests(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})).ServeHTTP(rec, req)

		if got := rec.Header().Get(requestIDHeader); got == hostile || got == "" {
			t.Fatalf("чужой id %q прошёл как есть (ответ %q)", hostile, got)
		}
	}
}

func TestWithDBDeadlineBoundsAndCancelsContext(t *testing.T) {
	var (
		hasDeadline bool
		remaining   time.Duration
		ctx         context.Context
	)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx = r.Context()
		deadline, ok := ctx.Deadline()
		hasDeadline = ok
		remaining = time.Until(deadline)
	})

	WithDBDeadline(inner).ServeHTTP(httptest.NewRecorder(), request(http.MethodGet, "/api/auth/me", "", "10.0.0.9:1", nil))

	if !hasDeadline {
		t.Fatal("у контекста запроса нет дедлайна — обращения к базе не ограничены")
	}
	if remaining <= 0 || remaining > dbRequestTimeout+time.Second {
		t.Fatalf("дедлайн вне ожидаемого окна: осталось %v при лимите %v", remaining, dbRequestTimeout)
	}

	// Обработчик вернулся — контекст обязан быть снят, иначе соединение и
	// таймер живут после запроса.
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("контекст не отменён после завершения обработчика")
	}
}

func TestRequestIDIsEmptyWithoutMiddleware(t *testing.T) {
	if got := RequestID(context.Background()); got != "" {
		t.Fatalf("без middleware ожидалась пустая строка, получили %q", got)
	}
	if got := RequestID(nil); got != "" {
		t.Fatalf("RequestID(nil) должен молча отдавать пусто, получили %q", got)
	}
}
