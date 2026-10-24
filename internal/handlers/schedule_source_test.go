package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NumerR/SOS/internal/apeks"
	"github.com/NumerR/SOS/internal/authz"
	"github.com/NumerR/SOS/internal/database"
	"github.com/NumerR/SOS/internal/store"
)

// Тесты внешнего источника расписания (T4.1).
//
// Проверяются три вещи, которые нельзя увидеть из тестов хранилища или парсера:
//
//  1. Разделение прав: чтение уже импортированных данных публично, а запросы,
//     которые идут к сайту вуза с токеном, — только администратору.
//  2. Секрет не покидает сервер: ни в ответе браузеру, ни в журнале действий,
//     ни в тексте ошибки источника.
//  3. Честные состояния: «источник не настроен» и «источник не ответил» —
//     разные коды и разные тексты, и ни то ни другое не подменяется пустым
//     расписанием.
//
// Сетевой слой подменён httptest.NewTLSServer: клиент при этом настоящий,
// поэтому декодирование ответа и разбор формата проверяются всерьёз, а не
// заглушкой.
//
// Админские вызовы идут через настоящую сессию (вход + csrf-кука), а не через
// подмешанного в контекст пользователя: RequireAuth смотрит на куку, и тест с
// контекстом проверял бы права там, где живой запрос до них не доходит.

// testApeksToken — заведомо ненастоящий токен. Он нужен ровно для одного:
// проверить, что куда бы запрос ни упал, этого значения нигде не оказывается.
const testApeksToken = "handler-test-токен-не-настоящий-42"

type scheduleSourceEnv struct {
	h   *Handlers
	db  *sql.DB
	src *store.ScheduleSourceStore

	// userSeq даёт уникальные имена: у users.username есть UNIQUE, и второй
	// «admin» в одном тесте упал бы duplicate вместо проверки прав.
	userSeq atomic.Int64
}

// newScheduleSourceEnv строит обработчики с настоящими таблицами источника и
// без клиента: тесту, которому нужен источник, достаточно вызвать attachApeks.
func newScheduleSourceEnv(t *testing.T) *scheduleSourceEnv {
	t.Helper()

	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("открыть базу: %v", err)
	}
	if err := database.Migrate(db); err != nil {
		t.Fatalf("миграции: %v", err)
	}
	// Таблицы аудита и источника создаёт сервер — миграциями модулей. Модули
	// импортируют этот пакет, поэтому тест берёт текст DDL напрямую из store.
	if _, err := db.Exec(store.AuditEventsMigration); err != nil {
		t.Fatalf("таблица аудита: %v", err)
	}
	if _, err := db.Exec(store.ScheduleSourceMigration); err != nil {
		t.Fatalf("таблицы источника: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	src := store.NewScheduleSourceStore(db)

	h := New(Deps{
		Users:          store.NewUserStore(db),
		Sessions:       store.NewSessionStore(db),
		Audit:          store.NewAuditStore(db),
		Schedule:       store.NewScheduleStore(db),
		ScheduleSource: src,
		Limits:         NewLimiters(),
	})

	return &scheduleSourceEnv{h: h, db: db, src: src}
}

// attachApeks подменяет источник TLS-сервером-заглушкой и возвращает счётчик
// обращений: часть проверок — «страница не должна дёргать сайт вуза».
func (e *scheduleSourceEnv) attachApeks(t *testing.T, handler http.HandlerFunc) *atomic.Int64 {
	t.Helper()

	calls := new(atomic.Int64)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		handler(w, r)
	}))
	t.Cleanup(server.Close)

	client, err := apeks.New(apeks.Config{
		Token:   testApeksToken,
		BaseURL: server.URL,
		HTTP:    server.Client(),
	})
	if err != nil {
		t.Fatalf("собрать клиента источника: %v", err)
	}
	e.h.Apeks = client

	return calls
}

// loginAs создаёт пользователя нужной роли и входит под ним: cookie сессии и
// csrf-кука достаются тесту настоящие, как на странице.
func (e *scheduleSourceEnv) loginAs(t *testing.T, role string) []*http.Cookie {
	t.Helper()

	name := role + "-" + strconv.FormatInt(e.userSeq.Add(1), 10)
	createUser(t, e.h, name, "password123", role)

	cookies := login(t, e.h, name, "password123", "10.0.0.9:1")
	if csrfFrom(cookies) == "" {
		t.Fatal("вход не выдал csrf-куку — проверка CSRF была бы фиктивной")
	}
	return cookies
}

// callAdmin обращается к админскому маршруту через те же middleware, что заявлены
// в модуле: право schedule.source.manage, а для mutate-запроса — ещё и CSRF.
func (e *scheduleSourceEnv) callAdmin(t *testing.T, role, target, body string, handler http.Handler, csrf bool) *httptest.ResponseRecorder {
	t.Helper()

	cookies := e.loginAs(t, role)

	wrapped := e.h.RequirePerm(authz.ScheduleSourceManage, "только администратор источника расписания", handler)
	if csrf {
		wrapped = e.h.RequireCSRF(wrapped)
	}

	req := request(http.MethodPost, target, body, "10.0.0.9:1", cookies)
	if csrf {
		req.Header.Set(csrfHeaderName, csrfFrom(cookies))
	}

	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)
	return rec
}

// callAdminGET обращается к админскому GET-маршруту: право требуется, а CSRF на
// безопасный метод не вешается (модуль declares CSRF: true только для POST).
func (e *scheduleSourceEnv) callAdminGET(t *testing.T, role, target string, handler http.Handler) *httptest.ResponseRecorder {
	t.Helper()

	cookies := e.loginAs(t, role)
	wrapped := e.h.RequirePerm(authz.ScheduleSourceManage, "только администратор источника расписания", handler)

	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, request(http.MethodGet, target, "", "10.0.0.9:1", cookies))
	return rec
}

// seedCatalog кладёт в каталог группы с указанными id и именами.
func (e *scheduleSourceEnv) seedCatalog(t *testing.T, groups ...store.SourceGroup) {
	t.Helper()

	if _, err := e.src.UpsertGroups(context.Background(), apeks.Source, groups); err != nil {
		t.Fatalf("наполнить каталог: %v", err)
	}
}

// seedMonth кладёт пары группы за месяц.
func (e *scheduleSourceEnv) seedMonth(t *testing.T, group string, month, year int, lessons ...store.SourceLesson) {
	t.Helper()

	if _, err := e.src.ReplaceMonth(context.Background(), apeks.Source, group, month, year, lessons); err != nil {
		t.Fatalf("импорт месяца %02d.%d: %v", month, year, err)
	}
}

func (e *scheduleSourceEnv) lessons(t *testing.T, group string) []store.SourceLesson {
	t.Helper()

	list, err := e.src.ListLessons(context.Background(), apeks.Source, group, 1, 99999999)
	if err != nil {
		t.Fatalf("прочитать пары: %v", err)
	}
	return list
}

// --- Публичное чтение --------------------------------------------------------

func sourceQuery(h *Handlers, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	http.HandlerFunc(h.ScheduleGroupsAPI).ServeHTTP(rec, request(http.MethodGet, target, "", "10.0.0.1:1", nil))
	return rec
}

func lessonsQuery(h *Handlers, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	http.HandlerFunc(h.ScheduleLessonsAPI).ServeHTTP(rec, request(http.MethodGet, target, "", "10.0.0.1:1", nil))
	return rec
}

// decode — разбор JSON-ответа в map: состав ответов источника меняется, и
// проверять конкретное поле проще всего так.
func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("разобрать ответ %q: %v", rec.Body.String(), err)
	}
	return payload
}

func TestScheduleGroupsAPIIsPublicAndHonestWhenEmpty(t *testing.T) {
	e := newScheduleSourceEnv(t)

	rec := sourceQuery(e.h, "/api/schedule/groups")
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d, ожидалось 200: %s", rec.Code, rec.Body.String())
	}

	payload := decode(t, rec)
	if payload["source"] != apeks.Source {
		t.Errorf("source = %v", payload["source"])
	}
	if payload["count"] != float64(0) {
		t.Errorf("count = %v при пустом каталоге", payload["count"])
	}
	// Пустой каталог — это [], а не null: на null страница падает на
	// groups.length, и «пусто» становится неотличимым от «вернулось не то».
	raw, ok := payload["groups"].([]any)
	if !ok {
		t.Fatalf("groups не массив: %T", payload["groups"])
	}
	if len(raw) != 0 {
		t.Errorf("при пустой базе groups содержит %d записей", len(raw))
	}
	if link, _ := payload["source_page_url"].(string); !strings.HasPrefix(link, "https://") {
		t.Errorf("ссылка на первоисточник должна быть в ответе, получили %v", payload["source_page_url"])
	}
}

func TestScheduleGroupsAPIListsCatalog(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t,
		store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"},
		store.SourceGroup{ExternalID: "495", Name: "Б-ИСиТ-23"},
	)

	rec := sourceQuery(e.h, "/api/schedule/groups?q=%D0%B8%D1%81%D0%B8%D1%82")
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d: %s", rec.Code, rec.Body.String())
	}

	payload := decode(t, rec)
	if payload["count"] != float64(2) {
		t.Fatalf("count = %v, ожидалось 2", payload["count"])
	}
	if payload["synced_at"] == float64(0) {
		t.Error("synced_at пустой — странице нечем подписать «каталог обновлён»")
	}
	if strings.Contains(rec.Body.String(), testApeksToken) {
		t.Fatal("токен источника в ответе списка групп")
	}

	// Верхний регистр в поиске работает так же: страница отдаёт то, что набрал
	// человек, а не то, что совпало побайтово.
	narrow := sourceQuery(e.h, "/api/schedule/groups?q=%D0%98%D0%A1%D0%98%D0%A2-23")
	if got := decode(t, narrow)["count"]; got != float64(1) {
		t.Errorf("по «ИСИТ-23» найдено %v групп", got)
	}
}

func TestScheduleLessonsAPIServesImportedWeek(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t, store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"})
	e.seedMonth(t, "494", 10, 2026,
		store.SourceLesson{ExternalID: "1", DayKey: 20261006, Date: "06.10.2026", Weekday: "Вт", LessonNumber: 1, Time: "08:30 – 10:00", Discipline: "Базы данных", Type: "Лк", Room: "201", Teacher: "Иванов И.И.", Subgroup: "Б-ИСиТ-22/1"},
		store.SourceLesson{ExternalID: "2", DayKey: 20261006, Date: "06.10.2026", Weekday: "Вт", LessonNumber: 2, Time: "10:10 – 11:40", Discipline: "Операционные системы", Type: "Лб", Room: "205", Teacher: "Петров П.П."},
		store.SourceLesson{ExternalID: "3", DayKey: 20261007, Date: "07.10.2026", Weekday: "Ср", LessonNumber: 1, Time: "08:30 – 10:00", Discipline: "Матанализ", Type: "Лк", Room: "301", Teacher: "Сидоров С.С."},
		store.SourceLesson{ExternalID: "4", DayKey: 20261013, Date: "13.10.2026", Weekday: "Вт", LessonNumber: 1, Time: "08:30 – 10:00", Discipline: "Вне недели"},
	)

	// Границы недели — unix-секунды (тот же контракт, что у ленты), но дни при
	// этом считаются по календарю источника.
	weekStart := time.Date(2026, 10, 5, 9, 0, 0, 0, time.Local).Unix()
	weekEnd := time.Date(2026, 10, 8, 9, 0, 0, 0, time.Local).Unix()

	rec := lessonsQuery(e.h, fmt.Sprintf("/api/schedule/lessons?group=494&from=%d&to=%d", weekStart, weekEnd))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d: %s", rec.Code, rec.Body.String())
	}

	payload := decode(t, rec)
	if payload["group_id"] != "494" || payload["group_name"] != "Б-ИСиТ-22" {
		t.Errorf("группа в ответе: %v / %v", payload["group_id"], payload["group_name"])
	}

	days, ok := payload["days"].([]any)
	if !ok {
		t.Fatalf("days не массив: %T", payload["days"])
	}
	if len(days) != 2 {
		t.Fatalf("дней в ответе %d, ожидалось 2 (пара 13.10 вне недели)", len(days))
	}

	first := days[0].(map[string]any)
	if first["day_key"] != float64(20261006) || first["weekday"] != "Вт" {
		t.Errorf("первый день: %v", first)
	}
	lessons, _ := first["lessons"].([]any)
	if len(lessons) != 2 {
		t.Fatalf("пар в первый день %d, ожидалось 2", len(lessons))
	}
	// Порядок пар на странице — по номеру: иначе «1-я пара» и «2-я пара»
	// менялись бы местами между перезагрузками.
	if lessons[0].(map[string]any)["lesson_number"] != float64(1) {
		t.Errorf("первая пара имеет номер %v", lessons[0].(map[string]any)["lesson_number"])
	}

	if updated, _ := payload["updated_at"].(float64); updated == 0 {
		t.Error("updated_at = 0 — подпись «обновлено» нечем построить")
	}
	months, _ := payload["imported_months"].([]any)
	if len(months) != 1 {
		t.Fatalf("imported_months: %v", payload["imported_months"])
	}
	if strings.Contains(rec.Body.String(), testApeksToken) {
		t.Fatal("токен источника в ответе расписания")
	}
}

func TestScheduleLessonsAPIByMonthAndYear(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t, store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"})
	e.seedMonth(t, "494", 10, 2026,
		store.SourceLesson{ExternalID: "1", DayKey: 20261001, Date: "01.10.2026", Weekday: "Чт", LessonNumber: 1},
		store.SourceLesson{ExternalID: "2", DayKey: 20261031, Date: "31.10.2026", Weekday: "Сб", LessonNumber: 2},
	)
	e.seedMonth(t, "494", 11, 2026,
		store.SourceLesson{ExternalID: "3", DayKey: 20261102, Date: "02.11.2026", Weekday: "Пн", LessonNumber: 1},
	)

	rec := lessonsQuery(e.h, "/api/schedule/lessons?group=%D0%91-%D0%98%D0%A1%D0%98%D0%A2-22&month=10&year=2026")
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d: %s", rec.Code, rec.Body.String())
	}

	body := decode(t, rec)
	// Границы месяца считает сервер: 31 октября внутри, 2 ноября — уже нет.
	rangeValue := body["range"].(map[string]any)
	if rangeValue["from"] != float64(20261001) || rangeValue["to"] != float64(20261031) {
		t.Errorf("диапазон месяца: %v", rangeValue)
	}
	days, _ := body["days"].([]any)
	if len(days) != 2 {
		t.Fatalf("дней %d, ожидалось 2 — ноябрьские пары попали в октябрь", len(days))
	}
	// Группа разрешена по имени: список на странице показывает названия, а id
	// человек не знает.
	if body["group_id"] != "494" {
		t.Errorf("разрешение по имени не сработало: %v", body["group_id"])
	}
	months, _ := body["imported_months"].([]any)
	if len(months) != 2 {
		t.Errorf("imported_months = %v, ожидалось 2 месяца", body["imported_months"])
	}
}

func TestScheduleLessonsAPIRejectsBadRequests(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t, store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"})

	cases := []struct {
		name   string
		target string
		code   int
	}{
		{"без группы", "/api/schedule/lessons", http.StatusUnprocessableEntity},
		{"несуществующая группа", "/api/schedule/lessons?group=777", http.StatusNotFound},
		{"месяц вне диапазона", "/api/schedule/lessons?group=494&month=13&year=2026", http.StatusUnprocessableEntity},
		{"месяц не число", "/api/schedule/lessons?group=494&month=%D0%BE%D0%BA%D1%82&year=2026", http.StatusUnprocessableEntity},
		{"год не число", "/api/schedule/lessons?group=494&month=10&year=%D0%B4%D0%B0", http.StatusUnprocessableEntity},
		{"отрицательный from", "/api/schedule/lessons?group=494&from=-5", http.StatusUnprocessableEntity},
		{"пустой месяц при заданном годе", "/api/schedule/lessons?group=494&year=2026", http.StatusUnprocessableEntity},
	}

	for _, testCase := range cases {
		rec := lessonsQuery(e.h, testCase.target)
		if rec.Code != testCase.code {
			t.Errorf("%s: код %d, ожидалось %d — %s", testCase.name, rec.Code, testCase.code, rec.Body.String())
		}
	}

	// «Группы нет» обязано подсказывать, что делать: почти всегда это значит,
	// что каталог ещё не синхронизировали.
	missing := lessonsQuery(e.h, "/api/schedule/lessons?group=777")
	if !strings.Contains(missing.Body.String(), "sync") {
		t.Errorf("ответ про неизвестную группу не подсказывает синхронизацию: %s", missing.Body.String())
	}
}

func TestScheduleLessonsAPIAmbiguousGroupIs422(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t,
		store.SourceGroup{ExternalID: "494", Name: "Одинаковая"},
		store.SourceGroup{ExternalID: "712", Name: "Одинаковая"},
	)

	rec := lessonsQuery(e.h, "/api/schedule/lessons?group=%D0%9E%D0%B4%D0%B8%D0%BD%D0%B0%D0%BA%D0%BE%D0%B2%D0%B0%D1%8F")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("код %d, ожидалось 422: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "494") || !strings.Contains(body, "712") {
		t.Errorf("в ответе нет кандидатов, из которых можно выбрать: %s", body)
	}
}

func TestScheduleSourceReadsMethodNotAllowed(t *testing.T) {
	e := newScheduleSourceEnv(t)

	post := httptest.NewRecorder()
	http.HandlerFunc(e.h.ScheduleGroupsAPI).ServeHTTP(post, request(http.MethodPost, "/api/schedule/groups", "{}", "10.0.0.1:1", nil))
	if post.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST на маршрут каталога: код %d, ожидалось 405", post.Code)
	}

	getImport := httptest.NewRecorder()
	http.HandlerFunc(e.h.ScheduleImportAPI).ServeHTTP(getImport, request(http.MethodGet, "/api/schedule/source/lessons", "", "10.0.0.1:1", nil))
	if getImport.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET на маршрут импорта: код %d, ожидалось 405", getImport.Code)
	}
}

// --- Права на управление -----------------------------------------------------

func TestScheduleStatusAPIOnlyForAdmin(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t, store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"})

	status := http.HandlerFunc(e.h.ScheduleStatusAPI)

	// Студент и преподаватель не могут ни спросить состояние источника, ни тем
	// более ходить к сайту вуза: у них нет права schedule.source.manage.
	for _, role := range []string{"student", "teacher"} {
		rec := e.callAdminGET(t, role, "/api/schedule/source/status", status)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: код %d, ожидалось 403 — %s", role, rec.Code, rec.Body.String())
		}
	}

	rec := e.callAdminGET(t, "admin", "/api/schedule/source/status", status)
	if rec.Code != http.StatusOK {
		t.Fatalf("админу отказано: %d %s", rec.Code, rec.Body.String())
	}

	payload := decode(t, rec)
	if payload["configured"] != false {
		t.Errorf("configured = %v при отсутствии клиента источника", payload["configured"])
	}
	// Не настроенный источник — состояние конфигурации, а не отказ: панель
	// должна показать подсказку, а не ошибку.
	if payload["missing_config"] != "APEKS_TOKEN" {
		t.Errorf("подсказка о настройке: %v", payload["missing_config"])
	}
	stats, ok := payload["stats"].(map[string]any)
	if !ok || stats["groups"] != float64(1) {
		t.Errorf("статистика источника: %v", payload["stats"])
	}
}

func TestSyncGroupsWritesCatalogAndAudit(t *testing.T) {
	e := newScheduleSourceEnv(t)
	calls := e.attachApeks(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("token"); got != testApeksToken {
			t.Errorf("источнику ушёл токен %q", got)
		}
		_, _ = w.Write([]byte(apeksGroupsPayload()))
	})

	rec := e.callAdmin(t, "admin", "/api/schedule/source/groups", "",
		http.HandlerFunc(e.h.ScheduleSyncGroupsAPI), true)

	if rec.Code != http.StatusOK {
		t.Fatalf("код %d: %s", rec.Code, rec.Body.String())
	}
	if calls.Load() != 1 {
		t.Errorf("обращений к источнику %d, ожидалось 1", calls.Load())
	}

	payload := decode(t, rec)
	if payload["fetched"] != float64(2) || payload["added"] != float64(2) {
		t.Errorf("ответ синхронизации: %v", payload)
	}
	if strings.Contains(rec.Body.String(), testApeksToken) {
		t.Fatal("токен в ответе синхронизации")
	}

	groups, err := e.src.ListGroups(context.Background(), apeks.Source, "", 10)
	if err != nil || len(groups) != 2 {
		t.Fatalf("каталог после синхронизации: %d (%v)", len(groups), err)
	}
	// Кафедра и направление сохраняются: по ним человек различает группы с
	// одинаковым коротким названием.
	if groups[0].Department == "" || groups[0].Course == "" {
		t.Errorf("кафедра и направление не сохранены: %+v", groups[0])
	}

	event := lastAudit(t, e.h, store.AuditScheduleGroupsSync)
	if !event.OK {
		t.Error("удачная синхронизация записана как неудачная")
	}
	if !strings.Contains(event.Detail, "added=2") {
		t.Errorf("детали события: %q", event.Detail)
	}
	assertNoSecretInAudit(t, e.h)
}

func TestSyncGroupsDeniedWithoutCSRF(t *testing.T) {
	e := newScheduleSourceEnv(t)
	calls := e.attachApeks(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(apeksGroupsPayload()))
	})

	cookies := e.loginAs(t, "admin")
	handler := e.h.RequireCSRF(e.h.RequirePerm(authz.ScheduleSourceManage, "нет права",
		http.HandlerFunc(e.h.ScheduleSyncGroupsAPI)))

	// Сессия настоящая, право есть — не хватает только заголовка double-submit.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, request(http.MethodPost, "/api/schedule/source/groups", "", "10.0.0.9:1", cookies))

	if rec.Code != http.StatusForbidden {
		t.Errorf("без CSRF-заголовка код %d, ожидалось 403 — %s", rec.Code, rec.Body.String())
	}
	// Источник не должен быть тронут: проверка CSRF стоит до обращения к чужому
	// сайту.
	if calls.Load() != 0 {
		t.Errorf("источник тронут %d раз при отказе CSRF", calls.Load())
	}
}

func TestSyncGroupsDeniedForTeacher(t *testing.T) {
	e := newScheduleSourceEnv(t)
	calls := e.attachApeks(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(apeksGroupsPayload()))
	})

	rec := e.callAdmin(t, "teacher", "/api/schedule/source/groups", "",
		http.HandlerFunc(e.h.ScheduleSyncGroupsAPI), true)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("код %d, ожидалось 403: %s", rec.Code, rec.Body.String())
	}
	if calls.Load() != 0 {
		t.Errorf("отказанный запрос всё равно дошёл до источника (%d раз)", calls.Load())
	}
}

func TestSyncGroupsRequiresSession(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.attachApeks(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("без пользователя запрос к источнику недопустим")
	})

	rec := httptest.NewRecorder()
	anonymous := request(http.MethodPost, "/api/schedule/source/groups", "", "10.0.0.9:1", nil)
	http.HandlerFunc(e.h.ScheduleSyncGroupsAPI).ServeHTTP(rec, anonymous)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("код %d, ожидалось 401", rec.Code)
	}
}

func TestImportWritesLessonsAndAudit(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t, store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"})

	var queries []string
	e.attachApeks(t, func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		queries = append(queries, strings.Join([]string{query.Get("group_id"), query.Get("month"), query.Get("year")}, "|"))
		_, _ = w.Write([]byte(apeksMonthPayload(7001, "06.10.2026")))
	})

	rec := e.callAdmin(t, "admin", "/api/schedule/source/lessons",
		`{"group":"494","month":10,"year":2026,"count":1}`,
		http.HandlerFunc(e.h.ScheduleImportAPI), true)

	if rec.Code != http.StatusOK {
		t.Fatalf("код %d: %s", rec.Code, rec.Body.String())
	}
	if len(queries) != 1 || queries[0] != "494|10|2026" {
		t.Errorf("источнику ушли не те параметры: %v", queries)
	}

	payload := decode(t, rec)
	if payload["lessons"] != float64(1) {
		t.Errorf("импортировано %v пар", payload["lessons"])
	}
	if payload["group_name"] != "Б-ИСиТ-22" {
		t.Errorf("имя группы в ответе: %v", payload["group_name"])
	}
	if strings.Contains(rec.Body.String(), testApeksToken) {
		t.Fatal("токен в ответе импорта")
	}

	stored := e.lessons(t, "494")
	if len(stored) != 1 {
		t.Fatalf("в базе %d пар", len(stored))
	}
	// Импорт разбор делает здесь, а не на странице: из аудитории вырезан HTML,
	// двойные пробелы схлопнуты, номер пары проставлен, день — YYYYMMDD по
	// календарю источника.
	if stored[0].Room != "201" {
		t.Errorf("из аудитории не вырезан HTML: %q", stored[0].Room)
	}
	if stored[0].Discipline != "Базы данных" {
		t.Errorf("в дисциплине остались двойные пробелы: %q", stored[0].Discipline)
	}
	if stored[0].Teacher != "Иванов И.И." {
		t.Errorf("преподаватель: %q", stored[0].Teacher)
	}
	if stored[0].DayKey != 20261006 || stored[0].Weekday != "Вт" {
		t.Errorf("день пары: %d %q", stored[0].DayKey, stored[0].Weekday)
	}
	if stored[0].LessonNumber != 1 {
		t.Errorf("номер пары: %d", stored[0].LessonNumber)
	}

	event := lastAudit(t, e.h, store.AuditScheduleImport)
	if !event.OK || event.TargetID != "494" {
		t.Errorf("событие импорта: ok=%v target=%q", event.OK, event.TargetID)
	}
	assertNoSecretInAudit(t, e.h)
}

func TestImportDefaultsToCurrentMonth(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t, store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"})

	var gotMonth, gotYear string
	e.attachApeks(t, func(w http.ResponseWriter, r *http.Request) {
		gotMonth, gotYear = r.URL.Query().Get("month"), r.URL.Query().Get("year")
		_, _ = w.Write([]byte(apeksMonthPayload(1, "06.10.2026")))
	})

	// Кнопка «импортировать это» без полей даты должна работать: требовать от
	// человека ввести месяц — значит дать повод ввести его неправильно.
	rec := e.callAdmin(t, "admin", "/api/schedule/source/lessons", `{"group":"494"}`,
		http.HandlerFunc(e.h.ScheduleImportAPI), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d: %s", rec.Code, rec.Body.String())
	}

	if gotMonth == "" || gotYear == "" {
		t.Fatalf("источник не спросили про месяц: %q %q", gotMonth, gotYear)
	}
	// Запуск ровно в полночь последнего числа месяца мог бы разойтись с
	// обработчиком на месяц, поэтому допускается и текущий, и предыдущий месяц —
	// но год обязан быть настоящим, а не нулём.
	now := time.Now().Local()
	previous := int(now.Month()) - 1
	if previous == 0 {
		previous = 12
	}
	if gotMonth != strconv.Itoa(int(now.Month())) && gotMonth != strconv.Itoa(previous) {
		t.Errorf("месяц запроса %q, ожидался текущий (%d или %d)", gotMonth, int(now.Month()), previous)
	}
	if gotYear != strconv.Itoa(now.Year()) {
		t.Errorf("год запроса %q, ожидался %d", gotYear, now.Year())
	}
}

func TestImportValidatesRequest(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t, store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"})
	e.attachApeks(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(apeksMonthPayload(1, "06.10.2026")))
	})

	cases := []struct {
		name string
		body string
		code int
	}{
		{"не JSON", "not json", http.StatusBadRequest},
		{"неизвестное поле", `{"group":"494","months":10}`, http.StatusBadRequest},
		{"без группы", `{"month":10,"year":2026}`, http.StatusUnprocessableEntity},
		{"месяц без года", `{"group":"494","month":10}`, http.StatusUnprocessableEntity},
		{"месяц вне диапазона", `{"group":"494","month":13,"year":2026}`, http.StatusUnprocessableEntity},
		{"подозрительный год", `{"group":"494","month":10,"year":1999}`, http.StatusUnprocessableEntity},
		{"слишком много месяцев", `{"group":"494","month":10,"year":2026,"count":4}`, http.StatusUnprocessableEntity},
		{"несуществующая группа", `{"group":"999","month":10,"year":2026}`, http.StatusNotFound},
	}

	for _, testCase := range cases {
		rec := e.callAdmin(t, "admin", "/api/schedule/source/lessons", testCase.body,
			http.HandlerFunc(e.h.ScheduleImportAPI), true)
		if rec.Code != testCase.code {
			t.Errorf("%s: код %d, ожидалось %d — %s", testCase.name, rec.Code, testCase.code, rec.Body.String())
		}
	}
}

func TestImportWalksMonthsSequentially(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t, store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"})

	var seen []string
	e.attachApeks(t, func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		seen = append(seen, query.Get("month")+"/"+query.Get("year"))
		_, _ = w.Write([]byte(apeksMonthPayload(100+len(seen), "06.10.2026")))
	})

	rec := e.callAdmin(t, "admin", "/api/schedule/source/lessons",
		`{"group":"494","month":10,"year":2026,"count":2}`,
		http.HandlerFunc(e.h.ScheduleImportAPI), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d: %s", rec.Code, rec.Body.String())
	}

	// Месяцы идут по одному запросом, а не веером: источник — чужой сайт.
	if strings.Join(seen, ",") != "10/2026,11/2026" {
		t.Errorf("месяцы запрошены не по порядку: %v", seen)
	}
	months, _ := decode(t, rec)["months"].([]any)
	if len(months) != 2 {
		t.Fatalf("в ответе %d месяцев", len(months))
	}
}

func TestImportWhenSourceIsNotConfigured(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t, store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"})

	// e.h.Apeks == nil: сервер запущен без APEKS_TOKEN. Это рабочее состояние,
	// и ответ должен говорить именно это, а не «внутренняя ошибка».
	rec := e.callAdmin(t, "admin", "/api/schedule/source/lessons",
		`{"group":"494","month":10,"year":2026}`,
		http.HandlerFunc(e.h.ScheduleImportAPI), true)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("код %d, ожидалось 503: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "APEKS_TOKEN") {
		t.Errorf("ответ не говорит, чего не хватает: %s", body)
	}
	// Отказ источника различим кодом, а не только текстом: страница решает по
	// нему «показать кнопку повтора», а не «показать красную строку».
	if decoded := decodeError(t, rec); decoded.Code != ErrSourceUnavailable {
		t.Errorf("код отказа %q, ожидалось %q", decoded.Code, ErrSourceUnavailable)
	}

	event := lastAudit(t, e.h, store.AuditScheduleImport)
	if event.OK {
		t.Error("импорт при ненастроенном источнике записан как удачный")
	}
	if !strings.Contains(event.Detail, "not_configured") {
		t.Errorf("в деталях события нет причины: %q", event.Detail)
	}
	assertNoSecretInAudit(t, e.h)
}

func TestImportWhenSourceFailsHidesDetails(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t, store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"})

	e.attachApeks(t, func(w http.ResponseWriter, r *http.Request) {
		// 403 — токен не принят. Настоящая причина нужна в логе процесса, а не
		// в браузере админа.
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"token":"` + testApeksToken + `"}`))
	})

	rec := e.callAdmin(t, "admin", "/api/schedule/source/lessons",
		`{"group":"494","month":10,"year":2026}`,
		http.HandlerFunc(e.h.ScheduleImportAPI), true)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("код %d, ожидалось 502: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, testApeksToken) {
		t.Fatal("токен источника утек в ответ браузеру")
	}
	if !strings.Contains(body, apeks.DisplayName) {
		t.Errorf("ответ не называет источник: %s", body)
	}
	// 502 и 503 для клиента — один код: «источник сейчас не даст данных».
	// Различать «не настроен» и «не ответил» приходится по тексту, а не по коду.
	if decoded := decodeError(t, rec); decoded.Code != ErrSourceUnavailable {
		t.Errorf("код отказа %q, ожидалось %q", decoded.Code, ErrSourceUnavailable)
	}

	// Ничего не записано: отказ источника не должен оставлять половину месяца в
	// базе.
	if stored := e.lessons(t, "494"); len(stored) != 0 {
		t.Errorf("после отказа источника в базе %d пар", len(stored))
	}

	event := lastAudit(t, e.h, store.AuditScheduleImport)
	if event.OK {
		t.Error("отказ источника записан как успех")
	}
	assertNoSecretInAudit(t, e.h)
}

func TestImportBrokenPayloadKeepsOldMonth(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t, store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"})
	e.seedMonth(t, "494", 10, 2026,
		store.SourceLesson{ExternalID: "old", DayKey: 20261006, Date: "06.10.2026", Discipline: "Было"})

	e.attachApeks(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"lessons":"не массив"}}`))
	})

	rec := e.callAdmin(t, "admin", "/api/schedule/source/lessons",
		`{"group":"494","month":10,"year":2026}`,
		http.HandlerFunc(e.h.ScheduleImportAPI), true)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("код %d, ожидалось 502: %s", rec.Code, rec.Body.String())
	}

	// Пакетная замена происходит только после удачного разбора: иначе кривой
	// ответ источника стирал бы уже импортированное расписание.
	stored := e.lessons(t, "494")
	if len(stored) != 1 || stored[0].Discipline != "Было" {
		t.Fatalf("импорт с битым ответом изменил базу: %+v", stored)
	}
}

func TestPublicReadsWorkWithoutSourceClient(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t, store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"})
	e.seedMonth(t, "494", 10, 2026,
		store.SourceLesson{ExternalID: "1", DayKey: 20261006, Date: "06.10.2026", Weekday: "Вт", LessonNumber: 1, Discipline: "Базы данных"})

	// Ключевое свойство архитектуры: витрина живёт без токена. Источник не
	// настроен, а страница расписания отдаёт импортированные данные.
	rec := lessonsQuery(e.h, "/api/schedule/lessons?group=494&month=10&year=2026")
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d: %s", rec.Code, rec.Body.String())
	}
	days, _ := decode(t, rec)["days"].([]any)
	if len(days) != 1 {
		t.Fatalf("дней %d, ожидалось 1", len(days))
	}

	if groups := sourceQuery(e.h, "/api/schedule/groups"); groups.Code != http.StatusOK {
		t.Errorf("каталог групп без клиента: код %d", groups.Code)
	}
}

func TestScheduleSourceStorageMissingIs503(t *testing.T) {
	// Deps без ScheduleSource — сборка, в которой модуль расписания не подключён.
	h, _ := newTestEnv(t)

	rec := sourceQuery(h, "/api/schedule/groups")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("код %d, ожидалось 503: %s", rec.Code, rec.Body.String())
	}

	lessons := lessonsQuery(h, "/api/schedule/lessons?group=494")
	if lessons.Code != http.StatusServiceUnavailable {
		t.Errorf("чтение пар без хранилища: код %d", lessons.Code)
	}
}

// --- Заглушки ответа источника -----------------------------------------------

// apeksGroupsPayload — каталог групп (форма «кафедра -> направление -> группа»).
func apeksGroupsPayload() string {
	return `{"data":{"groups":{
		"1":{"name":"Институт информационных технологий","items":{
			"2":{"name":"Информатика и вычислительная техника","items":{
				"3":{"id":494,"name":"Б-ИСиТ-22"},
				"4":{"id":495,"name":"Б-ИСиТ-23"}
			}}
		}}
	}}}`
}

// apeksMonthPayload — месяц одной группы: одна пара, причём аудитория с HTML
// (именно так источник отдаёт пустую аудиторию: <i class="fa ..."> без ауд. —
// см. фикстуру internal/apeks/testdata), а предмет — кривое число, какое
// встречается в самостоимтельной работе.
func apeksMonthPayload(lessonID int, day string) string {
	return fmt.Sprintf(`{"data":{
		"groupName":"Б-ИСиТ-22",
		"startDate":"01.10.2026","endDate":"31.10.2026",
		"lessonTimes":[{"id":1,"hour_from":8,"minute_from":30,"hour_to":10,"minute_to":0,"fullName":"08:30 – 10:00"}],
		"lessons":[{
			"id":%d,"date":"%s","lesson_time_id":1,
			"groupName":"Б-ИСиТ-22","discipline":"Базы    данных","class_type_name":"Лекция",
			"classroom":"<i class=\"fa fa-times\"></i> 201","staffNames":["Иванов    И.И."],
			"subgroup_id":0,"subgroupNames":"","is_empty":0,"self_work":""
		}]
	}}`, lessonID, day)
}

// assertNoSecretInAudit проверяет, что ни одна строка журнала не содержит секрета
// источника: деталь пишет админский импорт, а журнал читают и показывают.
func assertNoSecretInAudit(t *testing.T, h *Handlers) {
	t.Helper()

	events, err := h.Audit.List(context.Background(), store.AuditFilter{Limit: 200})
	if err != nil {
		t.Fatalf("прочитать журнал: %v", err)
	}

	for _, event := range events {
		blob := fmt.Sprintf("%s %s %s %s", event.Action, event.Detail, event.TargetID, event.TargetType)
		if strings.Contains(blob, testApeksToken) {
			t.Fatalf("токен источника в журнале: %+v", event)
		}
	}
}
