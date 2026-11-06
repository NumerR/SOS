package handlers

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/NumerR/SOS/internal/store"
)

// Подпись корпуса рядом с аудиторией (ADR-0045): ответ чтения расписания
// приносит поле room_buildings — для каждой строки аудитории номер корпуса и
// его имя. Проверяется здесь то, из-за чего поле вообще имеет смысл: подпись
// получает только та аудитория, корпус которой назван в справочнике, а номер
// не выдумывается там, где источник не назвал его однозначно.
//
// Разбор строки аудитории делает тот же код, что и слой карты, поэтому
// расхождение «карта показывает корпус, а расписание нет» в этих тестах
// невозможно по построению — но наоборот оно как раз возможно, если страница
// начнёт угадывать корпус сама. Отсюда и случай «201»: у источника такие
// аудитории бывают, и корпус для них нечем называть.

func seedPlaceRow(t *testing.T, e *scheduleSourceEnv, name, building string) {
	t.Helper()

	if _, err := e.db.ExecContext(context.Background(),
		`INSERT INTO places (name, building, address, lat, lon, created_at, updated_at)
		 VALUES (?, ?, '', 54.5, 36.27, 1, 0)`, name, building); err != nil {
		t.Fatalf("место %s: %v", building, err)
	}
}

func TestScheduleLessonsAnswersRoomBuildingNames(t *testing.T) {
	e := newScheduleSourceEnv(t)

	if _, err := e.db.Exec(store.PlacesMigration); err != nil {
		t.Fatalf("таблица places: %v", err)
	}
	seedPlaceRow(t, e, "Главный корпус", "1")
	seedPlaceRow(t, e, "Биофак", "3")
	// Корпус заведён, но без имени: карта его покажет точкой, а подписывать
	// аудиторию пустой строкой — значит рисовать пустое место в тексте.
	seedPlaceRow(t, e, "", "8")
	e.seedCatalog(t, store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"})
	e.seedMonth(t, "494", 10, 2026,
		store.SourceLesson{ExternalID: "1", DayKey: 20261006, Date: "06.10.2026", Weekday: "Вт",
			LessonNumber: 1, Time: "08:30 – 10:00", Discipline: "Базы данных", Room: "к. 1/710"},
		store.SourceLesson{ExternalID: "2", DayKey: 20261006, Date: "06.10.2026", Weekday: "Вт",
			LessonNumber: 2, Time: "10:10 – 11:40", Discipline: "Матанализ", Room: "к. 3/21"},
		store.SourceLesson{ExternalID: "3", DayKey: 20261007, Date: "07.10.2026", Weekday: "Ср",
			LessonNumber: 1, Time: "08:30 – 10:00", Discipline: "Физра", Room: "201"},
		store.SourceLesson{ExternalID: "4", DayKey: 20261007, Date: "07.10.2026", Weekday: "Ср",
			LessonNumber: 2, Time: "10:10 – 11:40", Discipline: "Двойная запись", Room: "к. 1/710, к. 1/711"},
		store.SourceLesson{ExternalID: "5", DayKey: 20261007, Date: "07.10.2026", Weekday: "Ср",
			LessonNumber: 3, Time: "12:00 – 13:30", Discipline: "Без имени", Room: "к. 8/5"},
		store.SourceLesson{ExternalID: "6", DayKey: 20261007, Date: "07.10.2026", Weekday: "Ср",
			LessonNumber: 4, Time: "14:00 – 15:30", Discipline: "Два корпуса", Room: "к. 1/710, к. 3/21"},
	)

	e.h.Places = store.NewPlaceStore(e.db)

	weekStart := time.Date(2026, 10, 5, 9, 0, 0, 0, time.Local).Unix()
	weekEnd := time.Date(2026, 10, 8, 9, 0, 0, 0, time.Local).Unix()
	rec := lessonsQuery(e.h, fmt.Sprintf("/api/schedule/lessons?group=494&from=%d&to=%d", weekStart, weekEnd))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d: %s", rec.Code, rec.Body.String())
	}

	raw, ok := decode(t, rec)["room_buildings"].(map[string]any)
	if !ok {
		t.Fatalf("room_buildings не объект: %T", decode(t, rec)["room_buildings"])
	}

	type entry struct{ building, name string }
	text := func(value any) string {
		s, _ := value.(string)
		return s
	}

	got := make(map[string]entry, len(raw))
	for room, value := range raw {
		item, isObject := value.(map[string]any)
		if !isObject {
			t.Fatalf("аудитория %q подписана %T, ожидался объект с номером и именем", room, value)
		}
		got[room] = entry{building: text(item["building"]), name: text(item["name"])}
	}

	want := map[string]entry{
		"к. 1/710": {building: "1", name: "Главный корпус"},
		"к. 3/21":  {building: "3", name: "Биофак"},
		// Одна и та же строка с двумя аудиториями одного корпуса — это один
		// корпус, и подпись у неё есть: источник так записывает параллельные
		// подгруппы.
		"к. 1/710, к. 1/711": {building: "1", name: "Главный корпус"},
		// Корпус без имени: подписывать текст нечем, но номер остаётся —
		// по нему переход на карту работает и для безымянной строки
		// справочника.
		"к. 8/5": {building: "8"},
	}
	for room, expected := range want {
		if got[room] != expected {
			t.Errorf("аудитория %q подписана %+v, ожидалось %+v", room, got[room], expected)
		}
	}

	// Две причины остаться без записи вовсе — и ни одна из них не права
	// страницы.
	for _, room := range []string{"201", "к. 1/710, к. 3/21"} {
		if _, found := got[room]; found {
			t.Errorf("аудитория %q получила %+v там, где корпус не назван однозначно",
				room, got[room])
		}
	}
	if len(got) != len(want) {
		t.Errorf("подписанных аудиторий %d, ожидалось %d: %v", len(got), len(want), got)
	}
}

func TestScheduleLessonsWithoutPlacesAnswersEmptyNames(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t, store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"})
	e.seedMonth(t, "494", 10, 2026,
		store.SourceLesson{ExternalID: "1", DayKey: 20261006, Date: "06.10.2026", Weekday: "Вт",
			LessonNumber: 1, Time: "08:30 – 10:00", Discipline: "Базы данных", Room: "к. 1/710"},
	)

	// e.h.Places не выставлен: сборка без справочника мест обязана отдавать
	// расписание без подписей, а не 500 и не null, который страница
	// различила бы как «сломано».
	weekStart := time.Date(2026, 10, 5, 9, 0, 0, 0, time.Local).Unix()
	weekEnd := time.Date(2026, 10, 8, 9, 0, 0, 0, time.Local).Unix()
	rec := lessonsQuery(e.h, fmt.Sprintf("/api/schedule/lessons?group=494&from=%d&to=%d", weekStart, weekEnd))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d: %s", rec.Code, rec.Body.String())
	}

	raw, ok := decode(t, rec)["room_buildings"].(map[string]any)
	if !ok {
		t.Fatalf("room_buildings отсутствует или не объект: %v", decode(t, rec)["room_buildings"])
	}
	if len(raw) != 0 {
		t.Errorf("без справочника подписей %d: %v", len(raw), raw)
	}

	days, _ := decode(t, rec)["days"].([]any)
	if len(days) != 1 {
		t.Fatalf("дней в ответе %d — расписание пострадало из-за подписи", len(days))
	}
}
