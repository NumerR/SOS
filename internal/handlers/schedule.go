package handlers

import (
	"net/http"
	"strconv"
	"time"
)

// SchedulePage отдаёт страницу расписания. Публична: витрина SOS показывает
// официальное расписание и гостям; персональная лента внутри страницы
// подгружается через /api/schedule и требует авторизации.
func (h *Handlers) SchedulePage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	serveWebFile(w, r, "schedule.html")
}

// ScheduleFeed — GET /api/schedule?from=&to= : агрегирующая лента дедлайнов.
// Диапазон в unix; по умолчанию — текущая неделя (сегодня 00:00 .. +7 дней).
func (h *Handlers) ScheduleFeed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}

	user := h.currentUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	from, to := defaultWeekRange()
	if v := r.URL.Query().Get("from"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			from = n
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			to = n
		}
	}
	if to < from {
		to = from
	}

	items, err := h.Schedule.Feed(r.Context(), user, from, to)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build schedule feed")
		return
	}

	writeJSON(w, http.StatusOK, items)
}

// defaultWeekRange возвращает [сегодня 00:00, +7 дней 23:59:59] в unix
// по локальному времени сервера.
func defaultWeekRange() (int64, int64) {
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	end := start.AddDate(0, 0, 7).Add(-time.Second)
	return start.Unix(), end.Unix()
}
