package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/NumerR/SOS/internal/models"
	"github.com/NumerR/SOS/internal/store"
)

type CreateEventRequest struct {
	Title        string `json:"title"`
	Description  string `json:"description"`
	EventDateStr string `json:"event_date"` // ISO8601 или пусто
	// PlaceID — ссылка на справочник мест. Указатель: отсутствие поля и
	// явный null значат одно и то же («места нет»), а 0 был бы id, которого
	// не существует, и отказ на него был бы неправдой про форму.
	PlaceID  *int64 `json:"place_id"`
	Category string `json:"category"`
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
		req.Category = strings.TrimSpace(req.Category)

		if req.Title == "" {
			writeFieldError(w, http.StatusUnprocessableEntity, "title", "title is required")
			return
		}
		if !validateLength(w, "title", req.Title, maxTitleLength) ||
			!validateLength(w, "description", req.Description, maxDescriptionLength) ||
			!validateLength(w, "category", req.Category, maxCategoryLength) {
			return
		}

		eventTimestamp, ok := parseDateField(w, "event_date", req.EventDateStr)
		if !ok {
			return
		}

		placeID, ok := h.eventPlaceID(w, r, req.PlaceID)
		if !ok {
			return
		}

		event, err := h.Events.Create(r.Context(), models.CreateEventInput{
			Title:       req.Title,
			Description: req.Description,
			EventDate:   eventTimestamp,
			PlaceID:     placeID,
			Category:    req.Category,
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

// eventPlaceID проверяет ссылку события на справочник мест и возвращает id
// таким, каким его знает база.
//
// Проверяется существование, а не только формат числа: повесить инфоповод на
// id, которого в справочнике нет, — значит создать событие, которое карта не
// покажет никогда, и человек не узнает причины. Именно для этого у слоя карты
// есть состояние missing: ссылка обязана быть проверена на входе, а не
// обнаруживаться пустым маркером через месяц.
//
// Зависимость спрашивается только когда место реально прислали: событие без
// места осталось тем же запросом, что было до справочника, и сборка без
// справочника не имеет права отказывать ей в ответе.
func (h *Handlers) eventPlaceID(w http.ResponseWriter, r *http.Request, id *int64) (*int64, bool) {
	if id == nil {
		return nil, true
	}

	if *id <= 0 {
		writeFieldError(w, http.StatusUnprocessableEntity, "place_id", "place_id must name a place")
		return nil, false
	}

	if h.Places == nil {
		writeError(w, http.StatusServiceUnavailable, "places directory is not connected")
		return nil, false
	}

	place, err := h.Places.Get(r.Context(), *id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeFieldError(w, http.StatusUnprocessableEntity, "place_id", "no such place")
			return nil, false
		}
		internalError(w, r, "failed to check the place of an event", err)
		return nil, false
	}

	return &place.ID, true
}
