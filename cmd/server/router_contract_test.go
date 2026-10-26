package main

import (
	"database/sql"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/NumerR/SOS/internal/handlers"
	"github.com/NumerR/SOS/internal/store"
)

// newTestHandlers — обработчики поверх уже открытой базы.
//
// Вынесено из newTestServer: проверке контракта маршрутизатора и фронтенда
// нужны те же зависимости, но без HTTP-сервера, миграций модулей и сессий.
// Разделение намеренное — второй список зависимостей в тесте неизбежно
// отстал бы от первого.
func newTestHandlers(db *sql.DB) (*handlers.Handlers, *store.UserStore) {
	userStore := store.NewUserStore(db)
	return handlers.New(handlers.Deps{
		Users:       userStore,
		Sessions:    store.NewSessionStore(db),
		Courses:     store.NewCourseStore(db),
		Enrollments: store.NewEnrollmentStore(db),
		Assignments: store.NewAssignmentStore(db),
		Submissions: store.NewSubmissionStore(db),
		Schedule:    store.NewScheduleStore(db),
		Resources:   store.NewResourceStore(db),
		Events:      store.NewEventStore(db),
		Audit:       store.NewAuditStore(db),
		Limits:      handlers.NewLimiters(),
	}), userStore
}

// Замер, из-за которого существует этот файл.
//
// Страница «Расписание и ресурсы» показывала человеку под полем группы текст
// `404 page not found` вместо таймтейбла. Так выглядит сервер, собранный раньше,
// чем у модуля появились маршруты: статика читается с диска в момент запроса, а
// набор маршрутов зашит в бинарник при сборке. Фронтенд просит
// /api/schedule/groups, старый бинарник его не знает, и ловит не Handlers.NotFound
// (конверт ADR-0003 для /api/, web/404.html для страниц), а молчаливый
// http.NotFound из net/http: текстовая строка без кода отказа и без request id
// в теле — ровно то, что человек и увидел в подсказке поля.
//
// Упавший бинарник лечится перезапуском. Этот тест не даёт классу вернуться в
// другом виде: он сверяет пути, которые просит web/js, с маршрутами, которые
// действительно заявил список moduleList. Переименование маршрута, модуль,
// забытый в списке, и опечатка в вызове становятся красным прогоном, а не
// пустой страницей у владельца.

// apiPathRef — путь из аргумента GS.fetchJSON: "/api/…" или `/api/…?group=…`.
// Кавычка или обратная кавычка слева обязательны: без них регулярка собирала бы
// адрес из комментариев и из чужих URL (https://api.example.test/api/courses).
var apiPathRef = regexp.MustCompile(`["` + "`" + `]/api/[A-Za-z0-9_/-]+`)

// frontendAPIPaths — все пути API, которые фронтенд вообще умеет запросить.
//
// Обходится весь web/js, а не только страницы: модальные окна и common.js тоже
// делают вызовы, и потерянный там маршрут человек увидел бы так же — текстом
// 404 вместо содержимого.
func frontendAPIPaths(t *testing.T) []string {
	t.Helper()

	root := filepath.Join("..", "..", "web", "js")
	var jsFiles []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".js") {
			jsFiles = append(jsFiles, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("не удалось обойти %s: %v (проверка без фронтенда ничего не утверждает)", root, err)
	}

	seen := map[string]bool{}
	for _, file := range jsFiles {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("читать %s: %v", file, err)
		}
		for _, match := range apiPathRef.FindAllString(string(body), -1) {
			seen[match[1:]] = true
		}
	}

	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// TestFrontendCallsOnlyRegisteredRoutes — каждый запрошенный путём вызов знает
// маршрутизатор живого списка модулей.
//
// Сверяется именно роутер, а не реестр: реестр утверждает, что маршрут заявлен,
// а спрашивает человек то, что к нему приводит http.ServeMux. Паттерн, которым
// mux отвечает на путь, обязан совпадать с самим путём — иначе запрос ушёл в
// catch-all «/» (страницы и Handlers.NotFound), и это тот самый текстовый 404.
func TestFrontendCallsOnlyRegisteredRoutes(t *testing.T) {
	paths := frontendAPIPaths(t)

	// Порог вырождения: если регулярка или обход каталога сломаются, список
	// станет пустым или крошечным, и проверка прошла бы молча, не проверив ни
	// одного вызова. Восемнадцать путей фронтенда были здесь на момент правки.
	if len(paths) < 18 {
		t.Fatalf("во фронтенде найдено %d путей API (%v) — настолько малый список значит, что разбор сломался", len(paths), paths)
	}

	db := openTestDBForApp(t)
	h, _ := newTestHandlers(db)
	router, err := newRouter(h, db, moduleList(h))
	if err != nil {
		t.Fatalf("сборка маршрутизатора: %v", err)
	}

	mux, ok := router.(*http.ServeMux)
	if !ok {
		t.Fatalf("newRouter вернул %T, а не *http.ServeMux — сверять паттерны больше нечем", router)
	}

	for _, path := range paths {
		route, pattern := mux.Handler(httptest.NewRequest(http.MethodGet, path, nil))
		if route == nil || pattern != path {
			t.Errorf("фронтенд просит %s, а маршрутизатор отвечает паттерном %q: запрос уйдёт в catch-all и человек увидит текстовый 404", path, pattern)
		}
	}
}

// TestFrontendScheduleRoutesArePublicReads — отдельная проверка двух маршрутов,
// из-за которых всё и началось.
//
// Публичность чтения расписания — контракт страницы (docs/02 §5): гость видит те
// же пары, что и студент. Если маршрут станет защищённым, страница не покажет
// 404 — она уведёт гостя на вход, и вместо «расписания нет» человек получит
// «войдите». Формально это тоже отказ рисовать таймтейбл, поэтому фиксируется
// здесь, рядом с остальными маршрутами расписания.
func TestFrontendScheduleRoutesArePublicReads(t *testing.T) {
	db := openTestDBForApp(t)
	h, _ := newTestHandlers(db)
	router, err := newRouter(h, db, moduleList(h))
	if err != nil {
		t.Fatalf("сборка маршрутизатора: %v", err)
	}

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	for _, path := range []string{"/api/schedule/groups", "/api/schedule/lessons?group=494&month=10&year=2026"} {
		resp, body := get(srv, path, nil)
		resp.Body.Close()

		// 401 здесь означает «чтение закрыли», 404 — «маршрута нет в этом
		// роутере». Ни то, ни ни другое гостю показывать нельзя.
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusNotFound {
			t.Errorf("гость GET %s: %d %s, ожидалось не 401 и не 404 (чтение расписания публичное)", path, resp.StatusCode, body)
		}
	}
}
