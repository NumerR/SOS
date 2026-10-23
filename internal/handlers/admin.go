package handlers

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/NumerR/SOS/internal/store"
)

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

// SetRoleRequest — тело POST /api/admin/users/role.
type SetRoleRequest struct {
	UserID int64  `json:"user_id"`
	Role   string `json:"role"`
}

// assignableRoles — роли, которые администратор может выставить вручную.
var assignableRoles = map[string]bool{
	"student": true,
	"teacher": true,
	"admin":   true,
}

// AdminSetRole меняет роль пользователя.
//
// Это единственный путь, которым роль вообще выдаётся: публичная регистрация
// создаёт только студентов. Маршрут закрыт RequireRole("admin") и RequireCSRF,
// поэтому здесь остаётся проверить лишь корректность самих значений.
func (h *Handlers) AdminSetRole(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}

	var req SetRoleRequest
	if !readJSON(w, r, &req) {
		return
	}

	role := strings.ToLower(strings.TrimSpace(req.Role))
	if !assignableRoles[role] {
		writeError(w, http.StatusUnprocessableEntity, "role must be student, teacher or admin")
		return
	}

	if req.UserID <= 0 {
		writeError(w, http.StatusUnprocessableEntity, "user_id is required")
		return
	}

	// Самоназначение/саморазжалование: админ, меняющий собственную роль,
	// может остаться без доступа к админ-контуру посреди сеанса.
	// RequireRole уже положил пользователя в контекст, nil здесь — ошибка сборки
	// маршрута, поэтому на него не опираемся, а просто подставляем 0 в лог.
	admin := h.currentUser(r)
	if admin != nil && admin.ID == req.UserID {
		writeError(w, http.StatusConflict, "you cannot change your own role")
		return
	}

	target, err := h.Users.GetByID(r.Context(), req.UserID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load user")
		return
	}

	if target.Role == role {
		writeJSON(w, http.StatusOK, target)
		return
	}

	if err := h.Users.SetRole(r.Context(), target.ID, role); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update role")
		return
	}

	// Сессии отзываем: иначе уже входящий «преподаватель» сохранит права ещё
	// на сутки, пока жива старая кука.
	if err := h.Sessions.DeleteAllForUser(r.Context(), target.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "role updated but sessions could not be revoked")
		return
	}

	var actorID int64
	if admin != nil {
		actorID = admin.ID
	}
	log.Printf("admin: user_id=%d роль %s -> %s (изменил user_id=%d)", target.ID, target.Role, role, actorID)

	target.Role = role
	writeJSON(w, http.StatusOK, target)
}
