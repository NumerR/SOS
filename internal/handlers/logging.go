package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Логирование запросов.
//
// Без request id в логах невозможно связать «пользователь жалуются на 500» с
// конкретной строкой: сервер обслуживает десятки запросов в секунду, и запись
// «failed to load assignments» без идентификатора — это догадка. ID попадает
// и в заголовок ответа, так что пользователь может продиктовать его, а не
// «примерно в пять вечера».

const (
	requestIDHeader = "X-Request-Id"
	// Ограничивает длину чужого заголовка: id из X-Request-Id за прокси
	// переезжает в лог, а лог-инъекция переводной строкой — классическая
	// гадость в разборе логов.
	maxRequestIDLength = 64
)

const requestIDContextKey contextKey = "request_id"

// newRequestID — 8 случайных байт в hex. crypto/rand, а не time.Now:
// счётчик по времени коллидируется двумя запросами в одну наносекунду и
// позволяет подделать «чужой» id в заголовке ответа.
func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Энтропии нет только на полностью сломанной системе; вместо паники
		// возвращаем хоть какой-то уникальный идентификатор.
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

// requestIDFor берёт id из заголовка, если мы за доверенным прокси, иначе
// генерирует новый. Без TRUST_PROXY заголовок игнорируется: клиент, который
// сам себе выбирает request id, превращает лог в инструмент дезинформации.
func requestIDFor(r *http.Request) string {
	if trustProxy {
		incoming := strings.TrimSpace(r.Header.Get(requestIDHeader))
		if incoming != "" && len(incoming) <= maxRequestIDLength && isPrintableID(incoming) {
			return incoming
		}
	}
	return newRequestID()
}

func isPrintableID(value string) bool {
	for _, runeValue := range value {
		if runeValue < 0x20 || runeValue == 0x7f {
			return false
		}
	}
	return true
}

// RequestID возвращает идентификатор текущего запроса ("" — если middleware
// логирования не был подключён).
func RequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(requestIDContextKey).(string)
	return id
}

// requestLogger — slog-логгер с пришитым request id. Все записи обработчиков
// идут через него, чтобы любую строку можно было привязать к запросу.
func requestLogger(r *http.Request) *slog.Logger {
	logger := slog.Default()
	if id := RequestID(r.Context()); id != "" {
		return logger.With(slog.String("request_id", id))
	}
	return logger
}

// LogRequests — middleware: выдаёт request id, приклеивает его к контексту и
// к ответу, пишет одну строку доступа на запрос.
//
// Тело запроса и ответа в лог не попадает намеренно: там пароли при входе и
// содержимое сдач. Пишем только метаданные транспорта.
func LogRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := requestIDFor(r)
		w.Header().Set(requestIDHeader, id)

		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(recorder, r.WithContext(context.WithValue(r.Context(), requestIDContextKey, id)))

		logger := slog.Default().With(
			slog.String("request_id", id),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", recorder.status),
			slog.Int64("duration_ms", time.Since(started).Milliseconds()),
			slog.Int("bytes", recorder.bytes),
			slog.String("ip", ClientIP(r)),
		)

		switch {
		case isStaticPath(r.URL.Path):
			// Статику по умолчанию не показываем: на загрузке страницы это
			// ~10 строк против одного осмысленного запроса.
			logger.Debug("http request")
		case recorder.status >= http.StatusInternalServerError:
			logger.Error("http request")
		case recorder.status >= http.StatusBadRequest:
			logger.Warn("http request")
		default:
			logger.Info("http request")
		}
	})
}

func isStaticPath(path string) bool {
	return strings.HasPrefix(path, "/css/") ||
		strings.HasPrefix(path, "/js/") ||
		path == "/favicon.ico"
}

// statusRecorder запоминает код ответа и объём тела: сам ResponseWriter их
// не отдаёт, а без них строка доступа бесполезна.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(status int) {
	s.status = status
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Write(body []byte) (int, error) {
	written, err := s.ResponseWriter.Write(body)
	s.bytes += written
	return written, err
}

// Flush нужен, если обработчик решит стримить ответ; без проброса промежуточный
// буфер просто молча сломал бы flushing-обработчики.
func (s *statusRecorder) Flush() {
	if flusher, ok := s.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
