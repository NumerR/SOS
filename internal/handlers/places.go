package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/NumerR/SOS/internal/store"
)

// Справочник мест (карта).
//
// Здесь только транспорт: разбор формы, отображение ошибок стора в поля и
// запись в журнал. Решения о том, когда место считается разolved, где граница
// длины и кому принадлежит корпус, живут в internal/store/places.go — иначе
// список мест и слой карты начали бы по-разному отвечать на один и тот же ввод.

// PlaceRequest — тело создания и правки места.
//
// Lat/Lon — указатели именно поэтому: различить «координат нет» и «координата
// равна нулю» обязан сервер, а не форма. Обычный float64 прочитал бы пропущенное
// поле как 0 и поставил бы корпус в Гвинейский залив — точка 0,0 существует и
// выглядит правдоподобно, в том и опасность.
type PlaceRequest struct {
	ID       int64    `json:"id"`
	Name     string   `json:"name"`
	Building string   `json:"building"`
	Address  string   `json:"address"`
	Lat      *float64 `json:"lat"`
	Lon      *float64 `json:"lon"`
}

// PlacesAPI — GET (всякий вошедший) / POST (право place.manage, ставит
// маршрутизатор) /api/places.
func (h *Handlers) PlacesAPI(w http.ResponseWriter, r *http.Request) {
	if h.Places == nil {
		internalError(w, r, "places directory is not available", errDependencyNotConnected)
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.placesList(w, r)
	case http.MethodPost:
		h.placesCreate(w, r)
	default:
		methodNotAllowed(w)
	}
}

// placesList — GET /api/places[?resolved=1].
//
// Права не требуется: места — география кампуса, а не чужие данные, и карта без
// списка мест бессмысленна. Достаточно входа, который проверяет RequireAuth
// (карта — для авторизованных, бриф п.2).
//
// resolved=1 отдаёт только то, что встанет точкой. Слою карты второй вариант не
// нужен, а тому, кто ведёт справочник, нужны оба — поэтому фильтр один, а не два
// маршрута.
func (h *Handlers) placesList(w http.ResponseWriter, r *http.Request) {
	onlyResolved := false

	if raw := strings.TrimSpace(r.URL.Query().Get("resolved")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			writeFieldError(w, http.StatusUnprocessableEntity, "resolved",
				"resolved must be a boolean")
			return
		}
		onlyResolved = parsed
	}

	places, err := h.Places.List(r.Context(), onlyResolved)
	if err != nil {
		internalError(w, r, "failed to load places", err)
		return
	}

	writeJSON(w, http.StatusOK, places)
}

// placesCreate — POST /api/places.
func (h *Handlers) placesCreate(w http.ResponseWriter, r *http.Request) {
	user := h.currentUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req PlaceRequest
	if !readJSON(w, r, &req) {
		return
	}

	place, err := h.Places.Create(r.Context(), placeInput(req), user.ID)
	if err != nil {
		h.writePlaceFailure(w, r, err)
		return
	}

	h.recordAudit(r, auditWrite{
		action:     store.AuditPlaceCreate,
		actor:      user,
		targetType: "place",
		targetID:   auditTargetID(place.ID),
		detail:     placeRecordDetail(place),
		ok:         true,
	})

	writeJSON(w, http.StatusCreated, place)
}

// PlaceUpdate — POST /api/places/update.
//
// Отдельный путь, а не PUT /api/places/{id}: маршрутизатор страниц и реестр
// API работают на плоских путях (см. cmd/server/modules.go), и правка читается
// как то же тело, что создание, плюс id.
//
// Полная замена, а не частичный патч: форма приносит объект целиком, поэтому
// отсутствие координат в теле означает «координат нет». Двусмысленность «не
// прислали = не менять» оставила бы в базе точку, которую человек убрал.
func (h *Handlers) PlaceUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}

	user := h.currentUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req PlaceRequest
	if !readJSON(w, r, &req) {
		return
	}

	if req.ID <= 0 {
		writeFieldError(w, http.StatusUnprocessableEntity, "id", "id must be a positive number")
		return
	}

	if h.Places == nil {
		internalError(w, r, "places directory is not available", errDependencyNotConnected)
		return
	}

	// Прежняя строка читается до правки: журнал обязан показывать переход
	// «из чего в что», а не только итог. Иначе «точку сдвинули» неотличим от
	// «точку поставили впервые», и разбор «корпус уехал» превращается в перебор.
	before, err := h.Places.Get(r.Context(), req.ID)
	if err != nil {
		h.writePlaceFailure(w, r, err)
		return
	}

	place, err := h.Places.Update(r.Context(), req.ID, placeInput(req))
	if err != nil {
		h.writePlaceFailure(w, r, err)
		return
	}

	h.recordAudit(r, auditWrite{
		action:     store.AuditPlaceUpdate,
		actor:      user,
		targetType: "place",
		targetID:   auditTargetID(place.ID),
		detail:     "from=" + placeRecordDetail(before) + " to=" + placeRecordDetail(place),
		ok:         true,
	})

	writeJSON(w, http.StatusOK, place)
}

// placeInput переводит тело запроса в данные стора.
//
// Обрезка пробелов сделана здесь, а не только в сторе: «пробелы вместо
// названия» обязаны быть отказом про имя, а не про координаты, — форма рисует
// сообщение под конкретным полем.
func placeInput(req PlaceRequest) store.PlaceInput {
	return store.PlaceInput{
		Name:     strings.TrimSpace(req.Name),
		Building: strings.TrimSpace(req.Building),
		Address:  strings.TrimSpace(req.Address),
		Lat:      req.Lat,
		Lon:      req.Lon,
	}
}

// writePlaceFailure — единственное место, где ошибки справочника становятся
// ответом транспорта.
//
// Ошибки ввода переводятся в 422 с именем поля, и тексты стора наружу не
// идут: они написаны по-русски для человека, читающего код, а клиентский
// контракт — английский (ADR-0003). Всё остальное, включая ErrNotFound,
// уходит через internalError: сбой базы не имеет права притворяться «места
// нет» (ADR-0029).
func (h *Handlers) writePlaceFailure(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrPlaceNameEmpty):
		writeFieldError(w, http.StatusUnprocessableEntity, "name", "name is required")
	case errors.Is(err, store.ErrPlaceNameTooLong):
		writeFieldError(w, http.StatusUnprocessableEntity, "name", "name is too long")
	case errors.Is(err, store.ErrPlaceBuildingLong):
		writeFieldError(w, http.StatusUnprocessableEntity, "building", "building is too long")
	case errors.Is(err, store.ErrPlaceAddressTooLong):
		writeFieldError(w, http.StatusUnprocessableEntity, "address", "address is too long")
	case errors.Is(err, store.ErrPlaceLatLngPair):
		writeFieldError(w, http.StatusUnprocessableEntity, "lat", "lat and lon must be sent together")
	case errors.Is(err, store.ErrPlaceLatRange):
		writeFieldError(w, http.StatusUnprocessableEntity, "lat", "lat must be between -90 and 90")
	case errors.Is(err, store.ErrPlaceLonRange):
		writeFieldError(w, http.StatusUnprocessableEntity, "lon", "lon must be between -180 and 180")
	case errors.Is(err, store.ErrPlaceDuplicateKey):
		writeFieldError(w, http.StatusUnprocessableEntity, "building", "this building already has a place")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "place not found")
	default:
		internalError(w, r, "failed to write place", err)
	}
}

// placeRecordDetail — одна строка места для журнала.
//
// Значения в кавычках: название корпуса — свободный текст, и «Главное здание
// building=9» без экранирования притворилось бы вторым полем в той же строке
// (тот же смысл, что у запрета переводить строку доступа в audit_events).
//
// Координаты печатаются с шестью знаками: это ~11 см, точнее корпус никто не
// ставит, а журнал с обрезанной долготой показывал бы другой дом.
func placeRecordDetail(place *store.Place) string {
	if place == nil {
		return "place=-"
	}

	return "name=" + quoteAuditValue(place.Name) +
		" building=" + quoteAuditValue(place.Building) +
		" point=" + coordinatePair(place.Lat, place.Lon)
}

// coordinatePair — координаты одной строкой; «-» вместо пустоты, чтобы «нет
// точки» в журнале не читалось как «точка на нуле».
func coordinatePair(lat, lon *float64) string {
	if lat == nil || lon == nil {
		return "-"
	}

	return strconv.FormatFloat(*lat, 'f', 6, 64) + "," + strconv.FormatFloat(*lon, 'f', 6, 64)
}

// quoteAuditValue — значение в кавычки с экранированием и обрезкой по символам.
func quoteAuditValue(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return `""`
	}

	if runes := []rune(trimmed); len(runes) > auditPlaceValueCut {
		trimmed = string(runes[:auditPlaceValueCut])
	}

	escaped := strings.ReplaceAll(trimmed, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)

	return `"` + escaped + `"`
}

// auditPlaceValueCut — длина свободного текста в строке журнала.
//
// Поля detail в audit_events обрезается до 200 символов целиком; запись правки
// несёт два места («from=... to=...»), и без своей границы вторая половина
// отрезалась бы молча — то есть именно то, ради чего журнал и читается.
const auditPlaceValueCut = 40
