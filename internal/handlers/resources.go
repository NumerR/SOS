package handlers

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/NumerR/SOS/internal/models"
)

type CreateResourceRequest struct {
	Title       string `json:"title"`
	Category    string `json:"category"`
	URL         string `json:"url"`
	Description string `json:"description"`
}

// ResourcesAPI — GET (все авторизованные) / POST (teacher, admin) /api/resources.
func (h *Handlers) ResourcesAPI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		resources, err := h.Resources.List(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load resources")
			return
		}
		writeJSON(w, http.StatusOK, resources)

	case http.MethodPost:
		user := h.currentUser(r)
		if user == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if !hasRole([]string{"teacher", "admin"}, user.Role) {
			writeError(w, http.StatusForbidden, "only teachers can add resources")
			return
		}

		var req CreateResourceRequest
		if !readJSON(w, r, &req) {
			return
		}

		req.Title = strings.TrimSpace(req.Title)
		req.Category = strings.TrimSpace(req.Category)
		req.URL = strings.TrimSpace(req.URL)
		req.Description = strings.TrimSpace(req.Description)

		if req.Title == "" {
			writeError(w, http.StatusUnprocessableEntity, "title is required")
			return
		}
		if utf8.RuneCountInString(req.Title) > 200 {
			writeError(w, http.StatusUnprocessableEntity, "title is too long")
			return
		}
		if utf8.RuneCountInString(req.Category) > 64 {
			writeError(w, http.StatusUnprocessableEntity, "category is too long")
			return
		}
		if utf8.RuneCountInString(req.URL) > 2000 {
			writeError(w, http.StatusUnprocessableEntity, "url is too long")
			return
		}
		if utf8.RuneCountInString(req.Description) > 2000 {
			writeError(w, http.StatusUnprocessableEntity, "description is too long")
			return
		}

		resource, err := h.Resources.Create(r.Context(), models.CreateResourceInput{
			Title:       req.Title,
			Category:    req.Category,
			URL:         req.URL,
			Description: req.Description,
		}, user.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create resource")
			return
		}
		writeJSON(w, http.StatusCreated, resource)

	default:
		methodNotAllowed(w)
	}
}
