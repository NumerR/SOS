package handlers

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/NumerR/SOS/internal/models"
)

type CreateAssignmentRequest struct {
	CourseID    int64  `json:"course_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	DueDateStr  string `json:"due_date"` // ISO8601 or empty
}

// ListAssignments handles GET /api/assignments?course_id=N
func (h *Handlers) ListAssignments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}

	courseIDStr := r.URL.Query().Get("course_id")
	if courseIDStr == "" {
		writeError(w, http.StatusBadRequest, "course_id is required")
		return
	}
	courseID, err := strconv.ParseInt(courseIDStr, 10, 64)
	if err != nil || courseID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid course_id")
		return
	}

	user := h.currentUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	owner, found, err := h.Enrollments.CourseOwner(r.Context(), courseID)
	if err != nil || !found {
		writeError(w, http.StatusNotFound, "course not found")
		return
	}

	isOwner := owner == user.ID
	isAdmin := user.Role == "admin"
	isEnrolled := false

	if !isOwner && !isAdmin {
		mine, err := h.Enrollments.ListCoursesByUser(r.Context(), user.ID)
		if err == nil {
			for _, c := range mine {
				if c.ID == courseID {
					isEnrolled = true
					break
				}
			}
		}
	}

	if !isOwner && !isAdmin && !isEnrolled {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	assignments, err := h.Assignments.ListByCourse(r.Context(), courseID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load assignments")
		return
	}

	writeJSON(w, http.StatusOK, assignments)
}

// CreateAssignment handles POST /api/assignments
func (h *Handlers) CreateAssignment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}

	user := h.currentUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if !hasRole([]string{"teacher", "admin"}, user.Role) {
		writeError(w, http.StatusForbidden, "only teachers can create assignments")
		return
	}

	var req CreateAssignmentRequest
	if !readJSON(w, r, &req) {
		return
	}

	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" {
		writeError(w, http.StatusUnprocessableEntity, "title is required")
		return
	}

	if user.Role != "admin" {
		owner, found, err := h.Enrollments.CourseOwner(r.Context(), req.CourseID)
		if err != nil || !found || owner != user.ID {
			writeError(w, http.StatusForbidden, "you can only create assignments for your own courses")
			return
		}
	}

	var dueTimestamp int64 = 0
	if req.DueDateStr != "" {
		t, err := time.Parse(time.RFC3339, req.DueDateStr)
		if err == nil {
			dueTimestamp = t.Unix()
		}
	}

	assignment, err := h.Assignments.Create(r.Context(), models.CreateAssignmentInput{
		CourseID:    req.CourseID,
		Title:       req.Title,
		Description: req.Description,
		DueDate:     dueTimestamp,
	}, user.ID)

	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create assignment")
		return
	}

	writeJSON(w, http.StatusCreated, assignment)
}
