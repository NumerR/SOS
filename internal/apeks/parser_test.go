package apeks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Тесты разбора ответа источника.
//
// Два слоя проверок: синтетический образец из Python-оригинала (test_parser.py)
// закрепляет правило сортировки пар по фактическому времени, а не по id;
// реальный срез ответа (testdata/apeks-2026-10-week.json, снят 2026-10-24 с
// живого эндпоинта) закрепляет всё остальное — подгруппы, «Поточку», подписи
// дней, чистку аудитории от HTML.
//
// Каталог групп проверяется по своему срезу — testdata/apeks-groups-tree.json
// (тот же день, те же 24.10.2026): он нужен ради формы «пустой уровень пришёл
// списком», на которой разбор каталога и падал.

func TestParseMonthSortsByRealStartTime(t *testing.T) {
	// Оракл перенесён из test_parser.py: там id слотов идут в обратном к
	// времени порядке, и правильный парсер обязан отсортировать пары по
	// hour_from, а не по id.
	payload := map[string]any{
		"lessonTimes": []map[string]any{
			{"id": 1, "hour_from": 11, "minute_from": 20, "hour_to": 12, "minute_to": 50, "fullName": "11:20 – 12:50"},
			{"id": 2, "hour_from": 8, "minute_from": 30, "hour_to": 10, "minute_to": 0, "fullName": "08:30 – 10:00"},
		},
		"lessons": []map[string]any{
			{"id": 101, "date": "07.09.2026", "lesson_time_id": 1, "groupName": "Б-ИСиТ-22", "discipline": "Поздняя", "class_type_name": "Лек", "classroom": "201", "staffNames": []any{"Иванов И.И."}},
			{"id": 102, "date": "07.09.2026", "lesson_time_id": 2, "groupName": "Б-ИСиТ-22", "discipline": "Ранняя", "class_type_name": "Пр", "classroom": "101", "staffNames": []any{"Петров П.П."}},
		},
	}

	month := mustParse(t, payload)
	if len(month.Lessons) != 2 {
		t.Fatalf("пар разобрано %d, ожидалось 2", len(month.Lessons))
	}

	gotTimes := []string{month.Lessons[0].Time, month.Lessons[1].Time}
	if want := []string{"08:30 – 10:00", "11:20 – 12:50"}; gotTimes[0] != want[0] || gotTimes[1] != want[1] {
		t.Errorf("порядок пар по времени: получили %v, ожидалось %v", gotTimes, want)
	}
	gotDisciplines := []string{month.Lessons[0].Discipline, month.Lessons[1].Discipline}
	if want := []string{"Ранняя", "Поздняя"}; gotDisciplines[0] != want[0] || gotDisciplines[1] != want[1] {
		t.Logf("получено %q / %q", month.Lessons[0].Discipline, month.Lessons[1].Discipline)
		t.Errorf("подписи пар: получили %v, ожидалось %v", gotDisciplines, want)
	}

	// Номера пар — заодно: «ранняя» обязана быть первой по счёту тоже.
	if month.Lessons[0].LessonNumber != 1 || month.Lessons[1].LessonNumber != 2 {
		t.Errorf("номера пар: %d и %d, ожидалось 1 и 2", month.Lessons[0].LessonNumber, month.Lessons[1].LessonNumber)
	}
}

func TestParseMonthRealSample(t *testing.T) {
	month := parseFixture(t)

	if len(month.Lessons) != 22 {
		t.Fatalf("пар в реальном срезе %d, ожидалось 22", len(month.Lessons))
	}
	if month.GroupID != "494" {
		t.Errorf("идентификатор группы %q, ожидалось \"494\"", month.GroupID)
	}
	if month.GroupName != "Б-ИСиТ-22" {
		t.Errorf("имя группы %q, ожидалось Б-ИСиТ-22", month.GroupName)
	}
	if month.StartDate != "01.10.2026" || month.EndDate != "31.10.2026" {
		t.Errorf("границы месяца: %s..%s", month.StartDate, month.EndDate)
	}

	// Дата 02.10.2026 — пятница; подпись дня берём из календаря, а не из ответа.
	first := month.Lessons[0]
	if first.Date != "02.10.2026" {
		t.Fatalf("первая пара %s, ожидалось 02.10.2026 (срез отсортирован по дате)", first.Date)
	}
	if first.Weekday != "Пт" {
		t.Errorf("день недели %q, ожидалось Пт", first.Weekday)
	}
	if first.DayKey != 20261002 {
		t.Errorf("ключ дня %d, ожидалось 20261002", first.DayKey)
	}

	// Единственная лекция-«поток» в срезе: 7 групп в имени, 6 со-групп,
	// аудитория с HTML-фрагментом и семь преподавателей.
	var stream *Lesson
	for index := range month.Lessons {
		lesson := &month.Lessons[index]
		if strings.Contains(lesson.Discipline, "Элективные") {
			stream = lesson
			break
		}
	}
	if stream == nil {
		t.Fatal("в срезе не найдена потоковая пара по физкультуре")
	}
	if stream.Subgroup != "Поточка" {
		t.Errorf("подгруппа потоковой пары %q, ожидалось Поточка", stream.Subgroup)
	}
	if strings.Contains(stream.Room, "<") || strings.Contains(stream.Room, "fa-times") {
		t.Errorf("в аудитории остался HTML: %q", stream.Room)
	}
	if stream.Room != "без ауд." {
		t.Errorf("аудитория %q, ожидалось «без ауд.»", stream.Room)
	}
	if !strings.Contains(stream.Teacher, "Минаев В.В.") {
		t.Errorf("двойной пробел в ФИО не схлопнут: %q", stream.Teacher)
	}

	// Пара 2-й подгруппы: subgroup_id 633 живёт в subgroupNames, а не в имени.
	var sub *Lesson
	for index := range month.Lessons {
		lesson := &month.Lessons[index]
		if lesson.Date == "02.10.2026" && lesson.LessonNumber == 6 {
			sub = lesson
			break
		}
	}
	if sub == nil {
		t.Fatal("лабораторная 6-й пары 02.10 не найдена")
	}
	if sub.Subgroup != "Б-ИСиТ-22/2" {
		t.Errorf("подгруппа %q, ожидалось имя из subgroupNames: Б-ИСиТ-22/2", sub.Subgroup)
	}
	if sub.Type != "Лб" {
		t.Errorf("тип занятия %q, ожидалось Лб", sub.Type)
	}

	// Общее правило: ни одна пара не осталась без номера и времени.
	for _, lesson := range month.Lessons {
		if lesson.LessonNumber == 0 {
			t.Errorf("пара %s (%s) без номера: lesson_time_id не найден в lessonTimes", lesson.Date, lesson.Discipline)
		}
		if lesson.Time == "" {
			t.Errorf("пара %s без времени", lesson.Date)
		}
	}

	// Сортировка по (дата, номер): проверяем инвариант, а не конкретный вывод —
	// так тест переживает добавление новых пар в фикстуру.
	for index := 1; index < len(month.Lessons); index++ {
		prev, next := month.Lessons[index-1], month.Lessons[index]
		if prev.DayKey > next.DayKey || (prev.DayKey == next.DayKey && prev.LessonNumber > next.LessonNumber) {
			t.Fatalf("порядок нарушен: %s#%d идёт перед %s#%d", prev.Date, prev.LessonNumber, next.Date, next.LessonNumber)
		}
	}
}

func TestParseMonthSubgroupRules(t *testing.T) {
	tests := []struct {
		name   string
		lesson map[string]any
		data   map[string]any
		want   string
	}{
		{
			name:   "id есть, имя есть в subgroupNames",
			lesson: map[string]any{"subgroup_id": 632, "groupName": "Б-ИСиТ-22/1"},
			data:   map[string]any{"subgroupNames": map[string]string{"632": "Б-ИСиТ-22/1"}},
			want:   "Б-ИСиТ-22/1",
		},
		{
			name:   "id есть, имя только в groupsList",
			lesson: map[string]any{"subgroup_id": 633, "groupName": "Б-ИСиТ-22"},
			data:   map[string]any{"groupsList": map[string]map[string]string{"Подгруппы": {"subgroup-633": "Б-ИСиТ-22/2"}}},
			want:   "Б-ИСиТ-22/2",
		},
		{
			name:   "id есть, имени нет нигде -> по суффиксу /1",
			lesson: map[string]any{"subgroup_id": 700, "groupName": "Б-ИСиТ-22/1"},
			data:   map[string]any{},
			want:   "1 подгруппа",
		},
		{
			name:   "три группы в потоке",
			lesson: map[string]any{"groupName": "А-Б-1, Б-В-2", "superflowGroupsIds": []int{1, 2, 3}},
			data:   map[string]any{},
			want:   "Поточка",
		},
		{
			name:   "поток по числу имён в groupName",
			lesson: map[string]any{"groupName": "А-Б-1, Б-В-2, В-Г-3"},
			data:   map[string]any{},
			want:   "Поточка",
		},
		{
			name:   "обычная пара без подгрупп",
			lesson: map[string]any{"groupName": "Б-ИСиТ-22"},
			data:   map[string]any{},
			want:   "—",
		},
		{
			name:   "подгруппа 2 по имени",
			lesson: map[string]any{"groupName": "Б-ИСиТ-22/2"},
			data:   map[string]any{},
			want:   "2 подгруппа",
		},
		{
			name:   "subgroup_id пришёл строкой",
			lesson: map[string]any{"subgroup_id": "632", "groupName": "Б-ИСиТ-22"},
			data:   map[string]any{"subgroupNames": map[string]string{"632": "Б-ИСиТ-22/1"}},
			want:   "Б-ИСиТ-22/1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Собираем lesson целиком: базовые поля плюс то, что описано
			// в случае.
			lesson := map[string]any{"id": 1, "date": "07.09.2026", "lesson_time_id": 1}
			for key, value := range tc.lesson {
				lesson[key] = value
			}
			data := map[string]any{"lessons": []map[string]any{lesson}}
			for key, value := range tc.data {
				data[key] = value
			}

			month := mustParse(t, data)
			if len(month.Lessons) != 1 {
				t.Fatalf("разобрано %d пар, ожидалась одна", len(month.Lessons))
			}
			if got := month.Lessons[0].Subgroup; got != tc.want {
				t.Errorf("подгруппа %q, ожидалось %q", got, tc.want)
			}
		})
	}
}

// TestParseMonthAcceptsEmptyDictionaries — словари источника могут прийти
// пустым списком.
//
// Тот же quirk сериализатора, что и в дереве групп: пустой словарь у АПЭКС-ВУЗа
// это [], а не {}. Для groupNames/subgroupNames/groupsList это значило бы одно:
// месяц с импортом не справился бы, хотя пар в ответе достаточно. Словари в
// разборе читаются только по ключам, поэтому пустой список здесь — ровно «пустой
// словарь», и пара уходит на страницу с подписью по запасному правилу.
func TestParseMonthAcceptsEmptyDictionaries(t *testing.T) {
	month := mustParse(t, map[string]any{
		"lessons": []map[string]any{
			{"id": 1, "date": "07.09.2026", "lesson_time_id": 1, "subgroup_id": 632, "groupName": "Б-ИСиТ-22/1"},
		},
		"groupNames":    []any{},
		"subgroupNames": []any{},
		"groupsList":    []any{},
	})

	if len(month.Lessons) != 1 {
		t.Fatalf("пар %d, ожидалась одна", len(month.Lessons))
	}
	if got := month.Lessons[0].Subgroup; got != "1 подгруппа" {
		t.Errorf("подгруппа %q, ожидалось запасное правило по имени «/1»", got)
	}
}

func TestParseMonthSpecialDisciplines(t *testing.T) {
	// Порядок проверок важнее факта: пустая пара первичнее самоподготовки,
	// как в Python-версии.
	tests := []struct {
		name   string
		fields map[string]any
		want   string
	}{
		{"обычная", map[string]any{"discipline": "Математика"}, "Математика"},
		{"is_empty=1", map[string]any{"is_empty": 1, "discipline": "Математика"}, "Пустая пара"},
		{"self_work=1", map[string]any{"self_work": 1, "discipline": "Математика"}, "Самоподготовка"},
		{"оба флага", map[string]any{"is_empty": 1, "self_work": 1}, "Пустая пара"},
		{"is_empty=true", map[string]any{"is_empty": true}, "Пустая пара"},
		{"is_empty=0", map[string]any{"is_empty": 0, "discipline": "Математика"}, "Математика"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lesson := map[string]any{"id": 1, "date": "07.09.2026", "lesson_time_id": 1, "groupName": "Б-ИСиТ-22"}
			for key, value := range tc.fields {
				lesson[key] = value
			}

			month := mustParse(t, map[string]any{"lessons": []map[string]any{lesson}})
			if got := month.Lessons[0].Discipline; got != tc.want {
				t.Errorf("дисциплина %q, ожидалось %q", got, tc.want)
			}
		})
	}
}

func TestParseMonthIgnoresUnknownAndBrokenFields(t *testing.T) {
	month := mustParse(t, map[string]any{
		"lessonTimes": []map[string]any{
			{"id": 1, "hour_from": 9, "minute_from": 0, "hour_to": 10, "minute_to": 30, "brand_new_field": map[string]any{"x": 1}},
		},
		"lessons": []map[string]any{
			// Дата в неизвестном формате: пара не должна исчезать и не должна
			// встать в начало списка.
			{"id": 2, "date": "не дата", "lesson_time_id": 1, "discipline": "Поздняя по дате"},
			{"id": 1, "date": "07.09.2026", "lesson_time_id": 1, "discipline": "Ранняя по дате"},
			// Пара без id: ключ дедупликации отсутствует, такая не импортируется.
			{"date": "07.09.2026", "lesson_time_id": 1, "discipline": "Без идентификатора"},
			// lesson_time_id указывает в никуда: время пустое, но пара остаётся.
			{"id": 3, "date": "07.09.2026", "lesson_time_id": 99, "discipline": "Без слота"},
		},
	})

	if len(month.Lessons) != 3 {
		t.Fatalf("разобрано %d пар, ожидалось 3 (без id пропускаем)", len(month.Lessons))
	}
	// Пара без слота получает номер 0 и в тот же день идёт первой — ровно как
	// в Python-версии, где lesson_number тоже 0. Проверяем это явно, чтобы
	// поведение было зафиксировано, а не «вышло из сортировки».
	if month.Lessons[0].Discipline != "Без слота" || month.Lessons[0].LessonNumber != 0 {
		t.Errorf("первой должна идти пара без слота (номер 0), получилось %q / %d",
			month.Lessons[0].Discipline, month.Lessons[0].LessonNumber)
	}
	if month.Lessons[1].Discipline != "Ранняя по дате" {
		t.Errorf("второй должна идти пара 07.09 с номером 1, получилось %q", month.Lessons[1].Discipline)
	}
	last := month.Lessons[len(month.Lessons)-1]
	if last.DayKey != unknownDayKey || last.Weekday != "" {
		t.Errorf("нечитаемая дата: day_key %d, день %q", last.DayKey, last.Weekday)
	}

	for _, lesson := range month.Lessons {
		if lesson.Discipline == "Без идентификатора" {
			t.Error("пара без id попала в результат")
		}
		if lesson.Discipline == "Без слота" && lesson.Time != "" {
			t.Errorf("пара без слота получила время %q", lesson.Time)
		}
	}
}

func TestParseMonthEmptyMonthIsNotAnError(t *testing.T) {
	// Каникулы: lessons пустой — это данные, а не сбой.
	month := mustParse(t, map[string]any{"lessons": []any{}, "group": map[string]any{"id": 494, "name": "Б-ИСиТ-22"}})
	if len(month.Lessons) != 0 {
		t.Fatalf("пар %d, ожидалось 0", len(month.Lessons))
	}
	if month.GroupName != "Б-ИСиТ-22" {
		t.Errorf("имя группы %q", month.GroupName)
	}
}

func TestParseMonthGroupNameFallsBackToID(t *testing.T) {
	month := mustParse(t, map[string]any{"group_id": 494, "lessons": []any{}})
	if month.GroupName != "494" {
		t.Errorf("имя группы %q, ожидалось откат на id", month.GroupName)
	}
}

func TestParseMonthRejectsGarbage(t *testing.T) {
	// Не JSON-объект в data: ошибку дать надо, но текстом без содержимого ответа.
	if _, err := ParseMonth([]byte(`"<строка вместо объекта>"`)); err == nil {
		t.Fatal("мусорный ответ принят без ошибки")
	}

	payload, err := json.Marshal(map[string]any{"lessons": []any{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseMonth(payload); err != nil {
		t.Errorf("пустой месяц не должен быть ошибкой: %v", err)
	}
}

func TestWeekdayAndDayKey(t *testing.T) {
	tests := []struct {
		date    string
		wantKey int
		wantDay string
	}{
		{"02.10.2026", 20261002, "Пт"},
		{"05.10.2026", 20261005, "Пн"},
		{"08.11.2026", 20261108, "Вс"},
		{"31.02.2026", unknownDayKey, ""}, // такого дня нет
		{"", unknownDayKey, ""},
		{"2.10.2026", 20261002, "Пт"}, // без ведущего нуля источник тоже пишет
	}

	for _, tc := range tests {
		if got := dayKey(tc.date); got != tc.wantKey {
			t.Errorf("day_key(%q) = %d, ожидалось %d", tc.date, got, tc.wantKey)
		}
		if got := weekday(tc.date); got != tc.wantDay {
			t.Errorf("weekday(%q) = %q, ожидалось %q", tc.date, got, tc.wantDay)
		}
	}
}

func TestNormAndStripHTML(t *testing.T) {
	// Символ именно U+00A0 (пишем \u00a0): \xa0 в Go — байт, и проверка
	// байтовой замены через него не состоялась бы.
	if got := norm("  Б-ИСиТ\u00a0-22   2 "); got != "Б-ИСиТ -22 2" {
		t.Errorf("norm вернул %q", got)
	}
	// Буквы, чья UTF-8-последовательность содержит байт 0xA0 («Р» = D0 A0),
	// нормализация обязана оставлять целыми — на этом первая версия ломалась.
	if got := norm("Ранняя программа \u00a0ВУЗа"); got != "Ранняя программа ВУЗа" {
		t.Errorf("norm испортил кириллицу: %q", got)
	}
	if got := norm("\ufeffБ-ИСиТ-22"); got != "Б-ИСиТ-22" {
		t.Errorf("BOM не убран: %q", got)
	}
	if got := stripHTML(`<i class="fa fa-times"></i> без ауд.`); got != "без ауд." {
		t.Errorf("stripHTML вернул %q", got)
	}
	// Закрывающий тег без открывающего и текст целиком в теге.
	if got := stripHTML("к. <b>3/314</b>"); got != "к. 3/314" {
		t.Errorf("stripHTML вернул %q", got)
	}
}

func TestFlexAndFlagDecoding(t *testing.T) {
	var holder struct {
		ID    flex `json:"id"`
		Name  flex `json:"name"`
		Nil   flex `json:"nil_field"`
		Miss  flex `json:"missing"`
		Empty flag `json:"empty"`
		One   flag `json:"one"`
		Two   flag `json:"two"`
		True  flag `json:"true_flag"`
		Str   flag `json:"str_flag"`
	}

	if err := json.Unmarshal([]byte(`{"id":"494","name":494,"nil_field":null,"empty":0,"one":1,"two":2,"true_flag":true,"str_flag":"1"}`), &holder); err != nil {
		t.Fatal(err)
	}

	if holder.ID.String() != "494" || holder.Name.String() != "494" {
		t.Errorf("число и строка разошлись: %q / %q", holder.ID.String(), holder.Name.String())
	}
	if holder.Nil.String() != "" || holder.Miss.String() != "" {
		t.Errorf("null и отсутствующее поле должны быть пустыми, получили %q / %q", holder.Nil.String(), holder.Miss.String())
	}
	if holder.Name.Int() != 494 {
		t.Errorf("Int() от «494» вернул %d", holder.Name.Int())
	}

	if holder.Empty.Bool() {
		t.Error("0 прочитан как истина")
	}
	if !holder.One.Bool() || !holder.Two.Bool() || !holder.True.Bool() || !holder.Str.Bool() {
		t.Error("1, 2, true и \"1\" обязаны читаться как истина")
	}
}

// TestParseGroupsOnLiveExcerpt разбирает срез живого каталога групп.
//
// Фикстура — не выдуманная схема: это живой ответ эндпоинта 24.10.2026, из
// которого вырезаны лишние узлы (значения полей не тронуты). В нём есть узел
// «Факультеты, Медицинский институт» с «items»: [] — именно он валил разбор
// каталога целиком.
func TestParseGroupsOnLiveExcerpt(t *testing.T) {
	groups := parseGroupsFixture(t)

	if len(groups) != 13 {
		t.Errorf("групп %d, ожидалось 13 (срез живого ответа)", len(groups))
	}

	byID := make(map[string]Group, len(groups))
	for _, group := range groups {
		byID[group.ID] = group
	}

	// Пустой отдел не должен был ни уронить разбор, ни породить пустую строку.
	for _, group := range groups {
		if group.Name == "" || group.ID == "" {
			t.Errorf("в каталоге пустая строка: %+v", group)
		}
		if group.Department == "" || group.Course == "" {
			t.Errorf("у группы %+v не заполнена ветка каталога", group)
		}
	}

	want := Group{
		ID:         "494",
		Name:       "Б-ИСиТ-22",
		Department: "Инженерно-технологический институт",
		Course:     "2 курс",
	}
	if byID[want.ID] != want {
		t.Errorf("группа 494: %+v, ожидалось %+v", byID[want.ID], want)
	}
}

// TestParseGroupsRejectsUnknownNodeShape — непустой список вместо словаря.
//
// Пустой [] мы принимаем как «узлов нет», но непустой массив — уже другая форма
// ответа, содержимое которой пришлось бы молча выбросить. Ошибка здесь честнее:
// админ увидит «источник не ответил», а не расписание с потерянной половиной.
func TestParseGroupsRejectsUnknownNodeShape(t *testing.T) {
	tests := []struct {
		name    string
		payload map[string]any
	}{
		{"курсы пришли списком", map[string]any{
			"groups": map[string]any{
				"1": map[string]any{"name": "Институт", "items": []any{
					map[string]any{"name": "Курс", "items": map[string]any{"494": map[string]any{"id": 494, "name": "Б-ИСиТ-22"}}},
				}},
			},
		}},
		{"группы пришли списком", map[string]any{
			"groups": map[string]any{
				"1": map[string]any{"name": "Институт", "items": map[string]any{
					"11": map[string]any{"name": "Курс", "items": []any{map[string]any{"id": 494, "name": "Б-ИСиТ-22"}}},
				}},
			},
		}},
	}

	for _, tc := range tests {
		raw, err := json.Marshal(tc.payload)
		if err != nil {
			t.Fatalf("%s: собрать фикстуру: %v", tc.name, err)
		}
		if _, err := ParseGroups(raw); err == nil {
			t.Errorf("%s: неизвестная форма принята без ошибки", tc.name)
		}
	}
}

// ---------- вспомогательное ----------

func fixturePath() string { return filepath.Join("testdata", "apeks-2026-10-week.json") }

// groupsFixturePath — срез живого каталога групп (24.10.2026).
func groupsFixturePath() string { return filepath.Join("testdata", "apeks-groups-tree.json") }

func parseGroupsFixture(t *testing.T) []Group {
	t.Helper()

	raw, err := os.ReadFile(groupsFixturePath())
	if err != nil {
		t.Fatalf("фикстура %s не читается: %v", groupsFixturePath(), err)
	}

	groups, err := ParseGroups(raw)
	if err != nil {
		t.Fatalf("разбор фикстуры каталога: %v", err)
	}
	return groups
}

func parseFixture(t *testing.T) *Month {
	t.Helper()

	raw, err := os.ReadFile(fixturePath())
	if err != nil {
		t.Fatalf("фикстура %s не читается: %v", fixturePath(), err)
	}

	month, err := ParseMonth(raw)
	if err != nil {
		t.Fatalf("разбор фикстуры: %v", err)
	}
	return month
}

func mustParse(t *testing.T, payload map[string]any) *Month {
	t.Helper()

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("собрать фикстуру: %v", err)
	}

	month, err := ParseMonth(raw)
	if err != nil {
		t.Fatalf("разбор: %v", err)
	}
	return month
}
