package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/NumerR/SOS/internal/models"
)

// Тесты на неразобранную дату (ADR-0032).
//
// Находка была не в коде ответа, а в исчезновении записи: до правки неудача
// time.Parse проглатывалась, в базу шёл ноль, клиент получал 201 «создано», а
// лента /schedule такую строку не показывала — все четыре её запроса фильтруют
// due_date > 0 / event_date > 0 (internal/store/schedule.go:48,110,131,152).
// Отсюда три части каждой проверки: статус, имя поля в конверте ADR-0003 и
// число строк в базе. Третья часть обязательна: без неё правка, которая
// отвечает 422, но строку всё равно пишет, осталась бы зелёной.

// dateCases — что отправляем в поле и что обязаны получить. want — ожидаемое
// значение в ответе для принятых; для reject'ов задаётся текстом ошибки.
type dateCase struct {
	value      string
	wantUnix   int64
	wantReject bool
}

// unix для принятых дат задан числом, а не пересчётом тем же time.Parse:
// сверка «сам с собой» не поймала бы ни съехавший часовой пояс, ни потерю
// дробных секунд. 1793383200 = 2026-10-30T18:00:00Z,
// 1793513700 = 2026-11-01T09:15:00.500+03:00 = 06:15:00Z того же дня.
func dateCases() []dateCase {
	return []dateCase{
		{value: "", wantUnix: 0},
		{value: "   ", wantUnix: 0},
		{value: "2026-10-30T18:00:00Z", wantUnix: 1793383200},
		{value: "2026-11-01T09:15:00.500+03:00", wantUnix: 1793513700},
		{value: "abc", wantReject: true},
		{value: "30.10.2026", wantReject: true},
		{value: "2026-10-30", wantReject: true},
		{value: "2026-10-30 18:00", wantReject: true},
		{value: "2026-13-45T10:00:00Z", wantReject: true},
	}
}

func countRows(t *testing.T, db *sql.DB, table string) int64 {
	t.Helper()

	var n int64
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
		t.Fatalf("посчитать строки в %s: %v", table, err)
	}
	return n
}

// decodeDate — поле из успешного ответа: 201 возвращает саму запись.
func decodeDate(t *testing.T, rec *httptest.ResponseRecorder, field string) int64 {
	t.Helper()

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("разобрать ответ %q: %v", rec.Body.String(), err)
	}
	value, ok := payload[field]
	if !ok {
		t.Fatalf("в ответе нет поля %q: %s", field, rec.Body.String())
	}
	unix, ok := value.(float64)
	if !ok {
		t.Fatalf("поле %q имеет тип %T: %s", field, value, rec.Body.String())
	}
	return int64(unix)
}

func TestCreateAssignmentDateIsNotSwallowed(t *testing.T) {
	h, db := newFullEnv(t)
	teacher := createUser(t, h, "d_assign", "password123", "teacher")
	course, err := h.Courses.Create(context.Background(),
		models.CreateCourseInput{Title: "Матанализ"}, teacher.ID)
	if err != nil {
		t.Fatalf("создать курс: %v", err)
	}

	for _, tc := range dateCases() {
		name := tc.value
		if name == "" {
			name = "пустая строка"
		}
		t.Run(name, func(t *testing.T) {
			before := countRows(t, db, "assignments")
			body := fmt.Sprintf(`{"course_id":%d,"title":"Лаба","description":"","due_date":%q}`,
				course.ID, tc.value)
			rec := callAs(http.MethodPost, "/api/assignments", body, teacher, http.HandlerFunc(h.CreateAssignment))

			if tc.wantReject {
				if rec.Code != http.StatusUnprocessableEntity {
					t.Fatalf("дата %q принята: %d %s — та же дата обязана быть 422, а не нулём в базе",
						tc.value, rec.Code, rec.Body.String())
				}
				if got := errorMessage(t, rec); got != "due_date must be an ISO8601 datetime" {
					t.Fatalf("текст ошибки %q", got)
				}
				if field := errorFieldName(t, rec); field != "due_date" {
					t.Fatalf("конверт не назвал поле: %q в %s", field, rec.Body.String())
				}
				if after := countRows(t, db, "assignments"); after != before {
					t.Fatalf("отклонённый запрос всё равно записал задание: было %d, стало %d", before, after)
				}
				return
			}

			if rec.Code != http.StatusCreated {
				t.Fatalf("дата %q отклонена: %d %s", tc.value, rec.Code, rec.Body.String())
			}
			if got := decodeDate(t, rec, "due_date"); got != tc.wantUnix {
				t.Fatalf("дата %q записана как %d, ожидалось %d", tc.value, got, tc.wantUnix)
			}
			if after := countRows(t, db, "assignments"); after != before+1 {
				t.Fatalf("успешный запрос не создал запись: было %d, стало %d", before, after)
			}
		})
	}
}

func TestCreateEventDateIsNotSwallowed(t *testing.T) {
	h, db := newFullEnv(t)
	student := createUser(t, h, "d_event", "password123", "student")

	for _, tc := range dateCases() {
		name := tc.value
		if name == "" {
			name = "пустая строка"
		}
		t.Run(name, func(t *testing.T) {
			before := countRows(t, db, "events")
			body := fmt.Sprintf(`{"title":"Олимпиада","description":"","event_date":%q}`, tc.value)
			rec := callAs(http.MethodPost, "/api/events", body, student, http.HandlerFunc(h.EventsAPI))

			if tc.wantReject {
				if rec.Code != http.StatusUnprocessableEntity {
					t.Fatalf("дата %q принята: %d %s", tc.value, rec.Code, rec.Body.String())
				}
				if got := errorMessage(t, rec); got != "event_date must be an ISO8601 datetime" {
					t.Fatalf("текст ошибки %q", got)
				}
				if field := errorFieldName(t, rec); field != "event_date" {
					t.Fatalf("конверт не назвал поле: %q в %s", field, rec.Body.String())
				}
				if after := countRows(t, db, "events"); after != before {
					t.Fatalf("отклонённый запрос всё равно записал событие: было %d, стало %d", before, after)
				}
				return
			}

			if rec.Code != http.StatusCreated {
				t.Fatalf("дата %q отклонена: %d %s", tc.value, rec.Code, rec.Body.String())
			}
			if got := decodeDate(t, rec, "event_date"); got != tc.wantUnix {
				t.Fatalf("дата %q записана как %d, ожидалось %d", tc.value, got, tc.wantUnix)
			}
			if after := countRows(t, db, "events"); after != before+1 {
				t.Fatalf("успешный запрос не создал запись: было %d, стало %d", before, after)
			}
		})
	}
}

// errorFieldName — единственное имя поля из конверта. Держит не только текст:
// страница подписывает отказ подписью input'а по keys(fields), и без имени поля
// человек прочитал бы «что-то не так», не зная, какое именно.
func errorFieldName(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()

	var payload struct {
		Error struct {
			Fields map[string]string `json:"fields"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("разобрать ответ %q: %v", rec.Body.String(), err)
	}
	keys := make([]string, 0, len(payload.Error.Fields))
	for k := range payload.Error.Fields {
		keys = append(keys, k)
	}
	if len(keys) != 1 {
		t.Fatalf("в конверте %d имён поля, ожидалось одно: %s", len(keys), rec.Body.String())
	}
	return keys[0]
}

// TestFeedHidesDatelessAssignment — половина про «исчезновение», записанная
// как факт, а не как правка: ноль в дате остаётся законным значением
// («задание без срока»), и лента его не показывает. Правка ADR-0032 меняет
// только то, что ноль больше не становится наградой за неразбираемую дату.
func TestFeedHidesDatelessAssignment(t *testing.T) {
	h, db := newFullEnv(t)
	teacher := createUser(t, h, "d_feed", "password123", "teacher")
	course, err := h.Courses.Create(context.Background(),
		models.CreateCourseInput{Title: "Дискретка"}, teacher.ID)
	if err != nil {
		t.Fatalf("создать курс: %v", err)
	}

	_, err = h.Assignments.Create(context.Background(), models.CreateAssignmentInput{
		CourseID: course.ID, Title: "Без срока", DueDate: 0,
	}, teacher.ID)
	if err != nil {
		t.Fatalf("создать задание: %v", err)
	}
	if _, err = h.Assignments.Create(context.Background(), models.CreateAssignmentInput{
		CourseID: course.ID, Title: "Со сроком", DueDate: 1793383200,
	}, teacher.ID); err != nil {
		t.Fatalf("создать второе задание: %v", err)
	}

	items, err := h.Schedule.Feed(context.Background(), teacher, 0, 4102444800)
	if err != nil {
		t.Fatalf("лента: %v", err)
	}

	var titles []string
	for _, it := range items {
		titles = append(titles, it.Title)
	}
	if len(items) != 1 || titles[0] != "Со сроком" {
		t.Fatalf("лента вернула %v, ожидалось одно задание со сроком — ноль фильтруется, и это надо видеть", titles)
	}
	if n := countRows(t, db, "assignments"); n != 2 {
		t.Fatalf("в базе %d заданий, ожидалось 2: лента не равна хранилищу", n)
	}
}
