// Package events — модуль событий университета.
package events

import (
	"net/http"

	"github.com/NumerR/SOS/internal/authz"
	"github.com/NumerR/SOS/internal/handlers"
	"github.com/NumerR/SOS/internal/platform"
)

// Module — декларация маршрутов событий.
type Module struct {
	h *handlers.Handlers
}

// New собирает модуль поверх набора обработчиков.
func New(h *handlers.Handlers) *Module { return &Module{h: h} }

func (m *Module) Name() string { return "events" }

// Migrations — таблица events пока в базовой схеме (см. T2.1).
func (m *Module) Migrations() []platform.Migration { return nil }

// Register заявляет маршруты модуля.
//
// Право authz.EventSourceManage здесь не задействовано: оно зарезервировано под
// T4.2, где появится управление внешним источником событий (парсер/бот).
// Добавление тех маршрутов = новые строки в этом Register, без правок ядра.
func (m *Module) Register(reg *platform.Registry) {
	h := m.h

	reg.Add(platform.Route{
		Path:    "/api/events",
		Method:  http.MethodGet,
		Handler: http.HandlerFunc(h.EventsAPI),
	})
	reg.Add(platform.Route{
		Path:        "/api/events",
		Method:      http.MethodPost,
		Perm:        authz.EventCreate,
		DenyMessage: "only teachers can add events",
		CSRF:        true,
		Handler:     http.HandlerFunc(h.EventsAPI),
	})
}
