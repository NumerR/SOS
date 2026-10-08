package handlers

import (
	"net/http"
	"strconv"
	"strings"
)

type SubmitRequest struct {
	AssignmentID int64  `json:"assignment_id"`
	Content      string `json:"content"`
}

type GradeRequest struct {
	SubmissionID int64   `json:"submission_id"`
	Score        float64 `json:"score"`
	Comment      string  `json:"comment"`
}

// SubmitWork handles POST /api/submissions
func (h *Handlers) SubmitWork(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}

	user := h.currentUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req SubmitRequest
	if !readJSON(w, r, &req) {
		return
	}

	if req.AssignmentID <= 0 {
		writeError(w, http.StatusBadRequest, "assignment_id is required")
		return
	}

	assignment, err := h.Assignments.GetByID(r.Context(), req.AssignmentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "assignment not found")
		return
	}

	mine, err := h.Enrollments.ListCoursesByUser(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to verify enrollment")
		return
	}

	isEnrolled := false
	for _, c := range mine {
		if c.ID == assignment.CourseID {
			isEnrolled = true
			break
		}
	}

	if !isEnrolled && user.Role != "admin" {
		writeError(w, http.StatusForbidden, "you must be enrolled in the course to submit work")
		return
	}

	sub, err := h.Submissions.Upsert(r.Context(), user.ID, req.AssignmentID, strings.TrimSpace(req.Content))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save submission")
		return
	}

	writeJSON(w, http.StatusOK, sub)
}

// ListSubmissionsForAssignment handles GET /api/submissions?assignment_id=N
func (h *Handlers) ListSubmissionsForAssignment(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, http.StatusForbidden, "only teachers can view submissions")
		return
	}

	aidStr := r.URL.Query().Get("assignment_id")
	if aidStr == "" {
		writeError(w, http.StatusBadRequest, "assignment_id is required")
		return
	}
	aid, err := strconv.ParseInt(aidStr, 10, 64)
	if err != nil || aid <= 0 {
		writeError(w, http.StatusBadRequest, "invalid assignment_id")
		return
	}

	assignment, err := h.Assignments.GetByID(r.Context(), aid)
	if err != nil {
		writeError(w, http.StatusNotFound, "assignment not found")
		return
	}

	if user.Role != "admin" && assignment.CreatedBy != user.ID {
		writeError(w, http.StatusForbidden, "you can only view submissions for your own assignments")
		return
	}

	subs, err := h.Submissions.ListByAssignment(r.Context(), aid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load submissions")
		return
	}

	writeJSON(w, http.StatusOK, subs)
}

// MySubmissions handles GET /api/submissions/mine
func (h *Handlers) MySubmissions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}

	user := h.currentUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	subs, err := h.Submissions.ListMine(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load submissions")
		return
	}

	writeJSON(w, http.StatusOK, subs)
}

// GradeSubmission handles POST /api/submissions/grade
func (h *Handlers) GradeSubmission(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, http.StatusForbidden, "only teachers can grade submissions")
		return
	}

	var req GradeRequest
	if !readJSON(w, r, &req) {
		return
	}

	if req.SubmissionID <= 0 {
		writeError(w, http.StatusBadRequest, "submission_id is required")
		return
	}

	err := h.Submissions.Grade(r.Context(), req.SubmissionID, req.Score, strings.TrimSpace(req.Comment))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to grade submission")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "graded"})
}
