package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Хранилище внешнего расписания (T4.1).
//
// Почему данные лежат в базе, а не отдаются браузеру прямым запросом в АПЭКС:
//
//   - токен источника не может оказаться в браузере — это доступ к API вуза,
//     а не к чужой учётной записи, и любой, кто откроет DevTools, получил бы
//     его вместе со страницей;
//   - бриф требует дедупликацию импортируемых данных, а дедуплицировать
//     нечего, если каждая загрузка страницы — новый ответ источника;
//   - источник живёт в сети вуза: без своей копии расписание платформы
//     недоступно ровно тогда, когда недоступен сайт вуза.
//
// Что лежит: две таблицы — каталог групп и сами пары. Пары хранятся уже
// разобраными (подгруппа посчитана, HTML из аудитории вырезан, номер пары
// проставлен), потому что разбор — часть импорта: повторный разбор на каждой
// загрузке страницы означал бы, что правила из internal/apeks живут в двух
// местах.
//
// Ключ дедупликации — (source, group_ext_id, external_id), где external_id это
// id пары у источника. На живом ответе за два месяца (октябрь и ноябрь 2026 для
// группы 494) идентификаторы не повторяются: 98 и 99 пар, 197 различных id. Но
// полагаться на глобальную уникальность чужого id нельзя, поэтому импорт
// устроен как замена месяца целиком, а не как «дописываем, чего нет»: строки
// выбранного (группа, месяц, год) удаляются и ставятся заново одним
// транзакционным шагом. Повторный импорт того же месяца не даёт ни дублей, ни
// осиротевших пар после переноса занятия.

// ScheduleSourceMigration — DDL таблиц внешнего расписания.
//
// Объявлен здесь по той же причине, что и DDL аудита (см. комментарий в
// AuditEventsMigration): текст нужен и модулю, который его заявляет, и тестам
// хранилища, а модуль импортировать в тест хранилища нельзя — будет цикл.
//
// Две таблицы в одной миграции — осознанно: они появляются и живут только
// вместе (пары без каталога групп не с чем сопоставлять), и отдельная версия
// для второй таблицы ничего не добавила бы, кроме второй строки в журнале.
const ScheduleSourceMigration = `
CREATE TABLE IF NOT EXISTS schedule_source_groups (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    source      TEXT    NOT NULL,
    external_id TEXT    NOT NULL,
    name        TEXT    NOT NULL,
    department  TEXT    NOT NULL DEFAULT '',
    course      TEXT    NOT NULL DEFAULT '',
    updated_at  INTEGER NOT NULL,
    UNIQUE (source, external_id)
);

CREATE TABLE IF NOT EXISTS schedule_source_lessons (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    source        TEXT    NOT NULL,
    external_id   TEXT    NOT NULL,
    group_ext_id  TEXT    NOT NULL,
    month         INTEGER NOT NULL,
    year          INTEGER NOT NULL,
    day_key       INTEGER NOT NULL,
    date          TEXT    NOT NULL DEFAULT '',
    weekday       TEXT    NOT NULL DEFAULT '',
    lesson_number INTEGER NOT NULL DEFAULT 0,
    time          TEXT    NOT NULL DEFAULT '',
    start_time    TEXT    NOT NULL DEFAULT '',
    end_time      TEXT    NOT NULL DEFAULT '',
    group_name    TEXT    NOT NULL DEFAULT '',
    subgroup      TEXT    NOT NULL DEFAULT '',
    discipline    TEXT    NOT NULL DEFAULT '',
    lesson_type   TEXT    NOT NULL DEFAULT '',
    room          TEXT    NOT NULL DEFAULT '',
    teacher       TEXT    NOT NULL DEFAULT '',
    imported_at   INTEGER NOT NULL,
    UNIQUE (source, group_ext_id, external_id)
);

CREATE INDEX IF NOT EXISTS idx_schedule_lessons_group_day ON schedule_source_lessons(source, group_ext_id, day_key, lesson_number);
CREATE INDEX IF NOT EXISTS idx_schedule_lessons_batch ON schedule_source_lessons(source, group_ext_id, month, year);
CREATE INDEX IF NOT EXISTS idx_schedule_groups_name ON schedule_source_groups(source, name);
`

// Максимальные значения полей, приходящих из источника. Ограничение нужно не
// для красоты: ответ чужого API попадает в базу и оттуда на страницу, и строка
// дисциплины в 1 МБ — это та же строка вDOM каждого посетителя.
const (
	maxScheduleTextLength    = 300
	maxScheduleNameLength    = 120
	maxScheduleTeacherLength = 600
	maxScheduleBatchSize     = 2000 // пар за один импорт месяца — с шестикратным запасом
)

// ScheduleSourceStore работает с таблицами внешнего расписания.
type ScheduleSourceStore struct {
	db *sql.DB
}

// NewScheduleSourceStore создаёт хранилище источника расписания.
func NewScheduleSourceStore(db *sql.DB) *ScheduleSourceStore {
	return &ScheduleSourceStore{db: db}
}

// SourceGroup — строка каталога групп источника.
type SourceGroup struct {
	ExternalID string `json:"external_id"`
	Name       string `json:"name"`
	Department string `json:"department"`
	Course     string `json:"course"`
	UpdatedAt  int64  `json:"updated_at"`
}

// SourceLesson — одна пара в том виде, как она импортирована.
//
// DayKey (YYYYMMDD) участвует в фильтрации и сортировке, Date остаёт строкой
// источника: день, названный вузом, не должен зависеть от часовой зоны
// сервера, который этот день читает.
type SourceLesson struct {
	ExternalID   string `json:"external_id"`
	GroupExtID   string `json:"group_ext_id"`
	Month        int    `json:"month"`
	Year         int    `json:"year"`
	DayKey       int    `json:"day_key"`
	Date         string `json:"date"`
	Weekday      string `json:"weekday"`
	LessonNumber int    `json:"lesson_number"`
	Time         string `json:"time"`
	Start        string `json:"start_time"`
	End          string `json:"end_time"`
	Group        string `json:"group_name"`
	Subgroup     string `json:"subgroup"`
	Discipline   string `json:"discipline"`
	Type         string `json:"lesson_type"`
	Room         string `json:"room"`
	Teacher      string `json:"teacher"`
	ImportedAt   int64  `json:"imported_at"`
}

// UpsertGroups сохраняет каталог групп.
//
// Возвращает количество записей, которых в базе не было, — админу важно видеть
// «добавлено 12 групп», а не голое «ок»: синхронизация запускается вручную, и
// тишина неотличима от сбоя.
func (s *ScheduleSourceStore) UpsertGroups(ctx context.Context, source string, groups []SourceGroup) (int, error) {
	if strings.TrimSpace(source) == "" {
		return 0, errors.New("store: пустое имя источника расписания")
	}
	if len(groups) == 0 {
		return 0, errors.New("store: пустой каталог групп не сохраняется")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: начать транзакцию каталога групп: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO schedule_source_groups (source, external_id, name, department, course, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (source, external_id) DO UPDATE SET
			name       = excluded.name,
			department = excluded.department,
			course     = excluded.course,
			updated_at = excluded.updated_at
	`)
	if err != nil {
		return 0, fmt.Errorf("store: подготовить запись группы: %w", err)
	}
	defer stmt.Close()

	added := 0
	now := time.Now().Unix()

	for _, group := range groups {
		externalID := trim(group.ExternalID, maxScheduleNameLength)
		name := trim(group.Name, maxScheduleNameLength)
		if externalID == "" || name == "" {
			// Узел без имени или id в каталоге быть не может — пропускаем,
			// как и парсер.
			continue
		}

		var exists int
		err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM schedule_source_groups WHERE source = ? AND external_id = ?`,
			source, externalID,
		).Scan(&exists)
		if err != nil {
			return 0, fmt.Errorf("store: проверить группу %s: %w", externalID, err)
		}
		if exists == 0 {
			added++
		}

		if _, err := stmt.ExecContext(ctx,
			source, externalID, name,
			trim(group.Department, maxScheduleTextLength),
			trim(group.Course, maxScheduleTextLength),
			now,
		); err != nil {
			return 0, fmt.Errorf("store: записать группу %s: %w", externalID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: зафиксировать каталог групп: %w", err)
	}

	return added, nil
}

// ListGroups — группы каталога, опционально отфильтрованные подстрокой имени.
//
// Поиск сделан в Go, а не в SQL, и это не лень: LIKE в SQLite не
// регистронезависим для кириллицы (его lower() знает только ASCII), поэтому
// «б-исит» не нашёл бы «Б-ИСиТ-22». Каталог — 587 строк, они помещаются в
// память запроса целиком; при переезде на PostgreSQL (T2.1) фильтр можно будет
// вернуть в SQL через ILIKE, изменив только этот метод.
func (s *ScheduleSourceStore) ListGroups(ctx context.Context, source, query string, limit int) ([]SourceGroup, error) {
	if limit <= 0 || limit > 2000 {
		limit = 200
	}

	rows, err := s.db.QueryContext(
		ctx,
		`
		SELECT external_id, name, department, course, updated_at
		FROM schedule_source_groups
		WHERE source = ?
		ORDER BY name ASC, external_id ASC
		`,
		source,
	)
	if err != nil {
		return nil, fmt.Errorf("store: прочитать каталог групп: %w", err)
	}
	defer rows.Close()

	needle := strings.ToLower(strings.TrimSpace(query)) // Lowercase в Go складывает и кириллицу.

	groups := make([]SourceGroup, 0, 64)
	for rows.Next() {
		var g SourceGroup
		if err := rows.Scan(&g.ExternalID, &g.Name, &g.Department, &g.Course, &g.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: разобрать строку группы: %w", err)
		}
		if needle != "" && !strings.Contains(strings.ToLower(g.Name), needle) {
			continue
		}
		if len(groups) >= limit {
			break
		}
		groups = append(groups, g)
	}

	return groups, rows.Err()
}

// ErrAmbiguousGroup — под запрос нашлась не одна группа.
//
// Отдельная ошибка (а не текст), потому что ответ разный: «не найдено» — это
// 404 с подсказкой «синхронизируйте каталог», а неоднозначность — 422 со
// списком id, из которого админ выбирает. Сравнивать их по подстроке в
// русском тексте — способ, который ломается от одной переформулировки.
var ErrAmbiguousGroup = errors.New("ambiguous group")

// ResolveGroup — группа по id или по точному имени.
//
// Нужен импорту: админ выбирает группу в списке, а запрос к источнику идёт по
// id; при этом живой ввод именем («Б-ИСиТ-22») должен работать так же, как в
// Python-версии, где find_group поднимал исключение на неоднозначности.
//
// Ошибкой возвращаются и «не найдено», и «найдено несколько» — вторая важна
// особенно: в каталоге есть одноимённые группы разных институтов, и молча
// импортировать первую из них означало бы повесить чужое расписание на
// страницу.
//
// Сравнение имени идёт в Go, как и в ListGroups, и это не вкусовщина: lower() в
// SQLite знает только ASCII, поэтому «LOWER(name) = LOWER(?)» не нашёл бы
// «б-исит-22» — ровно тот ввод, который печатает человек в русской раскладке.
// Первая версия этого метода так и делала, и тест нашёл её немедленно.
func (s *ScheduleSourceStore) ResolveGroup(ctx context.Context, source, idOrName string) (*SourceGroup, error) {
	needle := strings.TrimSpace(idOrName)
	if needle == "" {
		return nil, ErrNotFound
	}

	rows, err := s.db.QueryContext(
		ctx,
		`
		SELECT external_id, name, department, course, updated_at
		FROM schedule_source_groups
		WHERE source = ?
		ORDER BY name ASC, external_id ASC
		`,
		source,
	)
	if err != nil {
		return nil, fmt.Errorf("store: найти группу %q: %w", needle, err)
	}
	defer rows.Close()

	wantName := strings.ToLower(needle)

	var byID []SourceGroup
	var byName []SourceGroup

	for rows.Next() {
		var g SourceGroup
		if err := rows.Scan(&g.ExternalID, &g.Name, &g.Department, &g.Course, &g.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: разобрать найденную группу: %w", err)
		}
		switch {
		case g.ExternalID == needle:
			byID = append(byID, g)
		case strings.ToLower(strings.TrimSpace(g.Name)) == wantName:
			byName = append(byName, g)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: прочитать каталог при поиске группы: %w", err)
	}

	// Id важнее имени: (source, external_id) уникален, поэтому совпадение по id
	// всегда ровно одно — и админ, введший «494», получает группу 494.
	found := byID
	if len(found) == 0 {
		found = byName
	}

	switch len(found) {
	case 0:
		return nil, ErrNotFound
	case 1:
		group := found[0]
		return &group, nil
	default:
		names := make([]string, 0, len(found))
		for _, g := range found {
			names = append(names, fmt.Sprintf("%s (%s)", g.ExternalID, g.Name))
		}
		return nil, fmt.Errorf("%w: группа %q встречается в каталоге %d раз — уточните id (%s)",
			ErrAmbiguousGroup, needle, len(found), strings.Join(names, ", "))
	}
}

// LastGroupSync — когда каталог групп обновлялся в последний раз (0 — пусто).
func (s *ScheduleSourceStore) LastGroupSync(ctx context.Context, source string) (int64, error) {
	var at sql.NullInt64

	err := s.db.QueryRowContext(ctx,
		`SELECT MAX(updated_at) FROM schedule_source_groups WHERE source = ?`, source,
	).Scan(&at)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("store: прочитать время синхронизации групп: %w", err)
	}
	if !at.Valid {
		return 0, nil
	}
	return at.Int64, nil
}

// ReplaceMonth — импорт месяца одной группы.
//
// Внутри одна транзакция: удаление старых строк пакета'ов и вставка новых.
// Раздельные шаги оставили бы страницу без расписания на то время, пока
// второй шаг не выполнен, — а при ошибке вставки так и вовсе до следующего
// импорта.
//
// Пары с одинаковым external_id внутри одного ответа считаются одной парой
// (ON CONFLICT переписывает строку), а не двумя: источник выдаёт id
// однозначно, и повтор — это уже аномалия формата, которую нельзя превращать в
// дубль на странице.
func (s *ScheduleSourceStore) ReplaceMonth(ctx context.Context, source, groupExtID string, month, year int, lessons []SourceLesson) (int, error) {
	if strings.TrimSpace(source) == "" || strings.TrimSpace(groupExtID) == "" {
		return 0, errors.New("store: импорт без источника или без группы")
	}
	if month < 1 || month > 12 || year < 2000 || year > 2100 {
		return 0, fmt.Errorf("store: импорт за %02d.%d — дата вне диапазона", month, year)
	}
	if len(lessons) > maxScheduleBatchSize {
		return 0, fmt.Errorf("store: в пакете %d пар, лимит %d", len(lessons), maxScheduleBatchSize)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: начать транзакцию импорта: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM schedule_source_lessons WHERE source = ? AND group_ext_id = ? AND month = ? AND year = ?`,
		source, groupExtID, month, year,
	); err != nil {
		return 0, fmt.Errorf("store: очистить пакет %s/%02d.%d: %w", groupExtID, month, year, err)
	}

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO schedule_source_lessons (
			source, external_id, group_ext_id, month, year, day_key, date, weekday,
			lesson_number, time, start_time, end_time, group_name, subgroup,
			discipline, lesson_type, room, teacher, imported_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (source, group_ext_id, external_id) DO UPDATE SET
			month         = excluded.month,
			year          = excluded.year,
			day_key       = excluded.day_key,
			date          = excluded.date,
			weekday       = excluded.weekday,
			lesson_number = excluded.lesson_number,
			time          = excluded.time,
			start_time    = excluded.start_time,
			end_time      = excluded.end_time,
			group_name    = excluded.group_name,
			subgroup      = excluded.subgroup,
			discipline    = excluded.discipline,
			lesson_type   = excluded.lesson_type,
			room          = excluded.room,
			teacher       = excluded.teacher,
			imported_at   = excluded.imported_at
	`)
	if err != nil {
		return 0, fmt.Errorf("store: подготовить вставку пар: %w", err)
	}
	defer stmt.Close()

	now := time.Now().Unix()
	stored := 0

	for _, lesson := range lessons {
		externalID := trim(lesson.ExternalID, maxScheduleNameLength)
		if externalID == "" {
			continue
		}

		dayKey := lesson.DayKey
		if dayKey <= 0 {
			// Дата не читается: пара всё равно показывается (у источника
			// бывают переносы без даты), но сортируется в конце — тот же
			// смысл, что и day_key = 99999999 у парсера.
			dayKey = 99999999
		}

		if _, err := stmt.ExecContext(ctx,
			source, externalID, groupExtID, month, year, dayKey,
			trim(lesson.Date, 20),
			trim(lesson.Weekday, 8),
			lesson.LessonNumber,
			trim(lesson.Time, 20),
			trim(lesson.Start, 8),
			trim(lesson.End, 8),
			trim(lesson.Group, maxScheduleTextLength),
			trim(lesson.Subgroup, maxScheduleNameLength),
			trim(lesson.Discipline, maxScheduleTextLength),
			trim(lesson.Type, 40),
			trim(lesson.Room, 80),
			trim(lesson.Teacher, maxScheduleTeacherLength),
			now,
		); err != nil {
			return 0, fmt.Errorf("store: записать пару %s: %w", externalID, err)
		}
		stored++
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: зафиксировать импорт: %w", err)
	}

	return stored, nil
}

// ListLessons — пары группы в диапазоне дней [fromKey, toKey] включительно.
//
// Границы — YYYYMMDD, а не unix-секунды: фильтрация по «неделе» тогда не
// зависит от часовой зоны сервера, и проверка «сегодняшние пары» даёт ровно
// тот день, который назвал вуз.
func (s *ScheduleSourceStore) ListLessons(ctx context.Context, source, groupExtID string, fromKey, toKey int) ([]SourceLesson, error) {
	if toKey < fromKey {
		return nil, fmt.Errorf("store: пустой диапазон дней %d..%d", fromKey, toKey)
	}

	rows, err := s.db.QueryContext(
		ctx,
		`
		SELECT external_id, group_ext_id, month, year, day_key, date, weekday,
		       lesson_number, time, start_time, end_time, group_name, subgroup,
		       discipline, lesson_type, room, teacher, imported_at
		FROM schedule_source_lessons
		WHERE source = ? AND group_ext_id = ? AND day_key BETWEEN ? AND ?
		ORDER BY day_key ASC, lesson_number ASC, time ASC, discipline ASC, room ASC
		`,
		source, groupExtID, fromKey, toKey,
	)
	if err != nil {
		return nil, fmt.Errorf("store: прочитать пары группы %s: %w", groupExtID, err)
	}
	defer rows.Close()

	lessons := make([]SourceLesson, 0, 32)
	for rows.Next() {
		var l SourceLesson
		if err := rows.Scan(
			&l.ExternalID, &l.GroupExtID, &l.Month, &l.Year, &l.DayKey, &l.Date, &l.Weekday,
			&l.LessonNumber, &l.Time, &l.Start, &l.End, &l.Group, &l.Subgroup,
			&l.Discipline, &l.Type, &l.Room, &l.Teacher, &l.ImportedAt,
		); err != nil {
			return nil, fmt.Errorf("store: разобрать строку пары: %w", err)
		}
		lessons = append(lessons, l)
	}

	return lessons, rows.Err()
}

// LastImportAt — время последней импортированной пары группы (0 — импорта не
// было). Нужно подписью «обновлено» на странице: без него пустой список
// неотличим от «данные старые».
func (s *ScheduleSourceStore) LastImportAt(ctx context.Context, source, groupExtID string) (int64, error) {
	var at sql.NullInt64

	err := s.db.QueryRowContext(ctx,
		`SELECT MAX(imported_at) FROM schedule_source_lessons WHERE source = ? AND group_ext_id = ?`,
		source, groupExtID,
	).Scan(&at)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("store: прочитать время импорта группы %s: %w", groupExtID, err)
	}
	if !at.Valid {
		return 0, nil
	}
	return at.Int64, nil
}

// ImportedMonths — какие месяцы уже лежат в базе для группы.
//
// По этому списку страница решает, показывать ли «импорта этого месяца нет»
// вместо пустой недели: пусто в июле и «мы ещё не качали сентябрь» — разные
// ситуации, и врать про первую из них незачем.
func (s *ScheduleSourceStore) ImportedMonths(ctx context.Context, source, groupExtID string) ([]MonthKey, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`
		SELECT month, year, COUNT(*), MAX(imported_at)
		FROM schedule_source_lessons
		WHERE source = ? AND group_ext_id = ?
		GROUP BY month, year
		ORDER BY year ASC, month ASC
		`,
		source, groupExtID,
	)
	if err != nil {
		return nil, fmt.Errorf("store: прочитать месяцы группы %s: %w", groupExtID, err)
	}
	defer rows.Close()

	months := make([]MonthKey, 0, 4)
	for rows.Next() {
		var m MonthKey
		if err := rows.Scan(&m.Month, &m.Year, &m.Lessons, &m.ImportedAt); err != nil {
			return nil, fmt.Errorf("store: разобрать строку месяца: %w", err)
		}
		months = append(months, m)
	}

	sort.Slice(months, func(i, j int) bool {
		if months[i].Year != months[j].Year {
			return months[i].Year < months[j].Year
		}
		return months[i].Month < months[j].Month
	})

	return months, rows.Err()
}

// MonthKey — импортированный месяц группы с числом пар.
type MonthKey struct {
	Month      int   `json:"month"`
	Year       int   `json:"year"`
	Lessons    int   `json:"lessons"`
	ImportedAt int64 `json:"imported_at"`
}

// Stats — сколько данных источника лежит в базе. Ответ /api/schedule/source/status.
type SourceStats struct {
	Groups         int   `json:"groups"`
	Lessons        int   `json:"lessons"`
	GroupsAt       int64 `json:"groups_synced_at"`
	ImportedAt     int64 `json:"last_import_at"`
	ImportedGroups int   `json:"groups_with_lessons"`
}

// Stats считает всё одним запросом на таблицу, а не по группам: панель админа
// открывается часто, а расхождение в этих цифрах видно невооружённым глазом.
func (s *ScheduleSourceStore) Stats(ctx context.Context, source string) (*SourceStats, error) {
	stats := &SourceStats{}

	err := s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM schedule_source_groups WHERE source = ?),
			(SELECT COUNT(*) FROM schedule_source_lessons WHERE source = ?),
			(SELECT COALESCE(MAX(updated_at), 0) FROM schedule_source_groups WHERE source = ?),
			(SELECT COALESCE(MAX(imported_at), 0) FROM schedule_source_lessons WHERE source = ?),
			(SELECT COUNT(DISTINCT group_ext_id) FROM schedule_source_lessons WHERE source = ?)
	`, source, source, source, source, source).
		Scan(&stats.Groups, &stats.Lessons, &stats.GroupsAt, &stats.ImportedAt, &stats.ImportedGroups)
	if err != nil {
		return nil, fmt.Errorf("store: собрать статистику источника: %w", err)
	}

	return stats, nil
}

// trim — граница поля с нормализацией пробелов.
//
// Обрезка по рунам, а не по байтам: срединный байт кириллицы превратился бы в
// \ufffd на странице.
func trim(value string, limit int) string {
	value = collapseSpaces(value)
	if limit <= 0 || utf8Len(value) <= limit {
		return value
	}

	runes := []rune(value)
	return strings.TrimSpace(string(runes[:limit]))
}

func collapseSpaces(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func utf8Len(value string) int {
	return len([]rune(value))
}
