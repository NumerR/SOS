package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NumerR/SOS/internal/authz"
	"github.com/NumerR/SOS/internal/modules/audit"
	"github.com/NumerR/SOS/internal/platform"
	"github.com/NumerR/SOS/internal/store"
)

// Интеграционные тесты журнала действий (T1.2): путь чтения через живой
// маршрутизатор, права на него и события, которые пишет цепочка «курс →
// задание → сдача → оценка».
//
// Unit-тесты обработчиков (internal/handlers/audit_test.go) проверяют, ЧТО
// пишется. Здесь проверяется то, чего они проверить не могут: что маршрут
// вообще защищён, что право audit.view есть только у администратора и что
// событие доживает до ответа API с именем автора.

// readAudit читает журнал под указанной сессией.
func readAudit(t *testing.T, srv *httptest.Server, user *session, query string) ([]store.AuditEvent, string) {
	t.Helper()

	resp, body := get(srv, "/api/admin/audit"+query, user)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/admin/audit%s: код %d %s", query, resp.StatusCode, body)
	}

	var events []store.AuditEvent
	if err := json.Unmarshal([]byte(body), &events); err != nil {
		t.Fatalf("разобрать журнал: %v (%s)", err, body)
	}

	return events, body
}

func hasAction(events []store.AuditEvent, action store.AuditAction) bool {
	for _, event := range events {
		if event.Action == string(action) {
			return true
		}
	}
	return false
}

func findAction(events []store.AuditEvent, action store.AuditAction) *store.AuditEvent {
	for i := range events {
		if events[i].Action == string(action) {
			return &events[i]
		}
	}
	return nil
}

// TestAuditAccessIsAdminOnly — право audit.view проверяет маршрутизатор.
//
// Отдельно от user.list: в журнале лежат IP, User-Agent и имена учётных
// записей из неудачных входов, поэтому «админ видит пользователей» не
// означает «и журнал».
func TestAuditAccessIsAdminOnly(t *testing.T) {
	srv, users := newTestServer(t)

	anon := newSession(nil)
	student := loginAs(t, srv, users, "studyaudit1", "studentpass1")
	teacher := loginAs(t, srv, users, "teachaudit1", "teacherpass1")
	admin := loginAs(t, srv, users, "rootaudit1", "adminpass1")

	if resp, body := get(srv, "/api/admin/audit", anon); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("аноним GET /api/admin/audit: %d %s, ожидалось 401", resp.StatusCode, body)
	}

	for name, user := range map[string]*session{"student": student, "teacher": teacher} {
		resp, body := get(srv, "/api/admin/audit", user)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s GET /api/admin/audit: %d %s, ожидалось 403", name, resp.StatusCode, body)
		}
		if !strings.Contains(body, "only administrators can read the audit log") {
			t.Errorf("%s: отказ без внятного текста — %s", name, body)
		}
	}

	// GET, а значит CSRF не нужен: страница админки читает журнал сразу после
	// входа, и требование токена было бы лишним кругом.
	events, _ := readAudit(t, srv, admin, "")
	if !hasAction(events, store.AuditLogin) {
		t.Errorf("в журнале нет ни одного входа, хотя тест вошёл четырьмя учётками: %+v", events)
	}
}

// TestAuditRecordsGradingChain — оценка попадает в журнал с именем преподавателя.
//
// Это единственный тест, где цепочка курс → задание → сдача → оценка проходит
// целиком через HTTP, а не через один обработчик: хранилища заданий и сдач в
// unit-тестах handlers не подключены.
func TestAuditRecordsGradingChain(t *testing.T) {
	srv, users := newTestServer(t)

	student := loginAs(t, srv, users, "studyaudit2", "studentpass2")
	teacher := loginAs(t, srv, users, "teachaudit2", "teacherpass2")
	admin := loginAs(t, srv, users, "rootaudit2", "adminpass2")

	resp, body := post(srv, "/api/courses", `{"title":"Аудит курса","teacher":"Ivanova"}`, teacher)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("создать курс: %d %s", resp.StatusCode, body)
	}

	var course struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &course); err != nil || course.ID == 0 {
		t.Fatalf("разобрать курс: %v (%s)", err, body)
	}

	if resp, body = post(srv, "/api/enrollments", fmt.Sprintf(`{"course_id":%d}`, course.ID), student); resp.StatusCode != http.StatusCreated {
		t.Fatalf("записать студента: %d %s", resp.StatusCode, body)
	}

	resp, body = post(srv, "/api/assignments", fmt.Sprintf(`{"course_id":%d,"title":"Домашка"}`, course.ID), teacher)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("создать задание: %d %s", resp.StatusCode, body)
	}

	var assignment struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &assignment); err != nil || assignment.ID == 0 {
		t.Fatalf("разобрать задание: %v (%s)", err, body)
	}

	// Сдача — upsert, поэтому 200, а не 201: проверяем ровно тот код, который
	// отдаёт обработчик, а не «любой 2xx».
	resp, body = post(srv, "/api/submissions",
		fmt.Sprintf(`{"assignment_id":%d,"content":"решение"}`, assignment.ID), student)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("сдать работу: %d %s, ожидалось 200", resp.StatusCode, body)
	}

	var submission struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &submission); err != nil || submission.ID == 0 {
		t.Fatalf("разобрать сдачу: %v (%s)", err, body)
	}

	resp, body = post(srv, "/api/submissions/grade",
		fmt.Sprintf(`{"submission_id":%d,"score":9,"comment":""}`, submission.ID), teacher)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("оценить: %d %s", resp.StatusCode, body)
	}

	events, _ := readAudit(t, srv, admin, "?action="+string(store.AuditGrade))
	if len(events) != 1 {
		t.Fatalf("событий оценки: %d, ожидалось 1: %+v", len(events), events)
	}

	graded := events[0]
	if graded.Action != string(store.AuditGrade) || !graded.OK {
		t.Errorf("событие оценки записано неверно: %+v", graded)
	}
	if graded.TargetType != "submission" || graded.TargetID != fmt.Sprint(submission.ID) {
		t.Errorf("цель оценки = %s/%s, ожидался submission/%d",
			graded.TargetType, graded.TargetID, submission.ID)
	}
	if graded.Detail != "score=9" {
		t.Errorf("detail = %q, ожидалось score=9", graded.Detail)
	}
	// Имя автора приходит из JOIN: без него журнал — список чисел, которые
	// администратор обязан расшифровать руками.
	if graded.ActorUsername != "teachaudit2" {
		t.Errorf("actor_username = %q, ожидалось teachaudit2", graded.ActorUsername)
	}

	// Оценка студентом собственной работы не проходит — и в журнале не
	// появляется новое событие: отказ выдаёт маршрутизатор, тело обработчика
	// не выполняется.
	if resp, body = post(srv, "/api/submissions/grade",
		fmt.Sprintf(`{"submission_id":%d,"score":10,"comment":""}`, submission.ID), student); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("студент оценивает: %d %s, ожидалось 403", resp.StatusCode, body)
	}

	after, _ := readAudit(t, srv, admin, "?action="+string(store.AuditGrade))
	if len(after) != 1 {
		t.Errorf("после отказа с кодом 403 событий оценки: %d, ожидалось 1", len(after))
	}
}

// TestAuditReadFiltersAndLimits — фильтры пути чтения.
func TestAuditReadFiltersAndLimits(t *testing.T) {
	srv, users := newTestServer(t)

	first := loginAs(t, srv, users, "studyaudit3", "studentpass3")
	loginAs(t, srv, users, "studyaudit4", "studentpass4")
	admin := loginAs(t, srv, users, "rootaudit3", "adminpass3")

	// Несуществующее действие отвергается, а не отвечает пустым списком:
	// опечатка в фильтре не должна выглядеть как «нападений не было».
	if resp, body := get(srv, "/api/admin/audit?action=auth.teleported", admin); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "auth.login") {
		t.Errorf("неизвестное действие: %d %s, ожидалось 422 со списком допустимых", resp.StatusCode, body)
	}

	for _, bad := range []string{"?limit=0", "?limit=-5", "?limit=abc", "?offset=-1", "?actor_user_id=0", "?actor_user_id=xyz"} {
		if resp, body := get(srv, "/api/admin/audit"+bad, admin); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s, ожидалось 422", bad, resp.StatusCode, body)
		}
	}

	// LIMIT работает: одна строка на страницу.
	page, _ := readAudit(t, srv, admin, "?limit=1")
	if len(page) != 1 {
		t.Fatalf("limit=1 вернул %d строк", len(page))
	}

	// Граница сверху: всё, что больше 200, режется, а не выгружает таблицу.
	capped, _ := readAudit(t, srv, admin, "?limit=100000")
	if len(capped) > 200 {
		t.Errorf("limit=100000 вернул %d строк, ожидалось не больше 200", len(capped))
	}

	// Страницы не пересекаются.
	next, _ := readAudit(t, srv, admin, "?limit=1&offset=1")
	if len(next) != 1 || next[0].ID == page[0].ID {
		t.Errorf("вторая страница совпала с первой: %+v vs %+v", next, page)
	}

	// Фильтр по автору: у каждой учётки — только её события.
	studentID := userIDOf(t, users, "studyaudit3")
	otherID := userIDOf(t, users, "studyaudit4")

	byActor, _ := readAudit(t, srv, admin, "?actor_user_id="+fmt.Sprint(studentID))
	if len(byActor) == 0 {
		t.Fatal("фильтр по автору не нашёл событий студента")
	}
	for _, event := range byActor {
		if event.ActorUserID == nil {
			t.Errorf("в выборке по автору есть событие без автора: %+v", event)
			continue
		}
		if *event.ActorUserID != studentID {
			t.Errorf("чужое событие в выборке по автору %d: %+v", studentID, event)
		}
	}

	// Комбинация двух фильтров обязана пересекаться, а не складываться:
	// у студента событий смены роли нет — их делает только админ.
	none, _ := readAudit(t, srv, admin,
		"?actor_user_id="+fmt.Sprint(otherID)+"&action="+string(store.AuditRoleChange))
	if len(none) != 0 {
		t.Errorf("у студента %d событий смены роли, ожидалось 0", len(none))
	}

	// Отказ в доступе к журналу — не событие: путь чтения сам ничего не пишет,
	// иначе админ мог бы забить журнал собственными 403.
	before, _ := readAudit(t, srv, admin, "?limit=1")
	if resp, _ := get(srv, "/api/admin/audit", first); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("студент читает журнал: код %d, ожидалось 403", resp.StatusCode)
	}
	after, _ := readAudit(t, srv, admin, "?limit=1")
	if after[0].ID != before[0].ID {
		t.Errorf("неудачная попытка чтения журнала добавила запись: %+v → %+v", before, after)
	}
}

// userIDOf — id учётки по имени: фильтр журнала работает по идентификатору, а
// тестам удобнее ссылаться на логин.
func userIDOf(t *testing.T, users *store.UserStore, username string) int64 {
	t.Helper()

	user, err := users.GetByIdentifier(context.Background(), username)
	if err != nil {
		t.Fatalf("найти пользователя %s: %v", username, err)
	}

	return user.ID
}

// TestAuditRouteIsDeclaredByModule — маршрут журнала принадлежит модулю audit.
//
// Проверка на месте, где раньше терялись права: если из декларации модуля
// убрать Perm, тест получит 200 вместо 403 и упадёт.
func TestAuditRouteIsDeclaredByModule(t *testing.T) {
	registry := platform.NewRegistry()
	audit.New(nil).Register(registry)

	routes := registry.Routes()
	if len(routes) != 1 {
		t.Fatalf("модуль audit заявил %d маршрутов, ожидался 1", len(routes))
	}

	route := routes[0]
	if route.Path != "/api/admin/audit" || route.Method != http.MethodGet {
		t.Errorf("маршрут = %s %s, ожидался GET /api/admin/audit", route.Method, route.Path)
	}
	if route.Perm != authz.AuditView || !authz.ValidPermission(route.Perm) {
		t.Errorf("право маршрута = %q, ожидалось объявленное audit.view", route.Perm)
	}
	if route.Public {
		t.Error("журнал действий объявлен публичным")
	}

	migrations := audit.New(nil).Migrations()
	if len(migrations) != 1 || migrations[0].Version == "" || !strings.Contains(migrations[0].SQL, "audit_events") {
		t.Errorf("миграция журнала объявлена некорректно: %+v", migrations)
	}
}
