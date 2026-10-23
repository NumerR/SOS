package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/NumerR/SOS/internal/authz"
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
//
// Право на маршрут — «вход в систему»: сдавать может любая роль, если она
// записана на курс. Исключение для незаписанных — authz.SubmissionCreateAny.
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

	req.Content = strings.TrimSpace(req.Content)
	if !validateLength(w, "content", req.Content, maxSubmissionLength) {
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

	// Сдавать можно только с записью на курс; право submission.create.any
	// снимает это ограничение (оно одно у администратора).
	if !isEnrolled && !canHere(user, authz.SubmissionCreateAny) {
		writeError(w, http.StatusForbidden, "you must be enrolled in the course to submit work")
		return
	}

	sub, err := h.Submissions.Upsert(r.Context(), user.ID, req.AssignmentID, req.Content)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save submission")
		return
	}

	writeJSON(w, http.StatusOK, sub)
}

// ListSubmissionsForAssignment handles GET /api/submissions?assignment_id=N
//
// Право submission.view проверяет маршрутизатор; здесь остаётся «своё ли
// задание».
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

	if !canHere(user, authz.SubmissionViewAny) && assignment.CreatedBy != user.ID {
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
//
// Право submission.grade проверяет маршрутизатор; здесь — проверка владельца
// задания и валидация оценки.
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

	var req GradeRequest
	if !readJSON(w, r, &req) {
		return
	}

	if req.SubmissionID <= 0 {
		writeError(w, http.StatusBadRequest, "submission_id is required")
		return
	}

	// Оценку и комментарий проверяем до обращения к базе: запрос с мусором не
	// должен тратить три SELECT и соединение.
	req.Comment = strings.TrimSpace(req.Comment)
	if !validateScore(w, req.Score) || !validateLength(w, "comment", req.Comment, maxDescriptionLength) {
		return
	}

	// 1. Находим сабмит, чтобы узнать, к какому заданию он относится.
	sub, err := h.Submissions.GetByID(r.Context(), req.SubmissionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "submission not found")
		return
	}

	// 2. Находим задание и его владельца.
	assignment, err := h.Assignments.GetByID(r.Context(), sub.AssignmentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "assignment not found")
		return
	}

	// 3. Проверяем право: оценить может только владелец курса-задания либо админ.
	if !canHere(user, authz.SubmissionGradeAny) && assignment.CreatedBy != user.ID {
		writeError(w, http.StatusForbidden, "you can only grade submissions for your own courses")
		return
	}

	if err := h.Submissions.Grade(r.Context(), req.SubmissionID, req.Score, req.Comment); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to grade submission")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "graded"})
}
