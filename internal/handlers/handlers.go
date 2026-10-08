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

// Handlers — HTTP-обработчики.
type Handlers struct {
	Users       *store.UserStore
	Sessions    *store.SessionStore
	Courses     *store.CourseStore
	Enrollments *store.EnrollmentStore
	Assignments *store.AssignmentStore // НОВОЕ ПОЛЕ
	Submissions *store.SubmissionStore // НОВОЕ ПОЛЕ
}

// New создаёт набор обработчиков.
func New(
	users *store.UserStore,
	sessions *store.SessionStore,
	courses *store.CourseStore,
	enrollments *store.EnrollmentStore,
	assignments *store.AssignmentStore, // НОВЫЙ АРГУМЕНТ
	submissions *store.SubmissionStore, // НОВЫЙ АРГУМЕНТ
) *Handlers {
	return &Handlers{
		Users:       users,
		Sessions:    sessions,
		Courses:     courses,
		Enrollments: enrollments,
		Assignments: assignments, // ПРИСВАИВАЕМ
		Submissions: submissions, // ПРИСВАИВАЕМ
	}
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
func (h *Handlers) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := h.authenticatedUser(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		ctx := context.WithValue(r.Context(), userContextKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireRole — middleware: доступ только указанным ролям.
// Подразумевает авторизацию: без валидной сессии вернёт 401,
// с сессией, но чужой ролью — 403.
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
