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
	"time"

	"github.com/NumerR/SOS/internal/models"
	"github.com/NumerR/SOS/internal/store"
)

// Тесты второго слоя карты: GET /api/events/map и создание события с местом.
//
// Что доказывается:
//
//  1. Событие без действующей точки из ответа не исчезает: оно либо маркер без
//     координат, либо строка в unplaced с причиной.
//  2. Словарь состояний у обоих слоёв один — `place_state` фронтенд читает по
//     одному шаблону.
//  3. Период оба слоя понимают как один и тот же запрос: страница переключает
//     неделю один раз, а не подстраивает два фильтра.
//  4. Сбой базы не притворяется «событий нет» (ADR-0029), а неподключённая
//     зависимость отвечает 503, а не пустым списком.
//
// Ответ читается местными структурами, а не моделями и не типами обработчика:
// если декодировать ответ тем же типом, который его собрал, опечатка в
// json-теге останется невидимой.

// --- Фикстуры ----------------------------------------------------------------

type eventsMapEnv struct {
	h       *Handlers
	db      *sql.DB
	teacher *models.User
}

func newEventsMapEnv(t *testing.T) eventsMapEnv {
	t.Helper()

	h, db := newPlacesEnv(t)

	return eventsMapEnv{h: h, db: db, teacher: placeTeacher(t, h)}
}

// newPlace заводит место и возвращает его id: событию нужен именно id, а
// название двух одинаковых залов в разных корпусах не различит.
func newPlace(t *testing.T, h *Handlers, author *models.User, name, building string, coords *placeCoords) int64 {
	t.Helper()

	var lat, lon *float64
	if coords != nil {
		lat, lon = &coords.lat, &coords.lon
	}

	place, err := h.Places.Create(context.Background(), store.PlaceInput{
		Name:     name,
		Building: building,
		Lat:      lat,
		Lon:      lon,
	}, author.ID)
	if err != nil {
		t.Fatalf("завести место %q: %v", name, err)
	}

	return place.ID
}

// newEvent пишет событие прямо в хранилище: форма создания покрыта своими
// тестами ниже, а слою нужны готовые строки, в том числе недатированные —
// через POST таких не завести.
func newEvent(t *testing.T, h *Handlers, author *models.User, title, category string, when int64, placeID *int64) int64 {
	t.Helper()

	event, err := h.Events.Create(context.Background(), models.CreateEventInput{
		Title:     title,
		Category:  category,
		EventDate: when,
		PlaceID:   placeID,
	}, author.ID)
	if err != nil {
		t.Fatalf("завести событие %q: %v", title, err)
	}

	return event.ID
}

// eventAt — момент в локальной зоне сервера: период слоя считается по ней же, и
// фикстура обязана жить в той же системе координат, иначе «сегодняшний»
// инфоповод оказывался вчерашним в зависимости от того, где запущен тест.
func eventAt(year int, month time.Month, day, hour int) int64 {
	return time.Date(year, month, day, hour, 0, 0, 0, time.Local).Unix()
}

// todayAt — тот же час сегодня: проверки периода по умолчанию не должны
// зависеть от того, в каком году и месяце их запустил человек или CI.
func todayAt(offsetDays, hour int) int64 {
	return dayOffsetAt(time.Now(), offsetDays, hour)
}

func dayOffsetAt(now time.Time, offsetDays, hour int) int64 {
	day := now.AddDate(0, 0, offsetDays)

	return time.Date(day.Year(), day.Month(), day.Day(), hour, 0, 0, 0, time.Local).Unix()
}

// placeRef — место по id для CreateEventInput: указатель, потому что «места
// нет» и «место 0» — разные состояния.
func placeRef(value int64) *int64 { return &value }

// rangeQuery — период месяца целиком одной строкой.
func rangeQuery(path string) string {
	return fmt.Sprintf("%s?from=%d&to=%d", path,
		eventAt(2026, 11, 1, 0), eventAt(2026, 11, 30, 23))
}

func eventsMapQuery(h *Handlers, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	http.HandlerFunc(h.EventsMapAPI).ServeHTTP(rec, request(http.MethodGet, target, "", "10.0.0.1:1", nil))

	return rec
}

// --- Формат ответа -----------------------------------------------------------

type eventsTestMarker struct {
	PlaceID    int64  `json:"place_id"`
	Name       string `json:"name"`
	PlaceState string `json:"place_state"`
	Place      *struct {
		Name string  `json:"name"`
		Lat  float64 `json:"lat"`
		Lon  float64 `json:"lon"`
	} `json:"place"`
	EventCount int `json:"event_count"`
	Events     []struct {
		ID        int64  `json:"id"`
		Title     string `json:"title"`
		Category  string `json:"category"`
		EventDate int64  `json:"event_date"`
	} `json:"events"`
}

type eventsTestReason struct {
	Reason     string   `json:"reason"`
	EventCount int      `json:"event_count"`
	Titles     []string `json:"titles"`
}

type eventsTestPayload struct {
	Range struct {
		From int64 `json:"from"`
		To   int64 `json:"to"`
	} `json:"range"`
	Markers  []eventsTestMarker `json:"places"`
	Unplaced struct {
		EventCount int                `json:"event_count"`
		Reasons    []eventsTestReason `json:"reasons"`
	} `json:"unplaced"`
}

// markerOrder — строки «название=состояние» в порядке ответа. Название читается
// у самого маркера, а не из вложенной точки: у строки без координат точки нет, и
// без имени легенда осталась бы набором строк «1 событие не показано» непонятно
// про что.
func (p eventsTestPayload) markerOrder() string {
	names := make([]string, 0, len(p.Markers))
	for _, marker := range p.Markers {
		names = append(names, marker.Name+"="+marker.PlaceState)
	}

	return strings.Join(names, ",")
}

// shown — сколько событий встало маркерами: суммируется по маркерам, потому что
// слой обязан показывать ровно то, что лежит под точками.
func (p eventsTestPayload) shown() int {
	total := 0
	for _, marker := range p.Markers {
		total += marker.EventCount
	}

	return total
}

func (p eventsTestPayload) requireMarkers(t *testing.T, want int) {
	t.Helper()

	if len(p.Markers) != want {
		t.Fatalf("маркеров %d, ожидалось %d: %s", len(p.Markers), want, p.markerOrder())
	}
}

func decodeEventsMap(t *testing.T, rec *httptest.ResponseRecorder) eventsTestPayload {
	t.Helper()

	if rec.Code != http.StatusOK {
		t.Fatalf("слой событий отвечен %d: %s", rec.Code, rec.Body.String())
	}

	var payload eventsTestPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("разобрать ответ карты событий: %v (%s)", err, rec.Body.String())
	}

	return payload
}

func decodeEventList(t *testing.T, rec *httptest.ResponseRecorder) []models.Event {
	t.Helper()

	var events []models.Event
	if err := json.Unmarshal(rec.Body.Bytes(), &events); err != nil {
		t.Fatalf("разобрать список событий: %v (%s)", err, rec.Body.String())
	}

	return events
}

// mainEventsEnv — два места (один маркер, второй без точки) и по одному
// событию каждого вида: в точке, в месте без координат, без места, без даты.
func mainEventsEnv(t *testing.T) eventsMapEnv {
	t.Helper()

	e := newEventsMapEnv(t)

	hall := newPlace(t, e.h, e.teacher, "Актовый зал", "", nil)
	main := newPlace(t, e.h, e.teacher, "Главный корпус", "1", &placeCoords{52.123456, 104.287654})

	newEvent(t, e.h, e.teacher, "Концерт", "культура", eventAt(2026, 11, 5, 18), placeRef(hall))
	newEvent(t, e.h, e.teacher, "Олимпиада", "наука", eventAt(2026, 11, 12, 10), placeRef(main))
	newEvent(t, e.h, e.teacher, "Собрание", "", eventAt(2026, 11, 19, 9), placeRef(main))
	newEvent(t, e.h, e.teacher, "Экскурсия", "культура", eventAt(2026, 11, 20, 12), nil)
	newEvent(t, e.h, e.teacher, "Сессия", "учеба", 0, placeRef(main))

	return e
}

// --- Раскладка по местам -----------------------------------------------------

func TestEventsMapGroupsMarkersByPlace(t *testing.T) {
	e := mainEventsEnv(t)

	payload := decodeEventsMap(t, eventsMapQuery(e.h, rangeQuery("/api/events/map")))

	// Границы периода — эхо запроса, а не пересчёт: страница показывает подпись
	// «с 1 по 30 ноября» по этим числам.
	if payload.Range.From != eventAt(2026, 11, 1, 0) || payload.Range.To != eventAt(2026, 11, 30, 23) {
		t.Errorf("период %d..%d, ожидалось %d..%d",
			payload.Range.From, payload.Range.To,
			eventAt(2026, 11, 1, 0), eventAt(2026, 11, 30, 23))
	}

	// Порядок — по названию места: «Актовый» раньше «Главного» после
	// складывания регистра. SQLite так не умеет (NOCASE знает только ASCII), а
	// порядок по id был бы порядком заполнения справочника.
	payload.requireMarkers(t, 2)
	if order := payload.markerOrder(); order != "Актовый зал=unresolved,Главный корпус=resolved" {
		t.Errorf("порядок маркеров %q", order)
	}

	hall, main := payload.Markers[0], payload.Markers[1]

	// Место без координат остаётся в ответе, но точкой не становится — и не
	// теряет название: легенда обязана объяснять, какого места касается строка
	// «не показано».
	if hall.Place != nil {
		t.Errorf("у места без координат появилась точка: %+v", hall.Place)
	}
	if hall.Name != "Актовый зал" {
		t.Errorf("строка без точки потеряла название: %+v", hall)
	}
	if main.Name != "Главный корпус" || main.Place.Name != main.Name {
		t.Errorf("название маркера разошлось с названием точки: %+v", main)
	}
	if hall.EventCount != 1 || len(hall.Events) != 1 || hall.Events[0].Title != "Концерт" {
		t.Errorf("актовый зал: %+v", hall)
	}

	if main.Place == nil || main.Place.Lat != 52.123456 || main.Place.Lon != 104.287654 {
		t.Fatalf("главный корпус без точки: %+v", main)
	}
	if main.EventCount != 2 || len(main.Events) != 2 {
		t.Fatalf("событий у главного корпуса %+v, ожидалось 2", main)
	}

	// Внутри маркера — по дате, и категория доезжает до клиента: страница
	// красит маркер по ней, а не угадывает по названию.
	got := fmt.Sprintf("%s/%s,%s/%s", main.Events[0].Title, main.Events[0].Category,
		main.Events[1].Title, main.Events[1].Category)
	if got != "Олимпиада/наука,Собрание/" {
		t.Errorf("события маркера %q", got)
	}
	if main.Events[0].ID == 0 || main.Events[0].EventDate != eventAt(2026, 11, 12, 10) {
		t.Errorf("событие без id или даты: %+v", main.Events[0])
	}

	// Не показаны три: в месте без координат, без места и без даты. Число
	// обязано быть суммой причин — иначе подпись на карте врёт.
	if payload.Unplaced.EventCount != 3 {
		t.Errorf("не показано %d, ожидалось 3: %+v", payload.Unplaced.EventCount, payload.Unplaced.Reasons)
	}

	expected := []eventsTestReason{
		{Reason: "unresolved", EventCount: 1, Titles: []string{"Концерт"}},
		{Reason: "no_place", EventCount: 1, Titles: []string{"Экскурсия"}},
		{Reason: "no_date", EventCount: 1, Titles: []string{"Сессия"}},
	}
	if len(payload.Unplaced.Reasons) != len(expected) {
		t.Fatalf("причин %d, ожидалось %d: %+v",
			len(payload.Unplaced.Reasons), len(expected), payload.Unplaced.Reasons)
	}
	for i, want := range expected {
		if got := payload.Unplaced.Reasons[i]; !sameReason(got, want) {
			t.Errorf("причина %d: %+v, ожидалось %+v", i, got, want)
		}
	}
}

func sameReason(got, want eventsTestReason) bool {
	return got.Reason == want.Reason && got.EventCount == want.EventCount &&
		strings.Join(got.Titles, "|") == strings.Join(want.Titles, "|")
}

func TestEventsMapRangeDecidesWhatIsInView(t *testing.T) {
	e := mainEventsEnv(t)

	// Недельное окно внутри месяца видит одно событие главного корпуса.
	week := fmt.Sprintf("/api/events/map?from=%d&to=%d",
		eventAt(2026, 11, 11, 0), eventAt(2026, 11, 14, 0))
	payload := decodeEventsMap(t, eventsMapQuery(e.h, week))

	payload.requireMarkers(t, 1)
	if len(payload.Markers[0].Events) != 1 || payload.Markers[0].Events[0].Title != "Олимпиада" {
		t.Fatalf("за окно %q: %+v", week, payload.Markers[0])
	}
	// У события без даты нет шансов попасть в период, но из ответа оно не
	// исчезает: «не показано» обязано оставаться видимым.
	if payload.Unplaced.EventCount != 1 || payload.Unplaced.Reasons[0].Reason != "no_date" {
		t.Errorf("недатированное событие потеряно: %+v", payload.Unplaced)
	}

	// Границы включаются: полдень 12 ноября стоит ровно в окне из одной точки.
	exact := fmt.Sprintf("/api/events/map?from=%d&to=%d",
		eventAt(2026, 11, 12, 10), eventAt(2026, 11, 12, 10))
	only := decodeEventsMap(t, eventsMapQuery(e.h, exact))
	only.requireMarkers(t, 1)
	if only.Markers[0].EventCount != 1 {
		t.Errorf("границы периода не включаются: %+v", only.Markers)
	}
}

func TestEventsMapDefaultsToTheSameWeekAsLessonLayer(t *testing.T) {
	e := newScheduleMapEnv(t)
	teacher := createUser(t, e.h, "week-author", "password123", "teacher")

	main := newPlace(t, e.h, teacher, "Главный корпус", "1", &placeCoords{52.12, 104.28})
	newEvent(t, e.h, teacher, "Сегодня", "учеба", todayAt(0, 9), placeRef(main))
	newEvent(t, e.h, teacher, "Через три недели", "учеба", todayAt(21, 9), placeRef(main))

	// Ни одного параметра: оба слоя должны назвать одну и ту же неделю, иначе
	// переключатель на странице дёргал бы два разных фильтра.
	lessons := decodeMap(t, mapQuery(e.h, "/api/schedule/map"))
	events := decodeEventsMap(t, eventsMapQuery(e.h, "/api/events/map"))

	if dayKey(events.Range.From) != lessons.Range.From || dayKey(events.Range.To) != lessons.Range.To {
		t.Errorf("периоды разошлись: события %d..%d (дни %d..%d), пары %d..%d",
			events.Range.From, events.Range.To,
			dayKey(events.Range.From), dayKey(events.Range.To),
			lessons.Range.From, lessons.Range.To)
	}

	// В текущую неделю попадает одно событие из двух: «через три недели» — уже
	// другая неделя, и карта не обязана показывать её под подписью «эта».
	events.requireMarkers(t, 1)
	if events.shown() != 1 || events.Markers[0].Events[0].Title != "Сегодня" {
		t.Errorf("в текущую неделю попали не те события: %+v", events.Markers)
	}

	// Тот же период, заданный месяцем: границы — с полуночи первого дня до
	// последней секунды последнего. Месяц берётся текущий — проверка не должна
	// ломаться в другом году.
	now := time.Now()
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
	last := time.Date(now.Year(), now.Month(), daysInMonth(now.Year(), now.Month()), 23, 59, 59, 0, time.Local)

	byMonth := decodeEventsMap(t, eventsMapQuery(e.h,
		fmt.Sprintf("/api/events/map?month=%d&year=%d", int(now.Month()), now.Year())))

	if byMonth.Range.From != first.Unix() || byMonth.Range.To != last.Unix() {
		t.Errorf("месячные границы %d..%d, ожидалось %d..%d",
			byMonth.Range.From, byMonth.Range.To, first.Unix(), last.Unix())
	}
	// Месяц шире недели: событий за него видно не меньше, чем за неделю.
	if byMonth.shown() < events.shown() {
		t.Errorf("за месяц видно %d событий, за неделю %d", byMonth.shown(), events.shown())
	}
}

func TestEventsMapEmptyIsArraysNotNull(t *testing.T) {
	e := newEventsMapEnv(t)
	newPlace(t, e.h, e.teacher, "Главный корпус", "1", &placeCoords{52.12, 104.28})

	rec := eventsMapQuery(e.h, "/api/events/map?from=1&to=2")
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d: %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	for _, want := range []string{`"places":[]`, `"reasons":[]`} {
		if !strings.Contains(body, want) {
			t.Errorf("в пустом ответе нет %q: %s", want, body)
		}
	}

	if payload := decodeEventsMap(t, rec); payload.Unplaced.EventCount != 0 {
		t.Errorf("пустой период показан как %d непонятно чего", payload.Unplaced.EventCount)
	}
}

func TestEventsMapCapsReasonExamples(t *testing.T) {
	e := newEventsMapEnv(t)

	// Десять событий без места: примеры — три строки, счётчик — десять.
	for i := 0; i < 10; i++ {
		newEvent(t, e.h, e.teacher, fmt.Sprintf("Без места %d", i), "",
			eventAt(2026, 11, 2+i%20, 10), nil)
	}

	payload := decodeEventsMap(t, eventsMapQuery(e.h, rangeQuery("/api/events/map")))

	if payload.Unplaced.EventCount != 10 {
		t.Fatalf("не показано %d, ожидалось 10", payload.Unplaced.EventCount)
	}
	if len(payload.Unplaced.Reasons) != 1 || len(payload.Unplaced.Reasons[0].Titles) != 3 {
		t.Fatalf("примеры причин не ограничены тремя: %+v", payload.Unplaced.Reasons)
	}
	if payload.shown() != 0 {
		t.Errorf("у события без места появился маркер: %s", payload.markerOrder())
	}
}

// --- Отказы ------------------------------------------------------------------

func TestEventsMapRejectsBrokenRange(t *testing.T) {
	e := newEventsMapEnv(t)

	for _, tc := range []struct{ target, want string }{
		{"/api/events/map?from=abc", "from"},
		{"/api/events/map?to=abc", "to"},
		{"/api/events/map?month=13&year=2026", "month"},
		{"/api/events/map?month=11&year=1999", "year"},
	} {
		rec := eventsMapQuery(e.h, tc.target)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: код %d, ожидалось 422: %s", tc.target, rec.Code, rec.Body.String())
			continue
		}
		if message := errorMessage(t, rec); !strings.Contains(message, tc.want) {
			t.Errorf("%s: отказ %q не называет поле %q", tc.target, message, tc.want)
		}
	}
}

func TestEventsMapIsGETOnly(t *testing.T) {
	e := newEventsMapEnv(t)

	rec := callAs(http.MethodPost, "/api/events/map", "{}", e.teacher, http.HandlerFunc(e.h.EventsMapAPI))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST отвечен %d, ожидалось 405: %s", rec.Code, rec.Body.String())
	}
}

func TestEventsMapWithoutDependencies(t *testing.T) {
	// Сборка без хранилища событий: «карты нет», а не пустая карта и не 500.
	bare := New(Deps{Limits: NewLimiters()})
	rec := eventsMapQuery(bare, "/api/events/map")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("код %d, ожидалось 503: %s", rec.Code, rec.Body.String())
	}
	if body := decodeError(t, rec); !strings.Contains(body.Message, "events storage") {
		t.Errorf("отказ не называет причину: %+v", body)
	}

	// События есть, справочника нет: точки брать негде, и отдать пустую карту
	// было бы неправдой про справочник.
	e := newEventsMapEnv(t)
	e.h.Places = nil

	withoutPlaces := eventsMapQuery(e.h, "/api/events/map")
	if withoutPlaces.Code != http.StatusServiceUnavailable {
		t.Fatalf("код %d, ожидалось 503: %s", withoutPlaces.Code, withoutPlaces.Body.String())
	}
	if body := decodeError(t, withoutPlaces); !strings.Contains(body.Message, "places") {
		t.Errorf("отказ не называет причину: %+v", body)
	}
}

func TestEventsMapDatabaseFailureIsNotSilence(t *testing.T) {
	// Пустой слой при сгоревшей таблице выглядит как «событий нет» — ровно та
	// подмена, которую запрещает ADR-0029.
	eventsDown := newEventsMapEnv(t)
	breakTable(t, eventsDown.db, "events")
	expectStoreFailure(t, "карта событий при снятой таблице событий",
		eventsMapQuery(eventsDown.h, "/api/events/map?from=1&to=2"))

	placesDown := newEventsMapEnv(t)
	breakTable(t, placesDown.db, "places")
	expectStoreFailure(t, "карта событий при снятом справочнике",
		eventsMapQuery(placesDown.h, "/api/events/map?from=1&to=2"))
}

// --- Создание события с местом -----------------------------------------------

func TestCreateEventStoresPlaceAndCategory(t *testing.T) {
	e := newEventsMapEnv(t)
	main := newPlace(t, e.h, e.teacher, "Главный корпус", "1", &placeCoords{52.12, 104.28})

	rec := callAs(http.MethodPost, "/api/events",
		fmt.Sprintf(`{"title":"Конференция","description":"","event_date":"","category":"наука","place_id":%d}`, main),
		e.teacher, http.HandlerFunc(e.h.EventsAPI))

	if rec.Code != http.StatusCreated {
		t.Fatalf("создание отвечено %d: %s", rec.Code, rec.Body.String())
	}

	var created struct {
		ID       int64  `json:"id"`
		PlaceID  *int64 `json:"place_id"`
		Category string `json:"category"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("разобрать созданное событие: %v (%s)", err, rec.Body.String())
	}
	if created.ID == 0 {
		t.Errorf("созданное событие без id: %s", rec.Body.String())
	}
	if created.PlaceID == nil || *created.PlaceID != main || created.Category != "наука" {
		t.Errorf("место или категория не сохранены: %+v", created)
	}

	// Событие без даты, но с местом: на карте оно живёт в причине no_date, а
	// не под маркером — встать ему негде.
	payload := decodeEventsMap(t, eventsMapQuery(e.h, rangeQuery("/api/events/map")))
	if payload.shown() != 0 {
		t.Errorf("недатированное событие встало маркером: %s", payload.markerOrder())
	}
	if payload.Unplaced.EventCount != 1 || payload.Unplaced.Reasons[0].Reason != "no_date" {
		t.Errorf("недатированное событие потеряно из ответа: %+v", payload.Unplaced)
	}

	// Явный null читается как «места нет», а не как «id 0».
	nullPlace := callAs(http.MethodPost, "/api/events",
		`{"title":"Без места","event_date":"","place_id":null}`,
		e.teacher, http.HandlerFunc(e.h.EventsAPI))
	if nullPlace.Code != http.StatusCreated {
		t.Fatalf("place_id:null отвечен %d: %s", nullPlace.Code, nullPlace.Body.String())
	}

	again := decodeEventsMap(t, eventsMapQuery(e.h, rangeQuery("/api/events/map")))
	if again.Unplaced.EventCount != 2 {
		t.Errorf("второго события без места не видно: %+v", again.Unplaced)
	}
}

func TestCreateEventRejectsBadPlaceReference(t *testing.T) {
	e := newEventsMapEnv(t)
	main := newPlace(t, e.h, e.teacher, "Главный корпус", "1", &placeCoords{52.12, 104.28})

	cases := []struct{ name, body, field, message string }{
		{"места нет в справочнике", `{"title":"Конференция","place_id":999}`,
			"place_id", "no such place"},
		{"нулевой id", `{"title":"Конференция","place_id":0}`,
			"place_id", "place_id must name a place"},
		{"отрицательный id", `{"title":"Конференция","place_id":-3}`,
			"place_id", "place_id must name a place"},
		{"категория длиннее границы",
			fmt.Sprintf(`{"title":"Конференция","place_id":%d,"category":%q}`, main, strings.Repeat("н", 65)),
			"category", "category is too long"},
	}

	for _, tc := range cases {
		rec := callAs(http.MethodPost, "/api/events", tc.body, e.teacher, http.HandlerFunc(e.h.EventsAPI))
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: отвечено %d, ожидалось 422: %s", tc.name, rec.Code, rec.Body.String())
			continue
		}
		if got := fieldMessage(t, rec, tc.field); got != tc.message {
			t.Errorf("%s: поле %q отвечает %q, ожидалось %q", tc.name, tc.field, got, tc.message)
		}
	}

	// Ни один отказ не должен был оставить строку.
	list := callAs(http.MethodGet, "/api/events", "", e.teacher, http.HandlerFunc(e.h.EventsAPI))
	if events := decodeEventList(t, list); len(events) != 0 {
		t.Errorf("после одних отказов в базе %d событий", len(events))
	}
}

func TestCreateEventWithPlaceNeedsDirectory(t *testing.T) {
	// Место просят, справочника нет: 503 с причиной, а не «не найдено». События
	// при этом на месте — отказ касается только проверки ссылки.
	e := newEventsMapEnv(t)
	e.h.Places = nil

	rec := callAs(http.MethodPost, "/api/events", `{"title":"Конференция","place_id":7}`,
		e.teacher, http.HandlerFunc(e.h.EventsAPI))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("код %d, ожидалось 503: %s", rec.Code, rec.Body.String())
	}
	if body := decodeError(t, rec); !strings.Contains(body.Message, "places") {
		t.Errorf("отказ не называет причину: %+v", body)
	}

	// Событие без места справочника не требует: это тот же запрос, что был до
	// появления карты, — и отказывать из-за него он не обязан.
	noPlace := callAs(http.MethodPost, "/api/events", `{"title":"Конференция"}`,
		e.teacher, http.HandlerFunc(e.h.EventsAPI))
	if noPlace.Code != http.StatusCreated {
		t.Errorf("событие без места отвечено %d, ожидалось 201: %s",
			noPlace.Code, noPlace.Body.String())
	}
}
