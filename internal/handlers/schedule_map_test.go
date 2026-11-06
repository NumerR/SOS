package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NumerR/SOS/internal/apeks"
	"github.com/NumerR/SOS/internal/store"
)

// Тесты слоя карты «пары по корпусам» (GET /api/schedule/map).
//
// Слой существует ради одного вопроса: сколько пар идёт в каждом корпусе за
// период и какие из этих пар нельзя показать. Проверяются четыре вещи, которые
// не видно из тестов хранилища:
//
//  1. Корпус без координат остаётся в ответе — с числом пар и состоянием
//     точки: карта с угаданной точкой выглядит наполненной, но врёт.
//  2. Пара без корпуса или в двух корпусах не «выбирает» один: она попадает в
//     unplaced с причиной и примером строки аудитории.
//  3. Пустой ответ — это массивы [], а не null: страница падает на null и
//     «нет занятий» становится неотличимым от «сервер ответил не то».
//  4. Сбой базы отвечает 500, а не пустой сводкой (ADR-0029).
//
// Ключи JSON проверяются разбором в структуры этого файла, а не в
// scheduleMapResponse: если читать ответ тем же типом, который его собрал,
// опечатка в теге останется невидимой — ключ и проверка возьмутся из одного
// места.

// newScheduleMapEnv — контур источника плюс оба слоя карты: справочник мест,
// правка событий и их хранилище. Отдельная база не заводится: карте нужны ровно
// те же таблицы, что странице расписания, а второй набор сидов означал бы два
// разных ответа на вопрос «какая сегодня неделя» — такое расхождение всплывает
// только у живого человека, когда слои уже на одном экране.
func newScheduleMapEnv(t *testing.T) *scheduleSourceEnv {
	t.Helper()

	e := newScheduleSourceEnv(t)
	applyMapSchema(t, e.db)
	e.h.Places = store.NewPlaceStore(e.db)
	e.h.Events = store.NewEventStore(e.db)

	return e
}

// placeCoords — пара координат: справочник принимает широту и долготу только
// вместе, а nil означает «место заведено, точки нет» — то состояние, ради
// которого слой и существует.
type placeCoords struct{ lat, lon float64 }

// addPlace заводит место от имени преподавателя: created_by — внешний ключ на
// users, а foreign keys в этом проекте включены (database.Open), так что
// заведомо несуществующий id был бы отказом базы, а не проверкой слоя.
func (e *scheduleSourceEnv) addPlace(t *testing.T, name, building string, coords *placeCoords) {
	t.Helper()

	author := createUser(t, e.h, "map-"+strconv.FormatInt(e.userSeq.Add(1), 10), "password123", "teacher")

	var lat, lon *float64
	if coords != nil {
		lat, lon = &coords.lat, &coords.lon
	}

	if _, err := e.h.Places.Create(context.Background(), store.PlaceInput{
		Name:     name,
		Building: building,
		Lat:      lat,
		Lon:      lon,
	}, author.ID); err != nil {
		t.Fatalf("завести место %q: %v", name, err)
	}
}

// mapLesson — пара с аудиторией. Остальные поля слоя не касаются, но day_key и
// date обязаны сойтись: дни в сводке читаются отсюда.
func mapLesson(externalID string, dayKey, number int, room string) store.SourceLesson {
	return store.SourceLesson{
		ExternalID:   externalID,
		DayKey:       dayKey,
		Date:         fmt.Sprintf("%02d.%02d.%04d", dayKey%100, dayKey/100%100, dayKey/10000),
		LessonNumber: number,
		Discipline:   "Дисциплина " + externalID,
		Room:         room,
	}
}

// --- Разбор ответа -----------------------------------------------------------

type mapTestPlace struct {
	Name string  `json:"name"`
	Lat  float64 `json:"lat"`
	Lon  float64 `json:"lon"`
}

type mapTestDay struct {
	DayKey      int    `json:"day_key"`
	Date        string `json:"date"`
	LessonCount int    `json:"lesson_count"`
}

type mapTestLesson struct {
	ExternalID   string `json:"external_id"`
	DayKey       int    `json:"day_key"`
	LessonNumber int    `json:"lesson_number"`
	Discipline   string `json:"discipline"`
	Room         string `json:"room"`
}

type mapTestBuilding struct {
	Building    string          `json:"building"`
	PlaceState  string          `json:"place_state"`
	Place       *mapTestPlace   `json:"place"`
	LessonCount int             `json:"lesson_count"`
	Days        []mapTestDay    `json:"days"`
	Lessons     []mapTestLesson `json:"lessons"`
}

type mapTestReason struct {
	Reason      string   `json:"reason"`
	LessonCount int      `json:"lesson_count"`
	Rooms       []string `json:"rooms"`
}

type mapTestPayload struct {
	Source     string `json:"source"`
	SourceName string `json:"source_name"`
	Range      struct {
		From int `json:"from"`
		To   int `json:"to"`
	} `json:"range"`
	Group *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"group"`
	Buildings []mapTestBuilding `json:"buildings"`
	Unplaced  struct {
		LessonCount int             `json:"lesson_count"`
		Reasons     []mapTestReason `json:"reasons"`
	} `json:"unplaced"`
	UpdatedAt int64 `json:"updated_at"`
}

func mapQuery(h *Handlers, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	http.HandlerFunc(h.ScheduleMapAPI).ServeHTTP(rec, request(http.MethodGet, target, "", "10.0.0.1:1", nil))
	return rec
}

func decodeMap(t *testing.T, rec *httptest.ResponseRecorder) mapTestPayload {
	t.Helper()

	if rec.Code != http.StatusOK {
		t.Fatalf("код %d, ожидалось 200: %s", rec.Code, rec.Body.String())
	}

	var payload mapTestPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("разобрать ответ карты: %v (%s)", err, rec.Body.String())
	}

	return payload
}

// buildingOf — строка сводки по номеру корпуса: порядок в списке проверяется
// отдельно, а дальше тестам важнее содержимое конкретной строки.
func (p mapTestPayload) buildingOf(t *testing.T, building string) mapTestBuilding {
	t.Helper()

	for _, item := range p.Buildings {
		if item.Building == building {
			return item
		}
	}

	t.Fatalf("в сводке нет корпуса %q: %v", building, p.Buildings)
	return mapTestBuilding{}
}

func mapBuildingsOrder(items []mapTestBuilding) []string {
	order := make([]string, 0, len(items))
	for _, item := range items {
		order = append(order, item.Building)
	}
	return order
}

// --- Сводка по корпусам ------------------------------------------------------

// mainMapEnv — месяц, в котором есть корпус в каждом из состояний точки.
func mainMapEnv(t *testing.T) *scheduleSourceEnv {
	t.Helper()

	e := newScheduleMapEnv(t)
	e.seedCatalog(t, store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"})

	e.addPlace(t, "Главный корпус", "1", &placeCoords{52.123456, 104.287654})
	e.addPlace(t, "Корпус 2", "2", nil)
	e.addPlace(t, "Корпус 10", "10", &placeCoords{52.13, 104.29})
	// Площадка без корпуса: координаты есть, сопоставить ей пары не по чему.
	e.addPlace(t, "Актовый зал", "", &placeCoords{52.14, 104.30})

	e.seedMonth(t, "494", 11, 2026,
		mapLesson("m1", 20261102, 1, "к. 1/306"),
		mapLesson("m2", 20261102, 2, "к. 2/102"),
		mapLesson("m3", 20261102, 3, "к. 3/111"),
		mapLesson("m4", 20261102, 4, "без ауд."),
		mapLesson("m5", 20261102, 5, "к. 1/311, к. 3/213"),
		// Две аудитории одного корпуса — это одна точка, а не «пара в двух
		// корпусах»: формат источника знает оба случая.
		mapLesson("m6", 20261103, 1, "к. 10/12"),
		mapLesson("m7", 20261103, 2, "к. 1/710, к. 1/711"),
	)

	return e
}

func TestScheduleMapAggregatesLessonsByBuilding(t *testing.T) {
	e := mainMapEnv(t)

	payload := decodeMap(t, mapQuery(e.h, "/api/schedule/map?month=11&year=2026"))

	if payload.Source != apeks.Source {
		t.Errorf("source = %q", payload.Source)
	}
	if payload.Range.From != 20261101 || payload.Range.To != 20261130 {
		t.Errorf("range = %+v, ожидался весь ноябрь", payload.Range)
	}
	if payload.Group != nil {
		t.Errorf("без ?group поле группы обязано остаться null: %+v", payload.Group)
	}
	if payload.UpdatedAt == 0 {
		t.Error("updated_at = 0 — подписать «обновлено» нечем")
	}

	// Порядок — числом: «10» обязано идти после «2». Лексикографически оно
	// встало бы между «1» и «2», и человек, читающий список корпусов глазами,
	// спотыкался бы об такой порядок каждую страницу.
	if order := mapBuildingsOrder(payload.Buildings); strings.Join(order, ",") != "1,2,3,10" {
		t.Fatalf("порядок корпусов %v, ожидалось [1 2 3 10]", order)
	}

	first := payload.buildingOf(t, "1")
	if first.PlaceState != "resolved" || first.LessonCount != 2 {
		t.Errorf("корпус 1: состояние %q, пар %d", first.PlaceState, first.LessonCount)
	}
	if first.Place == nil || first.Place.Name != "Главный корпус" ||
		first.Place.Lat != 52.123456 || first.Place.Lon != 104.287654 {
		t.Errorf("корпус 1: точка %v — координаты должны прийти из справочника", first.Place)
	}
	if len(first.Days) != 2 || first.Days[0].DayKey != 20261102 || first.Days[0].LessonCount != 1 ||
		first.Days[0].Date != "02.11.2026" || first.Days[1].DayKey != 20261103 {
		t.Errorf("корпус 1 по дням: %+v", first.Days)
	}
	if len(first.Lessons) != 0 {
		t.Errorf("без ?building список пар в сводке не нужен: %+v", first.Lessons)
	}

	// Корпуса без точки остаются строками сводки: у «2» есть карточка в
	// справочнике, но координат нет, и выдавать его за «корпуса не существует»
	// нельзя — страница прочитала бы это как «в корпусе 2 занятий нет».
	second := payload.buildingOf(t, "2")
	if second.PlaceState != "unresolved" || second.LessonCount != 1 || second.Place != nil {
		t.Errorf("корпус 2: %+v", second)
	}

	third := payload.buildingOf(t, "3")
	if third.PlaceState != "missing" || third.LessonCount != 1 || third.Place != nil {
		t.Errorf("корпус 3: %+v — его нет в справочнике, точки быть не может", third)
	}

	tenth := payload.buildingOf(t, "10")
	if tenth.PlaceState != "resolved" || tenth.Place == nil || tenth.Place.Name != "Корпус 10" {
		t.Errorf("корпус 10: %+v", tenth)
	}

	// Ни один корпус не получает чужую точку: «Актовый зал» есть в справочнике
	// с координатами, но корпуса не называет, и подставить его корпусу 3
	// значило бы выдумать координату (бриф, п.3).
	for _, item := range payload.Buildings {
		if item.Place != nil && item.Place.Name == "Актовый зал" {
			t.Errorf("корпус %s получил точку площадки без корпуса", item.Building)
		}
		if item.Place != nil && item.PlaceState != "resolved" {
			t.Errorf("корпус %s: состояние %q, но координаты пришли", item.Building, item.PlaceState)
		}
		if item.PlaceState == "resolved" && item.Place == nil {
			t.Errorf("корпус %s объявлен resolved без координат", item.Building)
		}
	}

	// m2 (unresolved) + m3 (missing) + m4 (без корпуса) + m5 (два корпуса).
	if payload.Unplaced.LessonCount != 4 {
		t.Fatalf("не показано %d пар, ожидалось 4: %+v", payload.Unplaced.LessonCount, payload.Unplaced.Reasons)
	}

	wantReasons := []struct {
		reason string
		count  int
		room   string
	}{
		{"missing", 1, "к. 3/111"},
		{"unresolved", 1, "к. 2/102"},
		{"no_building", 1, "без ауд."},
		{"several", 1, "к. 1/311, к. 3/213"},
	}
	if len(payload.Unplaced.Reasons) != len(wantReasons) {
		t.Fatalf("причин %d: %+v", len(payload.Unplaced.Reasons), payload.Unplaced.Reasons)
	}
	for i, want := range wantReasons {
		got := payload.Unplaced.Reasons[i]
		if got.Reason != want.reason || got.LessonCount != want.count {
			t.Errorf("причина %d: %+v, ожидалось %s x%d", i, got, want.reason, want.count)
		}
		if len(got.Rooms) == 0 || got.Rooms[0] != want.room {
			t.Errorf("причина %s: примеры аудиторий %+v", want.reason, got.Rooms)
		}
	}

	// То, что m5 не раздалась по двум корпусам, видно по числам выше: корпус 1
	// взял две пары, корпус 3 — одну.
}

func TestScheduleMapBuildingFilterReturnsLessons(t *testing.T) {
	e := mainMapEnv(t)
	const period = "/api/schedule/map?month=11&year=2026"

	resolved := decodeMap(t, mapQuery(e.h, period+"&building=1"))
	if len(resolved.Buildings) != 1 || resolved.Buildings[0].Building != "1" {
		t.Fatalf("фильтр по корпусу 1 вернул %v", mapBuildingsOrder(resolved.Buildings))
	}

	lessons := resolved.Buildings[0].Lessons
	if len(lessons) != 2 || lessons[0].ExternalID != "m1" || lessons[1].ExternalID != "m7" {
		t.Fatalf("пары корпуса 1: %+v", lessons)
	}
	if lessons[0].DayKey != 20261102 || lessons[0].LessonNumber != 1 ||
		lessons[0].Discipline != "Дисциплина m1" || lessons[0].Room != "к. 1/306" {
		t.Errorf("содержимое пары: %+v", lessons[0])
	}
	// m7 — два помещения одного корпуса: они остаются одной точкой, и список
	// обязан показать обе аудитории, а не первую.
	if lessons[1].Room != "к. 1/710, к. 1/711" {
		t.Errorf("аудитория пары в двух помещениях: %q", lessons[1].Room)
	}

	// Клик по корпусу без точки тоже раскрывается списком: человеку важно
	// видеть, какие занятия сидят в корпусе, который карта не показывает.
	unresolved := decodeMap(t, mapQuery(e.h, period+"&building=2"))
	if len(unresolved.Buildings) != 1 || unresolved.Buildings[0].PlaceState != "unresolved" ||
		len(unresolved.Buildings[0].Lessons) != 1 {
		t.Fatalf("корпус 2 по фильтру: %+v", unresolved.Buildings)
	}

	// Корпус, у которого пар нет, даёт пустую сводку, а не 404: «такого корпуса
	// нет» и «в этом корпусе нет пар на период» — разные вещи, и обе честнее
	// отказа.
	unknown := decodeMap(t, mapQuery(e.h, period+"&building=7"))
	if len(unknown.Buildings) != 0 {
		t.Errorf("фильтр по несуществующему корпусу: %v", mapBuildingsOrder(unknown.Buildings))
	}

	// Корпус 3 принимает только m3: пара m5 названа в нём наравне с корпусом 1,
	// но показать её в одном из двух значило бы приписать корпусу занятие,
	// половину которого он не видел.
	third := decodeMap(t, mapQuery(e.h, period+"&building=3"))
	if lessons := third.Buildings[0].Lessons; len(lessons) != 1 || lessons[0].ExternalID != "m3" {
		t.Errorf("пары корпуса 3: %+v", lessons)
	}

	// Счётчик «не показано» относится ко всему запрошенному периоду, а не к
	// отобранной строке: страница показывает его рядом с заголовком сводки, и
	// менять его от клика по маркеру означало бы иметь две разные цифры про одно
	// и то же расписание.
	if unknown.Unplaced.LessonCount != 4 {
		t.Errorf("unplaced изменился от фильтра корпусов: %d", unknown.Unplaced.LessonCount)
	}
}

func TestScheduleMapScansAllGroupsWhenGroupIsEmpty(t *testing.T) {
	e := newScheduleMapEnv(t)
	e.seedCatalog(t,
		store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"},
		store.SourceGroup{ExternalID: "495", Name: "Б-ПОИ-22"},
	)
	e.addPlace(t, "Главный корпус", "1", &placeCoords{52.123456, 104.287654})

	e.seedMonth(t, "494", 11, 2026, mapLesson("a1", 20261102, 1, "к. 1/306"))
	e.seedMonth(t, "495", 11, 2026,
		mapLesson("b1", 20261102, 1, "к. 1/207"),
		mapLesson("b2", 20261103, 1, "к. 5/01"),
	)

	all := decodeMap(t, mapQuery(e.h, "/api/schedule/map?month=11&year=2026"))
	if got := all.buildingOf(t, "1"); got.LessonCount != 2 {
		t.Errorf("в корпусе 1 %d пар, ожидалось 2 (по всем группам)", got.LessonCount)
	}
	if len(all.Buildings) != 2 {
		t.Fatalf("корпусов %d: %v", len(all.Buildings), mapBuildingsOrder(all.Buildings))
	}

	// Тот же период с фильтром по группе: 494 видит свою пару, но не чужую —
	// и корпус 5, которого в её расписании нет, не появляется.
	one := decodeMap(t, mapQuery(e.h, "/api/schedule/map?month=11&year=2026&group=494"))
	if one.Group == nil || one.Group.ID != "494" || one.Group.Name != "Б-ИСиТ-22" {
		t.Fatalf("группа в ответе: %+v", one.Group)
	}
	if len(one.Buildings) != 1 || one.Buildings[0].Building != "1" || one.Buildings[0].LessonCount != 1 {
		t.Errorf("сводка одной группы: %+v", one.Buildings)
	}
	if one.Unplaced.LessonCount != 0 {
		t.Errorf("у группы 494 всё показано, но unplaced = %d", one.Unplaced.LessonCount)
	}
}

// --- Пустой ответ и примеры --------------------------------------------------

func TestScheduleMapEmptyIsArraysNotNull(t *testing.T) {
	e := newScheduleMapEnv(t)
	e.addPlace(t, "Главный корпус", "1", &placeCoords{52.123456, 104.287654})

	rec := mapQuery(e.h, "/api/schedule/map?month=1&year=2027")
	payload := decode(t, rec)

	for _, key := range []string{"buildings", "unplaced"} {
		if payload[key] == nil {
			t.Fatalf("%s закодирован как null — страница падает на .length", key)
		}
	}
	if raw, ok := payload["buildings"].([]any); !ok || len(raw) != 0 {
		t.Errorf("buildings при пустом периоде: %#v", payload["buildings"])
	}

	unplaced, ok := payload["unplaced"].(map[string]any)
	if !ok {
		t.Fatalf("unplaced не объект: %T", payload["unplaced"])
	}
	if reasons, ok := unplaced["reasons"].([]any); !ok || len(reasons) != 0 {
		t.Errorf("reasons при пустом периоде: %#v", unplaced["reasons"])
	}
	if unplaced["lesson_count"] != float64(0) {
		t.Errorf("lesson_count = %v", unplaced["lesson_count"])
	}
	// Ни один корпус не появляется «из справочника»: сводка строится по парам,
	// иначе карта показывала бы корпуса, где занятий на период нет.
	if strings.Contains(rec.Body.String(), "Главный") {
		t.Error("в сводку без пар попали места справочника")
	}
}

func TestScheduleMapKeepsUnplacedRoomsButCapsExamples(t *testing.T) {
	e := newScheduleMapEnv(t)
	e.seedCatalog(t, store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"})

	// Формат аудиторий взят из живого импорта: «без ауд.», пустая строка, номер
	// без корпуса, «к.» без цифр — и повтор той же строки (дубль не должен
	// увеличивать список примеров).
	e.seedMonth(t, "494", 11, 2026,
		mapLesson("n1", 20261102, 1, "без ауд."),
		mapLesson("n2", 20261102, 2, "201"),
		mapLesson("n3", 20261102, 3, "актовый зал"),
		mapLesson("n4", 20261102, 4, "к. /306"),
		mapLesson("n5", 20261102, 5, ""),
		mapLesson("n6", 20261102, 6, "лаборатория 4"),
		mapLesson("n7", 20261102, 7, "201"),
		mapLesson("n8", 20261102, 8, "без ауд."),
	)

	payload := decodeMap(t, mapQuery(e.h, "/api/schedule/map?month=11&year=2026"))

	if len(payload.Buildings) != 0 {
		t.Fatalf("ни одна строка не называет корпус, но сводка не пуста: %v",
			mapBuildingsOrder(payload.Buildings))
	}
	if payload.Unplaced.LessonCount != 8 {
		t.Errorf("не показано %d пар, ожидалось 8", payload.Unplaced.LessonCount)
	}
	if len(payload.Unplaced.Reasons) != 1 {
		t.Fatalf("причины: %+v", payload.Unplaced.Reasons)
	}

	reason := payload.Unplaced.Reasons[0]
	if reason.Reason != "no_building" || reason.LessonCount != 8 {
		t.Fatalf("причина: %+v", reason)
	}
	// Примеры — подпись для человека, а не выгрузка: три строки и не больше.
	if len(reason.Rooms) != 3 {
		t.Fatalf("примеров аудиторий %d: %q", len(reason.Rooms), reason.Rooms)
	}
	seen := map[string]bool{}
	for _, room := range reason.Rooms {
		if seen[room] {
			t.Errorf("пример аудиторий содержит дубль %q", room)
		}
		seen[room] = true
	}
}

// --- Период ------------------------------------------------------------------

func TestScheduleMapDefaultsToCurrentWeek(t *testing.T) {
	e := newScheduleMapEnv(t)
	e.seedCatalog(t, store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"})
	e.addPlace(t, "Главный корпус", "1", &placeCoords{52.123456, 104.287654})
	e.addPlace(t, "Корпус 2", "2", nil)

	now := time.Now()

	// Один пакет на обе пары: два вызова seedMonth за один месяц стёрли бы
	// друг друга — импорт заменяет пакет целиком. Дальняя пара лежит в пакете
	// «текущий месяц», но её day_key остаётся её собственным, и фильтр видит
	// именно день.
	e.seedMonth(t, "494", int(now.Month()), now.Year(),
		mapLesson("w1", dayKey(now.Unix()), 1, "к. 1/306"),
		mapLesson("w2", dayKey(now.AddDate(0, 0, 20).Unix()), 1, "к. 2/102"),
	)

	payload := decodeMap(t, mapQuery(e.h, "/api/schedule/map"))

	// Та же неделя, что у страницы расписания: from — сегодня, to — шестой
	// день после.
	if payload.Range.From != dayKey(now.Unix()) || payload.Range.To != dayKey(now.AddDate(0, 0, 6).Unix()) {
		t.Fatalf("range по умолчанию %+v, ожидалась текущая неделя", payload.Range)
	}
	if len(payload.Buildings) != 1 || payload.Buildings[0].Building != "1" {
		t.Fatalf("за пределы недели попали корпуса %v", mapBuildingsOrder(payload.Buildings))
	}
	if payload.Unplaced.LessonCount != 0 {
		t.Errorf("пара дальше недели попала в unplaced: %+v", payload.Unplaced.Reasons)
	}
}

func TestScheduleMapRangeFromUnixSeconds(t *testing.T) {
	e := mainMapEnv(t)

	from := time.Date(2026, 11, 3, 9, 0, 0, 0, time.Local).Unix()
	to := time.Date(2026, 11, 4, 9, 0, 0, 0, time.Local).Unix()

	payload := decodeMap(t, mapQuery(e.h, fmt.Sprintf("/api/schedule/map?from=%d&to=%d", from, to)))

	if payload.Range.From != 20261103 || payload.Range.To != 20261104 {
		t.Fatalf("range = %+v", payload.Range)
	}
	// На 3 ноября приходятся m6 и m7 — корпуса 10 и 1.
	if order := mapBuildingsOrder(payload.Buildings); strings.Join(order, ",") != "1,10" {
		t.Errorf("сводка одного дня: %v", order)
	}
	if payload.Unplaced.LessonCount != 0 {
		t.Errorf("в этот день всё показано, но unplaced = %d", payload.Unplaced.LessonCount)
	}

	// Перевёрнутый период обработчик разворачивает — так же, как лента: страница
	// может получить границы от двух независимых полей формы.
	swapped := decodeMap(t, mapQuery(e.h, fmt.Sprintf("/api/schedule/map?from=%d&to=%d", to, from)))
	if swapped.Range.From != 20261103 || swapped.Range.To != 20261104 {
		t.Errorf("перевёрнутый период дал range %+v", swapped.Range)
	}
}

func TestScheduleMapRejectsBrokenRange(t *testing.T) {
	e := mainMapEnv(t)

	cases := []struct {
		target string
		want   string
	}{
		{"/api/schedule/map?from=abc", "from"},
		{"/api/schedule/map?to=abc", "to"},
		{"/api/schedule/map?month=13&year=2026", "month"},
		{"/api/schedule/map?month=11&year=1999", "year"},
	}

	for _, test := range cases {
		rec := mapQuery(e.h, test.target)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: код %d, ожидалось 422: %s", test.target, rec.Code, rec.Body.String())
			continue
		}
		if message := errorMessage(t, rec); !strings.Contains(message, test.want) {
			t.Errorf("%s: отказ %q не называет поле %q", test.target, message, test.want)
		}
	}
}

// --- Группа ------------------------------------------------------------------

func TestScheduleMapResolvesGroupByName(t *testing.T) {
	e := mainMapEnv(t)

	// Страница передаёт то, что набрал человек: регистр и пробел по краям не
	// должны превращаться в 404. Пробел закодирован — httptest.NewRequest
	// падает на голом пробеле в URL.
	rec := mapQuery(e.h, "/api/schedule/map?month=11&year=2026&group=%D0%B1-%D0%B8%D1%81%D0%B8%D1%82-22%20")
	if rec.Code != http.StatusOK {
		t.Fatalf("поиск группы по названию отвечен %d: %s", rec.Code, rec.Body.String())
	}

	payload := decodeMap(t, rec)
	if payload.Group == nil || payload.Group.ID != "494" {
		t.Fatalf("группа по названию не разрешилась: %+v", payload.Group)
	}
}

func TestScheduleMapGroupErrorsNameTheGroup(t *testing.T) {
	e := mainMapEnv(t)
	// Две группы с одинаковым названием — состояние, которое импорт источника
	// допускает: выбрать по имени нельзя, и ответ с «первой попавшейся» показал
	// бы расписание не той группы.
	e.seedCatalog(t, store.SourceGroup{ExternalID: "495", Name: "Б-ИСиТ-22"})
	e.seedMonth(t, "495", 11, 2026, mapLesson("c1", 20261102, 1, "к. 1/100"))

	unknown := mapQuery(e.h, "/api/schedule/map?group=999")
	expectNotFound(t, "неизвестная группа", unknown)
	if message := fieldMessage(t, unknown, "group"); !strings.Contains(message, "catalogue") {
		t.Errorf("отказ про группу: %q", message)
	}

	ambiguous := mapQuery(e.h, "/api/schedule/map?group="+url.QueryEscape("Б-ИСиТ-22"))
	if ambiguous.Code != http.StatusUnprocessableEntity {
		t.Fatalf("неоднозначное имя отвечено %d: %s", ambiguous.Code, ambiguous.Body.String())
	}
	// Текст называет оба id: без них человек не знает, что именно выбрать.
	body := ambiguous.Body.String()
	if !strings.Contains(body, "494") || !strings.Contains(body, "495") {
		t.Errorf("в отказе нет списка групп: %s", body)
	}
}

// --- Метод, зависимости, сбой базы -------------------------------------------

func TestScheduleMapIsGETOnly(t *testing.T) {
	e := mainMapEnv(t)

	rec := httptest.NewRecorder()
	http.HandlerFunc(e.h.ScheduleMapAPI).ServeHTTP(rec,
		request(http.MethodPost, "/api/schedule/map", "{}", "10.0.0.1:1", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST отвечен %d, ожидалось 405: %s", rec.Code, rec.Body.String())
	}
}

func TestScheduleMapWithoutDependencies(t *testing.T) {
	// Сборка без хранилища источника: «карты нет», а не «пустая карта» и не 500
	// — запрос обработан верно, отсутствующая часть является конфигурацией
	// сервера.
	bare := New(Deps{Limits: NewLimiters()})
	rec := mapQuery(bare, "/api/schedule/map")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("код %d, ожидалось 503: %s", rec.Code, rec.Body.String())
	}
	if body := decodeError(t, rec); !strings.Contains(body.Message, "schedule source") {
		t.Errorf("отказ не называет причину: %+v", body)
	}

	// Источник есть, справочника мест нет: сводку построить не с чем, и
	// отдать её «без точек» было бы неправдой про справочник.
	e := newScheduleSourceEnv(t)
	if e.h.Places != nil {
		t.Fatal("контур источника не должен сам подключать справочник")
	}

	withoutPlaces := mapQuery(e.h, "/api/schedule/map")
	if withoutPlaces.Code != http.StatusServiceUnavailable {
		t.Fatalf("код %d, ожидалось 503: %s", withoutPlaces.Code, withoutPlaces.Body.String())
	}
	if body := decodeError(t, withoutPlaces); !strings.Contains(body.Message, "places") {
		t.Errorf("отказ не называет причину: %+v", body)
	}
}

func TestScheduleMapDatabaseFailureIsNotSilence(t *testing.T) {
	// Пустая сводка при сгоревшей таблице выглядит как «корпусов нет» — ровно
	// та подмена, которую запрещает ADR-0029.
	lessonsDown := mainMapEnv(t)
	breakTable(t, lessonsDown.db, "schedule_source_lessons")
	expectStoreFailure(t, "сводка при снятой таблице пар",
		mapQuery(lessonsDown.h, "/api/schedule/map?month=11&year=2026"))

	placesDown := mainMapEnv(t)
	breakTable(t, placesDown.db, "places")
	expectStoreFailure(t, "сводка при снятом справочнике",
		mapQuery(placesDown.h, "/api/schedule/map?month=11&year=2026"))
}
