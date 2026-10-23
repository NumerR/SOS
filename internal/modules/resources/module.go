// Package resources — модуль витрины материалов.
package resources

import (
	"net/http"

	"github.com/NumerR/SOS/internal/authz"
	"github.com/NumerR/SOS/internal/handlers"
	"github.com/NumerR/SOS/internal/platform"
)

// Module — декларация маршрутов материалов.
type Module struct {
	h *handlers.Handlers
}

// New собирает модуль поверх набора обработчиков.
func New(h *handlers.Handlers) *Module { return &Module{h: h} }

func (m *Module) Name() string { return "resources" }

// Migrations — таблица resources пока в базовой схеме (см. T2.1).
func (m *Module) Migrations() []platform.Migration { return nil }

// Register заявляет маршруты модуля.
func (m *Module) Register(reg *platform.Registry) {
	h := m.h

	reg.Add(platform.Route{
		Path:    "/api/resources",
		Method:  http.MethodGet,
		Handler: http.HandlerFunc(h.ResourcesAPI),
	})
	reg.Add(platform.Route{
		Path:        "/api/resources",
		Method:      http.MethodPost,
		Perm:        authz.ResourceCreate,
		DenyMessage: "only teachers can add resources",
		CSRF:        true,
		Handler:     http.HandlerFunc(h.ResourcesAPI),
	})
}
