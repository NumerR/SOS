// Package events — модуль событий университета.
package events

import (
	"net/http"

	"github.com/NumerR/SOS/internal/authz"
	"github.com/NumerR/SOS/internal/handlers"
	"github.com/NumerR/SOS/internal/platform"
	"github.com/NumerR/SOS/internal/store"
)

// Module — декларация маршрутов событий.
type Module struct {
	h *handlers.Handlers
}

// New собирает модуль поверх набора обработчиков.
func New(h *handlers.Handlers) *Module { return &Module{h: h} }

func (m *Module) Name() string { return "events" }

// Migrations — правка схемы под второй слой карты.
//
// Версия названа датой, как в places и audit: runner сравнивает строки
// (module, version), и следующая правка событий не должна выглядеть «старше»
// этой.
//
// Модуль стоит в списке после places (cmd/server/modules.go) именно поэтому:
// ALTER добавляет ссылку на таблицу мест, и к моменту применения она обязана
// существовать.
func (m *Module) Migrations() []platform.Migration {
	return []platform.Migration{
		{Version: "2026-11-02_events_map", SQL: store.EventsMapMigration},
	}
}

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
	// Слой карты. Отдельный путь, а не поле в /api/events: у ленты один
	// объект — список инфоповодов, у карты другой — что из них встанет
	// маркером, а что нет и почему.
	reg.Add(platform.Route{
		Path:    "/api/events/map",
		Method:  http.MethodGet,
		Handler: http.HandlerFunc(h.EventsMapAPI),
	})
}
