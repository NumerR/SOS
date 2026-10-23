package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NumerR/SOS/internal/database"
	"github.com/NumerR/SOS/internal/models"
	"github.com/NumerR/SOS/internal/store"
)

// Тесты границ текстовых полей и шкалы оценок (T0.3).
//
// Проверка идёт через сами обработчики, а не через validateLength напрямую:
// смысл в том, что запрос с мусором не доходит до записи в базу, а не в том,
// что функция сравнения чисел работает.

func newFullEnv(t *testing.T) (*Handlers, *sql.DB) {
	t.Helper()

	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("открыть базу: %v", err)
	}
	if err := database.Migrate(db); err != nil {
		t.Fatalf("миграции: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	return New(Deps{
		Users:       store.NewUserStore(db),
		Sessions:    store.NewSessionStore(db),
		Courses:     store.NewCourseStore(db),
		Enrollments: store.NewEnrollmentStore(db),
		Assignments: store.NewAssignmentStore(db),
		Submissions: store.NewSubmissionStore(db),
		Schedule:    store.NewScheduleStore(db),
		Resources:   store.NewResourceStore(db),
		Events:      store.NewEventStore(db),
		Limits:      NewLimiters(),
	}), db
}

// callAs — вызов обработчика от лица пользователя, минуя RequireAuth: сессии
// в этих тестах не нужны, нужна только роль в контексте.
func callAs(method, target, body string, user *models.User, handler http.Handler) *httptest.ResponseRecorder {
	req := request(method, target, body, "10.0.7.7:1", nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func errorMessage(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()

	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("разобрать ответ %q: %v", rec.Body.String(), err)
	}
	return payload.Error
}

func TestCreateAssignmentFieldLimits(t *testing.T) {
	h, _ := newFullEnv(t)

	// Курс ссылается на создателя внешним ключом, поэтому пользователи
	// реально записываются в базу, а не просто живут в контексте запроса.
	teacher := createUser(t, h, "teacher", "password123", "teacher")

	course, err := h.Courses.Create(context.Background(), models.CreateCourseInput{Title: "Матанализ"}, teacher.ID)
	if err != nil {
		t.Fatalf("создать курс: %v", err)
	}

	cases := []struct {
		name        string
		title       string
		description string
		wantStatus  int
		wantError   string
	}{
		{"заголовок ровно 200 символов проходит", strings.Repeat("а", 200), "", http.StatusCreated, ""},
		{"заголовок 201 символ отклонён", strings.Repeat("а", 201), "", http.StatusUnprocessableEntity, "title is too long"},
		{"описание ровно 2000 проходит", "Норм", strings.Repeat("б", 2000), http.StatusCreated, ""},
		{"описание 2001 символ отклонён", "Норм", strings.Repeat("б", 2001), http.StatusUnprocessableEntity, "description is too long"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"course_id":%d,"title":%q,"description":%q}`, course.ID, tc.title, tc.description)

			rec := callAs(http.MethodPost, "/api/assignments", body, teacher, http.HandlerFunc(h.CreateAssignment))
			if rec.Code != tc.wantStatus {
				t.Fatalf("ожидался %d, получился %d: %s", tc.wantStatus, rec.Code, rec.Body.String())
			}
			if tc.wantError != "" && errorMessage(t, rec) != tc.wantError {
				t.Fatalf("ожидался текст %q, получился %q", tc.wantError, errorMessage(t, rec))
			}
		})
	}
}

func TestSubmitWorkContentLimit(t *testing.T) {
	h, _ := newFullEnv(t)

	teacher := createUser(t, h, "teacher", "password123", "teacher")
	student := createUser(t, h, "student", "password123", "student")
	ctx := context.Background()

	course, err := h.Courses.Create(ctx, models.CreateCourseInput{Title: "Физика"}, teacher.ID)
	if err != nil {
		t.Fatalf("создать курс: %v", err)
	}
	if _, err := h.Enrollments.Enroll(ctx, student.ID, course.ID); err != nil {
		t.Fatalf("записать студента: %v", err)
	}
	assignment, err := h.Assignments.Create(ctx, models.CreateAssignmentInput{CourseID: course.ID, Title: "Лаба"}, teacher.ID)
	if err != nil {
		t.Fatalf("создать задание: %v", err)
	}

	// 20 000 кириллических символов — это 40 000 байт. Если бы граница была
	// байтовой, этот запрос упал бы с 422, а он обязан проходить.
	accepted := strings.Repeat("с", 20000)
	body := fmt.Sprintf(`{"assignment_id":%d,"content":%q}`, assignment.ID, accepted)
	rec := callAs(http.MethodPost, "/api/submissions", body, student, http.HandlerFunc(h.SubmitWork))
	if rec.Code != http.StatusOK {
		t.Fatalf("сдача на 20 000 символов отклонена: %d %s", rec.Code, rec.Body.String())
	}

	tooLong := strings.Repeat("с", maxSubmissionLength+1)
	body = fmt.Sprintf(`{"assignment_id":%d,"content":%q}`, assignment.ID, tooLong)
	rec = callAs(http.MethodPost, "/api/submissions", body, student, http.HandlerFunc(h.SubmitWork))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("ожидался 422, получился %d: %s", rec.Code, rec.Body.String())
	}
	if got := errorMessage(t, rec); got != "content is too long" {
		t.Fatalf("неожиданный текст ошибки %q", got)
	}

	// Отклонённая сдача не должна затереть принятую: проверка идёт до Upsert.
	stored, err := h.Submissions.GetByUserAndAssignment(ctx, student.ID, assignment.ID)
	if err != nil {
		t.Fatalf("прочитать сдачу: %v", err)
	}
	if runeLen(stored.Content) != maxSubmissionLength {
		t.Fatalf("содержимое сдачи изменилось: длина %d", runeLen(stored.Content))
	}
}

func TestGradeSubmissionScoreScale(t *testing.T) {
	h, _ := newFullEnv(t)

	teacher := createUser(t, h, "teacher", "password123", "teacher")
	student := createUser(t, h, "student", "password123", "student")
	ctx := context.Background()

	course, err := h.Courses.Create(ctx, models.CreateCourseInput{Title: "Химия"}, teacher.ID)
	if err != nil {
		t.Fatalf("создать курс: %v", err)
	}
	assignment, err := h.Assignments.Create(ctx, models.CreateAssignmentInput{CourseID: course.ID, Title: "Задача"}, teacher.ID)
	if err != nil {
		t.Fatalf("создать задание: %v", err)
	}
	submission, err := h.Submissions.Upsert(ctx, student.ID, assignment.ID, "решение")
	if err != nil {
		t.Fatalf("сдача: %v", err)
	}

	cases := []struct {
		name       string
		body       string
		wantStatus int
		wantError  string
	}{
		{"оценка 0 проходит", fmt.Sprintf(`{"submission_id":%d,"score":0,"comment":""}`, submission.ID), http.StatusOK, ""},
		{"оценка 5 проходит", fmt.Sprintf(`{"submission_id":%d,"score":5,"comment":""}`, submission.ID), http.StatusOK, ""},
		{"оценка 10.5 отклонена", fmt.Sprintf(`{"submission_id":%d,"score":10.5,"comment":""}`, submission.ID), http.StatusUnprocessableEntity, "score must be between 0 and 10"},
		{"отрицательная оценка отклонена", fmt.Sprintf(`{"submission_id":%d,"score":-1,"comment":""}`, submission.ID), http.StatusUnprocessableEntity, "score must be between 0 and 10"},
		{"комментарий 2001 символ отклонён", fmt.Sprintf(`{"submission_id":%d,"score":4,"comment":%q}`, submission.ID, strings.Repeat("х", 2001)), http.StatusUnprocessableEntity, "comment is too long"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := callAs(http.MethodPost, "/api/submissions/grade", tc.body, teacher, http.HandlerFunc(h.GradeSubmission))
			if rec.Code != tc.wantStatus {
				t.Fatalf("ожидался %d, получился %d: %s", tc.wantStatus, rec.Code, rec.Body.String())
			}
			if tc.wantError != "" && errorMessage(t, rec) != tc.wantError {
				t.Fatalf("ожидался текст %q, получился %q", tc.wantError, errorMessage(t, rec))
			}
		})
	}

	// Порядок подкейсов важен: успешные идут первыми (0, затем 5), отклонённые
	// — после. Значит «5» в базе означает, что последний успешный запрос
	// записался, а три отклонённых не поменяли оценку ни на йоту.
	stored, err := h.Submissions.GetByID(ctx, submission.ID)
	if err != nil {
		t.Fatalf("прочитать сдачу: %v", err)
	}
	if stored.Score != 5 {
		t.Fatalf("ожидалась оценка 5, в базе %v", stored.Score)
	}
}

// Курсы, ресурсы и события переехали на общий validateLength — сообщения
// об ошибках обязаны остаться прежними, иначе фронтенд начнёт показывать
// незнакомые строки.
func TestRefactoredHandlersKeepErrorTexts(t *testing.T) {
	h, _ := newFullEnv(t)

	teacher := createUser(t, h, "teacher", "password123", "teacher")
	long := strings.Repeat("д", 201)

	cases := []struct {
		name      string
		target    string
		body      string
		handler   func(http.ResponseWriter, *http.Request)
		wantError string
	}{
		{"курс: заголовок", "/api/courses", fmt.Sprintf(`{"title":%q,"description":"","teacher":""}`, long), h.CoursesAPI, "title is too long"},
		{"курс: преподаватель", "/api/courses", fmt.Sprintf(`{"title":"Курс","description":"","teacher":%q}`, long), h.CoursesAPI, "teacher name is too long"},
		{"ресурс: категория", "/api/resources", fmt.Sprintf(`{"title":"Конспект","category":%q,"url":"","description":""}`, strings.Repeat("к", 65)), h.ResourcesAPI, "category is too long"},
		{"ресурс: url", "/api/resources", fmt.Sprintf(`{"title":"Конспект","category":"","url":%q,"description":""}`, strings.Repeat("u", 2001)), h.ResourcesAPI, "url is too long"},
		{"событие: описание", "/api/events", fmt.Sprintf(`{"title":"Олимпиада","description":%q,"event_date":""}`, strings.Repeat("о", 2001)), h.EventsAPI, "description is too long"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := callAs(http.MethodPost, tc.target, tc.body, teacher, http.HandlerFunc(tc.handler))
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("ожидался 422, получился %d: %s", rec.Code, rec.Body.String())
			}
			if got := errorMessage(t, rec); got != tc.wantError {
				t.Fatalf("ожидался текст %q, получился %q", tc.wantError, got)
			}
		})
	}
}
