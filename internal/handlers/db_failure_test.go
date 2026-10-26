package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/NumerR/SOS/internal/models"
)

// ADR-0029: сбой базы на пути проверки прав обязан быть 500, а не 404 «нет
// такого курса» и не 403 «вам отказано».
//
// Разница не косметическая: 403 говорит человеку «вы не имеете права», и он
// идёт просить преподавателя, 404 говорит «такого курса не существует», и он
// перестаёт доверять списку. Оба ответа — про данные, а не про доступ, когда
// на самом деле не ответил SQL. Живой curl'ом такой ответ не попросишь:
// нужна база, где запрос падает, поэтому сбой моделируется снятием таблицы
// прямым запросом — поля Deps являются конкретными хранилищами, а не
// интерфейсами, и подставить отказывающий клиент иначе нельзя.
//
// Второй набор проверок держит настоящий 404: отсутствующая строка обязана
// остаться 404, иначе правка просто заменила бы один неверный код другим.

func breakTable(t *testing.T, db *sql.DB, table string) {
	t.Helper()

	if _, err := db.Exec("DROP TABLE " + table); err != nil {
		t.Fatalf("снять таблицу %s: %v", table, err)
	}
}

// expectStoreFailure — и статус, и код конверта: мониторинг читает код,
// поэтому 500 с кодом not_found был бы той же подменой на уровне JSON.
func expectStoreFailure(t *testing.T, what string, rec *httptest.ResponseRecorder) {
	t.Helper()

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("%s: сбой базы отвечен %d, ожидался 500: %s", what, rec.Code, rec.Body.String())
	}
	if body := decodeError(t, rec); body.Code != "internal" {
		t.Fatalf("%s: при 500 ожидался код %q, получен %q", what, "internal", body.Code)
	}
}

// expectNotFound — тот же конверт для случая, когда записи действительно нет.
func expectNotFound(t *testing.T, what string, rec *httptest.ResponseRecorder) {
	t.Helper()

	if rec.Code != http.StatusNotFound {
		t.Fatalf("%s: отвечен %d, ожидался 404: %s", what, rec.Code, rec.Body.String())
	}
	if body := decodeError(t, rec); body.Code != "not_found" {
		t.Fatalf("%s: ожидался код %q, получен %q", what, "not_found", body.Code)
	}
}

func newCourseWithStudent(t *testing.T, h *Handlers) (*models.User, *models.User, *models.Course, *models.Assignment) {
	t.Helper()

	teacher := createUser(t, h, "teacher", "password123", "teacher")
	student := createUser(t, h, "student", "password123", "student")

	course, err := h.Courses.Create(context.Background(), models.CreateCourseInput{Title: "Матанализ"}, teacher.ID)
	if err != nil {
		t.Fatalf("создать курс: %v", err)
	}
	if _, err := h.Enrollments.Enroll(context.Background(), student.ID, course.ID); err != nil {
		t.Fatalf("записать студента: %v", err)
	}
	assignment, err := h.Assignments.Create(context.Background(), models.CreateAssignmentInput{
		CourseID: course.ID,
		Title:    "Лаба",
	}, teacher.ID)
	if err != nil {
		t.Fatalf("создать задание: %v", err)
	}
	return teacher, student, course, assignment
}

func TestListAssignmentsCourseLookupFailure(t *testing.T) {
	h, db := newFullEnv(t)
	_, outsider, course, _ := newCourseWithStudent(t, h)

	breakTable(t, db, "courses")

	rec := callAs(http.MethodGet, fmt.Sprintf("/api/assignments?course_id=%d", course.ID), "", outsider,
		http.HandlerFunc(h.ListAssignments))
	expectStoreFailure(t, "ListAssignments/чтение владельца курса", rec)
}

func TestListAssignmentsEnrollmentFailure(t *testing.T) {
	h, db := newFullEnv(t)
	_, student, course, _ := newCourseWithStudent(t, h)

	// courses цела, поэтому CourseOwner отвечает успешно; падает следующий
	// запрос — список курсов, на которые записан студент.
	breakTable(t, db, "enrollments")

	rec := callAs(http.MethodGet, fmt.Sprintf("/api/assignments?course_id=%d", course.ID), "", student,
		http.HandlerFunc(h.ListAssignments))
	expectStoreFailure(t, "ListAssignments/проверка записи на курс", rec)
}

func TestCreateAssignmentOwnershipFailure(t *testing.T) {
	h, db := newFullEnv(t)
	_, student, course, _ := newCourseWithStudent(t, h)

	breakTable(t, db, "courses")

	body := fmt.Sprintf(`{"course_id":%d,"title":"Лаба"}`, course.ID)
	rec := callAs(http.MethodPost, "/api/assignments", body, student, http.HandlerFunc(h.CreateAssignment))
	expectStoreFailure(t, "CreateAssignment/проверка владения курсом", rec)
}

func TestCreateAssignmentMissingCourseIsNotFound(t *testing.T) {
	h, _ := newFullEnv(t)
	_, student, _, _ := newCourseWithStudent(t, h)

	// Этот случай отвечал 403 («создавать задания можно только на своих
	// курсах»), хотя курса нет: теперь 404.
	body := `{"course_id":999999,"title":"Лаба"}`
	rec := callAs(http.MethodPost, "/api/assignments", body, student, http.HandlerFunc(h.CreateAssignment))
	expectNotFound(t, "CreateAssignment/нет такого курса", rec)
}

func TestSubmitWorkAssignmentLookup(t *testing.T) {
	h, db := newFullEnv(t)
	_, student, _, assignment := newCourseWithStudent(t, h)

	body := `{"assignment_id":999999,"content":"готово"}`
	rec := callAs(http.MethodPost, "/api/submissions", body, student, http.HandlerFunc(h.SubmitWork))
	expectNotFound(t, "SubmitWork/нет такого задания", rec)

	breakTable(t, db, "assignments")

	body = fmt.Sprintf(`{"assignment_id":%d,"content":"готово"}`, assignment.ID)
	rec = callAs(http.MethodPost, "/api/submissions", body, student, http.HandlerFunc(h.SubmitWork))
	expectStoreFailure(t, "SubmitWork/чтение задания", rec)
}

func TestListSubmissionsAssignmentLookup(t *testing.T) {
	h, db := newFullEnv(t)
	teacher, _, _, assignment := newCourseWithStudent(t, h)

	target := fmt.Sprintf("/api/submissions?assignment_id=%d", assignment.ID)
	breakTable(t, db, "assignments")

	rec := callAs(http.MethodGet, target, "", teacher, http.HandlerFunc(h.ListSubmissionsForAssignment))
	expectStoreFailure(t, "ListSubmissionsForAssignment/чтение задания", rec)
}

// breakColumn — сбой SELECT на живой таблице: запрос ссылается на колонку,
// которой больше нет. Нужен там, где DROP TABLE не годится: снимая
// assignments, SQLite каскадом убирает и сдачи, и второе чтение в
// GradeSubmission стало бы «сдачи нет», а не «запрос не выполнен».
func breakColumn(t *testing.T, db *sql.DB, table, column string) {
	t.Helper()

	if _, err := db.Exec("ALTER TABLE " + table + " DROP COLUMN " + column); err != nil {
		t.Fatalf("снять колонку %s.%s: %v", table, column, err)
	}
}

// newSubmissionForTest — сдача студента на задание преподавателя: без неё
// нельзя дойти ни до одного из двух чтений в GradeSubmission.
func newSubmissionForTest(t *testing.T, h *Handlers, studentID, assignmentID int64) *models.Submission {
	t.Helper()

	if _, err := h.Submissions.Upsert(context.Background(), studentID, assignmentID, "готово"); err != nil {
		t.Fatalf("сдать работу: %v", err)
	}
	sub, err := h.Submissions.GetByUserAndAssignment(context.Background(), studentID, assignmentID)
	if err != nil {
		t.Fatalf("прочитать сдачу: %v", err)
	}
	return sub
}

func TestGradeSubmissionMissingIsNotFound(t *testing.T) {
	h, _ := newFullEnv(t)
	teacher, _, _, _ := newCourseWithStudent(t, h)

	rec := callAs(http.MethodPost, "/api/submissions/grade", `{"submission_id":999999,"score":5}`, teacher,
		http.HandlerFunc(h.GradeSubmission))
	expectNotFound(t, "GradeSubmission/нет такой сдачи", rec)
}

func TestGradeSubmissionReadFailures(t *testing.T) {
	cases := []struct {
		name    string
		what    string
		breakIt func(t *testing.T, db *sql.DB)
	}{
		{
			name:    "чтение сдачи",
			what:    "GradeSubmission/первое чтение",
			breakIt: func(t *testing.T, db *sql.DB) { breakTable(t, db, "submissions") },
		},
		{
			name:    "чтение задания",
			what:    "GradeSubmission/второе чтение",
			breakIt: func(t *testing.T, db *sql.DB) { breakColumn(t, db, "assignments", "due_date") },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, db := newFullEnv(t)
			teacher, student, _, assignment := newCourseWithStudent(t, h)
			sub := newSubmissionForTest(t, h, student.ID, assignment.ID)

			tc.breakIt(t, db)

			body := fmt.Sprintf(`{"submission_id":%d,"score":5}`, sub.ID)
			rec := callAs(http.MethodPost, "/api/submissions/grade", body, teacher,
				http.HandlerFunc(h.GradeSubmission))
			expectStoreFailure(t, tc.what, rec)
		})
	}
}
