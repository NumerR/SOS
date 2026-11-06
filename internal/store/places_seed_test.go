package store_test

import (
	"context"
	"math"
	"testing"

	"github.com/NumerR/SOS/internal/store"
)

// Сид пяти корпусов (ADR-0045). Координаты здесь не проверяются на
// «правдоподобие» — они взяты из слов владельца, и проверка ровно на то, что
// запись не исказилась при проходе через SQL: те же знаки после запятой, тот же
// номер корпуса, то же имя.
//
// Вторая линия проверок — идемпотентность и неприкосновенность. Миграция
// применяется к живой базе, где справочник уже мог быть наполнен руками, и
// сид не имеет права ни удвоить строку, ни поправить чужую координату «на всякий
// случай»: второе — это ровно те выдуманные данные, которые запрещает бриф.

type seedPlace struct {
	building string
	name     string
	lat      float64
	lon      float64
}

func campusSeed() []seedPlace {
	return []seedPlace{
		{"1", "Главный корпус", 54.509643, 36.274133},
		{"2", "Новый корпус", 54.509152, 36.273756},
		{"3", "Биофак", 54.509073, 36.272633},
		{"4", "Воскресенка", 54.507343, 36.260344},
		{"5", "Ленина", 54.51157013381475, 36.262126522868904},
	}
}

func TestCampusSeedAddsFiveResolvedBuildings(t *testing.T) {
	db := newPlacesDB(t)
	ctx := context.Background()

	if _, err := db.Exec(store.CampusBuildingsSeed); err != nil {
		t.Fatalf("сид корпусов: %v", err)
	}

	places, err := store.NewPlaceStore(db).List(ctx, false)
	if err != nil {
		t.Fatalf("список мест: %v", err)
	}
	if len(places) != len(campusSeed()) {
		t.Fatalf("мест %d, ожидалось %d", len(places), len(campusSeed()))
	}

	byBuilding := make(map[string]store.Place, len(places))
	for _, place := range places {
		byBuilding[place.Building] = place
	}

	for _, want := range campusSeed() {
		got, found := byBuilding[want.building]
		if !found {
			t.Fatalf("корпуса %s в справочнике нет", want.building)
		}
		if got.Name != want.name {
			t.Errorf("корпус %s назван %q, ожидалось %q", want.building, got.Name, want.name)
		}
		// Состояние читается из колонок: без него сид дал бы «место есть,
		// точки нет», и карта по-прежнему не рисовала бы ни одного корпуса.
		if got.GeoState != store.GeoResolved {
			t.Errorf("корпус %s в состоянии %q, ожидалось resolved", want.building, got.GeoState)
		}
		if got.Lat == nil || got.Lon == nil {
			t.Fatalf("у корпуса %s не читается координата", want.building)
		}
		if math.Abs(*got.Lat-want.lat) > 1e-9 || math.Abs(*got.Lon-want.lon) > 1e-9 {
			t.Errorf("корпус %s встал в %f,%f, ожидалось %f,%f",
				want.building, *got.Lat, *got.Lon, want.lat, want.lon)
		}
		if got.Address != "" {
			t.Errorf("корпус %s получил адрес %q, которого владелец не называл",
				want.building, got.Address)
		}
	}
}

func TestCampusSeedAppliedTwiceAddsNothing(t *testing.T) {
	db := newPlacesDB(t)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if _, err := db.Exec(store.CampusBuildingsSeed); err != nil {
			t.Fatalf("сид корпусов, заход %d: %v", i+1, err)
		}
	}

	places, err := store.NewPlaceStore(db).List(ctx, false)
	if err != nil {
		t.Fatalf("список мест: %v", err)
	}
	if len(places) != len(campusSeed()) {
		t.Fatalf("после второго наложения мест %d, ожидалось %d", len(places), len(campusSeed()))
	}
}

func TestCampusSeedKeepsHandEnteredPlace(t *testing.T) {
	db := newPlacesDB(t)
	ctx := context.Background()
	repo := store.NewPlaceStore(db)

	// Человек завёл корпус 2 раньше и знал о нём больше, чем сид: своё имя и
	// свою координату. Сид обязан пройти мимо этой строки.
	lat, lon := 54.5, 36.27
	created, err := repo.Create(ctx, store.PlaceInput{Name: "Корпус 2, как его заводил человек",
		Building: "2", Lat: &lat, Lon: &lon}, placeUser(t, db, "place-author"))
	if err != nil {
		t.Fatalf("создать место руками: %v", err)
	}

	if _, err := db.Exec(store.CampusBuildingsSeed); err != nil {
		t.Fatalf("сид корпусов: %v", err)
	}

	after, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("прочитать место: %v", err)
	}
	if after.Name != created.Name {
		t.Errorf("сид переименовал место в %q", after.Name)
	}
	if after.Lat == nil || math.Abs(*after.Lat-lat) > 1e-12 {
		t.Errorf("сид сдвинул широту: %v", after.Lat)
	}

	places, err := repo.List(ctx, false)
	if err != nil {
		t.Fatalf("список мест: %v", err)
	}
	if len(places) != len(campusSeed()) {
		t.Fatalf("мест %d: сид либо удвоил корпус 2, либо недоложил остальные", len(places))
	}

	count := 0
	for _, place := range places {
		if place.Building == "2" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("строк с корпусом 2: %d, ожидалось 1", count)
	}
}
