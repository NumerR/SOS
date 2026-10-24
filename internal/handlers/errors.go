package handlers

import (
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
