package handlers

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/NumerR/SOS/internal/apierror"
)

// Единый формат ошибки API (ADR-0003) живёт в internal/apierror — там конверт,
// коды и таблица «статус → код». Пакет неслучаен: тем же конвертом отвечает и
// маршрутизатор ядра, у которого отказа через обработчик не бывает вовсе (405).
//
// Здесь остаются только короткие имена, к которым привык код обработчиков:
// писать `writeFieldError(w, 422, "title", ...)` читаемее, чем нести префикс
// пакета через двести вызовов.

// Коды переэкспортируются: менять литералы в обработчиках нельзя — на них
// опирается JS-клиент.
const (
	ErrInvalidJSON       = apierror.ErrInvalidJSON
	ErrValidation        = apierror.ErrValidation
	ErrUnauthorized      = apierror.ErrUnauthorized
	ErrForbidden         = apierror.ErrForbidden
	ErrNotFound          = apierror.ErrNotFound
	ErrConflict          = apierror.ErrConflict
	ErrRateLimited       = apierror.ErrRateLimited
	ErrCSRF              = apierror.ErrCSRF
	ErrMethod            = apierror.ErrMethod
	ErrSourceUnavailable = apierror.ErrSourceUnavailable
	ErrInternal          = apierror.ErrInternal
)

// errorBody — форма конверта для тестов, разбирающих ответ. Алиас, а не копия
// структуры: расхождение полей между двумя описаниями одного ответа обнаружилось
// бы только в проде.
type errorBody = apierror.Body

// writeError — обычный путь: код выводится из статуса.
func writeError(w http.ResponseWriter, status int, message string) {
	apierror.Write(w, status, message)
}

// internalError — единственный разрешённый выход 500 (ADR-0030): причина
// уходит в лог вместе с request id, клиент получает конверт ADR-0003 с кодом
// internal и тем же текстом.
//
// До этого вызова в обработчиках было 47 мест `writeError(w, 500, …)`, и лишь
// пять из них писали в лог, что именно случилось. Строка доступа (ADR-0025,
// `logging.go:117`) поднимает такой ответ до Error и называет путь, код и
// request id — но не причину, а комментарий `internal/handlers/logging.go:18`
// обещает, что по логу можно связать жалобу человека с конкретной неудачей.
// Текст причины в поле `reason` совпадает с тем, что увидел клиент: искать
// отказ в логу можно по той же фразе, которую человек продиктовал.
func internalError(w http.ResponseWriter, r *http.Request, message string, cause error, attrs ...any) {
	fields := append([]any{slog.String("reason", message), slog.Any("error", cause)}, attrs...)
	requestLogger(r).Error("отказ сервера", fields...)
	writeError(w, http.StatusInternalServerError, message)
}

// writeFieldError — ошибка конкретного поля (валидация формы).
func writeFieldError(w http.ResponseWriter, status int, field, message string) {
	apierror.WriteField(w, status, field, message)
}

// writeCodeError — когда статус не отражает суть: например 400 за битым JSON
// и 400 за отсутствующим параметром — разные ошибки для клиента при одном
// HTTP-статусе.
func writeCodeError(w http.ResponseWriter, status int, code, message string) {
	apierror.WriteCode(w, status, code, message)
}

// codeForStatus — таблица соответствия из apierror; оставлена как обёртка,
// потому что тест проверяет её внутри пакета handlers.
func codeForStatus(status int) string {
	return apierror.CodeForStatus(status)
}

// errDependencyNotConnected — 500 без сбоя хранилища: модуль не подключён при
// сборке маршрутов (проверка `h.Audit == nil` в `AdminAudit`). Причина известна
// заранее и её обязано быть видно в логу: это конфигурация сервера, а не
// случайность конкретного запроса, и по «audit log is not available» в логе
// человек иначе стал бы искать битую базу.
var errDependencyNotConnected = errors.New("модуль не подключён при сборке сервера")
