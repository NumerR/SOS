package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/NumerR/SOS/internal/database"
	"github.com/NumerR/SOS/internal/models"
	"github.com/NumerR/SOS/internal/store"
)

// Тесты справочника мест со стороны обработчиков.
//
// Проверяется граница, а не SQL: что приходит в форму, какой статус и какое
// поле называет отказ, и что в журнале остаётся достаточно для ответа на
// «кто поставил сюда корпус». Само хранилище покрыто internal/store/
// places_test.go.
//
// Смысл файла держат три вещи:
//
//  1. Null в JSON обязан остаться «координат нет», а не превратиться в 0 —
//     иначе карта рисует корпус в Гвинейском заливе.
//  2. Отказ обязан называть поле: 422 без fields человек не понимает, что
//     исправить.
//  3. Сбой базы не притворяется пустым списком или 404 (ADR-0029).

func newPlacesEnv(t *testing.T) (*Handlers, *sql.DB) {
	t.Helper()

	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("открыть базу: %v", err)
	}
	if err := database.Migrate(db); err != nil {
		t.Fatalf("миграции: %v", err)
	}
	// Схема карты: справочник мест и правка событий под второй слой.
	applyMapSchema(t, db)
	if _, err := db.Exec(store.AuditEventsMigration); err != nil {
		t.Fatalf("таблица аудита: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	return New(Deps{
		Users:    store.NewUserStore(db),
		Sessions: store.NewSessionStore(db),
		Events:   store.NewEventStore(db),
		Places:   store.NewPlaceStore(db),
		Audit:    store.NewAuditStore(db),
		Limits:   NewLimiters(),
	}), db
}

func placeTeacher(t *testing.T, h *Handlers) *models.User {
	t.Helper()

	return createUser(t, h, "place-teacher", "password123", "teacher")
}

// decodePlaces — ответ списка мест как срез, а не как строка: проверка, что
// пустой справочник уезжает в JSON массивом, а не null'ом, иначе фронтенд
// падает на первой пустой базе.
func decodePlaces(t *testing.T, rec *httptest.ResponseRecorder) []store.Place {
	t.Helper()

	if rec.Body.String() == "null\n" || rec.Body.String() == "null" {
		t.Fatalf("список мест закодирован как null — клиент обязан получать []")
	}

	var places []store.Place
	if err := json.Unmarshal(rec.Body.Bytes(), &places); err != nil {
		t.Fatalf("разобрать список мест: %v (%s)", err, rec.Body.String())
	}

	return places
}

func fieldMessage(t *testing.T, rec *httptest.ResponseRecorder, field string) string {
	t.Helper()

	body := decodeError(t, rec)
	message, ok := body.Fields[field]
	if !ok {
		t.Fatalf("в отказе %d нет поля %q: %+v", rec.Code, field, body)
	}

	return message
}

func TestPlacesCreateWritesAudit(t *testing.T) {
	h, _ := newPlacesEnv(t)
	teacher := placeTeacher(t, h)

	rec := callAs(http.MethodPost, "/api/places",
		`{"name":"Главный корпус","building":"1","address":"пл. Ленина 3","lat":52.123456,"lon":104.287654}`,
		teacher, http.HandlerFunc(h.PlacesAPI))

	if rec.Code != http.StatusCreated {
		t.Fatalf("создание места отвечено %d: %s", rec.Code, rec.Body.String())
	}

	var place store.Place
	if err := json.Unmarshal(rec.Body.Bytes(), &place); err != nil {
		t.Fatalf("разобрать место: %v (%s)", err, rec.Body.String())
	}
	if place.ID == 0 || place.GeoState != store.GeoResolved {
		t.Errorf("место создано неверно: %+v", place)
	}
	if place.CreatedBy == nil || *place.CreatedBy != teacher.ID {
		t.Errorf("created_by = %v, ожидалось %d", place.CreatedBy, teacher.ID)
	}

	event := lastAudit(t, h, store.AuditPlaceCreate)
	if event.TargetID != strconv.FormatInt(place.ID, 10) {
		t.Errorf("журнал указывает на target_id=%q, ожидалось %d", event.TargetID, place.ID)
	}
	if !strings.Contains(event.Detail, "Главный корпус") || !strings.Contains(event.Detail, "52.123456,104.287654") {
		t.Errorf("в деталях журнала нет ни названия, ни точки: %q", event.Detail)
	}
	if event.OK != true {
		t.Errorf("событие помечено неудачным: %+v", event)
	}
}

// Ключевая проверка всего файла: пропущенные координаты не имеют права
// прочитаться как нулевые.
func TestPlacesCreateWithoutCoordinates(t *testing.T) {
	h, _ := newPlacesEnv(t)
	teacher := placeTeacher(t, h)

	rec := callAs(http.MethodPost, "/api/places",
		`{"name":"Площадка перед входом","lat":null,"lon":null}`,
		teacher, http.HandlerFunc(h.PlacesAPI))

	if rec.Code != http.StatusCreated {
		t.Fatalf("место без координат отвергнуто: %d %s", rec.Code, rec.Body.String())
	}

	var place store.Place
	if err := json.Unmarshal(rec.Body.Bytes(), &place); err != nil {
		t.Fatalf("разобрать место: %v", err)
	}
	if place.Lat != nil || place.Lon != nil {
		t.Errorf("координаты появились из null: lat=%v lon=%v", place.Lat, place.Lon)
	}
	if place.GeoState != store.GeoUnresolved {
		t.Errorf("geo_state = %q, ожидалось %q", place.GeoState, store.GeoUnresolved)
	}

	// Настоящий ноль — это координата, и она обязана сохраниться: проверка
	// выше не имеет смысла, если стор считает нуль отсутствием.
	zeroRec := callAs(http.MethodPost, "/api/places",
		`{"name":"Нулевой меридиан","lat":0,"lon":0}`,
		teacher, http.HandlerFunc(h.PlacesAPI))
	if zeroRec.Code != http.StatusCreated {
		t.Fatalf("нулевая пара отвергнута: %d %s", zeroRec.Code, zeroRec.Body.String())
	}

	var zero store.Place
	if err := json.Unmarshal(zeroRec.Body.Bytes(), &zero); err != nil {
		t.Fatalf("разобрать место с нулевой парой: %v", err)
	}
	if zero.Lat == nil || *zero.Lat != 0 || zero.GeoState != store.GeoResolved {
		t.Errorf("нулевая пара прочитана как %v/%q, ожидалось 0/resolved", zero.Lat, zero.GeoState)
	}
}

func TestPlacesCreateFieldErrors(t *testing.T) {
	h, _ := newPlacesEnv(t)
	teacher := placeTeacher(t, h)

	cases := []struct {
		name   string
		body   string
		field  string
		expect string
	}{
		{"без названия", `{"building":"1"}`, "name", "name is required"},
		{"название из пробелов", `{"name":"   "}`, "name", "name is required"},
		{"длинное название", `{"name":"` + strings.Repeat("а", 121) + `"}`, "name", "name is too long"},
		{"длинный корпус", `{"name":"Корпус","building":"` + strings.Repeat("9", 17) + `"}`, "building", "building is too long"},
		{"длинный адрес", `{"name":"Корпус","address":"` + strings.Repeat("у", 201) + `"}`, "address", "address is too long"},
		{"широта без долготы", `{"name":"Корпус","lat":52}`, "lat", "lat and lon must be sent together"},
		{"долгота без широты", `{"name":"Корпус","lon":104}`, "lat", "lat and lon must be sent together"},
		{"широта вне диапазона", `{"name":"Корпус","lat":91,"lon":104}`, "lat", "lat must be between -90 and 90"},
		{"долгота вне диапазона", `{"name":"Корпус","lat":52,"lon":181}`, "lon", "lon must be between -180 and 180"},
	}

	for _, tc := range cases {
		rec := callAs(http.MethodPost, "/api/places", tc.body, teacher, http.HandlerFunc(h.PlacesAPI))
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: отвечено %d, ожидалось 422: %s", tc.name, rec.Code, rec.Body.String())
			continue
		}
		if got := fieldMessage(t, rec, tc.field); got != tc.expect {
			t.Errorf("%s: поле %q отвечает %q, ожидалось %q", tc.name, tc.field, got, tc.expect)
		}
	}

	// Ни один отказ не должен был ничего записать.
	list := callAs(http.MethodGet, "/api/places", "", teacher, http.HandlerFunc(h.PlacesAPI))
	if places := decodePlaces(t, list); len(places) != 0 {
		t.Errorf("после одних отказов в справочнике %d мест: %+v", len(places), places)
	}
}

func TestPlacesBuildingConflictNamesBuilding(t *testing.T) {
	h, _ := newPlacesEnv(t)
	teacher := placeTeacher(t, h)

	first := callAs(http.MethodPost, "/api/places", `{"name":"Корпус 2","building":"2"}`,
		teacher, http.HandlerFunc(h.PlacesAPI))
	if first.Code != http.StatusCreated {
		t.Fatalf("первое место не создано: %d %s", first.Code, first.Body.String())
	}

	second := callAs(http.MethodPost, "/api/places", `{"name":"Корпус 2 дубль","building":"2"}`,
		teacher, http.HandlerFunc(h.PlacesAPI))
	if second.Code != http.StatusUnprocessableEntity {
		t.Fatalf("конфликт корпуса отвечен %d, ожидалось 422: %s", second.Code, second.Body.String())
	}
	if got := fieldMessage(t, second, "building"); got != "this building already has a place" {
		t.Errorf("конфликт корпуса отвечает %q, ожидалось сообщение про building", got)
	}
}

func TestPlacesListAndResolvedFilter(t *testing.T) {
	h, _ := newPlacesEnv(t)
	teacher := placeTeacher(t, h)

	callAs(http.MethodPost, "/api/places", `{"name":"Корпус 1","building":"1","lat":52,"lon":104}`,
		teacher, http.HandlerFunc(h.PlacesAPI))
	callAs(http.MethodPost, "/api/places", `{"name":"Площадка","building":""}`,
		teacher, http.HandlerFunc(h.PlacesAPI))

	all := callAs(http.MethodGet, "/api/places", "", teacher, http.HandlerFunc(h.PlacesAPI))
	if all.Code != http.StatusOK {
		t.Fatalf("список отвечен %d: %s", all.Code, all.Body.String())
	}
	if places := decodePlaces(t, all); len(places) != 2 {
		t.Errorf("мест %d, ожидалось 2", len(places))
	}

	resolved := callAs(http.MethodGet, "/api/places?resolved=1", "", teacher, http.HandlerFunc(h.PlacesAPI))
	places := decodePlaces(t, resolved)
	if len(places) != 1 || places[0].Name != "Корпус 1" {
		t.Errorf("выборка resolved вернула %+v, ожидалось один корпус с координатами", places)
	}

	// Пробел в цели запроса закодирован: httptest.NewRequest на голом пробеле
	// паникует, а проверке нужен именно неразбираемый value.
	for _, value := range []string{"maybe", "3", "yes%20please"} {
		rec := callAs(http.MethodGet, "/api/places?resolved="+value, "", teacher, http.HandlerFunc(h.PlacesAPI))
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("resolved=%q отвечен %d, ожидалось 422", value, rec.Code)
			continue
		}
		fieldMessage(t, rec, "resolved")
	}
}

func TestPlacesUpdateWritesTransition(t *testing.T) {
	h, _ := newPlacesEnv(t)
	teacher := placeTeacher(t, h)

	created := callAs(http.MethodPost, "/api/places", `{"name":"Корпус 3","building":"3","lat":52,"lon":104}`,
		teacher, http.HandlerFunc(h.PlacesAPI))
	var place store.Place
	if err := json.Unmarshal(created.Body.Bytes(), &place); err != nil {
		t.Fatalf("разобрать созданное место: %v (%s)", err, created.Body.String())
	}

	rec := callAs(http.MethodPost, "/api/places/update",
		`{"id":`+strconv.FormatInt(place.ID, 10)+`,"name":"Корпус 3 (левое крыло)","building":"3","lat":52.01,"lon":104.02}`,
		teacher, http.HandlerFunc(h.PlaceUpdate))

	if rec.Code != http.StatusOK {
		t.Fatalf("правка отвечена %d: %s", rec.Code, rec.Body.String())
	}

	var updated store.Place
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("разобрать исправленное место: %v", err)
	}
	if updated.Name != "Корпус 3 (левое крыло)" || *updated.Lat != 52.01 {
		t.Errorf("правка не применилась: %+v", updated)
	}

	event := lastAudit(t, h, store.AuditPlaceUpdate)
	if !strings.Contains(event.Detail, "from=") || !strings.Contains(event.Detail, "to=") {
		t.Errorf("журнал не показывает переход было/стало: %q", event.Detail)
	}
	// Старое значение обязано остаться в детали — иначе «точку сдвинули»
	// неотличимо от «точку поставили впервые».
	if !strings.Contains(event.Detail, "52.000000,104.000000") {
		t.Errorf("в журнале нет прежней точки: %q", event.Detail)
	}
}

func TestPlacesUpdateRejectsBadID(t *testing.T) {
	h, _ := newPlacesEnv(t)
	teacher := placeTeacher(t, h)

	for _, body := range []string{
		`{"name":"Корпус"}`,
		`{"id":0,"name":"Корпус"}`,
		`{"id":-3,"name":"Корпус"}`,
	} {
		rec := callAs(http.MethodPost, "/api/places/update", body, teacher, http.HandlerFunc(h.PlaceUpdate))
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("тело %s отвечено %d, ожидалось 422: %s", body, rec.Code, rec.Body.String())
			continue
		}
		fieldMessage(t, rec, "id")
	}
}

func TestPlacesUpdateMissingPlaceIs404(t *testing.T) {
	h, _ := newPlacesEnv(t)
	teacher := placeTeacher(t, h)

	rec := callAs(http.MethodPost, "/api/places/update", `{"id":4242,"name":"Нет такого"}`,
		teacher, http.HandlerFunc(h.PlaceUpdate))

	expectNotFound(t, "правка несуществующего места", rec)
}

func TestPlacesUpdateOnlyPost(t *testing.T) {
	h, _ := newPlacesEnv(t)
	teacher := placeTeacher(t, h)

	rec := callAs(http.MethodGet, "/api/places/update", "", teacher, http.HandlerFunc(h.PlaceUpdate))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET на путь правки отвечен %d, ожидалось 405: %s", rec.Code, rec.Body.String())
	}
}

func TestPlacesRequireUser(t *testing.T) {
	h, _ := newPlacesEnv(t)

	// callAs кладёт пользователя в контекст; здесь его нет — так проверяется,
	// что обработчик не выдаст 201 «в никуда» без автора.
	req := request(http.MethodPost, "/api/places", `{"name":"Корпус"}`, "10.0.7.7:1", nil)
	rec := httptest.NewRecorder()
	http.HandlerFunc(h.PlacesAPI).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("создание без пользователя отвечено %d, ожидалось 401: %s", rec.Code, rec.Body.String())
	}
}

func TestPlacesUnknownMethod(t *testing.T) {
	h, _ := newPlacesEnv(t)
	teacher := placeTeacher(t, h)

	rec := callAs(http.MethodDelete, "/api/places", "", teacher, http.HandlerFunc(h.PlacesAPI))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE отвечен %d, ожидалось 405: %s", rec.Code, rec.Body.String())
	}
}

// Сбой базы: список обязан ответить 500, а не пустой массив, и правка — 500,
// а не 404 «места нет» (ADR-0029).
func TestPlacesDatabaseFailureIsNotSilence(t *testing.T) {
	h, db := newPlacesEnv(t)
	teacher := placeTeacher(t, h)

	breakTable(t, db, "places")

	list := callAs(http.MethodGet, "/api/places", "", teacher, http.HandlerFunc(h.PlacesAPI))
	expectStoreFailure(t, "список мест при снятой таблице", list)

	missing := callAs(http.MethodPost, "/api/places/update", `{"id":1,"name":"Корпус"}`,
		teacher, http.HandlerFunc(h.PlaceUpdate))
	expectStoreFailure(t, "правка при снятой таблице", missing)

	created := callAs(http.MethodPost, "/api/places", `{"name":"Корпус"}`,
		teacher, http.HandlerFunc(h.PlacesAPI))
	expectStoreFailure(t, "создание при снятой таблице", created)
}

// Deps без справочника — конфигурация сервера, а не сбой запроса: ответ обязан
// быть 500 с причиной в логе, а не 200 с пустым списком (ADR-0030).
func TestPlacesWithoutStore(t *testing.T) {
	h := New(Deps{Limits: NewLimiters()})
	teacher := &models.User{ID: 7, Role: "teacher"}

	rec := callAs(http.MethodGet, "/api/places", "", teacher, http.HandlerFunc(h.PlacesAPI))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("отвечено %d, ожидалось 500: %s", rec.Code, rec.Body.String())
	}

	update := callAs(http.MethodPost, "/api/places/update", `{"id":1,"name":"Корпус"}`,
		teacher, http.HandlerFunc(h.PlaceUpdate))
	if update.Code != http.StatusInternalServerError {
		t.Errorf("правка без справочника отвечена %d, ожидалось 500", update.Code)
	}
}

// Битый JSON отклоняет readJSON (400 invalid_json) — проверено общим
// тестом helper'а; здесь важно, что до хранилища такое тело не доходит.
func TestPlacesBrokenJSON(t *testing.T) {
	h, _ := newPlacesEnv(t)
	teacher := placeTeacher(t, h)

	rec := callAs(http.MethodPost, "/api/places", `{"name":`, teacher, http.HandlerFunc(h.PlacesAPI))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("битое тело отвечено %d, ожидалось 400: %s", rec.Code, rec.Body.String())
	}

	list := callAs(http.MethodGet, "/api/places", "", teacher, http.HandlerFunc(h.PlacesAPI))
	if places := decodePlaces(t, list); len(places) != 0 {
		t.Errorf("после битого тела в справочнике %d мест", len(places))
	}
}
