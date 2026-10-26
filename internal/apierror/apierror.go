// Package apierror — единый конверт отказа HTTP API (ADR-0003).
//
// Формат: {"error": {"code": "...", "message": "...", "fields": {...}}}.
//
// Пакет вынесен из internal/handlers потому, что отказывать клиенту приходится
// не только обработчикам. Маршрутизатор ядра (internal/platform) отвечает 405
// для метода, которого у пути нет, минуя любой обработчик; если бы конверт жил
// только в handlers, этот случай молча вернулся бы к старой плоской форме
// {"error": "текст"} — и клиент получал бы два разных контракта в зависимости
// от того, кто отказал. Здесь формат один на всех.
//
// Разделение полей: `code` читает код клиента (на него завязаны ветки в JS),
// `message` печатается человеку, `fields` заполняется только когда сервер знает,
// какое именно поле плохо, — чтобы страница подсветила конкретный input.
//
// Коды стабильны: текст можно переформулировать, код менять нельзя.
package apierror

import (
	"encoding/json"
	"net/http"
)

// Коды отказов. Каждый обязан появиться и в CodeForStatus, и в тесте на множество
// объявленных кодов (internal/handlers/errors_test.go).
const (
	// ErrInvalidJSON — тело запроса не разбирается как JSON или содержит
	// неизвестное поле. Отличается от ErrValidation: там тело корректное,
	// но значения полей плохие.
	ErrInvalidJSON = "invalid_json"
	// ErrValidation — 400/422: не прошло проверку поля или параметра.
	ErrValidation = "validation"
	// ErrUnauthorized — сессии нет или она истекла; входите заново.
	ErrUnauthorized = "unauthorized"
	// ErrForbidden — пользователь известен, но права на это действия нет.
	ErrForbidden = "forbidden"
	// ErrNotFound — ресурс или маршрута не существует.
	ErrNotFound = "not_found"
	// ErrMethod — путь существует, но для этого метода не обслуживается (405).
	ErrMethod = "method_not_allowed"
	// ErrConflict — состояние уже занято: такой логин или такая запись.
	ErrConflict = "conflict"
	// ErrRateLimited — слишком много попыток, см. Retry-After.
	ErrRateLimited = "rate_limited"
	// ErrCSRF — 403 при непройденной сверке токена: это «подкрути токен»,
	// а не «нет прав», поэтому у отказа свой код при общем статусе.
	ErrCSRF = "csrf_failed"
	// ErrSourceUnavailable — внешний источник (АПЭКС-ВУЗ) не ответил.
	ErrSourceUnavailable = "source_unavailable"
	// ErrInternal — всё остальное: сервер не справился.
	ErrInternal = "internal"
)

// Body — содержимое конверта. Экспортирован, потому что тесты и другие пакеты
// разбирают ответ именно в эту форму.
type Body struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

type envelope struct {
	Error Body `json:"error"`
}

// Write — обычный путь: код выводится из статуса. Так вызывающему коду не
// нужно выбирать код вручную, а соответствие «статус → код» живёт в одном
// месте и проверяется тестом.
func Write(w http.ResponseWriter, status int, message string) {
	WriteCode(w, status, CodeForStatus(status), message)
}

// WriteField — ошибка конкретного поля (валидация формы). Сообщение уходит
// дважды — как текст и как подпись поля: текст читает человек, поле читает
// страница.
func WriteField(w http.ResponseWriter, status int, field, message string) {
	write(w, status, Body{
		Code:    CodeForStatus(status),
		Message: message,
		Fields:  map[string]string{field: message},
	})
}

// WriteCode — когда статус не отражает суть: битый JSON и отсутствующее
// значение поля — оба 400, но для клиента это разные неисправности.
func WriteCode(w http.ResponseWriter, status int, code, message string) {
	write(w, status, Body{Code: code, Message: message})
}

func write(w http.ResponseWriter, status int, body Body) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	// Encoder, а не ручная склейка строк: сообщение может содержать кавычку
	// или перевод строки, а JSON обязан оставаться валидным.
	_ = json.NewEncoder(w).Encode(envelope{Error: body})
}

// CodeForStatus — единственная таблица соответствия. Новый статус без строки
// здесь получит ErrInternal: это хуже правильной классификации, но не врёт
// клиенту и не роняет ответ.
func CodeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return ErrValidation
	case http.StatusUnauthorized:
		return ErrUnauthorized
	case http.StatusForbidden:
		return ErrForbidden
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusMethodNotAllowed:
		return ErrMethod
	case http.StatusConflict:
		return ErrConflict
	case http.StatusUnprocessableEntity:
		return ErrValidation
	case http.StatusTooManyRequests:
		return ErrRateLimited
	case http.StatusBadGateway, http.StatusServiceUnavailable:
		return ErrSourceUnavailable
	default:
		return ErrInternal
	}
}
