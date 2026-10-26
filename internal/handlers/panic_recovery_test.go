package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ADR-0033: паника обработчика обязана стать обычным 500 — с конвертом
// ADR-0003, причиной в логе (ADR-0030) и строкой доступа (ADR-0025).
//
// Замер до правки: настоящий http.Server обрывал соединение (клиент — EOF,
// браузер — «нет связи с сервером»), в slog не попадало ни одной строки, а
// стек уходил в ErrorLog net/http без request id. Проверяется здесь и то, и
// другое: поведение самого Recover, поведение ответа, которое уже начало
// писаться, и то, что сборка цепочки в cmd/server действительно подключает
// middleware (root_test.go в пакете main).

// lockedWriter — буфер со своим мьютексом: в тестах на настоящем сервере
// лог пишется из горотины соединения, а читается из теста.
type lockedWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *lockedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// captureLockedLogs — то же, что captureLogs, но потокобезопасный: нужен
// тестам, где ответ идёт через настоящий сокет.
func captureLockedLogs(t *testing.T) *lockedWriter {
	t.Helper()

	writer := &lockedWriter{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(writer, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	return writer
}

// panicIndexHandler паникует так, как паникует живой код: выход за границу
// среза. Значение паники — runtime error, то есть ветка panicCause с %w.
func panicIndexHandler() http.Handler {
	return http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		var empty []int
		_ = empty[3]
	})
}

func TestPanicBeforeResponseAnswersEnvelope500(t *testing.T) {
	buffer := captureLogs(t)
	rec := httptest.NewRecorder()

	LogRequests(Recover(panicIndexHandler())).ServeHTTP(
		rec, request(http.MethodGet, "/api/courses", "", "10.0.33.1:1", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("паника ответила %d, ожидалось 500: %s", rec.Code, rec.Body.String())
	}

	body := decodeError(t, rec)
	if body.Code != ErrInternal {
		t.Errorf("код отказа %q, ожидалось %q", body.Code, ErrInternal)
	}
	if body.Message != panicFailureReason {
		t.Errorf("текст клиента %q, ожидалось %q", body.Message, panicFailureReason)
	}

	// Отладочные данные сервера наружу не уходят: в тексте паники и в стеке
	// есть имена функций и пути файлов.
	if leaked := rec.Body.String(); strings.Contains(leaked, "goroutine") ||
		strings.Contains(leaked, "index out of range") {
		t.Fatalf("в ответе утёкли стек или текст паники: %s", leaked)
	}

	headerID := rec.Header().Get(requestIDHeader)
	if headerID == "" {
		t.Fatal("ответ с 500 от паники не содержит X-Request-Id")
	}

	logged := buffer.String()
	expectOneFailureLog(t, logged, headerID, panicFailureReason, "index out of range", "stack=")

	if !strings.Contains(logged, "status=500") {
		t.Fatalf("строка доступа не поднялась до 500:\n%s", logged)
	}
	// Ровно две Error-строки — отказ с причиной и строка доступа (ADR-0030).
	// Свой Error рядом с internalError удвоил бы счётчик и выглядел бы как
	// два разных сбоя одного запроса.
	if got := strings.Count(logged, "level=ERROR"); got != 2 {
		t.Fatalf("Error-строк %d, ожидалось 2 (отказ + строка доступа):\n%s", got, logged)
	}
}

func TestPanicAfterHeadersKeepsSentStatusAndMarksStarted(t *testing.T) {
	// Два способа «ответ уже пошёл»: явный WriteHeader(201) и молчаливый
	// Write, где net/http подставляет 200 сам. Второе важнее для правды о
	// признаке headerSent: засчитывать его только по WriteHeader значило бы,
	// что паника после первой же строки тела снова уходит в WriteHeader(500).
	cases := []struct {
		name     string
		handler  http.HandlerFunc
		wantCode int
		wantBody string
	}{
		{
			name: "явный WriteHeader",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"partial":true}`))
				var empty []int
				_ = empty[2]
			},
			wantCode: http.StatusCreated,
			wantBody: `{"partial":true}`,
		},
		{
			name: "только Write",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"partial":1}`))
				var empty []int
				_ = empty[2]
			},
			wantCode: http.StatusOK,
			wantBody: `{"partial":1}`,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			buffer := captureLogs(t)
			rec := httptest.NewRecorder()

			LogRequests(Recover(testCase.handler)).ServeHTTP(
				rec, request(http.MethodPost, "/api/courses", "", "10.0.33.2:1", nil))

			// Статус уже ушёл, и второй WriteHeader — это «superfluous
			// response.WriteHeader call» в stderr и человек в логе с 500
			// там, где клиент видел 201 с оборванным телом.
			if rec.Code != testCase.wantCode {
				t.Fatalf("начатый ответ получил статус %d, ожидалось %d", rec.Code, testCase.wantCode)
			}
			if rec.Body.String() != testCase.wantBody {
				t.Fatalf("тело начатого ответа изменено: %q", rec.Body.String())
			}

			headerID := rec.Header().Get(requestIDHeader)
			if headerID == "" {
				t.Fatal("у начатого ответа нет X-Request-Id — отказ не найти в логу")
			}
			logged := buffer.String()
			expectOneFailureLog(t, logged, headerID, panicFailureReason, "index out of range",
				"response_started=true", "stack=")

			// Отказ есть, а 500 в строке доступа — нет: код остался тот, что
			// увидел клиент.
			if !strings.Contains(logged, "status="+fmt.Sprint(testCase.wantCode)) {
				t.Fatalf("строка доступа назвала другой код:\n%s", logged)
			}
			if got := strings.Count(logged, "level=ERROR"); got != 1 {
				t.Fatalf("Error-строк %d, ожидалось 1 (отказ; доступ не поднимается до Error):\n%s", got, logged)
			}
		})
	}
}

func TestPanicValueWithoutErrorTypeIsStillNamed(t *testing.T) {
	cases := []struct {
		name  string
		raise func()
		want  string
	}{
		{
			name:  "строка",
			raise: func() { panic("источник вернул меньше строк, чем ждали") },
			want:  "паника: источник вернул меньше строк, чем ждали",
		},
		{
			name:  "ошибка",
			raise: func() { panic(fmt.Errorf("нет столбца %s", "gid")) },
			want:  "паника: нет столбца gid",
		},
		{
			name:  "структура",
			raise: func() { panic(struct{ code int }{code: 7}) },
			want:  "паника: {7}",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			buffer := captureLogs(t)
			rec := httptest.NewRecorder()

			handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { testCase.raise() })
			LogRequests(Recover(handler)).ServeHTTP(
				rec, request(http.MethodGet, "/api/courses", "", "10.0.33.3:1", nil))

			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("паника %q ответила %d", testCase.name, rec.Code)
			}
			logged := buffer.String()
			if !strings.Contains(logged, "error=\""+testCase.want+"\"") {
				t.Fatalf("в логе нет %q:\n%s", testCase.want, logged)
			}
			if strings.Contains(logged, "error=<nil>") || strings.Contains(logged, "error=\"паника: \"") {
				t.Fatalf("значение паники потерялось при превращении в ошибку:\n%s", logged)
			}
		})
	}
}

func TestRecoverAnswers500OverRealTransport(t *testing.T) {
	buffer := captureLockedLogs(t)
	serverLog := &lockedWriter{}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("слушатель: %v", err)
	}

	// Та же цепочка, что собирает cmd/server: без WithDBDeadline тест
	// проверял бы набор функций, а не то, как сервер их ставит.
	server := &http.Server{
		Handler:  LogRequests(Recover(WithDBDeadline(panicIndexHandler()))),
		ErrorLog: log.New(serverLog, "", 0),
	}
	go func() {
		// Ошибка Serve здесь не интересна: Shutdown возвращает
		// http.ErrServerClosed, а настоящая поломка видна ниже — как missing
		// или неверный ответ. Писать в t из уходящей горотины нельзя.
		_ = server.Serve(listener)
	}()

	resp, err := http.Get("http://" + listener.Addr().String() + "/api/courses")
	if err != nil {
		t.Fatalf("клиент не получил ответа: %v — именно это было до ADR-0033", err)
	}
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		t.Fatalf("прочитать тело: %v", readErr)
	}

	// Ответ получен, соединение живо, стек в ErrorLog больше не нужен — но он
	// нужен кому-то, кто читает лог: проверка, что отказ виден с request id.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("остановить сервер: %v", err)
	}

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("код %d, ожидалось 500: %s", resp.StatusCode, string(body))
	}
	if resp.Header.Get(requestIDHeader) == "" {
		t.Errorf("в настоящем ответе нет %s", requestIDHeader)
	}

	var envelope struct {
		Error errorBody `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("ответ не является конвертом ADR-0003: %v (%s)", err, body)
	}
	if envelope.Error.Code != ErrInternal || envelope.Error.Message != panicFailureReason {
		t.Fatalf("конверт отказался описывать сбой: %+v", envelope.Error)
	}

	if logged := serverLog.String(); logged != "" {
		t.Errorf("net/http по-прежнему ловит панику сам (стек без request id):\n%s", logged)
	}

	logged := buffer.String()
	for _, want := range []string{
		`msg="отказ сервера"`,
		`reason="` + panicFailureReason + `"`,
		"error=\"паника: runtime error: index out of range",
		"stack=",
		"status=500",
		"level=ERROR",
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("в slog-логе нет %q:\n%s", want, logged)
		}
	}
}

func TestRecoverLeavesSuccessfulRequestAlone(t *testing.T) {
	// Middleware не должен ни менять ответ, который собран нормально, ни
	// добавлять в лог строк: «отказ сервера» у успешного запроса — это
	// фантом, по которому ищут несуществующую аварию.
	buffer := captureLogs(t)
	rec := httptest.NewRecorder()

	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "no such course")
	})

	LogRequests(Recover(ok)).ServeHTTP(
		rec, request(http.MethodGet, "/api/courses/999", "", "10.0.33.4:1", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("код %d, ожидалось 404", rec.Code)
	}
	logged := buffer.String()
	if strings.Contains(logged, `msg="отказ сервера"`) {
		t.Fatalf("у успешного запроса появилась строка отказа:\n%s", logged)
	}
	if strings.Contains(logged, "level=ERROR") {
		t.Fatalf("у успешного запроса появился Error:\n%s", logged)
	}
}
