package handlers

import (
	"net/http"
	"strings"

	"github.com/NumerR/SOS/internal/models"
)

type CreateEventRequest struct {
	Title        string `json:"title"`
	Description  string `json:"description"`
	EventDateStr string `json:"event_date"` // ISO8601 или пусто
}

// EventsAPI — GET (все авторизованные) / POST (право event.create,
// проверяется маршрутизатором) /api/events.
func (h *Handlers) EventsAPI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		events, err := h.Events.List(r.Context())
		if err != nil {
			internalError(w, r, "failed to load events", err)
			return
		}
		writeJSON(w, http.StatusOK, events)

	case http.MethodPost:
		user := h.currentUser(r)
		if user == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		var req CreateEventRequest
		if !readJSON(w, r, &req) {
			return
		}

		req.Title = strings.TrimSpace(req.Title)
		req.Description = strings.TrimSpace(req.Description)

		if req.Title == "" {
			writeFieldError(w, http.StatusUnprocessableEntity, "title", "title is required")
			return
		}
		if !validateLength(w, "title", req.Title, maxTitleLength) ||
			!validateLength(w, "description", req.Description, maxDescriptionLength) {
			return
		}

		eventTimestamp, ok := parseDateField(w, "event_date", req.EventDateStr)
		if !ok {
			return
		}

		event, err := h.Events.Create(r.Context(), models.CreateEventInput{
			Title:       req.Title,
			Description: req.Description,
			EventDate:   eventTimestamp,
		}, user.ID)
		if err != nil {
			internalError(w, r, "failed to create event", err)
			return
		}
		writeJSON(w, http.StatusCreated, event)

	default:
		methodNotAllowed(w)
	}
}
