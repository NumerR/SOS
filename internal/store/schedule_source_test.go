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
	if _, err := db.Exec(store.ScheduleMapIndexMigration); err != nil {
		t.Fatalf("индекс слоя карты: %v", err)
	}
	if _, err := db.Exec(store.ScheduleCoverageMigration); err != nil {
		t.Fatalf("таблица покрытия месяцев: %v", err)
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

// Каталог больше прежнего умолчания: 0 — «весь список», а не первые 200.
// Страница строит из ответа datalist и подпись «В каталоге N групп», и
// усечённый каталог молча оставлял без подсказки все группы после 200-й.
func TestListGroupsWithoutLimitReturnsWholeCatalogue(t *testing.T) {
	s, _ := newSourceStore(t)
	ctx := context.Background()

	groups := make([]store.SourceGroup, 0, 205)
	for i := 1; i <= 205; i++ {
		groups = append(groups, store.SourceGroup{
			ExternalID: strconv.Itoa(1000 + i),
			Name:       "Г-" + strconv.Itoa(1000+i),
		})
	}
	mustGroups(t, s, groups...)

	all, err := s.ListGroups(ctx, testSource, "", 0)
	if err != nil {
		t.Fatalf("ListGroups без лимита: %v", err)
	}
	if len(all) != 205 {
		t.Fatalf("без лимита %d групп, ожидалось 205 (умолчание снова режет каталог)", len(all))
	}

	limited, err := s.ListGroups(ctx, testSource, "", 50)
	if err != nil {
		t.Fatalf("ListGroups с лимитом: %v", err)
	}
	if len(limited) != 50 {
		t.Fatalf("лимит 50 вернул %d строк", len(limited))
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

// TestMapLessonsAcrossGroups — вопрос слоя карты: дни по всему каталогу.
//
// Отдельный метод ради одного различения, которое ListLessons дать не может:
// «группа не указана» и «такой группы нет» — разные ситуации, и подменить первую
// второй молча нельзя.
func TestMapLessonsAcrossGroups(t *testing.T) {
	s, _ := newSourceStore(t)
	ctx := context.Background()

	mustGroups(t, s,
		store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"},
		store.SourceGroup{ExternalID: "501", Name: "Б-ПИ-31"},
	)

	first := lesson("a1", "20261006")
	second := lesson("b1", "20261007")
	second.Group = "Б-ПИ-31"

	mustImport(t, s, "494", 10, 2026, first, lesson("a9", "20261013"))
	mustImport(t, s, "501", 10, 2026, second)

	all, err := s.MapLessons(ctx, testSource, "", 20261005, 20261011)
	if err != nil {
		t.Fatalf("MapLessons по всем группам: %v", err)
	}
	if len(all) != 2 {
		ids := make([]string, 0, len(all))
		for _, l := range all {
			ids = append(ids, l.ExternalID)
		}
		t.Fatalf("пар в диапазоне %d (%v), ожидалось 2: по одной из каждой группы, 13.10 вне недели",
			len(all), ids)
	}
	if all[0].ExternalID != "a1" || all[1].ExternalID != "b1" {
		t.Errorf("порядок (день, номер пары) нарушен: %s затем %s", all[0].ExternalID, all[1].ExternalID)
	}
	if all[0].GroupExtID != "494" || all[1].GroupExtID != "501" {
		t.Errorf("группы перепутаны: %s и %s", all[0].GroupExtID, all[1].GroupExtID)
	}

	// Пробел вместо группы — то же «все группы»: фильтром управления быть не
	// может, «группы нет» обязан ответить тот, кто разбирает запрос.
	spaced, err := s.MapLessons(ctx, testSource, "   ", 20261005, 20261011)
	if err != nil || len(spaced) != len(all) {
		t.Errorf("MapLessons с пустой строкой вернул %d пар, %v", len(spaced), err)
	}

	// Одна группа — тот же ответ, что у ListLessons: два метода не имеют права
	// разойтись ни списком, ни порядком.
	one, err := s.MapLessons(ctx, testSource, "494", 20261005, 20261011)
	if err != nil {
		t.Fatalf("MapLessons по одной группе: %v", err)
	}
	viaList, err := s.ListLessons(ctx, testSource, "494", 20261005, 20261011)
	if err != nil {
		t.Fatalf("ListLessons: %v", err)
	}
	if len(one) != len(viaList) {
		t.Errorf("одна группа через разные методы: %d и %d пар", len(one), len(viaList))
	}

	if _, err := s.MapLessons(ctx, testSource, "", 20261011, 20261005); err == nil {
		t.Error("перевёрнутый диапазон принят без ошибки")
	}

	// Пустой период — пустой список, а не nil: карта рисует «пар нет», и JSON
	// обязан остаться массивом.
	empty, err := s.MapLessons(ctx, testSource, "", 20261201, 20261207)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Errorf("пустой период вернулся как %+v (%v), ожидался пустой срез", empty, err)
	}
}

// TestScheduleMapIndexMigration — индекс слоя карты применяется повторно.
//
// Миграции runner применяет один раз по (module, version), но сам DDL обязан
// быть терпеливым к повтору: тестовая база пересоздаётся на каждом прогоне, а
// живая база переживает и откат, и переустановку бинарника.
func TestScheduleMapIndexMigration(t *testing.T) {
	db := newScheduleSourceDB(t)

	for i := 0; i < 2; i++ {
		if _, err := db.Exec(store.ScheduleMapIndexMigration); err != nil {
			t.Fatalf("повтор %d: %v", i, err)
		}
	}

	var names []string
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = 'schedule_source_lessons'`)
	if err != nil {
		t.Fatalf("список индексов: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("имя индекса: %v", err)
		}
		names = append(names, name)
	}

	if !strings.Contains(strings.Join(names, ","), "idx_schedule_lessons_source_day") {
		t.Errorf("индекса дней по всем группам нет; есть %v", names)
	}
}

func TestMonthCoverageStates(t *testing.T) {
	s, _ := newSourceStore(t)
	ctx := context.Background()

	empty, err := s.MonthCoverage(ctx, testSource, "494")
	if err != nil {
		t.Fatalf("MonthCoverage на пустой группе: %v", err)
	}
	if empty == nil {
		t.Fatal("покрытие вернулось nil — JSON ответа стал бы null вместо массива")
	}
	if len(empty) != 0 {
		t.Fatalf("до импорта найдено %d месяцев: %+v", len(empty), empty)
	}

	mustImport(t, s, "494", 11, 2026, lesson("b1", "20261102"), lesson("b2", "20261103"))
	mustImport(t, s, "494", 10, 2026, lesson("a1", "20261006"))

	months, err := s.MonthCoverage(ctx, testSource, "494")
	if err != nil {
		t.Fatalf("MonthCoverage: %v", err)
	}
	if len(months) != 2 {
		t.Fatalf("покрытие %d месяцев, ожидалось 2: %+v", len(months), months)
	}
	// Отсортировано по возрастанию: страница по этому списку решает, куда
	// двигать стрелку «предыдущий месяц».
	if months[0].Year != 2026 || months[0].Month != 10 || months[1].Month != 11 {
		t.Fatalf("порядок месяцев: %+v", months)
	}
	if months[0].Lessons != 1 || months[1].Lessons != 2 {
		t.Fatalf("число пар по месяцам: %+v", months)
	}
	for _, m := range months {
		if !m.OK {
			t.Errorf("%02d.%d: ok=0 после успешного импорта", m.Month, m.Year)
		}
		if m.RefreshedAt == 0 || m.AttemptedAt == 0 {
			t.Errorf("%02d.%d: времени покрытия 0 — страница сочла бы месяц непроверенным", m.Month, m.Year)
		}
	}
}

// TestMonthCoverageEmptyMonth — «занятий нет» и «месяц не качали» — разные факты.
//
// До таблицы покрытия это различие выводилось из строк пар и потому было
// неотличимо: пустой месяц давал ровно столько же строк, сколько нескачанный.
func TestMonthCoverageEmptyMonth(t *testing.T) {
	s, _ := newSourceStore(t)
	ctx := context.Background()

	stored, err := s.ReplaceMonth(ctx, testSource, "494", 11, 2026, nil)
	if err != nil {
		t.Fatalf("пустой месяц: %v", err)
	}
	if stored != 0 {
		t.Fatalf("пустой месяц вернул %d пар", stored)
	}

	months, err := s.MonthCoverage(ctx, testSource, "494")
	if err != nil {
		t.Fatalf("MonthCoverage: %v", err)
	}
	if len(months) != 1 {
		t.Fatalf("строк покрытия: %d, ожидалась одна: %+v", len(months), months)
	}
	if !months[0].OK || months[0].Lessons != 0 {
		t.Fatalf("пустой месяц записан как %+v", months[0])
	}
}

// TestMonthCoverageFailureKeepsArchive — неудачная проверка не стирает архив.
//
// Это и есть вторая половина требования «если расписание на сайте вуза
// перестанет работать, оно должно сохраняться в собственной базе»: строка
// покрытия обязана рассказать о сбое, не соврав о том, что данных нет.
func TestMonthCoverageFailureKeepsArchive(t *testing.T) {
	s, _ := newSourceStore(t)
	ctx := context.Background()

	mustImport(t, s, "494", 11, 2026, lesson("b1", "20261102"))
	before, err := s.MonthCoverage(ctx, testSource, "494")
	if err != nil || len(before) != 1 {
		t.Fatalf("покрытие до сбоя: %+v (%v)", before, err)
	}

	if err := s.MarkMonthAttempt(ctx, testSource, "494", 11, 2026); err != nil {
		t.Fatalf("MarkMonthAttempt по лежалому месяцу: %v", err)
	}

	after, err := s.MonthCoverage(ctx, testSource, "494")
	if err != nil {
		t.Fatalf("MonthCoverage: %v", err)
	}
	if len(after) != 1 {
		t.Fatalf("после сбоя строк покрытия: %d", len(after))
	}
	if after[0].OK {
		t.Error("сбойная попытка не пометила месяц")
	}
	if after[0].Lessons != before[0].Lessons || after[0].RefreshedAt != before[0].RefreshedAt {
		t.Errorf("сбой стёр архив: было %+v, стало %+v", before[0], after[0])
	}
	if after[0].AttemptedAt < before[0].AttemptedAt {
		t.Errorf("attempted_at не продвинулся: %d → %d", before[0].AttemptedAt, after[0].AttemptedAt)
	}

	if err := s.MarkMonthAttempt(ctx, testSource, "495", 3, 2027); err != nil {
		t.Fatalf("MarkMonthAttempt по нескачанному месяцу: %v", err)
	}
	never, err := s.MonthCoverage(ctx, testSource, "495")
	if err != nil {
		t.Fatalf("MonthCoverage: %v", err)
	}
	if len(never) != 1 || never[0].OK || never[0].Lessons != 0 || never[0].RefreshedAt != 0 {
		t.Errorf("нескачанный месяц после сбоя записан как %+v", never)
	}

	// Пустой external_id — не строка покрытия: иначе журнал попыток набился бы
	// строками, которые ни к какой группе не относятся.
	if err := s.MarkMonthAttempt(ctx, testSource, "", 11, 2026); err == nil {
		t.Error("отметка попытки без группы принята")
	}
	if err := s.MarkMonthAttempt(ctx, testSource, "494", 13, 2026); err == nil {
		t.Error("отметка попытки за 13-й месяц принята")
	}
}

// TestMonthCoverageCountsDedup — покрытие считает реальные строки, а не длину ответа.
func TestMonthCoverageCountsDedup(t *testing.T) {
	s, _ := newSourceStore(t)
	ctx := context.Background()

	// Источник выдаёт повтор id при аномалии формата: пара должна остаться
	// одной, и цифра покрытия обязана совпасть с тем, что реально покажет
	// страница, — иначе на ней будет написано «3 занятия» над списком из двух.
	stored := mustImport(t, s, "494", 11, 2026,
		lesson("dup", "20261102"), lesson("dup", "20261102"), lesson("uniq", "20261103"))
	if stored != 2 {
		t.Fatalf("повтор external_id лёг %d строками, ожидалось 2", stored)
	}

	months, err := s.MonthCoverage(ctx, testSource, "494")
	if err != nil || len(months) != 1 {
		t.Fatalf("покрытие: %+v (%v)", months, err)
	}
	if months[0].Lessons != stored {
		t.Errorf("покрытие %+v не совпадает с числом вставок %d", months[0], stored)
	}

	// Перенос занятия в другой месяц: пакет уменьшается, и покрытие обязано
	// показать новое число, а не то, что было при первом импорте.
	stored = mustImport(t, s, "494", 11, 2026, lesson("dup", "20261102"))
	months, err = s.MonthCoverage(ctx, testSource, "494")
	if err != nil {
		t.Fatalf("MonthCoverage: %v", err)
	}
	if stored != 1 || months[0].Lessons != 1 {
		t.Errorf("после переноса: вставок %d, покрытие %+v", stored, months[0])
	}
}

// TestScheduleCoverageMigrationBackfills — живая база получает покрытие из того, что уже лежит.
//
// Миграция добавляет таблицу к базе, в которой месяцы импортированы ещё до неё:
// без backfill эти группы выглядели бы неимпортированными, и read-through
// заново сходил бы в АПЭКС за каждые 588 групп.
func TestScheduleCoverageMigrationBackfills(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("открыть базу: %v", err)
	}
	defer func() { _ = db.Close() }()

	if err := database.Migrate(db); err != nil {
		t.Fatalf("базовая схема: %v", err)
	}
	if _, err := db.Exec(store.ScheduleSourceMigration); err != nil {
		t.Fatalf("таблицы источника расписания: %v", err)
	}

	s := store.NewScheduleSourceStore(db)
	ctx := context.Background()

	// Строки пар вставляются SQL'ом, а не через ReplaceMonth: после таблицы
	// покрытия этот метод пишет обе сразу, а здесь нужен ровно прежний мир —
	// пары без всякого покрытия.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO schedule_source_lessons
			(source, external_id, group_ext_id, month, year, day_key, imported_at)
		VALUES (?, 'a1', '494', 10, 2026, 20261006, 1760000000),
		       (?, 'a2', '494', 10, 2026, 20261007, 1760000500)
	`, testSource, testSource); err != nil {
		t.Fatalf("импорт до миграции покрытия: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM schedule_source_lessons WHERE external_id = 'a2'`); err != nil {
		t.Fatalf("убрать пару: %v", err)
	}

	if _, err := db.Exec(store.ScheduleCoverageMigration); err != nil {
		t.Fatalf("миграция покрытия: %v", err)
	}

	// Читается через MonthCoverage, а не голым запросом: именно этот метод
	// решает, идти ли в источник, и backfill обязан быть виден ему.
	backfilled, err := s.MonthCoverage(ctx, testSource, "494")
	if err != nil {
		t.Fatalf("MonthCoverage после backfill: %v", err)
	}
	if len(backfilled) != 1 {
		t.Fatalf("строк покрытия после backfill: %d (%+v)", len(backfilled), backfilled)
	}
	if backfilled[0].Lessons != 1 || !backfilled[0].OK ||
		backfilled[0].RefreshedAt == 0 || backfilled[0].AttemptedAt == 0 {
		t.Errorf("backfill дал %+v", backfilled[0])
	}

	// Повторное применение не должно ни менять цифры, ни плодить строки.
	if _, err := db.Exec(store.ScheduleCoverageMigration); err != nil {
		t.Fatalf("повторное применение миграции покрытия: %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schedule_source_months WHERE source = ?`, testSource,
	).Scan(&count); err != nil {
		t.Fatalf("после повтора: %v", err)
	}
	if count != 1 {
		t.Errorf("повтор миграции дал %d строк покрытия", count)
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
	// Покрытие — две разные цифры: панель админа должна отличать «месяцев
	// коснулись» от «месяцев, где ничего не вышло», иначе сбой источника
	// выглядит как тишина.
	if stats.MonthsCovered != 2 || stats.MonthsFailed != 0 {
		t.Errorf("покрытие в статистике: %+v", stats)
	}

	if err := s.MarkMonthAttempt(ctx, testSource, "496", 12, 2026); err != nil {
		t.Fatalf("MarkMonthAttempt: %v", err)
	}
	failed, err := s.Stats(ctx, testSource)
	if err != nil {
		t.Fatalf("Stats после сбоя: %v", err)
	}
	if failed.MonthsFailed != 1 || failed.MonthsCovered != 2 {
		t.Errorf("сбойная попытка не отразилась в статистике: %+v", failed)
	}
	if failed.ImportedGroups != 2 {
		t.Errorf("групп с расписанием стало %d — сбой посчитался данными", failed.ImportedGroups)
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
