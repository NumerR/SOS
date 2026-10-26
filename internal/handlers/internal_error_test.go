package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ADR-0030: у каждого отказа 500 в логе обязана быть причина.
//
// Строка доступа (ADR-0025) называет путь, код и request id, но не причину: до
// этого шага из 47 мест `writeError(w, 500, …)` причину писали пять. Для
// человека, который разбирает жалобу «не грузится список заданий», это значило
// «500 был, почему — неизвестно». Helper `internalError` закрыл и то, и другое
// разом: текст отказа остаётся у клиента, а `error=` уходит в лог.
//
// Отдельно проверяется, что на один отказ в логе ровно одна строка «отказ
// сервера» и ровно две Error-строки всего: двойная запись (своя причина рядом с
// helper) выглядела бы как два разных сбоя одного запроса.

// expectOneFailureLog — ровно одна строка «отказ сервера» с причиной, request id
// и тем же текстом, который увидел клиент.
func expectOneFailureLog(t *testing.T, logged string, headerID string, reason string, want ...string) {
	t.Helper()

	lines := []string{}
	for _, line := range strings.Split(logged, "\n") {
		if strings.Contains(line, `msg="отказ сервера"`) {
			lines = append(lines, line)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("строк «отказ сервера» %d, ожидалась одна:\n%s", len(lines), logged)
	}

	for _, field := range append([]string{`reason="` + reason + `"`, "error=", "request_id=" + headerID}, want...) {
		if !strings.Contains(lines[0], field) {
			t.Fatalf("в строке отказа нет %q:\n%s", field, lines[0])
		}
	}
}

func TestInternalErrorLogsCauseOnce(t *testing.T) {
	h, db := newFullEnv(t)
	_, student, course, _ := newCourseWithStudent(t, h)
	buffer := captureLogs(t)

	// Тот же сбой, что ADR-0029 ловит на уровне статуса: список курсов студента
	// не читается, поэтому проверка прав не может ответить ни «да», ни «нет».
	breakTable(t, db, "enrollments")

	req := request(http.MethodGet, fmt.Sprintf("/api/assignments?course_id=%d", course.ID), "", "10.0.0.9:1", nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, student))
	rec := httptest.NewRecorder()
	LogRequests(http.HandlerFunc(h.ListAssignments)).ServeHTTP(rec, req)

	expectStoreFailure(t, "ListAssignments/проверка записи на курс", rec)
	headerID := rec.Header().Get(requestIDHeader)
	if headerID == "" {
		t.Fatal("ответ не содержит X-Request-Id")
	}

	logged := buffer.String()
	expectOneFailureLog(t, logged, headerID, "failed to verify enrollment", "no such table")

	// Строка доступа того же запроса обязана остаться рядом и называть 500.
	if !strings.Contains(logged, "status=500") {
		t.Fatalf("строка доступа не поднялась до 500:\n%s", logged)
	}

	// Ровно две Error-строки на один отказ: отказ с причиной и строка доступа.
	// Проверка держит то, что счётчик строк «отказ сервера» не ловит — свою
	// строку `requestLogger(r).Error("что-то пошло не так", …)` рядом с helper:
	// человек, разбирающий лог, видит два сбоя одного запроса и ищет второй
	// там, где его нет.
	if got := strings.Count(logged, "level=ERROR"); got != 2 {
		t.Fatalf("Error-строк %d, ожидалось 2 (отказ + строка доступа):\n%s", got, logged)
	}
}

func TestInternalErrorOnMissingDependencyKeepsReason(t *testing.T) {
	h, _ := newFullEnv(t)
	// newFullEnv намеренно не подключает модуль журнала: Deps.Audit == nil, и
	// обработчик отвечает 500 без всякого сбоя базы.
	admin := createUser(t, h, "audit-admin", "password123", "admin")
	buffer := captureLogs(t)

	rec := callAs(http.MethodGet, "/api/admin/audit", "", admin, http.HandlerFunc(h.AdminAudit))
	expectStoreFailure(t, "AdminAudit/модуль не подключён", rec)

	logged := buffer.String()
	for _, want := range []string{
		`msg="отказ сервера"`,
		`reason="audit log is not available"`,
		`error="модуль не подключён при сборке сервера"`,
		"module=audit",
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("в логе нет %q:\n%s", want, logged)
		}
	}

	// Внутренняя формулировка причины остаётся в логе: клиенту уходит публичный
	// текст, а не «модуль не подключён при сборке сервера».
	if body := decodeError(t, rec); body.Message != "audit log is not available" {
		t.Fatalf("клиенту утёк текст причины: %q", body.Message)
	}
}
