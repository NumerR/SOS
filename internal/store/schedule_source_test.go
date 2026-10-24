package store_test

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/NumerR/SOS/internal/database"
	"github.com/NumerR/SOS/internal/store"
)

// Тесты хранилища внешнего расписания (T4.1).
//
// Что здесь главное — не «запись прошла», а поведение дедупликации и пакетного
// импорта: именно эти два механизма защищают страницу от дублей и от чужого
// расписания, висящего после переноса занятия. Проверки второго порядка —
// каталог групп (поиск кириллицы, неоднозначность) и цифры статистики, на
// которые смотрит админ.

const testSource = "apeks"

func newScheduleSourceDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("открыть базу: %v", err)
	}
	if err := database.Migrate(db); err != nil {
		t.Fatalf("базовая схема: %v", err)
	}
	// Миграция заявлена модулем schedule, но модуль импортирует handlers, а
	// handlers — этот пакет: цикл. Текст DDL живёт в store именно поэтому.
	if _, err := db.Exec(store.ScheduleSourceMigration); err != nil {
		t.Fatalf("таблицы источника расписания: %v", err)
	}

	t.Cleanup(func() { _ = db.Close() })

	return db
}

func newSourceStore(t *testing.T) (*store.ScheduleSourceStore, *sql.DB) {
	t.Helper()

	db := newScheduleSourceDB(t)
	return store.NewScheduleSourceStore(db), db
}

// lessonBuilder — пара с минимумом осмысленных полей. Остальные важны только
// там, где они проверяются. dayKey задаётся строкой YYYYMMDD, как его называет
// источник.
func lesson(externalID, dayKey string) store.SourceLesson {
	key, _ := strconv.Atoi(dayKey)

	return store.SourceLesson{
		ExternalID:   externalID,
		DayKey:       key,
		Date:         "06.10.2026",
		Weekday:      "Вт",
		LessonNumber: 2,
		Time:         "08:30 – 10:00",
		Start:        "08:30",
		End:          "10:00",
		Group:        "Б-ИСиТ-22",
		Subgroup:     "Б-ИСиТ-22/1",
		Discipline:   "Базы данных",
		Type:         "Лк",
		Room:         "201",
		Teacher:      "Иванов И.И.",
	}
}

func mustGroups(t *testing.T, s *store.ScheduleSourceStore, groups ...store.SourceGroup) int {
	t.Helper()

	added, err := s.UpsertGroups(context.Background(), testSource, groups)
	if err != nil {
		t.Fatalf("UpsertGroups: %v", err)
	}
	return added
}

func mustImport(t *testing.T, s *store.ScheduleSourceStore, group string, month, year int, lessons ...store.SourceLesson) int {
	t.Helper()

	stored, err := s.ReplaceMonth(context.Background(), testSource, group, month, year, lessons)
	if err != nil {
		t.Fatalf("ReplaceMonth(%s, %02d.%d): %v", group, month, year, err)
	}
	return stored
}

// --- Каталог групп -----------------------------------------------------------

func TestUpsertGroupsCountsOnlyNew(t *testing.T) {
	s, db := newSourceStore(t)
	ctx := context.Background()

	added := mustGroups(t, s,
		store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"},
		store.SourceGroup{ExternalID: "495", Name: "Б-ИСиТ-23"},
	)
	if added != 2 {
		t.Fatalf("первая синхронизация: добавлено %d, ожидалось 2", added)
	}

	// Повторная синхронизация того же каталога не должна выглядеть как «ничего
	// не произошло»: количество новых — 0, но строки при этом обновляются.
	added = mustGroups(t, s,
		store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22 (переименована)"},
		store.SourceGroup{ExternalID: "496", Name: "Б-ПИ-31"},
	)
	if added != 1 {
		t.Fatalf("вторая синхронизация: добавлено %d, ожидалось 1", added)
	}

	var name string
	if err := db.QueryRowContext(ctx,
		`SELECT name FROM schedule_source_groups WHERE source = ? AND external_id = ?`,
		testSource, "494").Scan(&name); err != nil {
		t.Fatalf("прочитать группу: %v", err)
	}
	if name != "Б-ИСиТ-22 (переименована)" {
		t.Fatalf("имя группы не обновилось: %q", name)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schedule_source_groups`).Scan(&count); err != nil {
		t.Fatalf("посчитать группы: %v", err)
	}
	if count != 3 {
		t.Fatalf("в каталоге %d групп, ожидалось 3", count)
	}
}

func TestUpsertGroupsRejectsGarbage(t *testing.T) {
	s, _ := newSourceStore(t)
	ctx := context.Background()

	if _, err := s.UpsertGroups(ctx, "  ", []store.SourceGroup{{ExternalID: "1", Name: "X"}}); err == nil {
		t.Fatal("пустое имя источника принято")
	}
	if _, err := s.UpsertGroups(ctx, testSource, nil); err == nil {
		t.Fatal("пустой каталог принят — синхронизация молча «удалась»")
	}

	// Узлы без id или без имени пропускаются, как их пропускает парсер, но
	// остальной каталог всё равно сохраняется: одна кривая строка не должна
	// отменять импорт 587 групп.
	added, err := s.UpsertGroups(ctx, testSource, []store.SourceGroup{
		{ExternalID: "", Name: "Без id"},
		{ExternalID: "500", Name: "   "},
		{ExternalID: "501", Name: "Б-ИСиТ-22"},
	})
	if err != nil {
		t.Fatalf("UpsertGroups с мусором: %v", err)
	}
	if added != 1 {
		t.Fatalf("добавлено %d, ожидалось 1", added)
	}
}

func TestListGroupsSearchesCyrillicCaseInsensitively(t *testing.T) {
	s, _ := newSourceStore(t)
	ctx := context.Background()

	mustGroups(t, s,
		store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"},
		store.SourceGroup{ExternalID: "495", Name: "Б-ИСиТ-23"},
		store.SourceGroup{ExternalID: "600", Name: "Б-ПИ-31"},
	)

	// «б-исит» нижним регистром — ровно то, что печатает пользователь. lower()
	// в SQLite знает только ASCII и до кириллицы не доходит, поэтому фильтр
	// сделан в Go; проверка и фиксирует, что он работает.
	found, err := s.ListGroups(ctx, testSource, "б-исит", 50)
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("по «б-исит» найдено %d групп: %+v", len(found), found)
	}
	if found[0].ExternalID != "494" || found[1].ExternalID != "495" {
		t.Fatalf("сортировка по имени нарушена: %+v", found)
	}

	all, err := s.ListGroups(ctx, testSource, "", 50)
	if err != nil {
		t.Fatalf("ListGroups без фильтра: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("без фильтра %d групп, ожидалось 3", len(all))
	}

	limited, err := s.ListGroups(ctx, testSource, "", 1)
	if err != nil {
		t.Fatalf("ListGroups с лимитом: %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("лимит 1 вернул %d строк", len(limited))
	}
}

func TestResolveGroup(t *testing.T) {
	s, _ := newSourceStore(t)
	ctx := context.Background()

	mustGroups(t, s,
		store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"},
		store.SourceGroup{ExternalID: "496", Name: "Б-ПИ-31"},
	)

	byID, err := s.ResolveGroup(ctx, testSource, "494")
	if err != nil {
		t.Fatalf("разрешение по id: %v", err)
	}
	if byID.Name != "Б-ИСиТ-22" {
		t.Fatalf("по id 494 пришла группа %q", byID.Name)
	}

	// Имя ищется без учёта регистра и обрамляющих пробелов — живой ввод админа.
	byName, err := s.ResolveGroup(ctx, testSource, "  б-исит-22 ")
	if err != nil {
		t.Fatalf("разрешение по имени: %v", err)
	}
	if byName.ExternalID != "494" {
		t.Fatalf("по имени пришла группа %s", byName.ExternalID)
	}

	if _, err := s.ResolveGroup(ctx, testSource, "нет-такой"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("отсутствие группы должно быть ErrNotFound, получили %v", err)
	}
	if _, err := s.ResolveGroup(ctx, testSource, "   "); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("пустой запрос должен быть ErrNotFound, получили %v", err)
	}
}

func TestResolveGroupAmbiguous(t *testing.T) {
	s, _ := newSourceStore(t)
	ctx := context.Background()

	// Две группы с одинаковым названием (в каталоге вуза такое есть: разные
	// институты, однофамиальные сокращения). Молча взять первую — повесить на
	// страницу чужое расписание.
	mustGroups(t, s,
		store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"},
		store.SourceGroup{ExternalID: "712", Name: "Б-ИСиТ-22"},
	)

	group, err := s.ResolveGroup(ctx, testSource, "Б-ИСиТ-22")
	if group != nil {
		t.Fatalf("неоднозначный запрос вернул группу %+v", group)
	}
	if !errors.Is(err, store.ErrAmbiguousGroup) {
		t.Fatalf("ожидался ErrAmbiguousGroup, получили %v", err)
	}
	// Текст ошибки уходит админу — в нём должны быть оба id, иначе выбор
	// невозможно сделать не выходя из панели.
	msg := err.Error()
	if !strings.Contains(msg, "494") || !strings.Contains(msg, "712") {
		t.Fatalf("в тексте неоднозначности нет кандидатов: %q", msg)
	}

	// Тот же запрос по id однозначен.
	if _, err := s.ResolveGroup(ctx, testSource, "712"); err != nil {
		t.Fatalf("разрешение по id при дубле имён: %v", err)
	}
}

func TestLastGroupSyncIsEmptyBeforeFirstRun(t *testing.T) {
	s, _ := newSourceStore(t)
	ctx := context.Background()

	at, err := s.LastGroupSync(ctx, testSource)
	if err != nil {
		t.Fatalf("LastGroupSync на пустой базе: %v", err)
	}
	if at != 0 {
		t.Fatalf("до синхронизации время = %d, ожидалось 0", at)
	}

	mustGroups(t, s, store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"})

	at, err = s.LastGroupSync(ctx, testSource)
	if err != nil {
		t.Fatalf("LastGroupSync: %v", err)
	}
	if at == 0 {
		t.Fatal("после синхронизации время осталось 0")
	}
}

// --- Импорт пар --------------------------------------------------------------

func TestReplaceMonthIsRepeatable(t *testing.T) {
	s, db := newSourceStore(t)
	ctx := context.Background()

	first := mustImport(t, s, "494", 10, 2026, lesson("1001", "20261006"), lesson("1002", "20261007"))
	if first != 2 {
		t.Fatalf("первый импорт сохранил %d пар, ожидалось 2", first)
	}

	// Тот же месяц ещё раз — то же количество строк. Без пакетной замены здесь
	// появились бы дубли, и каждая пара на странице показывалась бы дважды.
	second := mustImport(t, s, "494", 10, 2026, lesson("1001", "20261006"), lesson("1002", "20261007"))
	if second != 2 {
		t.Fatalf("повторный импорт сохранил %d пар", second)
	}

	var rows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schedule_source_lessons`).Scan(&rows); err != nil {
		t.Fatalf("посчитать пары: %v", err)
	}
	if rows != 2 {
		t.Fatalf("после повторного импорта в базе %d строк, ожидалось 2", rows)
	}
}

func TestReplaceMonthDropsStaleLessons(t *testing.T) {
	s, db := newSourceStore(t)
	ctx := context.Background()

	mustImport(t, s, "494", 10, 2026, lesson("1001", "20261006"), lesson("1002", "20261007"))

	// Источник убрал пару (перенос/отмена) — повторный импорт обязан убрать её
	// и у нас. «Дописываем, чего нет» оставило бы на странице занятие, которого
	// в вузе уже нет.
	mustImport(t, s, "494", 10, 2026, lesson("1001", "20261006"))

	var remaining int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schedule_source_lessons WHERE external_id = ?`, "1002").Scan(&remaining); err != nil {
		t.Fatalf("посчитать осиротевшие пары: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("пара 1002 осталась в базе после импорта без неё")
	}
}

func TestReimportMovesLessonBetweenMonths(t *testing.T) {
	s, db := newSourceStore(t)
	ctx := context.Background()

	// Занятие перенесли из октября в ноябрь: external_id тот же, месяц другой.
	// UNIQUE (source, group_ext_id, external_id) плюс upsert означает, что
	// строка переезжает, а не появляется второй раз.
	mustImport(t, s, "494", 10, 2026, lesson("1001", "20261006"))
	mustImport(t, s, "494", 11, 2026, lesson("1001", "20261106"))

	var rows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schedule_source_lessons`).Scan(&rows); err != nil {
		t.Fatalf("посчитать пары: %v", err)
	}
	if rows != 1 {
		t.Fatalf("после переноса в базе %d строк, ожидалась 1", rows)
	}

	var month, dayKey int
	err := db.QueryRowContext(ctx,
		`SELECT month, day_key FROM schedule_source_lessons WHERE external_id = ?`, "1001").Scan(&month, &dayKey)
	if err != nil {
		t.Fatalf("прочитать перенесённую пару: %v", err)
	}
	if month != 11 || dayKey != 20261106 {
		t.Fatalf("пара осталась в %d на %d, ожидался ноябрь 20261106", month, dayKey)
	}

	// И из октябрьского диапазона она должна исчезнуть.
	october, err := s.ListLessons(ctx, testSource, "494", 20261001, 20261031)
	if err != nil {
		t.Fatalf("ListLessons за октябрь: %v", err)
	}
	if len(october) != 0 {
		t.Fatalf("в октябре осталось %d пар после переноса", len(october))
	}
}

func TestReplaceMonthValidation(t *testing.T) {
	s, _ := newSourceStore(t)
	ctx := context.Background()

	if _, err := s.ReplaceMonth(ctx, "", "494", 10, 2026, nil); err == nil {
		t.Fatal("импорт без источника принят")
	}
	if _, err := s.ReplaceMonth(ctx, testSource, " ", 10, 2026, nil); err == nil {
		t.Fatal("импорт без группы принят")
	}
	for _, bad := range [][2]int{{0, 2026}, {13, 2026}, {10, 1999}, {10, 2101}} {
		if _, err := s.ReplaceMonth(ctx, testSource, "494", bad[0], bad[1], nil); err == nil {
			t.Fatalf("импорт за %02d.%d принят", bad[0], bad[1])
		}
	}

	// Пакет больше лимита — отказ целиком, а не «первые 2000»: половина месяца
	// на странице хуже, чем не импортированный месяц.
	huge := make([]store.SourceLesson, 2001)
	for i := range huge {
		huge[i] = lesson(string(rune('a'+i%26))+strconv.Itoa(i), "20261006")
	}
	if _, err := s.ReplaceMonth(ctx, testSource, "494", 10, 2026, huge); err == nil {
		t.Fatal("пакет из 2001 пары принят")
	}

	// Пустой external_id — пара без идентификатора: пропускается, остальное
	// сохраняется.
	stored, err := s.ReplaceMonth(ctx, testSource, "495", 10, 2026, []store.SourceLesson{
		lesson("", "20261006"),
		lesson("2001", "20261006"),
	})
	if err != nil {
		t.Fatalf("ReplaceMonth с пустым id: %v", err)
	}
	if stored != 1 {
		t.Fatalf("сохранено %d пар, ожидалось 1", stored)
	}
}

func TestReplaceMonthNormalizesText(t *testing.T) {
	s, _ := newSourceStore(t)
	ctx := context.Background()

	longDiscipline := strings.Repeat("Д", 400)
	mustImport(t, s, "494", 10, 2026, store.SourceLesson{
		ExternalID: "3001",
		DayKey:     0, // дата не читается
		Discipline: longDiscipline,
		Teacher:    "Минаев    В.В.",
		Room:       "  201 ",
	})

	lessons, err := s.ListLessons(ctx, testSource, "494", 1, 99999999)
	if err != nil {
		t.Fatalf("ListLessons: %v", err)
	}
	if len(lessons) != 1 {
		t.Fatalf("прочитано %d пар", len(lessons))
	}
	got := lessons[0]

	if got.DayKey != 99999999 {
		t.Fatalf("пара без даты получила day_key %d, ожидалось 99999999 (конец списка)", got.DayKey)
	}
	if len([]rune(got.Discipline)) > 300 {
		t.Fatalf("дисциплина не обрезана: %d рун", len([]rune(got.Discipline)))
	}
	if strings.ContainsAny(got.Discipline, "\ufffd") {
		t.Fatal("обрезка Discipline разрезала руну — обрезка идёт не по байтам")
	}
	if got.Teacher != "Минаев В.В." {
		t.Fatalf("двойные пробелы не схлопнуты: %q", got.Teacher)
	}
	if got.Room != "201" {
		t.Fatalf("аудитория не нормализована: %q", got.Room)
	}
}

func TestListLessonsRangeAndOrder(t *testing.T) {
	s, _ := newSourceStore(t)
	ctx := context.Background()

	week := []store.SourceLesson{
		lesson("a3", "20261007"),
		lesson("a1", "20261006"),
		lesson("a2", "20261006"),
		lesson("a9", "20261013"),
	}
	// Одна пара с одинаковым external_id внутри пакета — аномалия формата,
	// которая не должна превратиться в дубль на странице.
	week = append(week, lesson("a2", "20261006"))

	mustImport(t, s, "494", 10, 2026, week...)

	lessons, err := s.ListLessons(ctx, testSource, "494", 20261005, 20261011)
	if err != nil {
		t.Fatalf("ListLessons: %v", err)
	}
	if len(lessons) != 3 {
		t.Fatalf("в диапазоне %d пар, ожидалось 3 (пара 13.10 вне недели)", len(lessons))
	}
	if lessons[0].ExternalID != "a1" || lessons[1].ExternalID != "a2" || lessons[2].ExternalID != "a3" {
		ids := make([]string, 0, len(lessons))
		for _, l := range lessons {
			ids = append(ids, l.ExternalID)
		}
		t.Fatalf("сортировка по (день, номер пары) нарушена: %v", ids)
	}

	// Границы включаются с обеих сторон.
	if all, err := s.ListLessons(ctx, testSource, "494", 20261006, 20261013); err != nil || len(all) != 4 {
		t.Fatalf("полный диапазон: %d пар, %v", len(all), err)
	}

	if _, err := s.ListLessons(ctx, testSource, "494", 20261011, 20261005); err == nil {
		t.Fatal("перевёрнутый диапазон принят — страница получила бы пустой список без объяснения")
	}
}

func TestImportedMonthsAndLastImport(t *testing.T) {
	s, _ := newSourceStore(t)
	ctx := context.Background()

	at, err := s.LastImportAt(ctx, testSource, "494")
	if err != nil {
		t.Fatalf("LastImportAt на пустой группе: %v", err)
	}
	if at != 0 {
		t.Fatalf("до импорта время = %d", at)
	}
	months, err := s.ImportedMonths(ctx, testSource, "494")
	if err != nil {
		t.Fatalf("ImportedMonths на пустой группе: %v", err)
	}
	if len(months) != 0 {
		t.Fatalf("до импорта найдено %d месяцев", len(months))
	}

	mustImport(t, s, "494", 11, 2026, lesson("b1", "20261102"), lesson("b2", "20261103"))
	mustImport(t, s, "494", 10, 2026, lesson("a1", "20261006"))

	months, err = s.ImportedMonths(ctx, testSource, "494")
	if err != nil {
		t.Fatalf("ImportedMonths: %v", err)
	}
	if len(months) != 2 {
		t.Fatalf("импортировано %d месяцев, ожидалось 2", len(months))
	}
	// Отсортировано по возрастанию: страница по этому списку решает, куда
	// двигать стрелку «предыдущий месяц».
	if months[0].Year != 2026 || months[0].Month != 10 || months[1].Month != 11 {
		t.Fatalf("порядок месяцев: %+v", months)
	}
	if months[0].Lessons != 1 || months[1].Lessons != 2 {
		t.Fatalf("число пар по месяцам: %+v", months)
	}

	at, err = s.LastImportAt(ctx, testSource, "494")
	if err != nil {
		t.Fatalf("LastImportAt: %v", err)
	}
	if at == 0 {
		t.Fatal("после импорта время осталось 0")
	}
	if months[0].ImportedAt == 0 {
		t.Fatal("у импортированного месяца не заполнен imported_at")
	}
}

func TestStatsCountsGroupsLessonsAndImportedGroups(t *testing.T) {
	s, _ := newSourceStore(t)
	ctx := context.Background()

	empty, err := s.Stats(ctx, testSource)
	if err != nil {
		t.Fatalf("Stats на пустой базе: %v", err)
	}
	if empty.Groups != 0 || empty.Lessons != 0 || empty.ImportedGroups != 0 {
		t.Fatalf("на пустой базе статистика не нулевая: %+v", empty)
	}

	mustGroups(t, s,
		store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"},
		store.SourceGroup{ExternalID: "495", Name: "Б-ИСиТ-23"},
	)
	mustImport(t, s, "494", 10, 2026, lesson("c1", "20261006"), lesson("c2", "20261007"))
	mustImport(t, s, "495", 10, 2026, lesson("d1", "20261006"))

	stats, err := s.Stats(ctx, testSource)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Groups != 2 {
		t.Fatalf("групп в статистике: %d", stats.Groups)
	}
	if stats.Lessons != 3 {
		t.Fatalf("пар в статистике: %d", stats.Lessons)
	}
	if stats.ImportedGroups != 2 {
		t.Fatalf("групп с расписанием: %d", stats.ImportedGroups)
	}
	if stats.GroupsAt == 0 || stats.ImportedAt == 0 {
		t.Fatalf("временные метки статистики: %+v", stats)
	}
}

func TestScheduleSourceMigrationIsIdempotent(t *testing.T) {
	db := newScheduleSourceDB(t)

	// Платформа применяет миграции повторно при каждом старте; вторая серия
	// CREATE TABLE не должна ронять сервер.
	if _, err := db.Exec(store.ScheduleSourceMigration); err != nil {
		t.Fatalf("повторное применение миграции: %v", err)
	}
}
