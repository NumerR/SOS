// Package assignments — модуль заданий и сдач работ.
//
// Задания и сдачи — один модуль намеренно: у них общий объект доступа
// (задание), а проверки владельца читают одну и ту же пару таблиц. Разносить их
// по разным модулям означало бы дублировать доступ к AssignmentStore.
package assignments

import (
	"net/http"

	"github.com/NumerR/SOS/internal/authz"
	"github.com/NumerR/SOS/internal/handlers"
	"github.com/NumerR/SOS/internal/platform"
)

// Module — декларация маршрутов заданий и сдач.
type Module struct {
	h *handlers.Handlers
}

// New собирает модуль поверх набора обработчиков.
func New(h *handlers.Handlers) *Module { return &Module{h: h} }

func (m *Module) Name() string { return "assignments" }

// Migrations — таблицы assignments и submissions создаёт базовая схема; см. T2.1.
func (m *Module) Migrations() []platform.Migration { return nil }

// Register заявляет маршруты модуля.
func (m *Module) Register(reg *platform.Registry) {
	h := m.h

	// GET читает курс, к которому у человека есть доступ (владелец, записанный
	// или право assignment.view.any) — это решение обработчика, а не роли,
	// поэтому маршрут требует только входа.
	reg.Add(platform.Route{
		Path:    "/api/assignments",
		Method:  http.MethodGet,
		Handler: http.HandlerFunc(h.ListAssignments),
	})
	reg.Add(platform.Route{
		Path:        "/api/assignments",
		Method:      http.MethodPost,
		Perm:        authz.AssignmentCreate,
		DenyMessage: "only teachers can create assignments",
		CSRF:        true,
		Handler:     http.HandlerFunc(h.CreateAssignment),
	})

	// Сдачи: смотреть и оценивать — права преподавателя; сдавать — любая
	// вошедшая роль, у которой есть запись на курс (проверка в обработчике).
	reg.Add(platform.Route{
		Path:        "/api/submissions",
		Method:      http.MethodGet,
		Perm:        authz.SubmissionView,
		DenyMessage: "only teachers can view submissions",
		Handler:     http.HandlerFunc(h.ListSubmissionsForAssignment),
	})
	reg.Add(platform.Route{
		Path:    "/api/submissions",
		Method:  http.MethodPost,
		CSRF:    true,
		Handler: http.HandlerFunc(h.SubmitWork),
	})
	reg.Add(platform.Route{
		Path:    "/api/submissions/mine",
		Method:  http.MethodGet,
		Handler: http.HandlerFunc(h.MySubmissions),
	})
	reg.Add(platform.Route{
		Path:        "/api/submissions/grade",
		Method:      http.MethodPost,
		Perm:        authz.SubmissionGrade,
		DenyMessage: "only teachers can grade submissions",
		CSRF:        true,
		Handler:     http.HandlerFunc(h.GradeSubmission),
	})
}
