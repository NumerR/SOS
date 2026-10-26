package handlers

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
)

// ADR-0033: паника обработчика обязана остаться обычным 500, а не оборвать
// соединение.
//
// До этого шага recover() в дереве не было вовсе, и панику ловил net/http.
// Замер до правки (настоящий http.Server и настоящий клиент): ответ — EOF, то
// есть браузер показывает «нет связи с сервером» там, где сервер живой; в slog
// — ни строки доступа (ADR-0025), ни причины (ADR-0030); X-Request-Id теряется
// вместе с незаписанными заголовками; стек уходит в http.Server.ErrorLog без
// request id, поэтому жалобу человека нельзя связать ни с какой записью.
//
// Что именно паникуется в обработчике — вопрос отдельный и от этого middleware
// не зависит: разбор ответа источника, работа со слайсами, порядок сборки
// маршрутов. Здесь решается только то, что получают человек и лог, когда
// паника всё-таки случилась.

// panicFailureReason — текст клиента и он же `reason` в логу (ADR-0030 требует
// совпадения): по этой фразе отказ находится в логу по продиктованному
// человеку сообщению.
//
// Ни текста паники, ни стека клиент не получает: в них имена функций, пути
// файлов и значения аргументов — это отладка сервера, а не описание проблемы
// пользователя, и наружу им делать нечего.
const panicFailureReason = "the server failed to finish this request"

// responseStarter — умение сказать «заголовки ответа уже ушли». Реализует
// statusRecorder из LogRequests. Без этого вопроса Recover звал бы
// WriteHeader(500) после отправленного ответа: net/http печатал бы
// «superfluous response.WriteHeader call» в свой ErrorLog, человек в логе
// видел бы исходный код (обычно 200), а тело его ответа осталось бы
// оборванным — то есть отказ снова стал бы невидимым.
type responseStarter interface {
	headerSent() bool
}

// Recover превращает панику нижележащего обработчика в 500 через internalError.
//
// Место в цепочке задаёт всё (см. rootHandler в cmd/server): внутри LogRequests,
// иначе строка доступа не увидит итоговый статус и не поднимет его до Error;
// снаружи WithDBDeadline, чтобы закрыть и панику того же middleware, а не только
// обработчика.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			panicked := recover()
			if panicked == nil {
				return
			}

			cause := panicCause(panicked)
			// Стек — в лог, одним полем той же строки: две записи об одном
			// отказе (ADR-0030 красит их счётчик) выглядели бы как два сбоя.
			stack := slog.String("stack", string(debug.Stack()))

			if starter, ok := w.(responseStarter); ok && starter.headerSent() {
				// Статус менять поздно: он уже ушёл. Молчать всё равно хуже,
				// чем записать отказ с request id.
				logFailure(r, panicFailureReason, cause, stack, slog.Bool("response_started", true))
				return
			}

			internalError(w, r, panicFailureReason, cause, stack)
		}()

		next.ServeHTTP(w, r)
	})
}

// panicCause приводит значение паники к ошибке. Паниковать в Go может чем
// угодно, а поле error= обязано читаться и для runtime error
// («index out of range [3] with length 0»), и для panic("строка"): у второй
// ветки типа error нет, и без %v в логе осталось бы только «паника: ».
func panicCause(panicked any) error {
	if err, ok := panicked.(error); ok {
		return fmt.Errorf("паника: %w", err)
	}
	return fmt.Errorf("паника: %v", panicked)
}
