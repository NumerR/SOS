package handlers

import (
	"context"
	"net/http"

	"github.com/NumerR/SOS/internal/models"
	"github.com/NumerR/SOS/internal/store"
)

const sessionCookieName = "gs_session"

type contextKey string

const userContextKey contextKey = "user"

// Deps — зависимости обработчиков. Собираются в main.go инициализатором
// с именами полей: девять позиционных аргументов подряд легко перепутать
// местами, и компилятор это не заметит.
type Deps struct {
	Users       *store.UserStore
	Sessions    *store.SessionStore
	Courses     *store.CourseStore
	Enrollments *store.EnrollmentStore
	Assignments *store.AssignmentStore
	Submissions *store.SubmissionStore
	Schedule    *store.ScheduleStore
	Resources   *store.ResourceStore
	Events      *store.EventStore
	Limits      Limiters
}

// Handlers — HTTP-обработчики. Поля Deps доступны напрямую: h.Users, h.Sessions…
type Handlers struct {
	Deps
}

// New создаёт набор обработчиков.
func New(deps Deps) *Handlers {
	return &Handlers{Deps: deps}
}

// currentUser возвращает пользователя из контекста запроса.
func (h *Handlers) currentUser(r *http.Request) *models.User {
	if r == nil {
		return nil
	}
	user, ok := r.Context().Value(userContextKey).(*models.User)
	if !ok {
		return nil
	}
	return user
}

// authenticatedUser проверяет cookie-сессию и возвращает пользователя.
func (h *Handlers) authenticatedUser(r *http.Request) (*models.User, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return nil, false
	}

	session, err := h.Sessions.Get(r.Context(), cookie.Value)
	if err != nil {
		return nil, false
	}

	user, err := h.Users.GetByID(r.Context(), session.UserID)
	if err != nil {
		return nil, false
	}

	return user, true
}

// RequireAuth — middleware: доступ только авторизованным.
// Попутно выпускает csrf-cookie, если её ещё нет (совместимость со старыми сессиями).
func (h *Handlers) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := h.authenticatedUser(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		ensureCSRFCookie(w, r)

		ctx := context.WithValue(r.Context(), userContextKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireRole — middleware: доступ только указанным ролям.
func (h *Handlers) RequireRole(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, ok := h.authenticatedUser(r)
			if !ok {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			if !hasRole(roles, user.Role) {
				writeError(w, http.StatusForbidden, "forbidden")
				return
			}
			ctx := context.WithValue(r.Context(), userContextKey, user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func hasRole(allowed []string, role string) bool {
	for _, r := range allowed {
		if r == role {
			return true
		}
	}
	return false
}
