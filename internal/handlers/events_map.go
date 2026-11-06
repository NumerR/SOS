package handlers

import (
	"net/http"

	"github.com/NumerR/SOS/internal/store"
)

// Слой карты «события по местам» (GET /api/events/map).
//
// Второй слой поверх того же справочника, что и первый, поэтому словарь
// состояний точки у них общий (map_state.go): фронтенд рисует одну легенду по
// ответам обоих путей.
//
// Чем этот слой отличается от первого. У пары расписания места нет — есть
// строка аудитории из чужого API, и корпус из неё вытаскивает правило
// (internal/apeks). У события место выбирает человек, и ссылка лежит в самой
// строке. Отсюда два новых состояния: no_place («место не выбрано») вместо
// no_building и no_date («событию нечего встать в период»).

// eventsMapEvent — одно событие под маркером.
//
// Состояние точки и координаты здесь не повторяются: они уже стоят на маркере,
// то есть в eventsMapPlace. Второе число на то же место расходится с первым
// при любой правке справочника.
type eventsMapEvent struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Category  string `json:"category"`
	EventDate int64  `json:"event_date"`
}

// eventsMapPlace — маркер: место и всё, что в него встало за период.
//
// Ключ — id места, а не название: два места могут называться одинаково
// («Актовый зал» в разных корпусах), и по названию страница не отличила бы
// один маркер от другого.
//
// Название стоит рядом с `place`, а не только внутри него — как у первого слоя
// номер корпуса стоит рядом, а не внутри координат. Из `place` без координат
// название исчезло бы вместе с ними, и строка «место есть, точки нет» в легенде
// осталась бы безымянной: человек увидел бы «1 событие не показано» и не понял
// бы, какого корпуса это касается.
type eventsMapPlace struct {
	PlaceID    int64            `json:"place_id"`
	Name       string           `json:"name"`
	PlaceState string           `json:"place_state"`
	Place      *mapPlace        `json:"place"`
	EventCount int              `json:"event_count"`
	Events     []eventsMapEvent `json:"events"`
}

// eventsMapReason — одна причина «не показали» с примерами названий. Три
// строки — тот же потолок, что у слоя пар: примеры нужны для подписи «N
// событий не показаны», а не для выгрузки списка.
type eventsMapReason struct {
	Reason     string   `json:"reason"`
	EventCount int      `json:"event_count"`
	Titles     []string `json:"titles"`
}

type eventsMapUnplaced struct {
	EventCount int               `json:"event_count"`
	Reasons    []eventsMapReason `json:"reasons"`
}

type eventsMapResponse struct {
	// Range — границы периода в unix-секундах включительно: в этой форме дата
	// хранится у события. Слой пар отвечает ключами дней, потому что пары
	// приходят из источника именно так.
	Range    map[string]int64  `json:"range"`
	Places   []eventsMapPlace  `json:"places"`
	Unplaced eventsMapUnplaced `json:"unplaced"`
}

// EventsMapAPI — GET /api/events/map?from=&to= (или month=&year=).
//
// Период задаётся теми же параметрами, что у /api/schedule/map, и по умолчанию
// это текущая неделя: человек переключает неделю один раз, а не подстраивает
// два слоя под два разных фильтра.
//
// Права не требуется: события университета уже открыты всякому вошедшему через
// GET /api/events, и карта не показывает ни на йоту больше.
func (h *Handlers) EventsMapAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}

	// Та же пара отказов, что у первого слоя: сборка без зависимости отвечает
	// «карты нет» (503), а не пустым списком и не 500. Два слоя одного экрана
	// обязаны на одно и то же состояние сборки отвечать одинаково.
	if h.Events == nil {
		writeError(w, http.StatusServiceUnavailable, "events storage is not connected")
		return
	}
	if h.Places == nil {
		writeError(w, http.StatusServiceUnavailable, "places directory is not connected")
		return
	}

	from, to, err := scheduleTimeRange(r)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	events, err := h.Events.MapEvents(r.Context(), from, to)
	if err != nil {
		internalError(w, r, "failed to load events for the map", err)
		return
	}

	places, err := h.Places.List(r.Context(), false)
	if err != nil {
		internalError(w, r, "failed to load places for the map", err)
		return
	}

	writeJSON(w, http.StatusOK, buildEventsMap(events, places, from, to))
}

// eventsMapBucket — накопитель одного маркера.
type eventsMapBucket struct {
	state  string
	events []eventsMapEvent
}

// buildEventsMap — чистая часть слоя: раскладка событий по местам.
//
// Вынесена из обработчика, как и у первого слоя: здесь живут оба решения,
// которые надо проверять без HTTP, — что делать с событием без места и что
// делать с местом без координат.
func buildEventsMap(events []store.MapEvent, places []store.Place, from, to int64) eventsMapResponse {
	directory := make(map[int64]*store.Place, len(places))
	for i := range places {
		directory[places[i].ID] = &places[i]
	}

	buckets := make(map[int64]*eventsMapBucket, 8)
	reasons := make(map[string]*eventsMapReason, 4)
	unplaced := 0

	for _, event := range events {
		// Даты нет — событию негде встать: период это и есть даты. В накопитель
		// места такое событие не кладётся: маркер обещал бы инфоповод в этот
		// период, а его там нет.
		if event.EventDate == 0 {
			unplaced++
			addEventsReason(reasons, placeStateNoDate, event.Title)
			continue
		}

		if event.PlaceID == nil {
			unplaced++
			addEventsReason(reasons, placeStateNoPlace, event.Title)
			continue
		}

		place := directory[*event.PlaceID]
		if place == nil {
			// Место не разрешилось — событие считается как «без места», а не
			// исчезает из ответа. Отдельного состояния на этот случай нет
			// нарочно: ссылка на places защищена внешним ключом, и словарь
			// карты не обязан описывать ветку, которую база не пропускает.
			unplaced++
			addEventsReason(reasons, placeStateNoPlace, event.Title)
			continue
		}

		state := placeStateResolved
		if place.Lat == nil || place.Lon == nil {
			state = placeStateUnresolved
		}

		bucket := buckets[*event.PlaceID]
		if bucket == nil {
			bucket = &eventsMapBucket{state: state}
			buckets[*event.PlaceID] = bucket
		}

		if state != placeStateResolved {
			unplaced++
			addEventsReason(reasons, state, event.Title)
		}

		bucket.events = append(bucket.events, eventsMapEvent{
			ID:        event.ID,
			Title:     event.Title,
			Category:  event.Category,
			EventDate: event.EventDate,
		})
	}

	return eventsMapResponse{
		Range:    map[string]int64{"from": from, "to": to},
		Places:   renderEventsBuckets(buckets, places),
		Unplaced: renderEventsReasons(reasons, unplaced),
	}
}

// renderEventsBuckets — маркеры в порядке справочника.
//
// Порядок берётся у PlaceStore.List, а не пересчитывается здесь: тот уже
// сортирует по названию в Go, потому что SQLite не складывает в нижний регистр
// кириллицу, — вторая копия этого правила расходилась бы с первой при любой
// правке справочника. Строка справочника заодно даёт и координаты: поле `place`
// остаётся null, когда их нет, и фронтенд видит «место есть, точки нет» по
// `place_state`, а не по догадке.
func renderEventsBuckets(buckets map[int64]*eventsMapBucket, places []store.Place) []eventsMapPlace {
	out := make([]eventsMapPlace, 0, len(buckets))

	for _, place := range places {
		bucket := buckets[place.ID]
		if bucket == nil {
			continue
		}

		item := eventsMapPlace{
			PlaceID:    place.ID,
			Name:       place.Name,
			PlaceState: bucket.state,
			EventCount: len(bucket.events),
			Events:     bucket.events,
		}
		if place.Lat != nil && place.Lon != nil {
			item.Place = &mapPlace{Name: place.Name, Lat: *place.Lat, Lon: *place.Lon}
		}

		out = append(out, item)
	}

	return out
}

func addEventsReason(reasons map[string]*eventsMapReason, state, title string) {
	entry, found := reasons[state]
	if !found {
		entry = &eventsMapReason{Reason: state, Titles: []string{}}
		reasons[state] = entry
	}

	entry.EventCount++

	if len(entry.Titles) < 3 {
		for _, known := range entry.Titles {
			if known == title {
				return
			}
		}
		entry.Titles = append(entry.Titles, title)
	}
}

// renderEventsReasons — сводка «почему не показали».
//
// Порядок фиксированный, а не по частоте: страница вставляет строки в одном и
// том же порядке на каждом обновлении, иначе список подпрыгивает на глазах.
// Сначала география (место без координат), потом выбор человека (места нет),
// потом дата (событию негде встать в период).
func renderEventsReasons(reasons map[string]*eventsMapReason, total int) eventsMapUnplaced {
	states := []string{placeStateUnresolved, placeStateNoPlace, placeStateNoDate}

	out := eventsMapUnplaced{
		EventCount: total,
		Reasons:    []eventsMapReason{},
	}

	for _, state := range states {
		if entry, found := reasons[state]; found {
			out.Reasons = append(out.Reasons, *entry)
		}
	}

	return out
}
