package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/NumerR/SOS/internal/authz"
	"github.com/NumerR/SOS/internal/store"
)

// AdminUsers возвращает список всех пользователей.
// Маршрут защищён правом authz.UserList, поэтому
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
//
// Список совпадает со всеми ролями системы (authz.Roles()), потому что право на
// назначение роли уже отдано администратору целиком. Отдельный набор здесь был
// бы вторым местом, где перечислены роли, и он бы разъехался с authz при
// добавлении четвёртой роли — поэтому он вычислен из authz.
var assignableRoles = func() map[string]bool {
	roles := make(map[string]bool, len(authz.Roles()))
	for _, role := range authz.Roles() {
		roles[string(role)] = true
	}
	return roles
}()

// AdminSetRole меняет роль пользователя.
//
// Это единственный путь, которым роль вообще выдаётся: публичная регистрация
// создаёт только студентов. Маршрут закрыт правом authz.UserRoleManage и
// RequireCSRF, поэтому здесь остаётся проверить лишь корректность самих значений.
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
		writeFieldError(w, http.StatusUnprocessableEntity, "role", "role must be student, teacher or admin")
		return
	}

	if req.UserID <= 0 {
		writeFieldError(w, http.StatusUnprocessableEntity, "user_id", "user_id is required")
		return
	}

	// Самоназначение/саморазжалование: админ, меняющий собственную роль,
	// может остаться без доступа к админ-контуру посреди сеанса.
	// Middleware права уже положил пользователя в контекст, nil здесь — ошибка сборки
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
		h.recordAudit(r, auditWrite{
			action:     store.AuditRoleChange,
			actor:      admin,
			targetType: "user",
			targetID:   auditTargetID(target.ID),
			detail:     "from=" + target.Role + " to=" + role,
			ok:         false,
		})
		writeError(w, http.StatusInternalServerError, "failed to update role")
		return
	}

	// Сессии отзываем: иначе уже входящий «преподаватель» сохранит права ещё
	// на сутки, пока жива старая кука.
	if err := h.Sessions.DeleteAllForUser(r.Context(), target.ID); err != nil {
		h.recordAudit(r, auditWrite{
			action:     store.AuditRoleChange,
			actor:      admin,
			targetType: "user",
			targetID:   auditTargetID(target.ID),
			detail:     "from=" + target.Role + " to=" + role + ", sessions not revoked",
			ok:         false,
		})
		writeError(w, http.StatusInternalServerError, "role updated but sessions could not be revoked")
		return
	}

	var actorID int64
	if admin != nil {
		actorID = admin.ID
	}

	// Строка лога осталась намеренно: у неё есть request_id, по которому
	// смена роли привязывается к конкретному HTTP-запросу, а в audit_events
	// лежит то, что спрашивает администратор через интерфейс. Два канала — для
	// двух разных читателей (ADR-0012).
	requestLogger(r).Info("admin: роль изменена",
		slog.Int64("target_user_id", target.ID),
		slog.String("from_role", target.Role),
		slog.String("to_role", role),
		slog.Int64("actor_user_id", actorID),
		slog.String("ip", ClientIP(r)),
	)

	h.recordAudit(r, auditWrite{
		action:     store.AuditRoleChange,
		actor:      admin,
		targetType: "user",
		targetID:   auditTargetID(target.ID),
		detail:     "from=" + target.Role + " to=" + role,
		ok:         true,
	})

	target.Role = role
	writeJSON(w, http.StatusOK, target)
}
