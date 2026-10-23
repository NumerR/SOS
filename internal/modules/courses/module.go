// Package courses — модуль курсов и записей на них.
package courses

import (
	"net/http"

	"github.com/NumerR/SOS/internal/authz"
	"github.com/NumerR/SOS/internal/handlers"
	"github.com/NumerR/SOS/internal/platform"
)

// Module — декларация маршрутов курсов.
type Module struct {
	h *handlers.Handlers
}

// New собирает модуль поверх набора обработчиков.
func New(h *handlers.Handlers) *Module { return &Module{h: h} }

func (m *Module) Name() string { return "courses" }

// Migrations — таблицы courses и enrollments пока создаёт базовая схема
// (internal/database/db.go); переезд на миграции модулей — T2.1.
func (m *Module) Migrations() []platform.Migration { return nil }

// Register заявляет маршруты модуля.
func (m *Module) Register(reg *platform.Registry) {
	h := m.h

	// Каталог курсов виден любому вошедшему: студент выбирает, на что
	// записываться. Создание курса — право course.create.
	reg.Add(platform.Route{
		Path:    "/api/courses",
		Method:  http.MethodGet,
		Handler: http.HandlerFunc(h.CoursesAPI),
	})
	reg.Add(platform.Route{
		Path:        "/api/courses",
		Method:      http.MethodPost,
		Perm:        authz.CourseCreate,
		DenyMessage: "only teachers can create courses",
		CSRF:        true,
		Handler:     http.HandlerFunc(h.CoursesAPI),
	})

	// Записи на курсы.
	reg.Add(platform.Route{
		Path:    "/api/enrollments/mine",
		Method:  http.MethodGet,
		Handler: http.HandlerFunc(h.MyEnrollments),
	})
	reg.Add(platform.Route{
		Path:        "/api/enrollments",
		Method:      http.MethodPost,
		Perm:        authz.EnrollmentManage,
		DenyMessage: "only students can enroll in courses",
		CSRF:        true,
		Handler:     http.HandlerFunc(h.Enroll),
	})
	reg.Add(platform.Route{
		Path:        "/api/enrollments/cancel",
		Method:      http.MethodPost,
		Perm:        authz.EnrollmentManage,
		DenyMessage: "only students can cancel enrollment",
		CSRF:        true,
		Handler:     http.HandlerFunc(h.CancelEnrollment),
	})

	// Список записанных читается, поэтому CSRF не нужен; право
	// course.students.view, а «свой ли это курс» проверяет обработчик — там для
	// этого есть данные из базы.
	reg.Add(platform.Route{
		Path:        "/api/enrollments/course",
		Method:      http.MethodGet,
		Perm:        authz.CourseStudentsView,
		DenyMessage: "only teachers can view enrolled students",
		Handler:     http.HandlerFunc(h.CourseStudents),
	})
}
