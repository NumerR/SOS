package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// PlaceStore читает и пишет справочник мест (places) — единственный источник
// координат для карты.
//
// Почему это отдельная таблица, а не колонка в events. Место бывает нужно
// двум слоям сразу: событию («концерт в главном корпусе») и паре расписания
// («занятие в корпусе 2»). Если бы координаты жили в строке события, каждый
// новый слой копировал бы их себе, и расхождение «главный корпус на карте
// в двухстах метрах от самого себя» стало бы вопросом времени. Справочник
// держит геометрию в одном месте, а слои ссылаются на неё.
//
// Почему координаты вносит человек, а не код. Бриф (п.3) запрещает
// вымышленные данные, и это именно тот случай: у нас нет ни генератора
// координат, ни права их придумать. Поэтому место без координат — рабочее
// состояние, а не ошибка: оно есть в списке, но на карте не рисуется
// (geo_state = "unresolved").
//
// Колонки времени — Unix-секунды, как во всей текущей схеме; `timestamptz`
// и `?`-плейсхолдеры уезжают вместе с PostgreSQL в T2.1.

// PlacesMigration — DDL таблицы places.
//
// Текст объявлен в store, а заявляет его модуль (internal/modules/places) —
// та же граница, что у AuditEventsMigration: модуль импортирует handlers,
// handlers импортируют store, и DDL внутри модуля замкнул бы цикл в тестах.
//
// Частичный уникальный индекс по building (только непустые значения) нужен
// потому, что корпус есть не у всякого места: «Главный корпус, пл. Ленина 3»
// привязан к строке источника «к. 1/311», а «площадка перед входом» корпуса
// не имеет вовсе. Обычный UNIQUE заставил бы заполнять для таких фиктивное
// значение — то есть выдумывать данные ровно там, где их нет.
//
// Индекса по названию нет намеренно: список справочника сортируется в Go
// (см. List — SQLite не складывает кириллицу), а точечного поиска по названию
// у API нет. Индекс, которого никто не спрашивает, только замедляет запись и
// объясняет следующему читателю несуществующий запрос.
const PlacesMigration = `
CREATE TABLE IF NOT EXISTS places (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL,
    building    TEXT    NOT NULL DEFAULT '',
    address     TEXT    NOT NULL DEFAULT '',
    lat         REAL,
    lon         REAL,
    created_by  INTEGER,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY(created_by) REFERENCES users(id) ON DELETE SET NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_places_building ON places(building) WHERE building <> '';
`

// CampusBuildingsSeed — пять корпусов, которые назвал владелец 2026-11-06, с его
// же координатами.
//
// Это не то, о чём говорит комментарий к таблице выше. Тот запрещает коду
// *придумывать* географию; здесь код записывает слова владельца, потому что
// другого носителя у них нет: до этой правки справочник был пуст, и карта
// честно отвечала «корпуса нет в справочнике» на каждое занятие вуза. Координаты
// взяты из сообщения владельца дословно, включая знаки после запятой, и никакая
// строка тут не округлена и не достроена.
//
// Каждая вставка защищена WHERE NOT EXISTS по номеру корпуса, а не
// INSERT OR IGNORE: второй вариант молча проглатывает любой UNIQUE, включая
// будущий индекс на название, и правка админа («Главный корпус стоит не там»)
// выглядела бы как несработавшая миграция. Так сид дописывает только то, чего
// нет, и никогда не перезатирает то, что человек поправил руками.
//
// address остаётся пустым: адрес корпуса владелец не называл, а выдуманная
// «пл. Ленина, 1» попала бы и в список, и в подпись точки.
const CampusBuildingsSeed = `
INSERT INTO places (name, building, address, lat, lon, created_at, updated_at)
SELECT 'Главный корпус', '1', '', 54.509643, 36.274133, strftime('%s','now'), 0
WHERE NOT EXISTS (SELECT 1 FROM places WHERE building = '1');

INSERT INTO places (name, building, address, lat, lon, created_at, updated_at)
SELECT 'Новый корпус', '2', '', 54.509152, 36.273756, strftime('%s','now'), 0
WHERE NOT EXISTS (SELECT 1 FROM places WHERE building = '2');

INSERT INTO places (name, building, address, lat, lon, created_at, updated_at)
SELECT 'Биофак', '3', '', 54.509073, 36.272633, strftime('%s','now'), 0
WHERE NOT EXISTS (SELECT 1 FROM places WHERE building = '3');

INSERT INTO places (name, building, address, lat, lon, created_at, updated_at)
SELECT 'Воскресенка', '4', '', 54.507343, 36.260344, strftime('%s','now'), 0
WHERE NOT EXISTS (SELECT 1 FROM places WHERE building = '4');

INSERT INTO places (name, building, address, lat, lon, created_at, updated_at)
SELECT 'Ленина', '5', '', 54.51157013381475, 36.262126522868904, strftime('%s','now'), 0
WHERE NOT EXISTS (SELECT 1 FROM places WHERE building = '5');
`

// Границы полей места. Значения приходят из формы, то есть извне.
//
// Превышение — отказ, а не молчаливая обрезка. Казалось бы, подрезать
// удобнее: запрос всегда успешен. Но обрезанное название остаётся в базе и
// в списке, человек видит «Корпус на площади Ленина» вместо того, что написал,
// и никто не знает, где ещё потерялись символы. Отказ пополю дешевле такой
// неизвестности.
const (
	maxPlaceNameLength    = 120
	maxPlaceBuildingLen   = 16
	maxPlaceAddressLength = 200
)

// Ошибки ввода. Отдельные на каждое поле, а не одна «некорректные данные»:
// обработчик обязан назвать полю, что именно в нём не так, — форма рисует
// сообщение рядом с конкретным input (единый формат ошибки, ADR-0003).
//
// ErrPlaceDuplicateKey рождается из SQLite-сообщения про UNIQUE — тем же
// распознавателем (isUniqueConstraintError из users.go), что и остальные
// места пакета. Второй, свой, означал бы две разные трактовки одного сбоя
// в одной базе.
var (
	ErrPlaceNameEmpty      = errors.New("название места обязательно")
	ErrPlaceNameTooLong    = errors.New("название места длиннее допустимого")
	ErrPlaceBuildingLong   = errors.New("номер корпуса длиннее допустимого")
	ErrPlaceAddressTooLong = errors.New("адрес длиннее допустимого")
	ErrPlaceLatLngPair     = errors.New("координаты задаются только парой")
	ErrPlaceLatRange       = errors.New("широта должна быть от -90 до 90")
	ErrPlaceLonRange       = errors.New("долгота должна быть от -180 до 180")
	ErrPlaceDuplicateKey   = errors.New("место с таким корпусом уже есть")
)

// Place — одна строка справочника.
//
// Lat/Lon — указатели: nil читается как «координат нет», а не как «координаты
// на экваторе и нулевом меридиане». 0.0 — правдоподобное значение (западная
// Африка), и подменить его вместо «неизвестно» означало бы нарисовать корпус
// там, где его никогда не было.
//
// Building — номер корпуса ровно в той форме, в какой его отдаёт источник
// расписания: строка аудитории «к. 1/311» даёт "1". Ключ сопоставляется
// посимвольно, поэтому поля с пробелами и нулями («01») — это другое место,
// и их заводит человек осознанно, а не угадывает код.
type Place struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	Building  string   `json:"building"`
	Address   string   `json:"address"`
	Lat       *float64 `json:"lat"`
	Lon       *float64 `json:"lon"`
	GeoState  string   `json:"geo_state"`
	CreatedBy *int64   `json:"created_by"`
	CreatedAt int64    `json:"created_at"`
	UpdatedAt int64    `json:"updated_at"`
}

const (
	// GeoResolved — координаты есть, точка встаёт на карту.
	GeoResolved = "resolved"
	// GeoUnresolved — координат нет: место видно списком, но не точкой.
	// Термин и поле задуманы в целевой схеме карты (docs/02 §3), чтобы
	// отсутствующая география отличалась от «места вообще нет».
	GeoUnresolved = "unresolved"
)

// PlaceInput — данные для создания и правки места.
type PlaceInput struct {
	Name     string
	Building string
	Address  string
	Lat      *float64
	Lon      *float64
}

// PlaceStore работает с таблицей places.
type PlaceStore struct {
	db *sql.DB
}

// NewPlaceStore создаёт хранилище справочника мест.
func NewPlaceStore(db *sql.DB) *PlaceStore {
	return &PlaceStore{db: db}
}

// normalizePlace приводит вход к виду, который можно сравнить и положить в
// базу: обрезка пробелов, границы длин и проверка, что координаты пришли
// парой и в допустимом диапазоне.
//
// Координаты проверяются последними: название обязательное, и пустое поле имени
// при битой долготе должно ругаться именно на имя, — форма показывает одно
// сообщение, и человек иначе чинит не то поле.
//
// Пара — обязательное условие, а не вежливость: строка с широтой без долготы
// неразолима, и оставить её означало бы показать на карте точку с известной
// половиной адреса. Лучше явный 422 в форму.
func normalizePlace(in PlaceInput) (PlaceInput, error) {
	place := PlaceInput{
		Name:     strings.TrimSpace(in.Name),
		Building: strings.TrimSpace(in.Building),
		Address:  strings.TrimSpace(in.Address),
		Lat:      in.Lat,
		Lon:      in.Lon,
	}

	switch {
	case place.Name == "":
		return place, ErrPlaceNameEmpty
	case placeOverruns(place.Name, maxPlaceNameLength):
		return place, ErrPlaceNameTooLong
	case placeOverruns(place.Building, maxPlaceBuildingLen):
		return place, ErrPlaceBuildingLong
	case placeOverruns(place.Address, maxPlaceAddressLength):
		return place, ErrPlaceAddressTooLong
	}

	if (place.Lat == nil) != (place.Lon == nil) {
		return place, ErrPlaceLatLngPair
	}
	if place.Lat != nil && (*place.Lat < -90 || *place.Lat > 90) {
		return place, ErrPlaceLatRange
	}
	if place.Lon != nil && (*place.Lon < -180 || *place.Lon > 180) {
		return place, ErrPlaceLonRange
	}

	return place, nil
}

// placeOverruns — длиннее ли поле границы. Считает символы, а не байты:
// названия приходят с кириллицей, и байтовая граница превратила бы «120
// символов» в ~60.
func placeOverruns(value string, limit int) bool {
	return utf8.RuneCountInString(value) > limit
}

// Create добавляет место.
//
// Ошибка уникального корпуса не глушится: форма обязана показать, что
// корпус уже занят, иначе человек решит, что «не сохранилось вообще», и
// попробует ещё раз — удвоив справочник двумя названиями одного здания.
func (s *PlaceStore) Create(ctx context.Context, in PlaceInput, createdBy int64) (*Place, error) {
	if s == nil || s.db == nil {
		return nil, sql.ErrConnDone
	}

	place, err := normalizePlace(in)
	if err != nil {
		return nil, err
	}

	now := time.Now().Unix()

	res, err := s.db.ExecContext(
		ctx,
		`
		INSERT INTO places (name, building, address, lat, lon, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`,
		place.Name,
		place.Building,
		place.Address,
		coordinateArg(place.Lat),
		coordinateArg(place.Lon),
		createdBy,
		now,
		now,
	)
	if err != nil {
		if isUniqueConstraintError(err) {
			return nil, ErrPlaceDuplicateKey
		}
		return nil, err
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}

	return &Place{
		ID:        id,
		Name:      place.Name,
		Building:  place.Building,
		Address:   place.Address,
		Lat:       place.Lat,
		Lon:       place.Lon,
		GeoState:  geoStateOf(place.Lat),
		CreatedBy: &createdBy,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// Update правит место по id целиком и возвращает сохранённую строку.
//
// Семантика — полная замена: PUT приносит весь объект, поэтому пустой building
// означает «корпуса нет», а nil координаты — «координат нет». Двусмысленность
// «не прислали = не менять» здесь запрещена: она бы молча оставляла в базе
// старую точку после того, как человек убрал её из формы, и справочник
// показывал бы корпус там, где его уже нет.
//
// Автор создания (created_by) не перезаписывается: править место может другой
// человек, и журнал обязан продолжать видеть, кто его завёл.
func (s *PlaceStore) Update(ctx context.Context, id int64, in PlaceInput) (*Place, error) {
	if s == nil || s.db == nil {
		return nil, sql.ErrConnDone
	}

	place, err := normalizePlace(in)
	if err != nil {
		return nil, err
	}

	now := time.Now().Unix()

	res, err := s.db.ExecContext(
		ctx,
		`
		UPDATE places
		SET name = ?, building = ?, address = ?, lat = ?, lon = ?, updated_at = ?
		WHERE id = ?
		`,
		place.Name,
		place.Building,
		place.Address,
		coordinateArg(place.Lat),
		coordinateArg(place.Lon),
		now,
		id,
	)
	if err != nil {
		if isUniqueConstraintError(err) {
			return nil, ErrPlaceDuplicateKey
		}
		return nil, err
	}

	if affected, err := res.RowsAffected(); err != nil {
		return nil, err
	} else if affected == 0 {
		return nil, ErrNotFound
	}

	return s.Get(ctx, id)
}

// Get читает одно место.
func (s *PlaceStore) Get(ctx context.Context, id int64) (*Place, error) {
	if s == nil || s.db == nil {
		return nil, sql.ErrConnDone
	}

	row := s.db.QueryRowContext(ctx, placeSelect+` WHERE p.id = ?`, id)

	place, err := scanPlace(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	return place, nil
}

// List читает справочник по названию.
//
// Порядок — по названию, а не по id: список показывают человеку, и «Корпус 1,
// Корпус 2» должны идти рядом, тогда как порядок вставки зависит от того, с
// какого корпуса начали заполнять.
//
// Сортировка сделана в Go, а не в SQL, и это не причуда: `COLLATE NOCASE`
// в SQLite складывает в нижний регистр только ASCII. Проверено на живой базе —
// «Корпус 3» оказался раньше «актовый зал», потому что заглавная кириллическая
// «К» (U+041A) идёт раньше строчной «а» (U+0430). Для продукта, где все
// названия по-русски, это значит «список отсортирован как попало», а с
// переходом на PostgreSQL (T2.1) ORDER BY вообще уехал бы в другую локаль.
// strings.ToLower складывает кириллицу, и результат не зависит от того, какая
// база под хранилищем.
//
// onlyResolved — режим «только те, что встанут на карту»: путь карты не хочет
// получать места без координат, а справочная форма хочет оба вида, поэтому
// фильтр один, а не два запроса.
func (s *PlaceStore) List(ctx context.Context, onlyResolved bool) ([]Place, error) {
	if s == nil || s.db == nil {
		return nil, sql.ErrConnDone
	}

	query := placeSelect

	if onlyResolved {
		query += ` WHERE p.lat IS NOT NULL AND p.lon IS NOT NULL`
	}

	query += ` ORDER BY p.id ASC`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	places := make([]Place, 0, 8)

	for rows.Next() {
		place, err := scanPlace(rows)
		if err != nil {
			return nil, err
		}
		places = append(places, *place)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Стабильная сортировка по регистронезависимому названию: при равных
	// ключах («Корпус» и «корпус») остаётся порядок id, который дал запрос.
	sort.SliceStable(places, func(i, j int) bool {
		left, right := strings.ToLower(places[i].Name), strings.ToLower(places[j].Name)
		if left != right {
			return left < right
		}

		return places[i].Name < places[j].Name
	})

	return places, nil
}

// ByBuilding читает место по номеру корпуса — точкой сопоставления строк
// расписания с геометрией.
//
// sql.ErrNoRows здесь не ошибка, а «места нет»: вызывающий кладёт пару в
// unresolved, и именно так карта отличается от списка с выдуманными домами.
//
// Ключ длиннее границы тоже даёт ErrNotFound, а не обрезку: в справочнике
// корпуса такой длины заведомо нет (Create её отвергает), и молча искать
// «к. 12345678901234567» по первым шестнадцати символам означало бы иногда
// находить чужой корпус.
func (s *PlaceStore) ByBuilding(ctx context.Context, building string) (*Place, error) {
	if s == nil || s.db == nil {
		return nil, sql.ErrConnDone
	}

	key := strings.TrimSpace(building)
	if key == "" || placeOverruns(key, maxPlaceBuildingLen) {
		return nil, ErrNotFound
	}

	row := s.db.QueryRowContext(ctx, placeSelect+` WHERE p.building = ?`, key)

	place, err := scanPlace(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	return place, nil
}

// placeSelect — единый текст чтения места. Нужен одной строкой, чтобы Get,
// List и ByBuilding не могли разойтись в наборе колонок: расхождение тут
// ловится только в рантайме, а скан в одном месте.
const placeSelect = `
	SELECT p.id, p.name, p.building, p.address, p.lat, p.lon,
	       p.created_by, p.created_at, p.updated_at
	FROM places p
`

// placeScanner — общий интерфейс для строки QueryRow и набора строк.
type placeScanner interface {
	Scan(dest ...any) error
}

func scanPlace(row placeScanner) (*Place, error) {
	var (
		place     Place
		lat       sql.NullFloat64
		lon       sql.NullFloat64
		createdBy sql.NullInt64
	)

	if err := row.Scan(
		&place.ID,
		&place.Name,
		&place.Building,
		&place.Address,
		&lat,
		&lon,
		&createdBy,
		&place.CreatedAt,
		&place.UpdatedAt,
	); err != nil {
		return nil, err
	}

	if lat.Valid && lon.Valid {
		place.Lat = &lat.Float64
		place.Lon = &lon.Float64
	}
	if createdBy.Valid {
		author := createdBy.Int64
		place.CreatedBy = &author
	}

	// geo_state считается, а не хранится: колонка «состояние геометрии»
	// расходится с самими координатами при любой правке, а спросить её можно
	// только в одном месте — здесь.
	place.GeoState = geoStateOf(place.Lat)

	return &place, nil
}

// geoStateOf — состояние точки по её наличию.
func geoStateOf(lat *float64) string {
	if lat == nil {
		return GeoUnresolved
	}
	return GeoResolved
}

// coordinateArg переводит *float64 в параметр: nil обязан стать NULL, иначе
// «координат нет» поедет в базу как «координата на нуле».
func coordinateArg(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}
