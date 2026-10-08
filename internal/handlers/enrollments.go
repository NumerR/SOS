package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/NumerR/SOS/internal/store"
)

type EnrollRequest struct {
	CourseID int64 `json:"course_id"`
}

// MyEnrollments — GET /api/enrollments/mine: курсы текущего пользователя.
func (h *Handlers) MyEnrollments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}

	user := h.currentUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	courses, err := h.Enrollments.ListCoursesByUser(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load enrollments")
		return
	}

	writeJSON(w, http.StatusOK, courses)
}

// Enroll — POST /api/enrollments: записаться на курс (student/admin).
func (h *Handlers) Enroll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}

	user := h.currentUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if !hasRole([]string{"student", "admin"}, user.Role) {
		writeError(w, http.StatusForbidden, "only students can enroll in courses")
		return
	}

	var req EnrollRequest
	if !readJSON(w, r, &req) {
		return
	}

	if req.CourseID <= 0 {
		writeError(w, http.StatusBadRequest, "course_id is required")
		return
	}

	created, err := h.Enrollments.Enroll(r.Context(), user.ID, req.CourseID)
	if err != nil {
		if errors.Is(err, store.ErrCourseNotFound) {
			writeError(w, http.StatusNotFound, "course not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to enroll")
		return
	}

	if !created {
		writeError(w, http.StatusConflict, "you are already enrolled in this course")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"status":    "enrolled",
		"course_id": req.CourseID,
	})
}

// CancelEnrollment — POST /api/enrollments/cancel: отписаться (student/admin).
func (h *Handlers) CancelEnrollment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}

	user := h.currentUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if !hasRole([]string{"student", "admin"}, user.Role) {
		writeError(w, http.StatusForbidden, "only students can cancel enrollment")
		return
	}

	var req EnrollRequest
	if !readJSON(w, r, &req) {
		return
	}

	if req.CourseID <= 0 {
		writeError(w, http.StatusBadRequest, "course_id is required")
		return
	}

	if err := h.Enrollments.Cancel(r.Context(), user.ID, req.CourseID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to cancel enrollment")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "cancelled",
		"course_id": req.CourseID,
	})
}

// CourseStudents — GET /api/enrollments/course?course_id=N: кто записан (teacher-владелец/admin).
func (h *Handlers) CourseStudents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}

	user := h.currentUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if !hasRole([]string{"teacher", "admin"}, user.Role) {
		writeError(w, http.StatusForbidden, "only teachers can view enrolled students")
		return
	}

	courseID, err := strconv.ParseInt(r.URL.Query().Get("course_id"), 10, 64)
	if err != nil || courseID <= 0 {
		writeError(w, http.StatusBadRequest, "valid course_id is required")
		return
	}

	// Преподаватель видит список только своих курсов; админ — любых.
	if user.Role == "teacher" {
		owner, found, err := h.Enrollments.CourseOwner(r.Context(), courseID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to check course ownership")
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, "course not found")
			return
		}
		if owner != user.ID {
			writeError(w, http.StatusForbidden, "you can view students only of your own courses")
			return
		}
	}

	users, err := h.Enrollments.ListUsersByCourse(r.Context(), courseID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load students")
		return
	}

	writeJSON(w, http.StatusOK, users)
}
