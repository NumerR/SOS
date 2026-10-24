package handlers

import (
	"context"
	"net/http"
	"time"
)

// dbRequestTimeout — сколько запрос имеет права ждать базу.
//
// Почему на уровне всего запроса, а не вокруг каждого store.*: вариантов
// обращения к БД в обработчиках десятки, и обёртывать каждый — это сотни
// строк шаблонного кода, в котором легко пропустить один вызов. Здесь же
// дедлайн получает контекст запроса, а все обработчики передают r.Context()
// в хранилище как уже делали. Пропустить вызов теперь нельзя структурно.
//
// 5 секунд: обычный API-ответ здесь — одиночный индексированный SELECT,
// десятки миллисекунд. Всё, что дольше, либо вешает воркер, либо держит
// соединение, и ждать его смысла нет.
const dbRequestTimeout = 5 * time.Second

// WithDBDeadline ограничивает время жизни контекста запроса, а значит и всех
// обращений к базе внутри него.
//
// Отмена идёт в стандартную ошибку database/sql, которую обработчики уже
// умеют отдавать как 500 — отдельной ветки обработки не появляется.
func WithDBDeadline(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), dbRequestTimeout)
		defer cancel()

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
