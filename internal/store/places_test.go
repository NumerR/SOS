package store_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/NumerR/SOS/internal/database"
	"github.com/NumerR/SOS/internal/store"
)

// Тесты справочника мест на уровне хранилища.
//
// Главный инвариант файла — «нет выдуманных координат» (бриф, п.3): место без
// геометрии обязано оставаться местом без геометрии, а не точкой в нулевых
// координатах. Поэтому большинство проверок смотрит не на текст ошибок, а на
// то, что реально легло в колонки lat/lon и что из них читается.

func newPlacesDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("открыть базу: %v", err)
	}
	if err := database.Migrate(db); err != nil {
		t.Fatalf("базовая схема: %v", err)
	}
	// Таблицу создаёт модуль places, но модуль импортирует handlers; здесь
	// достаточно того же текста миграции.
	if _, err := db.Exec(store.PlacesMigration); err != nil {
		t.Fatalf("таблица places: %v", err)
	}

	t.Cleanup(func() { _ = db.Close() })

	return db
}

func placeUser(t *testing.T, db *sql.DB, username string) int64 {
	t.Helper()

	result, err := db.ExecContext(context.Background(),
		`INSERT INTO users (username, email, full_name, password_hash, role) VALUES (?, ?, '', 'x', 'teacher')`,
		username, username+"@example.test",
	)
	if err != nil {
		t.Fatalf("создать пользователя %s: %v", username, err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("id пользователя: %v", err)
	}

	return id
}

func coord(value float64) *float64 { return &value }

func createPlace(t *testing.T, places *store.PlaceStore, in store.PlaceInput, author int64) *store.Place {
	t.Helper()

	place, err := places.Create(context.Background(), in, author)
	if err != nil {
		t.Fatalf("создать место %q (корпус %q): %v", in.Name, in.Building, err)
	}

	return place
}

func TestPlaceCreateKeepsCoordinates(t *testing.T) {
	db := newPlacesDB(t)
	places := store.NewPlaceStore(db)
	author := placeUser(t, db, "author")

	place := createPlace(t, places, store.PlaceInput{
		Name:     "Главный корпус",
		Building: "1",
		Address:  "пл. Ленина 3",
		Lat:      coord(52.123456),
		Lon:      coord(104.287654),
	}, author)

	if place.GeoState != store.GeoResolved {
		t.Errorf("geo_state = %q, ожидалось %q", place.GeoState, store.GeoResolved)
	}
	if place.CreatedBy == nil || *place.CreatedBy != author {
		t.Errorf("created_by = %v, ожидалось %d", place.CreatedBy, author)
	}

	read, err := places.Get(context.Background(), place.ID)
	if err != nil {
		t.Fatalf("прочитать место: %v", err)
	}

	if read.Lat == nil || *read.Lat != 52.123456 {
		t.Errorf("широта после чтения = %v, ожидалось 52.123456", read.Lat)
	}
	if read.Lon == nil || *read.Lon != 104.287654 {
		t.Errorf("долгота после чтения = %v, ожидалось 104.287654", read.Lon)
	}
	if read.Building != "1" || read.Name != "Главный корпус" {
		t.Errorf("строка читается иначе: %+v", read)
	}
}

// Точка 0,0 существует (Гвинейский залив) и выглядит правдоподобно. Если
// «координат нет» где-то станет нулём, карта покажет корпус там, где его
// никогда не было, и отличить это на глаз невозможно.
func TestPlaceWithoutCoordinatesStaysUnresolved(t *testing.T) {
	db := newPlacesDB(t)
	places := store.NewPlaceStore(db)
	author := placeUser(t, db, "author")

	place := createPlace(t, places, store.PlaceInput{Name: "Площадка перед входом"}, author)

	if place.GeoState != store.GeoUnresolved {
		t.Errorf("geo_state = %q, ожидалось %q", place.GeoState, store.GeoUnresolved)
	}
	if place.Lat != nil || place.Lon != nil {
		t.Errorf("координаты заполнились: lat=%v lon=%v", place.Lat, place.Lon)
	}

	var latNull, lonNull bool
	if err := db.QueryRowContext(context.Background(),
		`SELECT lat IS NULL, lon IS NULL FROM places WHERE id = ?`, place.ID,
	).Scan(&latNull, &lonNull); err != nil {
		t.Fatalf("прочитать колонки: %v", err)
	}

	if !latNull || !lonNull {
		t.Errorf("в базе lat/lon не NULL (latNull=%v lonNull=%v) — «нет координат» подменилось значением",
			latNull, lonNull)
	}
}

// Половина координаты бесполезна и опаснее отсутствия: строка с широтой без
// долготы выглядит как «место почти известно».
func TestPlaceCoordinatesMustComeAsPair(t *testing.T) {
	db := newPlacesDB(t)
	places := store.NewPlaceStore(db)
	author := placeUser(t, db, "author")

	cases := []struct {
		name string
		in   store.PlaceInput
	}{
		{"только широта", store.PlaceInput{Name: "Корпус", Lat: coord(52.1)}},
		{"только долгота", store.PlaceInput{Name: "Корпус", Lon: coord(104.2)}},
	}

	for _, tc := range cases {
		if _, err := places.Create(context.Background(), tc.in, author); !errors.Is(err, store.ErrPlaceLatLngPair) {
			t.Errorf("%s: ошибка %v, ожидалось ErrPlaceLatLngPair", tc.name, err)
		}
	}

	// Нулевая пара — не «координат нет», а точка в 0,0: её стор обязан принять
	// как настоящие координаты, иначе проверка пары стала бы проверкой значения.
	zero := store.PlaceInput{Name: "Нулевая пара", Lat: coord(0), Lon: coord(0)}
	place, err := places.Create(context.Background(), zero, author)
	if err != nil {
		t.Fatalf("нулевые координаты отвергнуты: %v", err)
	}
	if place.GeoState != store.GeoResolved {
		t.Errorf("нулевая пара дала geo_state=%q, ожидалось resolved", place.GeoState)
	}
}

func TestPlaceCoordinateRanges(t *testing.T) {
	db := newPlacesDB(t)
	places := store.NewPlaceStore(db)
	author := placeUser(t, db, "author")

	cases := []struct {
		lat, lon float64
		want     error
	}{
		{90.0001, 104, store.ErrPlaceLatRange},
		{-90.0001, 104, store.ErrPlaceLatRange},
		{52, 180.0001, store.ErrPlaceLonRange},
		{52, -180.0001, store.ErrPlaceLonRange},
		{90, 180, nil},
		{-90, -180, nil},
	}

	for _, tc := range cases {
		_, err := places.Create(context.Background(), store.PlaceInput{
			Name: "Проверка диапазона",
			Lat:  coord(tc.lat),
			Lon:  coord(tc.lon),
		}, author)

		if tc.want == nil && err != nil {
			t.Errorf("(%v, %v): предельная пара отвергнута: %v", tc.lat, tc.lon, err)
		}
		if tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("(%v, %v): ошибка %v, ожидалось %v", tc.lat, tc.lon, err, tc.want)
		}
	}
}

func TestPlaceNameValidation(t *testing.T) {
	db := newPlacesDB(t)
	places := store.NewPlaceStore(db)
	author := placeUser(t, db, "author")

	// Пустое и «только пробелы» — один и тот же отказ: форма, отправленная
	// с пробелом в поле, не должна создать место без названия.
	for _, name := range []string{"", "   ", "\t\n"} {
		if _, err := places.Create(context.Background(), store.PlaceInput{Name: name}, author); !errors.Is(err, store.ErrPlaceNameEmpty) {
			t.Errorf("название %q: ошибка %v, ожидалось ErrPlaceNameEmpty", name, err)
		}
	}

	long := strings.Repeat("а", 121)
	if _, err := places.Create(context.Background(), store.PlaceInput{Name: long}, author); !errors.Is(err, store.ErrPlaceNameTooLong) {
		t.Errorf("название из 121 символа: ошибка %v, ожидалось ErrPlaceNameTooLong", err)
	}

	// Обрезка не молча подрезает: 120 символов проходят целиком.
	edge := strings.Repeat("а", 120)
	place := createPlace(t, places, store.PlaceInput{Name: edge}, author)
	if place.Name != edge {
		t.Errorf("название из 120 символов сохранено как %d символов", utf8.RuneCountInString(place.Name))
	}
}

func TestPlaceFieldLimits(t *testing.T) {
	db := newPlacesDB(t)
	places := store.NewPlaceStore(db)
	author := placeUser(t, db, "author")

	if _, err := places.Create(context.Background(), store.PlaceInput{
		Name: "Корпус", Building: strings.Repeat("9", 17),
	}, author); !errors.Is(err, store.ErrPlaceBuildingLong) {
		t.Errorf("корпус из 17 символов: ошибка %v, ожидалось ErrPlaceBuildingLong", err)
	}

	if _, err := places.Create(context.Background(), store.PlaceInput{
		Name: "Корпус", Address: strings.Repeat("у", 201),
	}, author); !errors.Is(err, store.ErrPlaceAddressTooLong) {
		t.Errorf("адрес из 201 символа: ошибка %v, ожидалось ErrPlaceAddressTooLong", err)
	}
}

// Корпус — ключ сопоставления с расписанием, и два места с одним корпусом
// дали бы паре две точки одновременно.
func TestPlaceBuildingIsUnique(t *testing.T) {
	db := newPlacesDB(t)
	places := store.NewPlaceStore(db)
	author := placeUser(t, db, "author")

	createPlace(t, places, store.PlaceInput{Name: "Корпус 2", Building: "2"}, author)

	_, err := places.Create(context.Background(), store.PlaceInput{
		Name: "Дубль корпуса 2", Building: "2",
	}, author)
	if !errors.Is(err, store.ErrPlaceDuplicateKey) {
		t.Errorf("повторный корпус: ошибка %v, ожидалось ErrPlaceDuplicateKey", err)
	}
}

// Пустой корпус — не значение, а «корпуса нет»: площадка перед входом,
// стадион. Обычный UNIQUE заставил бы для них выдумывать метку, поэтому
// индекс частичный (WHERE building <> ”).
func TestPlaceWithoutBuildingMayRepeat(t *testing.T) {
	db := newPlacesDB(t)
	places := store.NewPlaceStore(db)
	author := placeUser(t, db, "author")

	for _, name := range []string{"Площадка", "Стадион", "Общежитие у парка"} {
		createPlace(t, places, store.PlaceInput{Name: name}, author)
	}

	all, err := places.List(context.Background(), false)
	if err != nil {
		t.Fatalf("прочитать справочник: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("мест без корпуса %d, ожидалось 3", len(all))
	}
}

func TestPlaceListOrderAndResolvedFilter(t *testing.T) {
	db := newPlacesDB(t)
	places := store.NewPlaceStore(db)
	author := placeUser(t, db, "author")

	// Заведомо не по алфавиту и со смешанным регистром: порядок в списке —
	// для человека, а не тот, с какого корпуса начали заполнять.
	createPlace(t, places, store.PlaceInput{Name: "Корпус 3", Building: "3"}, author)
	createPlace(t, places, store.PlaceInput{Name: "актовый зал", Building: "5", Lat: coord(52), Lon: coord(104)}, author)
	createPlace(t, places, store.PlaceInput{Name: "библиотека", Building: "4", Lat: coord(52), Lon: coord(104)}, author)

	all, err := places.List(context.Background(), false)
	if err != nil {
		t.Fatalf("прочитать справочник: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("мест %d, ожидалось 3", len(all))
	}

	got := []string{all[0].Name, all[1].Name, all[2].Name}
	want := []string{"актовый зал", "библиотека", "Корпус 3"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("порядок [%d] = %q, ожидалось %q (весь список %q)", i, got[i], want[i], got)
		}
	}

	resolved, err := places.List(context.Background(), true)
	if err != nil {
		t.Fatalf("прочитать разрознённые места: %v", err)
	}
	if len(resolved) != 2 {
		t.Fatalf("разрознённых %d, ожидалось 2", len(resolved))
	}
	for _, place := range resolved {
		if place.Lat == nil || place.Lon == nil {
			t.Errorf("в выборку resolved попало место без координат: %+v", place)
		}
	}
}

// Полная замена: пустые координаты в Update означают «координат нет».
// Двусмысленность «не прислали = не менять» оставила бы на карте точку,
// которую человек убрал из формы.
func TestPlaceUpdateReplacesGeometry(t *testing.T) {
	db := newPlacesDB(t)
	places := store.NewPlaceStore(db)
	author := placeUser(t, db, "author")

	place := createPlace(t, places, store.PlaceInput{
		Name: "Корпус 1", Building: "1", Lat: coord(52.1), Lon: coord(104.2),
	}, author)

	updated, err := places.Update(context.Background(), place.ID, store.PlaceInput{
		Name: "Учебный корпус 1", Building: "1",
	})
	if err != nil {
		t.Fatalf("обновить место: %v", err)
	}

	if updated.GeoState != store.GeoUnresolved || updated.Lat != nil {
		t.Errorf("координаты не сбросились: %+v", updated)
	}
	if updated.Name != "Учебный корпус 1" {
		t.Errorf("название = %q, ожидалось обновлённое", updated.Name)
	}
	if updated.CreatedBy == nil || *updated.CreatedBy != author {
		t.Errorf("автор создания потерялся: %v", updated.CreatedBy)
	}
	// Сравнение created_at/updated_at здесь было бы flaky: обе метки — целые
	// секунды, и тест успевает убраться в одну.
	if updated.UpdatedAt == 0 {
		t.Errorf("updated_at не заполнен: %+v", updated)
	}
}

func TestPlaceUpdateRejectsBadInput(t *testing.T) {
	db := newPlacesDB(t)
	places := store.NewPlaceStore(db)
	author := placeUser(t, db, "author")

	place := createPlace(t, places, store.PlaceInput{
		Name: "Корпус 6", Building: "6", Lat: coord(52), Lon: coord(104),
	}, author)

	// Занятый корпус чужого места.
	other := createPlace(t, places, store.PlaceInput{Name: "Корпус 7", Building: "7"}, author)
	if _, err := places.Update(context.Background(), other.ID, store.PlaceInput{
		Name: "Корпус 7", Building: "6",
	}); !errors.Is(err, store.ErrPlaceDuplicateKey) {
		t.Errorf("перехват корпуса: ошибка %v, ожидалось ErrPlaceDuplicateKey", err)
	}

	// Битая координата не должна оставить место испорченным.
	if _, err := places.Update(context.Background(), place.ID, store.PlaceInput{
		Name: "Корпус 6", Lat: coord(91), Lon: coord(104),
	}); !errors.Is(err, store.ErrPlaceLatRange) {
		t.Errorf("широта 91: ошибка %v, ожидалось ErrPlaceLatRange", err)
	}

	read, err := places.Get(context.Background(), place.ID)
	if err != nil {
		t.Fatalf("прочитать место после отказа: %v", err)
	}
	if read.Lat == nil || *read.Lat != 52 {
		t.Errorf("отказанная правка изменила широту: %v", read.Lat)
	}
}

func TestPlaceUpdateMissingID(t *testing.T) {
	db := newPlacesDB(t)
	places := store.NewPlaceStore(db)

	if _, err := places.Update(context.Background(), 4242, store.PlaceInput{Name: "Нет такого"}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("правка несуществующего места: ошибка %v, ожидалось ErrNotFound", err)
	}
}

func TestPlaceByBuilding(t *testing.T) {
	db := newPlacesDB(t)
	places := store.NewPlaceStore(db)
	author := placeUser(t, db, "author")

	createPlace(t, places, store.PlaceInput{Name: "Корпус 1", Building: "1", Lat: coord(52), Lon: coord(104)}, author)

	found, err := places.ByBuilding(context.Background(), " 1 ")
	if err != nil {
		t.Fatalf("место по корпусу «1»: %v", err)
	}
	if found.Name != "Корпус 1" {
		t.Errorf("найдено %q, ожидалось «Корпус 1»", found.Name)
	}

	// «01» — другой ключ: сопоставление посимвольное, и номер с ведущим нулём
	// обязан остаться неразрешённым, а не притвориться корпусом 1.
	for _, key := range []string{"01", "2", "", "  ", strings.Repeat("9", 17)} {
		if _, err := places.ByBuilding(context.Background(), key); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("корпус %q: ошибка %v, ожидалось ErrNotFound", key, err)
		}
	}
}

// Место переживает удаление автора: справочник общий, и вместе с учёткой
// преподавателя не должны исчезать корпуса, на которые смотрит расписание.
func TestPlaceSurvivesUserDeletion(t *testing.T) {
	db := newPlacesDB(t)
	places := store.NewPlaceStore(db)
	author := placeUser(t, db, "leaves")

	place := createPlace(t, places, store.PlaceInput{Name: "Корпус 8", Building: "8"}, author)

	if _, err := db.Exec(`DELETE FROM users WHERE id = ?`, author); err != nil {
		t.Fatalf("удалить автора: %v", err)
	}

	read, err := places.Get(context.Background(), place.ID)
	if err != nil {
		t.Fatalf("место пропало вместе с автором: %v", err)
	}
	if read.CreatedBy != nil {
		t.Errorf("created_by = %v, ожидалось NULL после удаления автора", *read.CreatedBy)
	}
}

// Пустой выборка — не nil: JSON-клиент обязан получить [], а не null, иначе
// карта упадёт на первой же пустой базе.
func TestPlaceListEmptyIsNotNil(t *testing.T) {
	db := newPlacesDB(t)
	places := store.NewPlaceStore(db)

	all, err := places.List(context.Background(), false)
	if err != nil {
		t.Fatalf("прочитать пустой справочник: %v", err)
	}
	if all == nil {
		t.Errorf("пустой список вернулся nil — в JSON это null вместо []")
	}
	if len(all) != 0 {
		t.Errorf("мест %d, ожидалось 0", len(all))
	}
}
