// Package schedule — модуль ленты расписания.
//
// Сейчас лента собирается из заданий и событий (внутренний источник). T4.1
// добавит сюда пакет source с внешним источником расписания — и это будет
// новый файл внутри этого модуля, а не правка ядра.
package schedule

import (
	"net/http"

	"github.com/NumerR/SOS/internal/authz"
	"github.com/NumerR/SOS/internal/handlers"
	"github.com/NumerR/SOS/internal/platform"
)

// Module — декларация маршрутов ленты.
type Module struct {
	h *handlers.Handlers
}

// New собирает модуль поверх набора обработчиков.
func New(h *handlers.Handlers) *Module { return &Module{h: h} }

func (m *Module) Name() string { return "schedule" }

// Migrations — у ленты своей таблицы нет: она читает assignments и events.
func (m *Module) Migrations() []platform.Migration { return nil }

// Register заявляет маршруты модуля.
func (m *Module) Register(reg *platform.Registry) {
	h := m.h

	// Право schedule.view есть у всех ролей, но объявлено оно здесь не для
	// формы: маршрутизатор откажет роли, которой права нет, ещё до обращения к
	// базе. Область видимости (свои курсы / всё) — authz.Scope, который
	// спрашивает ScheduleStore.
	reg.Add(platform.Route{
		Path:        "/api/schedule",
		Method:      http.MethodGet,
		Perm:        authz.ScheduleView,
		DenyMessage: "schedule is available to enrolled users",
		Handler:     http.HandlerFunc(h.ScheduleFeed),
	})
}
