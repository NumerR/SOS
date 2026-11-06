package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Тесты конфигурации карты (GET /api/map/config).
//
// Здесь проверяется не формат JSON, а два обещания, которые страница держит
// через этот ответ:
//
//  1. Без тайл-сервера конфиг отвечает 200 с `tiles: null`, а не 503. Отсутствие
//     подложки — штатное состояние сборки: слои карты читаются и на пустом фоне,
//     и «оператор ничего не задал» не является сбоем сервера.
//  2. Негодное значение не уезжает в браузер. Адрес тайл-сервера оператор может
//     написать с ключом, и ответ карты — единственное место, куда этот адрес
//     мог бы попасть из окружения.
//
// Шаблон тайлов сравнивается дословно: `{z}/{x}/{y}` правит не сервер, а
// Leaflet, и любая «вежливая» нормализация адреса сломала бы карту, оставив
// ответ красивым.

func newMapConfigEnv(t *testing.T, tiles MapTiles) *Handlers {
	t.Helper()

	h, _ := newPlacesEnv(t)
	h.MapTiles = tiles

	return h
}

func mapConfigQuery(h *Handlers) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	http.HandlerFunc(h.MapConfigAPI).ServeHTTP(rec, request(http.MethodGet, "/api/map/config", "", "10.0.0.1:1", nil))

	return rec
}

type mapConfigView struct {
	Tiles *struct {
		URL         string `json:"url"`
		Attribution string `json:"attribution"`
	} `json:"tiles"`
	Problem string `json:"problem"`
}

func decodeMapConfig(t *testing.T, rec *httptest.ResponseRecorder) mapConfigView {
	t.Helper()

	if rec.Code != http.StatusOK {
		t.Fatalf("конфиг карты отвечен %d: %s", rec.Code, rec.Body.String())
	}

	var view mapConfigView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("разобрать конфиг карты: %v (%s)", err, rec.Body.String())
	}

	return view
}

// --- Состояние «подложки нет» ------------------------------------------------

func TestMapConfigWithoutTilesIsNotAFailure(t *testing.T) {
	// Сборка вообще без настройки тайлов: карта обязана остаться рабочей.
	h := newMapConfigEnv(t, MapTiles{})

	rec := mapConfigQuery(h)
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d, ожидалось 200: %s", rec.Code, rec.Body.String())
	}

	// Именно `null`, а не absent-поле и не пустой объект: фронтенд проверяет
	// одно поле и не должен разбирать два вида «нет».
	if body := rec.Body.String(); !strings.Contains(body, `"tiles":null`) {
		t.Errorf("конфиг без тайлов не говорит `tiles:null`: %s", body)
	}

	view := decodeMapConfig(t, rec)
	if view.Tiles != nil {
		t.Errorf("у ненастроенной подложки появились тайлы: %+v", view.Tiles)
	}
	if view.Problem != "MAP_TILES_URL" {
		t.Errorf("отказ не называет переменную: %+v", view)
	}
}

// --- Годная конфигурация -----------------------------------------------------

func TestMapConfigPassesTileTemplateUnchanged(t *testing.T) {
	const template = "https://tiles.example.test/v1/{z}/{x}/{y}.png"

	h := newMapConfigEnv(t, MapTiles{
		URL: template,
		// Пробелы по краям — как в реальности: значение часто приходит из
		// многострочного env-файла.
		Attribution: "  Вуз, 2026  ",
	})

	view := decodeMapConfig(t, mapConfigQuery(h))

	if view.Tiles == nil {
		t.Fatalf("настроенная подложка отвечена без тайлов: %+v", view)
	}
	if view.Tiles.URL != template {
		t.Errorf("шаблон тайлов изменён: %q, ожидалось %q", view.Tiles.URL, template)
	}
	if view.Tiles.Attribution != "Вуз, 2026" {
		t.Errorf("подпись не приведена: %q", view.Tiles.Attribution)
	}
	if view.Problem != "" {
		t.Errorf("у годной конфигурации есть отказ: %q", view.Problem)
	}

	// problem пустой — поля нет в ответе вовсе: клиент, читающий `problem`,
	// не должен спотыкаться о пустую строку в нормальном случае.
	if body := mapConfigQuery(h).Body.String(); strings.Contains(body, `"problem"`) {
		t.Errorf("при настроенных тайлах ответ всё равно содержит problem: %s", body)
	}
}

func TestMapConfigWithoutAttributionIsStillTiles(t *testing.T) {
	// Провайдер не требует подписи: это не повод ни выдумывать её, ни лишать
	// страницу подложки.
	h := newMapConfigEnv(t, MapTiles{URL: "http://localhost:8080/tiles/{z}/{x}/{y}.png"})

	view := decodeMapConfig(t, mapConfigQuery(h))
	if view.Tiles == nil {
		t.Fatalf("тайлы без подписки отвечены как отсутствующие: %+v", view)
	}
	if view.Tiles.Attribution != "" {
		t.Errorf("подписка придумана сервером: %q", view.Tiles.Attribution)
	}
}

// --- Отказ от значения, которое не может быть адресом -----------------------

func TestMapConfigRejectsUnusableTemplates(t *testing.T) {
	for _, tc := range []struct{ name, url string }{
		{"не адрес", "tiles.example.test/{z}/{x}/{y}.png"},
		{"без хоста", "https:///{z}/{x}/{y}.png"},
		{"чужая схема", "file:///var/tiles/{z}/{x}/{y}.png"},
		{"ключ в адресе", "https://user:secretnabor@tiles.example.test/{z}/{x}/{y}.png"},
		{"нет плейсхолдера", "https://tiles.example.test/static.png"},
		{"пустой шаблон", "https://tiles.example.test/{}"},
	} {
		h := newMapConfigEnv(t, MapTiles{URL: tc.url, Attribution: "Вуз"})

		rec := mapConfigQuery(h)
		view := decodeMapConfig(t, rec)

		if view.Tiles != nil {
			t.Errorf("%s: отказ не сработал, тайлы %+v", tc.name, view.Tiles)
		}
		if view.Problem != "MAP_TILES_URL" {
			t.Errorf("%s: отказ не называет переменную: %+v", tc.name, view)
		}

		// Значение переменной в ответ не попадает — ни адрес целиком, ни ключ
		// из него: страница показывает отказ, а не содержимое окружения.
		if strings.Contains(rec.Body.String(), tc.url) {
			t.Errorf("%s: ответ повторил значение переменной: %s", tc.name, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "secretnabor") {
			t.Errorf("%s: в ответе оказался секрет из адреса: %s", tc.name, rec.Body.String())
		}
	}
}

func TestMapConfigAcceptsOtherTemplateForms(t *testing.T) {
	// Формы, которые Leaflet понимает, но которые не похожи на классический
	// XYZ: TMS и WMS. Сервер не вправе требовать `{z}/{x}/{y}` — он обязан
	// пропустить любой шаблон с плейсхолдером.
	for _, template := range []string{
		"https://tiles.example.test/{TileMatrix}/{TileRow}/{TileCol}.png",
		"https://tiles.example.test/wms?bbox={bbox-epsg3857}&service=WMS",
		"https://tiles.example.test/{z}/{x}/{y}.png?v={s}",
	} {
		view := decodeMapConfig(t, mapConfigQuery(newMapConfigEnv(t, MapTiles{URL: template})))

		if view.Tiles == nil || view.Tiles.URL != template {
			t.Errorf("шаблон %q отвергнут: %+v", template, view)
		}
	}
}

// --- Форма ответа ------------------------------------------------------------

func TestMapConfigIsGETOnly(t *testing.T) {
	h := newMapConfigEnv(t, MapTiles{URL: "https://tiles.example.test/{z}/{x}/{y}.png"})

	rec := callAs(http.MethodPost, "/api/map/config", "{}", placeTeacher(t, h), http.HandlerFunc(h.MapConfigAPI))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST отвечен %d, ожидалось 405: %s", rec.Code, rec.Body.String())
	}
}

// --- Разбор решения на уровне самого значения --------------------------------

func TestMapTilesProblemNamesTheVariable(t *testing.T) {
	// Подсказка оператору — второй адресат того же решения: её читают в логе
	// запуска, и она обязана объяснять, что не так, не раскрывая само значение.
	for _, tc := range []struct{ name, url string }{
		{"не задан", ""},
		{"не адрес", "tiles.example.test/{z}"},
		{"без хоста", "https:///{z}"},
		{"чужая схема", "ftp://tiles.example.test/{z}"},
		{"ключ в адресе", "https://user:token@tiles.example.test/{z}"},
		{"нет плейсхолдера", "https://tiles.example.test/static.png"},
	} {
		env, hint := MapTiles{URL: tc.url}.Problem()

		if env != "MAP_TILES_URL" {
			t.Errorf("%s: решение %q, ожидалось имя переменной", tc.name, env)
		}
		if hint == "" {
			t.Errorf("%s: подсказка оператору пустая", tc.name)
		}
		if strings.Contains(hint, tc.url) && tc.url != "" {
			t.Errorf("%s: подсказка повторяет значение %q", tc.name, tc.url)
		}
	}

	if env, hint := (MapTiles{URL: "https://tiles.example.test/{z}/{x}/{y}.png"}).Problem(); env != "" || hint != "" {
		t.Errorf("годная конфигурация получила отказ %q %q", env, hint)
	}
}

func TestMapTilesOriginHidesTheRestOfTheURL(t *testing.T) {
	// Строка запуска: происхождение полезно, полный адрес — нет (в query может
	// стоять ключ провайдера).
	if got := (MapTiles{URL: "https://tiles.example.test/wmts?key=secretnabor&tile={z}"}).Origin(); got != "https://tiles.example.test" {
		t.Errorf("происхождение %q", got)
	}

	for _, bad := range []string{"", "tiles.example.test/{z}", "https://user:token@tiles.example.test/{z}"} {
		if got := (MapTiles{URL: bad}).Origin(); got != "" {
			t.Errorf("у негодной конфигурации %q произошло %q, ожидалось пустое", bad, got)
		}
	}
}
