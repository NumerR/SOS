package handlers

import (
	"net/http"
	"strings"

	"github.com/NumerR/SOS/internal/models"
)

type CreateCourseRequest struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Teacher     string `json:"teacher"`
}

// CoursesAPI обрабатывает GET и POST /api/courses.
// GET — любой авторизованный (каталог виден студентам).
// POST — только teacher и admin.
func (h *Handlers) CoursesAPI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		courses, err := h.Courses.List(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load courses")
			return
		}

		writeJSON(w, http.StatusOK, courses)

	case http.MethodPost:
		user := h.currentUser(r)
		if user == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		if !hasRole([]string{"teacher", "admin"}, user.Role) {
			writeError(w, http.StatusForbidden, "only teachers can create courses")
			return
		}

		var req CreateCourseRequest
		if !readJSON(w, r, &req) {
			return
		}

		req.Title = strings.TrimSpace(req.Title)
		req.Description = strings.TrimSpace(req.Description)
		req.Teacher = strings.TrimSpace(req.Teacher)

		if req.Title == "" {
			writeError(w, http.StatusUnprocessableEntity, "title is required")
			return
		}

		if !validateLength(w, "title", req.Title, maxTitleLength) ||
			!validateLength(w, "description", req.Description, maxDescriptionLength) ||
			!validateLength(w, "teacher name", req.Teacher, maxPersonNameLength) {
			return
		}

		teacher := req.Teacher
		if teacher == "" {
			teacher = user.FullName
		}
		if teacher == "" {
			teacher = user.Username
		}

		course, err := h.Courses.Create(
			r.Context(),
			models.CreateCourseInput{
				Title:       req.Title,
				Description: req.Description,
				Teacher:     teacher,
			},
			user.ID,
		)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create course")
			return
		}

		writeJSON(w, http.StatusCreated, course)

	default:
		methodNotAllowed(w)
	}
}
