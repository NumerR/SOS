package handlers

import (
	"net/http"
	"sort"
	"strings"

	"github.com/NumerR/SOS/internal/apeks"
	"github.com/NumerR/SOS/internal/store"
)

// Слой карты «пары по корпусам» (GET /api/schedule/map).
//
// Почему это не /api/schedule/lessons: у страницы расписания один объект —
// список пар одной группы, у карты другой — сколько пар стоит в каждом корпусе
// за период. Второе не вынуть из первого, не перебирая весь каталог запросами,
// поэтому у слоя свой вопрос в store (MapLessons) и своя агрегация здесь.
//
// Главное правило слоя следует из брифа (п.3 — «нет вымышленных координат»):
// корпус без точки остаётся в ответе, но маркером не становится. Фронтенд
// обязан отличить «такого корпуса в справочнике нет» от «место есть, координат
// пока нет» и показать обе строки числом «N пар не показаны». Молча выбросить
// пару нельзя: пустая площадка на карте выглядит как «в этом корпусе занятий
// нет», и это уже неправда про расписание вуза.

// scheduleMapDay — сколько пар в этом корпусе в этот день. Дни нужны списком, а
// не одним числом: страница переключает день недели, и второй запрос за этим
// же периодом был бы дороже этого ответа.
type scheduleMapDay struct {
	DayKey      int    `json:"day_key"`
	Date        string `json:"date"`
	LessonCount int    `json:"lesson_count"`
}

type scheduleMapBuilding struct {
	Building    string               `json:"building"`
	PlaceState  string               `json:"place_state"`
	Place       *mapPlace            `json:"place"`
	LessonCount int                  `json:"lesson_count"`
	Days        []scheduleMapDay     `json:"days"`
	Lessons     []store.SourceLesson `json:"lessons,omitempty"`
}

// scheduleMapReason — одна причина «не показали» с примерами строк аудиторий.
// Примеры ограничены несколькими строками: они для подписи, а не для выгрузки.
type scheduleMapReason struct {
	Reason      string   `json:"reason"`
	LessonCount int      `json:"lesson_count"`
	Rooms       []string `json:"rooms"`
}

type scheduleMapUnplaced struct {
	LessonCount int                 `json:"lesson_count"`
	Reasons     []scheduleMapReason `json:"reasons"`
}

type scheduleMapGroup struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type scheduleMapResponse struct {
	Source     string                `json:"source"`
	SourceName string                `json:"source_name"`
	Range      map[string]int        `json:"range"`
	Group      *scheduleMapGroup     `json:"group"`
	Buildings  []scheduleMapBuilding `json:"buildings"`
	Unplaced   scheduleMapUnplaced   `json:"unplaced"`

	// UpdatedAt — самый свежий импорт среди попавших в период пар. Подпись
	// «обновлено N минут назад» на карте обязана быть тем же числом, что и на
	// странице расписания, а не временем ответа: иначе две страницы одного
	// источника расходились бы на глазах у человека.
	UpdatedAt int64 `json:"updated_at"`
}

// ScheduleMapAPI — GET /api/schedule/map?from=&to=&group=&building=.
//
// from/to — unix-секунды, как у /api/schedule/lessons (общий scheduleDayRange),
// по умолчанию текущая неделя; month/year работают так же, как там.
//
// group пустой — все группы каталога: карте нужен «сколько пар в корпусе», а
// не «что у этой группы». building сужает сводку до одного корпуса и добавляет
// ему список пар — это второй запрос страницы, когда человек кликнул маркер.
func (h *Handlers) ScheduleMapAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	// Отсутствие зависимости — 503, как у всех путей этого модуля: сборка без
	// хранилища источника или без справочника отвечает «карты сейчас нет», а не
	// «внутренняя ошибка». Молча отдать пустую сводку было бы хуже обоих: карта
	// выглядела бы корпусом без занятий.
	if h.ScheduleSource == nil {
		writeError(w, http.StatusServiceUnavailable, "schedule source storage is not connected")
		return
	}
	if h.Places == nil {
		writeError(w, http.StatusServiceUnavailable, "places directory is not connected")
		return
	}

	query := r.URL.Query()

	var group *scheduleMapGroup

	if ref := strings.TrimSpace(query.Get("group")); ref != "" {
		resolved, err := h.ScheduleSource.ResolveGroup(r.Context(), apeks.Source, ref)
		if err != nil {
			h.writeScheduleSourceError(w, r, "group", err)
			return
		}
		group = &scheduleMapGroup{ID: resolved.ExternalID, Name: resolved.Name}
	}

	fromKey, toKey, err := scheduleDayRange(r)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	groupExtID := ""
	if group != nil {
		groupExtID = group.ID
	}

	lessons, err := h.ScheduleSource.MapLessons(r.Context(), apeks.Source, groupExtID, fromKey, toKey)
	if err != nil {
		internalError(w, r, "failed to load lessons for the map", err)
		return
	}

	places, err := h.Places.List(r.Context(), false)
	if err != nil {
		internalError(w, r, "failed to load places for the map", err)
		return
	}

	writeJSON(w, http.StatusOK, buildScheduleMap(lessons, places, group, fromKey, toKey,
		strings.TrimSpace(query.Get("building"))))
}

// scheduleMapBucket — накопитель одного корпуса.
type scheduleMapBucket struct {
	state   string
	place   *store.Place
	count   int
	days    map[int]*scheduleMapDay
	lessons []store.SourceLesson
}

// placesByBuilding — справочник, индексированный номером корпуса.
//
// Одна функция на два места: слой карты сопоставляет пары корпусам, а строка
// аудитории в расписании — имя корпуса. Сводить то и другое к одному ключу
// дважды значило бы иметь два разных правила сопоставления, и расхождение
// «карта знает корпус, а подпись нет» стало бы вопросом времени.
func placesByBuilding(places []store.Place) map[string]*store.Place {
	directory := make(map[string]*store.Place, len(places))
	for i := range places {
		key := strings.TrimSpace(places[i].Building)
		if key == "" {
			// Место без номера корпуса — это площадка («актовый зал»), и
			// сопоставлять ей пары не по чему.
			continue
		}
		directory[key] = &places[i]
	}

	return directory
}

// buildScheduleMap — чистая часть слоя: агрегация пар по корпусам и дням.
//
// Вынесена из обработчика потому, что именно здесь живут оба решения, которые
// надо проверять без HTTP: что делать с парой без корпуса и что делать с
// корпусом, которого нет в справочнике.
func buildScheduleMap(lessons []store.SourceLesson, places []store.Place, group *scheduleMapGroup,
	fromKey, toKey int, building string) scheduleMapResponse {

	directory := placesByBuilding(places)

	buckets := make(map[string]*scheduleMapBucket, 8)
	reasons := make(map[string]*scheduleMapReason, 4)
	unplaced := 0
	latest := int64(0)

	for _, lesson := range lessons {
		if lesson.ImportedAt > latest {
			latest = lesson.ImportedAt
		}

		buildings := apeks.BuildingsFromRoom(lesson.Room)

		if len(buildings) != 1 {
			// Ноль — «без ауд.» и просто «201»; больше одного — пара, у
			// источника названная в двух корпусах. Ни то ни другое нельзя
			// «выбрать первое»: точка уедет в дом, где занятия не было.
			state := placeStateNoBuilding
			if len(buildings) > 1 {
				state = placeStateSeveral
			}
			unplaced++
			addMapReason(reasons, state, lesson.Room)
			continue
		}

		key := buildings[0]

		bucket := buckets[key]
		if bucket == nil {
			bucket = &scheduleMapBucket{days: make(map[int]*scheduleMapDay, 8)}
			known := directory[key]
			switch {
			case known == nil:
				bucket.state = placeStateMissing
			case known.Lat == nil || known.Lon == nil:
				bucket.state = placeStateUnresolved
			default:
				bucket.state = placeStateResolved
			}
			bucket.place = known
			buckets[key] = bucket
		}

		if bucket.state != placeStateResolved {
			unplaced++
			addMapReason(reasons, bucket.state, lesson.Room)
		}

		bucket.count++

		day, found := bucket.days[lesson.DayKey]
		if !found {
			day = &scheduleMapDay{DayKey: lesson.DayKey, Date: lesson.Date}
			bucket.days[lesson.DayKey] = day
		}
		day.LessonCount++

		if building != "" && building == key {
			bucket.lessons = append(bucket.lessons, lesson)
		}
	}

	return scheduleMapResponse{
		Source:     apeks.Source,
		SourceName: apeks.DisplayName,
		Range:      map[string]int{"from": fromKey, "to": toKey},
		Group:      group,
		Buildings:  renderMapBuckets(buckets, building),
		Unplaced:   renderMapReasons(reasons, unplaced),
		UpdatedAt:  latest,
	}
}

func addMapReason(reasons map[string]*scheduleMapReason, state, room string) {
	entry, found := reasons[state]
	if !found {
		entry = &scheduleMapReason{Reason: state, Rooms: []string{}}
		reasons[state] = entry
	}

	entry.LessonCount++

	if len(entry.Rooms) < 3 {
		for _, known := range entry.Rooms {
			if known == room {
				return
			}
		}
		entry.Rooms = append(entry.Rooms, room)
	}
}

// renderMapBuckets — сводка по корпусам.
//
// Порядок — номером, а не строкой: «10» обязан идти после «2», а не между «1»
// и «3», и список корпусов человек читает глазами. Ключи, где цифр нет
// («корпус А»), дописываются после чисел по алфавиту.
//
// Пустая сводка — это [], а не null: страница рисует «пар на период нет», а не
// падает на первом map.
func renderMapBuckets(buckets map[string]*scheduleMapBucket, filter string) []scheduleMapBuilding {
	keys := make([]string, 0, len(buckets))
	for key := range buckets {
		if filter != "" && key != filter {
			continue
		}
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return lessBuilding(keys[i], keys[j]) })

	out := make([]scheduleMapBuilding, 0, len(keys))
	for _, key := range keys {
		bucket := buckets[key]

		item := scheduleMapBuilding{
			Building:    key,
			PlaceState:  bucket.state,
			LessonCount: bucket.count,
			Days:        renderMapDays(bucket.days),
			Lessons:     bucket.lessons,
		}
		if place := bucket.place; place != nil && place.Lat != nil && place.Lon != nil {
			item.Place = &mapPlace{Name: place.Name, Lat: *place.Lat, Lon: *place.Lon}
		}

		out = append(out, item)
	}

	return out
}

func renderMapDays(days map[int]*scheduleMapDay) []scheduleMapDay {
	keys := make([]int, 0, len(days))
	for key := range days {
		keys = append(keys, key)
	}
	sort.Ints(keys)

	out := make([]scheduleMapDay, 0, len(keys))
	for _, key := range keys {
		out = append(out, *days[key])
	}
	return out
}

func renderMapReasons(reasons map[string]*scheduleMapReason, total int) scheduleMapUnplaced {
	states := []string{placeStateMissing, placeStateUnresolved, placeStateNoBuilding, placeStateSeveral}

	out := scheduleMapUnplaced{
		LessonCount: total,
		Reasons:     []scheduleMapReason{},
	}

	for _, state := range states {
		if entry, found := reasons[state]; found {
			out.Reasons = append(out.Reasons, *entry)
		}
	}

	return out
}

// lessBuilding — порядок номеров корпусов: числа по величине, затем всё
// остальное лексикографически.
//
// Число сравнивается длиной, а потом строкой, без превращения в int: номер
// корпуса приходит из текста источника, и «к. 99999999999999999999/1»
// переполнил бы число и уронил такой корпус в начало списка. Для настоящих
// номеров («1», «2», «10») результат тот же, что у арифметики.
func lessBuilding(left, right string) bool {
	leftNumeric, rightNumeric := isBuildingNumber(left), isBuildingNumber(right)

	switch {
	case leftNumeric && rightNumeric:
		if len(left) != len(right) {
			return len(left) < len(right)
		}
		return left < right
	case leftNumeric:
		return true
	case rightNumeric:
		return false
	default:
		return strings.ToLower(left) < strings.ToLower(right)
	}
}

// isBuildingNumber — весь ли ключ корпуса состоит из цифр. Пустая строка — нет:
// корпуса без номера в сводке не бывает, и «пустое» не должно вставать в начало.
func isBuildingNumber(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
