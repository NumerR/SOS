package platform_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NumerR/SOS/internal/apierror"
	"github.com/NumerR/SOS/internal/authz"
	"github.com/NumerR/SOS/internal/database"
	"github.com/NumerR/SOS/internal/platform"
)

// okHandler — заглушка обработчика: тестам важна цепочка обёрток, а не содержимое.
func okHandler(marker string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Marker", marker)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(marker))
	})
}

// recorder фиксирует, какие обёртки платформа реально поставила.
type recorder struct {
	authCalls   int
	csrfCalls   int
	permissions []authz.Permission
	denies      []string
}

func (rec *recorder) middleware() platform.Middleware {
	return platform.Middleware{
		Auth: func(next http.Handler) http.Handler {
			rec.authCalls++
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Auth", "checked")
				next.ServeHTTP(w, r)
			})
		},
		CSRF: func(next http.Handler) http.Handler {
			rec.csrfCalls++
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Csrf", "checked")
				next.ServeHTTP(w, r)
			})
		},
		Permission: func(perm authz.Permission, deny string, next http.Handler) http.Handler {
			rec.permissions = append(rec.permissions, perm)
			rec.denies = append(rec.denies, deny)
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Perm", string(perm))
				next.ServeHTTP(w, r)
			})
		},
	}
}

// TestMountDispatchesByMethod — один путь, разные методы, разные права.
//
// Это главный смысл разделения Route по методам: GET /api/courses остаётся
// «просто войти», а POST /api/courses требует course.create. Если бы маршрут
// объявлялся одним handler-ом на путь, разница жила бы внутри обработчика.
func TestMountDispatchesByMethod(t *testing.T) {
	rec := &recorder{}
	reg := platform.NewRegistry()

	reg.Add(platform.Route{Path: "/api/courses", Method: "GET", Handler: okHandler("list")})
	reg.Add(platform.Route{
		Path:        "/api/courses",
		Method:      "POST",
		Perm:        authz.CourseCreate,
		DenyMessage: "only teachers can create courses",
		CSRF:        true,
		Handler:     okHandler("create"),
	})
	reg.Add(platform.Route{
		Path:    "/api/auth/login",
		Method:  "POST",
		Public:  true,
		Handler: okHandler("login"),
	})

	mux := http.NewServeMux()
	if err := platform.Mount(mux, reg, rec.middleware()); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	// GET: обёртка входа поставлена, CSRF — нет (метод безопасный).
	recGET := httptest.NewRecorder()
	mux.ServeHTTP(recGET, httptest.NewRequest(http.MethodGet, "/api/courses", nil))
	if recGET.Header().Get("X-Auth") != "checked" {
		t.Error("GET /api/courses не прошёл через обёртку Auth")
	}
	if recGET.Header().Get("X-Csrf") != "" {
		t.Error("на GET была поставлена обёртка CSRF")
	}
	if body := recGET.Body.String(); body != "list" {
		t.Errorf("GET /api/courses отдал %q, ожидалось %q", body, "list")
	}

	// POST: право + CSRF, причём право запрашивается то, что заявлено.
	recPOST := httptest.NewRecorder()
	mux.ServeHTTP(recPOST, httptest.NewRequest(http.MethodPost, "/api/courses", nil))
	if recPOST.Header().Get("X-Perm") != string(authz.CourseCreate) {
		t.Errorf("POST /api/courses: право %q, ожидалось %q", recPOST.Header().Get("X-Perm"), authz.CourseCreate)
	}
	if recPOST.Header().Get("X-Csrf") != "checked" {
		t.Error("POST /api/courses не прошёл через обёртку CSRF")
	}

	// Публичный маршрут не должен тянуть обёртку входа: иначе login стал бы
	// закрыт для тех, кто ещё не вошёл.
	recLogin := httptest.NewRecorder()
	mux.ServeHTTP(recLogin, httptest.NewRequest(http.MethodPost, "/api/auth/login", nil))
	if recLogin.Header().Get("X-Auth") != "" {
		t.Error("публичный маршрут /api/auth/login прошёл через Auth")
	}

	if len(rec.permissions) != 1 || rec.permissions[0] != authz.CourseCreate {
		t.Errorf("Permission-обёртка вызвана для %v, ожидалось [course.create]", rec.permissions)
	}
	if len(rec.denies) != 1 || rec.denies[0] != "only teachers can create courses" {
		t.Errorf("текст отказа передан неверно: %v", rec.denies)
	}
}

// errorCode — код отказа из конверта ADR-0003.
//
// Тест разбирает JSON, а не ищет подстроку «"error"»: проверка-подстрока
// прошла бы и на старом плоском {"error": "текст"}, из-за чего возврат к нему
// в транспорте остался бы незамеченным.
func errorCode(t *testing.T, body string) string {
	t.Helper()

	var raw struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatalf("отказ не является JSON: %v (%s)", err, body)
	}
	if len(raw.Error) == 0 {
		t.Fatalf("в отказе нет поля error: %s", body)
	}
	if raw.Error[0] == '"' {
		t.Fatalf("отказ вернулся в старом плоском виде: %s", body)
	}

	var decoded apierror.Body
	if err := json.Unmarshal(raw.Error, &decoded); err != nil {
		t.Fatalf("разобрать объект ошибки: %v (%s)", err, body)
	}
	if decoded.Code == "" {
		t.Fatalf("у отказа нет кода: %s", body)
	}
	return decoded.Code
}

// TestUnknownMethodIs405 — у пути есть обработчики, но не для этого метода.
//
// 405, а не 404: клиенту важно различать «такого ресурса нет» и «ресурс есть,
// но так с ним не обращаются». Ответ — тот же конверт, что пишут обработчики:
// у транспорта нет причины отвечать иначе.
func TestUnknownMethodIs405(t *testing.T) {
	reg := platform.NewRegistry()
	reg.Add(platform.Route{Path: "/api/courses", Method: "GET", Handler: okHandler("list")})

	mux := http.NewServeMux()
	if err := platform.Mount(mux, reg, (&recorder{}).middleware()); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/courses", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("код %d, ожидалось 405", rec.Code)
	}
	if code := errorCode(t, rec.Body.String()); code != apierror.ErrMethod {
		t.Errorf("код отказа %q, ожидалось %q", code, apierror.ErrMethod)
	}

	// Незнакомый путь обязан остаться за mux-ом (404), а не превратиться в 405.
	missing := httptest.NewRecorder()
	mux.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/api/unknown", nil))
	if missing.Code != http.StatusNotFound {
		t.Errorf("несуществующий путь отдал %d, ожидалось 404", missing.Code)
	}
}

// TestDefaultErrorWriterKeepsTheEnvelope — отказ транспорта без переданного
// Middleware.Error.
//
// Поле Error можно не заполнять (тесты ядра его не заполняют), но форма отказа
// от этого меняться не должна: клиент не обязан знать, кто именно ему отказал.
func TestDefaultErrorWriterKeepsTheEnvelope(t *testing.T) {
	reg := platform.NewRegistry()
	reg.Add(platform.Route{Path: "/api/thing", Method: "GET", Public: true, Handler: okHandler("thing")})

	mux := http.NewServeMux()
	if err := platform.Mount(mux, reg, platform.Middleware{}); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/thing", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("код %d, ожидалось 405", rec.Code)
	}
	if code := errorCode(t, rec.Body.String()); code != apierror.ErrMethod {
		t.Errorf("код отказа %q, ожидалось %q", code, apierror.ErrMethod)
	}
}

// TestCatchAllMethod — маршрут без метода принимает любой.
//
// Нужен для обработчиков, которые сами разбирают метод (так живут страницы и
// статика). При этом декларация «любой метод» не должна мешать объявить рядом
// конкретный метод на том же пути.
func TestCatchAllMethod(t *testing.T) {
	reg := platform.NewRegistry()
	reg.Add(platform.Route{Path: "/api/ping", Handler: okHandler("any")})

	mux := http.NewServeMux()
	if err := platform.Mount(mux, reg, (&recorder{}).middleware()); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPatch} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, "/api/ping", nil))
		if rec.Body.String() != "any" {
			t.Errorf("%s /api/ping отдал %q, ожидалось %q", method, rec.Body.String(), "any")
		}
	}
}

// TestRegistryValidation — ошибки декларации собираются списком.
//
// Каждая строка cases — это способ случайно оставить маршрут
// незащищённым или неработающим. Проверяем, что Mount не молчит ни в одном.
func TestRegistryValidation(t *testing.T) {
	cases := []struct {
		name   string
		routes []platform.Route
		want   string
	}{
		{"пустой путь", []platform.Route{{Method: "GET", Handler: okHandler("x")}}, "пустой Path"},
		{"путь без слэша", []platform.Route{{Path: "api/x", Method: "GET", Handler: okHandler("x")}}, "обязан начинаться"},
		{"nil обработчик", []platform.Route{{Path: "/api/x", Method: "GET"}}, "nil Handler"},
		{"public вместе с правом", []platform.Route{{Path: "/api/x", Method: "GET", Public: true, Perm: authz.CourseCreate, Handler: okHandler("x")}}, "противоречивы"},
		{
			"право не из реестра authz",
			[]platform.Route{{Path: "/api/x", Method: "POST", Perm: authz.Permission("note.teleport"), Handler: okHandler("x")}},
			"не объявлено в authz.Registry()",
		},
		{
			"дубликат метода на пути",
			[]platform.Route{
				{Path: "/api/x", Method: "GET", Handler: okHandler("a")},
				{Path: "/api/x", Method: "GET", Handler: okHandler("b")},
			},
			"объявлен дважды",
		},
		{
			"два catch-all на пути",
			[]platform.Route{
				{Path: "/api/x", Handler: okHandler("a")},
				{Path: "/api/x", Handler: okHandler("b")},
			},
			"объявлен дважды",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := platform.NewRegistry()
			for _, route := range tc.routes {
				reg.Add(route)
			}

			err := platform.Mount(http.NewServeMux(), reg, (&recorder{}).middleware())
			if err == nil {
				t.Fatalf("ожидалась ошибка, Mount прошёл")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("текст ошибки %q не содержит %q", err.Error(), tc.want)
			}
		})
	}
}

// TestMissingMiddlewareReported — платформа не может молча пропустить проверку.
//
// Если у маршрута заявлено право, а Permission-обёртка не передана, доступ
// стал бы открытым для всех. Такое ловится на старте.
func TestMissingMiddlewareReported(t *testing.T) {
	reg := platform.NewRegistry()
	reg.Add(platform.Route{Path: "/api/courses", Method: "POST", Perm: authz.CourseCreate, Handler: okHandler("x")})
	reg.Add(platform.Route{Path: "/api/courses", Method: "DELETE", CSRF: true, Handler: okHandler("x")})

	err := platform.Mount(http.NewServeMux(), reg, platform.Middleware{})
	if err == nil {
		t.Fatal("ожидалась ошибка о незаполненном Middleware")
	}
	if !strings.Contains(err.Error(), "Permission") || !strings.Contains(err.Error(), "CSRF") {
		t.Errorf("ошибка не перечисляет недостающие обёртки: %v", err)
	}
}

// TestRegistryCopiesIndependent — Routes()/Problems() отдают копии.
func TestRegistryCopiesIndependent(t *testing.T) {
	reg := platform.NewRegistry()
	reg.Add(platform.Route{Path: "/api/x", Method: "GET", Handler: okHandler("x")})

	routes := reg.Routes()
	routes[0].Path = "/hacked"

	if again := reg.Routes(); again[0].Path == "/hacked" {
		t.Error("изменение копии Routes() повлияло на реестр")
	}

	if len(reg.Problems()) != 0 {
		t.Errorf("корректный реестр сообщил о проблемах: %v", reg.Problems())
	}
}

// notesModule — пример «нового модуля» из docs/how-to-add-a-module.md.
//
// Он объявлен прямо здесь, чтобы проверка «модуль подключается без правок ядра»
// была исполняемой, а не обещанием в документации. Ядро (platform, authz,
// cmd/server) в этом тесте не меняется ни одной строкой.
type notesModule struct{}

func (notesModule) Name() string { return "notes" }

func (notesModule) Migrations() []platform.Migration {
	return []platform.Migration{
		{
			Version: "0001_create_notes",
			SQL: `CREATE TABLE IF NOT EXISTS notes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    author_id INTEGER NOT NULL,
    text TEXT NOT NULL
);`,
		},
	}
}

func (notesModule) Register(reg *platform.Registry) {
	reg.Add(platform.Route{Path: "/api/notes", Method: "GET", Handler: okHandler("notes-list")})
	reg.Add(platform.Route{Path: "/api/notes", Method: "POST", CSRF: true, Handler: okHandler("notes-create")})
}

// TestApplyMigrations — журнал миграций: применил один раз, повтор не трогает.
func TestApplyMigrations(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	modules := []platform.Module{notesModule{}}

	if err := platform.ApplyMigrations(ctx, db, modules); err != nil {
		t.Fatalf("первое применение: %v", err)
	}

	// Таблица модуля появилась.
	if _, err := db.ExecContext(ctx, `INSERT INTO notes (author_id, text) VALUES (1, 'первая');`); err != nil {
		t.Fatalf("таблица notes не создана: %v", err)
	}

	// И запись в журнале — ровно одна.
	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM platform_module_migrations WHERE module = 'notes';`).Scan(&count); err != nil {
		t.Fatalf("прочитать журнал: %v", err)
	}
	if count != 1 {
		t.Errorf("в журнале %d записей, ожидалось 1", count)
	}

	// Повторный запуск обязан пройти молча: иначе каждый рестарт сервера
	// падал бы на «table already exists».
	if err := platform.ApplyMigrations(ctx, db, modules); err != nil {
		t.Fatalf("повторное применение: %v", err)
	}
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM platform_module_migrations WHERE module = 'notes';`).Scan(&count); err != nil {
		t.Fatalf("прочитать журнал: %v", err)
	}
	if count != 1 {
		t.Errorf("после повтора в журнале %d записей, ожидалось 1", count)
	}
}

// TestApplyMigrationsRollsBackOnBadSQL — неудачная миграция не записывается.
//
// Иначе следующая попытка старта сочла бы её выполненной, и схема осталась бы
// с половиной таблицы навсегда — без единого сообщения об ошибке.
func TestApplyMigrationsRollsBackOnBadSQL(t *testing.T) {
	db := openTestDB(t)

	if err := platform.ApplyMigrations(context.Background(), db, []platform.Module{brokenModule{}}); err == nil {
		t.Fatal("миграция с битым SQL прошла без ошибки")
	}

	var count int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM platform_module_migrations WHERE module = 'broken';`).Scan(&count); err != nil {
		t.Fatalf("прочитать журнал: %v", err)
	}
	if count != 0 {
		t.Errorf("неудачная миграция записана в журнал (%d строк)", count)
	}
}

type brokenModule struct{}

func (brokenModule) Name() string { return "broken" }
func (brokenModule) Migrations() []platform.Migration {
	return []platform.Migration{{Version: "0001_broken", SQL: `CREATE TABLE broken_table (id INTEGER PRIMARY KEY); SYNTAX ERROR HERE;`}}
}
func (brokenModule) Register(*platform.Registry) {}

// TestApplyMigrationsRejectsBadModuleList — имена и версии модулей проверяются
// до первого запроса к базе.
func TestApplyMigrationsRejectsBadModuleList(t *testing.T) {
	db := openTestDB(t)

	cases := []struct {
		name    string
		modules []platform.Module
		want    string
	}{
		{"nil в списке", []platform.Module{nil}, "nil"},
		{"два одинаковых имени", []platform.Module{notesModule{}, notesModule{}}, "объявлен дважды"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := platform.ApplyMigrations(context.Background(), db, tc.modules)
			if err == nil {
				t.Fatalf("ожидалась ошибка (%s)", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("текст ошибки %q не содержит %q", err, tc.want)
			}
		})
	}

	// Дубликат версии внутри одного модуля.
	dup := duplicateVersionModule{}
	if err := platform.ApplyMigrations(context.Background(), db, []platform.Module{dup}); err == nil {
		t.Error("модуль с дубликатом версии прошёл проверку")
	} else if !strings.Contains(err.Error(), "дважды") {
		t.Errorf("текст ошибки %q не про дубликат версии", err)
	}

	// Пустая миграция.
	empty := emptySQLModule{}
	err := platform.ApplyMigrations(context.Background(), db, []platform.Module{empty})
	if err == nil {
		t.Fatal("пустая миграция прошла проверку")
	}
	if !strings.Contains(err.Error(), "пустая") {
		t.Errorf("текст ошибки %q не про пустую миграцию", err)
	}

	// nil база.
	if err := platform.ApplyMigrations(context.Background(), nil, []platform.Module{notesModule{}}); err == nil {
		t.Error("ApplyMigrations с nil-базой не вернул ошибку")
	}
}

type duplicateVersionModule struct{}

func (duplicateVersionModule) Name() string { return "dups" }
func (duplicateVersionModule) Migrations() []platform.Migration {
	return []platform.Migration{
		{Version: "0001_a", SQL: `CREATE TABLE dups_a (id INTEGER PRIMARY KEY);`},
		{Version: "0001_a", SQL: `CREATE TABLE dups_b (id INTEGER PRIMARY KEY);`},
	}
}
func (duplicateVersionModule) Register(*platform.Registry) {}

type emptySQLModule struct{}

func (emptySQLModule) Name() string { return "empty" }
func (emptySQLModule) Migrations() []platform.Migration {
	return []platform.Migration{{Version: "0001_empty", SQL: "   "}}
}
func (emptySQLModule) Register(*platform.Registry) {}

// TestMountAndMigrationsTogether — полный путь модуля: миграция + маршруты.
func TestMountAndMigrationsTogether(t *testing.T) {
	db := openTestDB(t)
	modules := []platform.Module{notesModule{}}

	ctx := context.Background()
	if err := platform.ApplyMigrations(ctx, db, modules); err != nil {
		t.Fatalf("миграции: %v", err)
	}

	reg := platform.NewRegistry()
	for _, module := range modules {
		module.Register(reg)
	}

	mux := http.NewServeMux()
	if err := platform.Mount(mux, reg, (&recorder{}).middleware()); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	resp, err := http.Get(server.URL + "/api/notes")
	if err != nil {
		t.Fatalf("GET /api/notes: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("код %d, ожидалось 200", resp.StatusCode)
	}

	// Таблица модуля реально доступна: миграция применилась до запроса.
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notes;`).Scan(&count); err != nil {
		t.Fatalf("запрос к таблице модуля: %v", err)
	}

	// POST требует CSRF — обёртка заявлена в маршруте.
	respPost, err := http.Post(server.URL+"/api/notes", "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatalf("POST /api/notes: %v", err)
	}
	defer respPost.Body.Close()
	if respPost.Header.Get("X-Csrf") != "checked" {
		t.Error("POST /api/notes не прошёл через CSRF-обёртку")
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := database.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("открыть базу: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := database.Migrate(db); err != nil {
		t.Fatalf("базовая схема: %v", err)
	}

	return db
}
