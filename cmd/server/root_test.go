package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

// Тесты сборки цепочки (ADR-0033).
//
// Поведение Recover проверяется в internal/handlers. Здесь проверяется другое:
// что живой сервер эту обёртку действительно получает. Middleware, который
// никто не подключил, ведёт себя ровно как его отсутствие, а тесты пакета
// handlers про main ничего не знают.
//
// newTestServer (modules_test.go) поднимает голый маршрутизатор без обёрток —
// поэтому эти тесты собирают цепочку сами, через rootHandler, и идут через
// настоящий сервер с настоящим клиентом: обрыв соединения виден только там.

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *lockedBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *lockedBuffer) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func panicHandler() http.Handler {
	return http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		var empty []int
		_ = empty[3]
	})
}

func TestRootHandlerAnswersPanicWithEnvelope(t *testing.T) {
	logged := &lockedBuffer{}
	netHTTPLog := &lockedBuffer{}

	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logged, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	srv := httptest.NewUnstartedServer(rootHandler(panicHandler()))
	srv.Config.ErrorLog = log.New(netHTTPLog, "", 0)
	srv.Start()
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/courses")
	if err != nil {
		t.Fatalf("клиент не получил ответа: %v — так было до ADR-0033", err)
	}
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		t.Fatalf("прочитать ответ: %v", readErr)
	}

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("код %d, ожидалось 500: %s", resp.StatusCode, body)
	}
	requestID := resp.Header.Get("X-Request-Id")
	if requestID == "" {
		t.Fatal("в ответе нет X-Request-Id: отказ нельзя связать с логом")
	}

	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("ответ не является конвертом ADR-0003: %v (%s)", err, body)
	}
	if envelope.Error.Code != "internal" {
		t.Errorf("код отказа %q, ожидалось internal", envelope.Error.Code)
	}
	if !strings.Contains(envelope.Error.Message, "failed") {
		t.Errorf("текст отказа %q не объясняет человеку, что случилось", envelope.Error.Message)
	}
	if strings.Contains(string(body), "index out of range") || strings.Contains(string(body), "goroutine") {
		t.Errorf("в ответе утёкли отладочные данные сервера: %s", body)
	}

	text := logged.String()
	for _, want := range []string{
		`msg="отказ сервера"`,
		"error=\"паника: runtime error: index out of range",
		"stack=",
		"request_id=" + requestID,
		"status=500",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("в логе нет %q:\n%s", want, text)
		}
	}
	if strings.Count(text, "level=ERROR") != 2 {
		t.Errorf("Error-строк %d, ожидалось 2 (отказ + строка доступа):\n%s",
			strings.Count(text, "level=ERROR"), text)
	}

	// Стек обязан уйти в slog с request id, а не в ErrorLog net/http без него.
	if out := netHTTPLog.String(); strings.Contains(out, "panic serving") {
		t.Errorf("панику снова ловит net/http (в логе нет request id):\n%s", out)
	}
}

func TestServerChainIsBuiltInOnePlace(t *testing.T) {
	// Единственная точка сборки цепочки — rootHandler. Если в main появится
	// второй `handlers.LogRequests(...)`, живой сервер и тест разойдутся, и
	// ровно этот сценарий (обёртка есть, но не подключена) проверять будет
	// некому.
	body, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("прочитать main.go: %v", err)
	}
	source := string(body)

	for _, call := range []string{
		"handlers.LogRequests(",
		"handlers.Recover(",
		"handlers.WithDBDeadline(",
	} {
		if got := strings.Count(source, call); got != 1 {
			t.Errorf("вызовов %q в main.go %d, ожидался 1 (внутри rootHandler)", call, got)
		}
	}

	// Дальше проверять «root передан в http.Server» не нужно: неиспользуемая
	// переменная не соберётся, а подменить её на голый mux этот же счётчик
	// ловит по rootHandler(mux).
	if got := strings.Count(source, "rootHandler(mux)"); got != 1 {
		t.Errorf("rootHandler(mux) встречается %d раз, ожидался 1", got)
	}
}
