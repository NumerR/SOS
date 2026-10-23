package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NumerR/SOS/internal/database"
	"github.com/NumerR/SOS/internal/handlers"
	"github.com/NumerR/SOS/internal/platform"
	"github.com/NumerR/SOS/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// TestRouterFromModuleList — интеграционный тест всей цепочки T1.1.
//
// Он отвечает на вопрос, который unit-тесты обработчиков проверить не могут:
// «а защищён ли маршрут вообще». Раньше это зависело от аккуратности человека,
// который в main.go оборачивал h.RequireAuth(...): один пропущенный wrapper —
// и чужие данные открываются анониму.
//
// Список модулей берётся тот же, что у живого сервера (moduleList), а сверху
// добавлен тестовый модуль notes: он доказывает, что новый модуль со своей
// таблицей подключается без правок ядра.
func TestRouterFromModuleList(t *testing.T) {
	srv, users := newTestServer(t)

	// Живость отвечает до всякой авторизации — она нужна мониторингу.
	resp, body := get(srv, "/api/health", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"db":"ok"`) {
		t.Errorf("health: %d %s, ожидалось 200 и db:ok", resp.StatusCode, body)
	}

	anon := newSession(nil)
	student := loginAs(t, srv, users, "study1", "studentpass1")
	owner := loginAs(t, srv, users, "teach1", "teacherpass1")
	other := loginAs(t, srv, users, "teach2", "teacherpass2")
	admin := loginAs(t, srv, users, "root1", "adminpass1")

	// 1. Аноним не читает чужой каталог курсов.
	if resp, body = get(srv, "/api/courses", anon); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("аноним GET /api/courses: %d %s, ожидалось 401", resp.StatusCode, body)
	}

	// 2. Студент не создаёт курсы: право проверяет маршрутизатор, и отказ
	//    приходит с тем текстом, который человек видит в интерфейсе.
	resp, body = post(srv, "/api/courses", `{"title":"Курс от студента"}`, student)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "only teachers can create courses") {
		t.Errorf("student POST /api/courses: %d %s", resp.StatusCode, body)
	}

	// 3. Преподаватель создаёт.
	resp, body = post(srv, "/api/courses", `{"title":"Матанализ","teacher":"Petrov"}`, owner)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("owner POST /api/courses: %d %s", resp.StatusCode, body)
	}

	var course struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &course); err != nil || course.ID == 0 {
		t.Fatalf("не разобрать созданный курс: %v (%s)", err, body)
	}

	// 4. Чужой курс: преподаватель без права на чужие курсы задание не создаст.
	//    Это проверка владельца, она осталась в обработчике — право
	//    assignment.create её не заменяет.
	resp, body = post(srv, "/api/assignments",
		fmt.Sprintf(`{"course_id":%d,"title":"Чужое задание"}`, course.ID), other)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("чужой преподаватель: %d %s, ожидалось 403", resp.StatusCode, body)
	}

	// 5. ...а админ создаёт: у него есть assignment.create.any.
	resp, body = post(srv, "/api/assignments",
		fmt.Sprintf(`{"course_id":%d,"title":"Задание от админа"}`, course.ID), admin)
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("admin POST /api/assignments: %d %s, ожидалось 201", resp.StatusCode, body)
	}

	// 6. Студент без записи на курс заданий не видит.
	resp, body = get(srv, fmt.Sprintf("/api/assignments?course_id=%d", course.ID), student)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("незаписанный студент: %d %s, ожидалось 403", resp.StatusCode, body)
	}

	// 7. Право enrollment.manage — у студента, и запись открывает задания.
	resp, body = post(srv, "/api/enrollments", fmt.Sprintf(`{"course_id":%d}`, course.ID), student)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("student POST /api/enrollments: %d %s", resp.StatusCode, body)
	}
	if resp, body = get(srv, fmt.Sprintf("/api/assignments?course_id=%d", course.ID), student); resp.StatusCode != http.StatusOK {
		t.Errorf("записанный студент: %d %s, ожидалось 200", resp.StatusCode, body)
	}

	// 8. Преподаватель записываться сам не может — права нет.
	resp, body = post(srv, "/api/enrollments", fmt.Sprintf(`{"course_id":%d}`, course.ID), owner)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "only students can enroll in courses") {
		t.Errorf("teacher POST /api/enrollments: %d %s", resp.StatusCode, body)
	}

	// 9. Админ-контур: анонима — 401, остальных ролей — 403, админа — 200.
	if resp, body = get(srv, "/api/admin/users", anon); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("аноним GET /api/admin/users: %d %s, ожидалось 401", resp.StatusCode, body)
	}
	for name, u := range map[string]*session{"student": student, "teacher": owner} {
		if resp, body = get(srv, "/api/admin/users", u); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s GET /api/admin/users: %d %s, ожидалось 403", name, resp.StatusCode, body)
		}
	}
	if resp, body = get(srv, "/api/admin/users", admin); resp.StatusCode != http.StatusOK {
		t.Errorf("admin GET /api/admin/users: %d %s", resp.StatusCode, body)
	}

	// 10. Лента расписания открыта всем ролям (право schedule.view).
	for name, u := range map[string]*session{"student": student, "teacher": owner, "admin": admin} {
		if resp, body = get(srv, "/api/schedule", u); resp.StatusCode != http.StatusOK {
			t.Errorf("%s GET /api/schedule: %d %s", name, resp.StatusCode, body)
		}
	}

	// 11. Недекларированный метод даёт 405 в формате API, а не html-текст:
	//     раньше за это отвечал switch внутри обработчика.
	resp, body = do(srv, http.MethodPut, "/api/courses", `{}`, student)
	if resp.StatusCode != http.StatusMethodNotAllowed || !strings.Contains(body, `"error"`) {
		t.Errorf("PUT /api/courses: %d %s, ожидалось 405 + JSON", resp.StatusCode, body)
	}

	// 12. Незнакомый API-путь остаётся 404.
	if resp, body = get(srv, "/api/definitely-not-here", student); resp.StatusCode != http.StatusNotFound {
		t.Errorf("несуществующий путь: %d %s, ожидалось 404", resp.StatusCode, body)
	}

	// 13. POST без CSRF-токена не проходит, с токеном — создаёт запись в
	//     таблице, которую принёс сам модуль.
	resp, body = do(srv, http.MethodPost, "/api/notes", `{"text":"конспект"}`, withoutCSRF(student))
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("notes POST без токена: %d %s, ожидалось 403", resp.StatusCode, body)
	}

	resp, body = get(srv, "/api/notes", student)
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(body) != "[]" {
		t.Errorf("notes GET до записи: %d %q, ожидалось 200 и []", resp.StatusCode, body)
	}

	resp, body = post(srv, "/api/notes", `{"text":"конспект"}`, student)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("notes POST: %d %s", resp.StatusCode, body)
	}

	resp, body = get(srv, "/api/notes", student)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "конспект") {
		t.Errorf("notes GET после записи: %d %s", resp.StatusCode, body)
	}

	// 14. Аноним к чужим заметкам не допускается: обёртка входа стоит по
	//     умолчанию, даже когда модуль не заявил права.
	if resp, body = get(srv, "/api/notes", anon); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("аноним GET /api/notes: %d %s, ожидалось 401", resp.StatusCode, body)
	}
}

// TestNewModuleWithoutCoreEdits — формальное содержание приёмки T1.1.
//
// Модуль notes объявлен в этом файле, а в ядре (internal/platform,
// internal/authz, cmd/server/modules.go) не изменено ни одной строки. Если
// подключение модуля снова начнёт требовать правок ядра, тест либо перестанет
// собираться, либо упадёт.
func TestNewModuleWithoutCoreEdits(t *testing.T) {
	srv, users := newTestServer(t)

	student := loginAs(t, srv, users, "study2", "studentpass2")
	if resp, body := post(srv, "/api/notes", `{"text":"первая заметка"}`, student); resp.StatusCode != http.StatusCreated {
		t.Fatalf("создание заметки: %d %s", resp.StatusCode, body)
	}

	// Права на маршруте нет — значит достаточно входа, и преподаватель видит
	// то, что создал студент. Так работает маршрут «для всех ролей».
	teacher := loginAs(t, srv, users, "teach3", "teacherpass3")
	if resp, body := get(srv, "/api/notes", teacher); resp.StatusCode != http.StatusOK || !strings.Contains(body, "первая заметка") {
		t.Errorf("чтение заметок преподавателем: %d %s", resp.StatusCode, body)
	}
}

// session — клиент тестового сервера: куки сессии плюс CSRF-токен.
type session struct {
	cookies []*http.Cookie
	csrf    string
}

func newSession(cookies []*http.Cookie) *session {
	current := &session{cookies: cookies}
	for _, cookie := range cookies {
		if cookie.Name == "csrf_token" {
			current.csrf = cookie.Value
		}
	}
	return current
}

// withoutCSRF — та же сессия, но токен не отправляется: так проверяется, что
// CSRF-обёртка действительно стоит на маршруте.
func withoutCSRF(user *session) *session {
	return &session{cookies: user.cookies}
}

func newTestServer(t *testing.T) (*httptest.Server, *store.UserStore) {
	t.Helper()

	db := openTestDBForApp(t)

	userStore := store.NewUserStore(db)
	h := handlers.New(handlers.Deps{
		Users:       userStore,
		Sessions:    store.NewSessionStore(db),
		Courses:     store.NewCourseStore(db),
		Enrollments: store.NewEnrollmentStore(db),
		Assignments: store.NewAssignmentStore(db),
		Submissions: store.NewSubmissionStore(db),
		Schedule:    store.NewScheduleStore(db),
		Resources:   store.NewResourceStore(db),
		Events:      store.NewEventStore(db),
		Limits:      handlers.NewLimiters(),
	})

	// Список модулей живого сервера + тестовый модуль. Вот и весь «плагин»:
	// одна строка в списке.
	modules := append(moduleList(h), newNotesModule(db))

	if err := applyModuleMigrations(context.Background(), db, modules); err != nil {
		t.Fatalf("миграции модулей: %v", err)
	}

	router, err := newRouter(h, db, modules)
	if err != nil {
		t.Fatalf("сборка маршрутизатора: %v", err)
	}

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	return srv, userStore
}

func openTestDBForApp(t *testing.T) *sql.DB {
	t.Helper()

	db, err := database.Open(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatalf("открыть базу: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := database.Migrate(db); err != nil {
		t.Fatalf("базовая схема: %v", err)
	}

	return db
}

// createUser заводит учётку прямо в базе: публичная регистрация ограничена по
// IP (10/час), а тестам нужно несколько ролей с одного адреса.
func createUser(t *testing.T, users *store.UserStore, username, password, role string) {
	t.Helper()

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("захэшировать пароль: %v", err)
	}

	if _, _, err := users.Ensure(context.Background(), username, username+"@example.com", username, string(hash), role); err != nil {
		t.Fatalf("создать пользователя %s: %v", username, err)
	}
}

// loginAs входит через API и собирает сессию из ответа сервера.
func loginAs(t *testing.T, srv *httptest.Server, users *store.UserStore, username, password string) *session {
	t.Helper()

	createUser(t, users, username, password, roleFor(username))

	resp, body := do(srv, http.MethodPost, "/api/auth/login",
		`{"identifier":"`+username+`","password":"`+password+`"}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("вход %s: %d %s", username, resp.StatusCode, body)
	}

	current := newSession(resp.Cookies())
	if current.csrf == "" {
		t.Fatalf("сервер не выдал csrf_token при входе %s", username)
	}

	return current
}

// roleFor выводит роль из имени тестовой учётки: root* — админ, teach* —
// преподаватель, остальные — студенты. Так имя и роль не могут разойтись.
func roleFor(username string) string {
	switch {
	case strings.HasPrefix(username, "root"):
		return "admin"
	case strings.HasPrefix(username, "teach"):
		return "teacher"
	default:
		return "student"
	}
}

func get(srv *httptest.Server, path string, user *session) (*http.Response, string) {
	return do(srv, http.MethodGet, path, "", user)
}

func post(srv *httptest.Server, path, body string, user *session) (*http.Response, string) {
	return do(srv, http.MethodPost, path, body, user)
}

// do — один запрос к тестовому серверу.
//
// Возвращает и ответ, и уже прочитанное тело: почти каждый ассерт проверяет
// код И текст, а читать body twice невозможно.
func do(srv *httptest.Server, method, path, body string, user *session) (*http.Response, string) {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}

	req, err := http.NewRequest(method, srv.URL+path, reader)
	if err != nil {
		panic("do: собрать запрос: " + err.Error())
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	if user != nil {
		for _, cookie := range user.cookies {
			req.AddCookie(cookie)
		}
		if user.csrf != "" {
			req.Header.Set("X-CSRF-Token", user.csrf)
		}
	}

	resp, err := srv.Client().Do(req)
	if err != nil {
		panic("do: " + err.Error())
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		panic("do: прочитать тело: " + err.Error())
	}

	return resp, string(raw)
}

// notesModule — пример модуля, который принёс и свою таблицу, и свои маршруты.
//
// Живёт в тестовом файле нарочно: «заметки» не входят в продукт, а
// продемонстрировать контракт нужно исполняемо. Инструкция для настоящего
// модуля — docs/how-to-add-a-module.md.
type notesModule struct {
	db *sql.DB
}

func newNotesModule(db *sql.DB) platform.Module { return &notesModule{db: db} }

func (m *notesModule) Name() string { return "notes" }

func (m *notesModule) Migrations() []platform.Migration {
	return []platform.Migration{
		{
			Version: "0001_create_notes",
			SQL: `CREATE TABLE IF NOT EXISTS test_notes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    author_id INTEGER NOT NULL,
    text TEXT NOT NULL,
    FOREIGN KEY(author_id) REFERENCES users(id) ON DELETE CASCADE
);`,
		},
	}
}

func (m *notesModule) Register(reg *platform.Registry) {
	// Право не заявлено: достаточно входа в систему (обёртка Auth ставится по
	// умолчанию). CSRF обязателен для записи — как у любого mutate-маршрута.
	reg.Add(platform.Route{Path: "/api/notes", Method: http.MethodGet, Handler: http.HandlerFunc(m.list)})
	reg.Add(platform.Route{Path: "/api/notes", Method: http.MethodPost, CSRF: true, Handler: http.HandlerFunc(m.create)})
}

func (m *notesModule) list(w http.ResponseWriter, r *http.Request) {
	rows, err := m.db.QueryContext(r.Context(), `SELECT text FROM test_notes ORDER BY id;`)
	if err != nil {
		http.Error(w, "query", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	items := []string{}
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			http.Error(w, "scan", http.StatusInternalServerError)
			return
		}
		items = append(items, text)
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(items)
}

func (m *notesModule) create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	user := handlers.UserFromContext(r)
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if _, err := m.db.ExecContext(r.Context(),
		`INSERT INTO test_notes (author_id, text) VALUES (?, ?);`, user.ID, req.Text); err != nil {
		http.Error(w, "insert", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "created"})
}
