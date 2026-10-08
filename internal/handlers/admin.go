package handlers

import "net/http"

// AdminUsers возвращает список всех пользователей.
// Маршрут защищён middleware RequireRole("admin"), поэтому
// здесь роль уже проверена — остаётся только отдать данные.
func (h *Handlers) AdminUsers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}

	users, err := h.Users.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load users")
		return
	}

	writeJSON(w, http.StatusOK, users)
}
