package apeks

import (
	"strings"
	"testing"
)

// Тесты выделения корпуса из строки аудитории.
//
// Случаи взяты из импортированных данных, а не из сочинённого списка: «к. 1/с/з»
// с чертой внутри номера аудитории и «к. 1/710, к. 1/711» с двумя аудиториями
// появились бы в тесте только если подсмотреть их в реальной строке, — именно
// они и ломают наивный разбор «от «к.» до первой «/»».

func TestBuildingsFromRoom(t *testing.T) {
	cases := []struct {
		room     string
		expected []string
	}{
		{"к. 3/306", []string{"3"}},
		{"к. 1/с/з", []string{"1"}},
		{"к. 1/710, к. 1/711", []string{"1"}},
		{"к. 1/311, к. 3/213", []string{"1", "3"}},
		{"к.3/401", []string{"3"}},
		{"К. 2/102", []string{"2"}},
		{"без ауд.", nil},
		{"", nil},
		{"201", nil},
		{"к.", nil},
		{"к. /306", nil},
		{"актовый зал", nil},
		// Тег из источника доходит до стора уже вырезанным (stripHTML), но
		// правило обязано держаться и на сырой строке: иначе разбор формата
		// оказался бы зависим от того, чистили текст или нет.
		{`<i class="fa fa-times"></i> без ауд.`, nil},
	}

	for _, tc := range cases {
		got := BuildingsFromRoom(tc.room)
		if len(got) != len(tc.expected) {
			t.Errorf("%q: корпуса %v, ожидалось %v", tc.room, got, tc.expected)
			continue
		}
		for i := range got {
			if got[i] != tc.expected[i] {
				t.Errorf("%q: корпус %d = %q, ожидалось %q", tc.room, i, got[i], tc.expected[i])
			}
		}
	}
}

// TestBuildingsFromRealFixture — ни одна пара реального среза не теряет корпус
// молча.
//
// Проверяется не «правильный номер», а связь: если в строке есть «к.», корпус
// обязан быть найден; если его нет — найден быть не может. Так тест ловит и
// сдвинувшийся формат источника, и слишком жадное правило, которое лепит номер
// там, где его нет.
func TestBuildingsFromRealFixture(t *testing.T) {
	month := parseFixture(t)
	if len(month.Lessons) == 0 {
		t.Fatalf("в фикстуре нет пар — проверять нечего")
	}

	var withBuilding, withoutBuilding int

	for _, lesson := range month.Lessons {
		buildings := BuildingsFromRoom(lesson.Room)
		marks := strings.Contains(strings.ToLower(lesson.Room), "к.")

		switch {
		case len(buildings) == 0:
			withoutBuilding++
			if marks {
				t.Errorf("корпус не прочитан из %q", lesson.Room)
			}
		case len(buildings) == 1:
			withBuilding++
			if !marks {
				t.Errorf("корпус %q выдуман из строки без «к.»: %q", buildings[0], lesson.Room)
			}
		default:
			// Несколько корпусов в одной паре — не ошибка разбора, а факт
			// источника, и карта обязана показать такую пару неотмеченной, а не
			// «выбрать первый».
			t.Errorf("пара %q встала в %d корпусов сразу: %v (%q)",
				lesson.Discipline, len(buildings), buildings, lesson.Room)
		}
	}

	if withBuilding == 0 {
		t.Errorf("ни одна пара реального среза не встала бы на карту; без корпуса %d пар", withoutBuilding)
	}
}
