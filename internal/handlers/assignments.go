package handlers

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/NumerR/SOS/internal/authz"
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
		writeFieldError(w, http.StatusBadRequest, "course_id", "course_id is required")
		return
	}
	courseID, err := strconv.ParseInt(courseIDStr, 10, 64)
	if err != nil || courseID <= 0 {
		writeFieldError(w, http.StatusBadRequest, "course_id", "invalid course_id")
		return
	}

	user := h.currentUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	owner, found, err := h.Enrollments.CourseOwner(r.Context(), courseID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check course ownership")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "course not found")
		return
	}

	// Видеть задания чужого курса можно, если ты его владелец или записан на
	// него. Право assignment.view.any снимает обе проверки — оно есть только у
	// администратора и заменяло собой сравнение user.Role == "admin".
	//
	// Ошибка хранилища здесь — 500, а не 403: «не удалось прочитать список
	// записей» и «тебя в списке нет» для клиента разные случаи, и во втором
	// человек хотя бы понимает, что делать (записаться), а в первом ему
	// остаётся только повторить. Тот же порядок уже в ListStudentsByCourse и
	// в SubmitAssignment.
	isOwner := owner == user.ID
	canSeeAny := canHere(user, authz.AssignmentViewAny)
	isEnrolled := false

	if !isOwner && !canSeeAny {
		mine, err := h.Enrollments.ListCoursesByUser(r.Context(), user.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to verify enrollment")
			return
		}
		for _, c := range mine {
			if c.ID == courseID {
				isEnrolled = true
				break
			}
		}
	}

	if !isOwner && !canSeeAny && !isEnrolled {
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
//
// Право assignment.create проверяет маршрутизатор; здесь остаётся проверка
// владельца курса, для которой нужны данные из базы.
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

	var req CreateAssignmentRequest
	if !readJSON(w, r, &req) {
		return
	}

	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" {
		writeFieldError(w, http.StatusUnprocessableEntity, "title", "title is required")
		return
	}

	// Описания раньше не ограничивались ничем: 1 МиБ текста в description —
	// и страница со списком заданий отдаёт мегабайты на каждый GET.
	if !validateLength(w, "title", req.Title, maxTitleLength) ||
		!validateLength(w, "description", req.Description, maxDescriptionLength) {
		return
	}

	if !canHere(user, authz.AssignmentCreateAny) {
		owner, found, err := h.Enrollments.CourseOwner(r.Context(), req.CourseID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to check course ownership")
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, "course not found")
			return
		}
		if owner != user.ID {
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
