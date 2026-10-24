package apeks

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Разбор ответа источника (порт parser.py).
//
// Правила перенесены один в один, потому что они сняты с того, как расписание
// рисует сама страница вуза, а не с нашей фантазии про неё:
//
//   - номер пары считается не по id, а по времени начала: lessonTimes сначала
//     собираются в словарь по id, потом сортируются по hour_from*60+minute_from
//     — отсюда и порядок «Ранняя пара раньше Поздней», даже если id у них
//     обратные;
//   - подгруппа определяется каскадом: subgroup_id -> subgroupNames ->
//     groupsList["Подгруппы"], затем «Поточка» по superflowGroupsIds (>2 групп)
//     или по числу токенов в имени, затем «/1» и «/2» в имени группы;
//   - пустая пара и самоподготовка подменяют дисциплину;
//   - итоговая сортировка — (дата, номер пары, время, дисциплина, аудитория).
//
// Единственное расхождение с оригиналом — stripHTML для аудитории (см.
// комментарий в value.go): наш фронтенд экранирует текст, поэтому тег
// нужно вырезать, а не оставлять.

// weekdayNames — подписи дней недели, Пн первым (как WEEKDAYS в parser.py).
var weekdayNames = [...]string{"Пн", "Вт", "Ср", "Чт", "Пт", "Сб", "Вс"}

// unknownDayKey — дата, которую не удалось прочитать.
//
// Число 99999999 играет роль «сортируется в конце»: так пара с побитой датой
// не встанет между уроками, а уедет в конец списка, где её видно. Python-версия
// делала то же через date.max.
const unknownDayKey = 99999999

// Lesson — одна строка расписания в том виде, в котором её показывают.
type Lesson struct {
	// ExternalID — id пары у источника. Ключ дедупликации импорта: повторный
	// импорт того же месяца не должен плодить вторую копию пары.
	ExternalID string

	// Date — исходная дата строкой ("02.10.2026"): её показываем, по ней же
	// сверяемся при разборе.
	Date string

	// DayKey — дата числом YYYYMMDD (20261002). Хранится и фильтруется по ней,
	// чтобы диапазон недель не зависел от часовой зоны сервера: источник
	// называет день, мы его и храним.
	DayKey int

	Weekday      string // "Пн".."Вс", пусто для нечитаемой даты
	LessonNumber int    // 1..N по времени начала
	Time         string // "08:30 – 10:05" (тире — такое же, как у источника)
	Start        string // "08:30"
	End          string // "10:05"

	Group      string // имя группы (и со-участники потоковых пар)
	Subgroup   string // "1 подгруппа" | "2 подгруппа" | "Поточка" | "—" | имя из источника
	Discipline string
	Type       string // "Лек" | "Пр" | "Лб" | "Инд"
	Room       string // аудитория, без HTML
	Teacher    string // подписи преподавателей через запятую
}

// payload — поля ответа источника, которые участвуют в разборе.
//
// Разбираются только они: всё остальное (journal_lesson_id, topic_code,
// расписания звонков lessonTimesEnabled) при добавлении нового поля у источника
// не должно ломать импорт, поэтому строгой структуры на весь ответ нет.
type payload struct {
	GroupID       flex                         `json:"group_id"`
	Group         payloadGroup                 `json:"group"`
	StartDate     string                       `json:"start_date"`
	EndDate       string                       `json:"end_date"`
	Lessons       []rawLesson                  `json:"lessons"`
	LessonTimes   []rawLessonTime              `json:"lessonTimes"`
	GroupNames    map[string]string            `json:"groupNames"`
	SubgroupNames map[string]string            `json:"subgroupNames"`
	GroupsList    map[string]map[string]string `json:"groupsList"`
}

type payloadGroup struct {
	ID   flex   `json:"id"`
	Name string `json:"name"`
}

type rawLessonTime struct {
	ID         flex `json:"id"`
	HourFrom   flex `json:"hour_from"`
	MinuteFrom flex `json:"minute_from"`
	HourTo     flex `json:"hour_to"`
	MinuteTo   flex `json:"minute_to"`
	FullName   flex `json:"fullName"`
}

type rawLesson struct {
	ID                    flex   `json:"id"`
	Date                  string `json:"date"`
	LessonTimeID          flex   `json:"lesson_time_id"`
	SubgroupID            flex   `json:"subgroup_id"`
	GroupName             string `json:"groupName"`
	Discipline            string `json:"discipline"`
	Classroom             string `json:"classroom"`
	ClassTypeName         string `json:"class_type_name"`
	StaffNames            list   `json:"staffNames"`
	SuperflowGroupsIds    list   `json:"superflowGroupsIds"`
	SuperflowSubgroupsIds list   `json:"superflowSubgroupsIds"`
	IsEmpty               flag   `json:"is_empty"`
	SelfWork              flag   `json:"self_work"`
}

// ParseMonth разбирает содержимое поля "data" в список пар.
//
// Пустой lessons — НЕ ошибка: у источника месяцы каникул выглядят ровно так,
// и страница обязана показать «пар нет», а не «импорт не удался». Ошибкой
// считается только неразобранный ответ целиком.
func ParseMonth(data []byte) (*Month, error) {
	var p payload
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, errors.New("apeks: ответ источника расписания пришёл в неизвестном формате")
	}

	order := lessonOrder(p.LessonTimes)
	times := lessonTimesByID(p.LessonTimes)

	lessons := make([]Lesson, 0, len(p.Lessons))
	for _, raw := range p.Lessons {
		externalID := raw.ID.String()
		if externalID == "" {
			// Пара без id не может быть ни показана, ни загружена повторно
			// (дедупликация идёт по нему) — пропускаем, как и Python-версия,
			// где такие записи не доходили до renderer.
			continue
		}

		date := norm(raw.Date)
		slot, hasSlot := times[raw.LessonTimeID.String()]

		start, end, timeText := "", "", ""
		if hasSlot {
			start, end, timeText = slot.start(), slot.end(), slot.timeText()
		}

		lessons = append(lessons, Lesson{
			ExternalID:   externalID,
			Date:         date,
			DayKey:       dayKey(date),
			Weekday:      weekday(date),
			LessonNumber: order[raw.LessonTimeID.String()],
			Time:         timeText,
			Start:        start,
			End:          end,
			Group:        groupDisplay(raw, &p),
			Subgroup:     determineSubgroup(raw, &p),
			Discipline:   disciplineOf(raw),
			Type:         norm(raw.ClassTypeName),
			Room:         stripHTML(raw.Classroom),
			Teacher:      strings.Join(raw.StaffNames.Values(), ", "),
		})
	}

	sort.SliceStable(lessons, func(i, j int) bool {
		a, b := lessons[i], lessons[j]
		if a.DayKey != b.DayKey {
			return a.DayKey < b.DayKey
		}
		if a.LessonNumber != b.LessonNumber {
			return a.LessonNumber < b.LessonNumber
		}
		if a.Time != b.Time {
			return a.Time < b.Time
		}
		if a.Discipline != b.Discipline {
			return a.Discipline < b.Discipline
		}
		return a.Room < b.Room
	})

	groupName := norm(p.Group.Name)
	if groupName == "" {
		// Ответ по одной группе всегда приносит group.name; пустое имя —
		// признак того, что эндпоинт отдаёт что-то другое. Пишем в месяц id,
		// чтобы страница могла честно сказать «группа 494».
		groupName = groupIDOf(p)
	}

	return &Month{
		GroupID:   groupIDOf(p),
		GroupName: groupName,
		StartDate: norm(p.StartDate),
		EndDate:   norm(p.EndDate),
		Lessons:   lessons,
	}, nil
}

// groupIDOf — id группы, для которой пришёл ответ.
//
// Сначала верхнее поле group_id (его подставляет сам запрос), затем group.id:
// на живом ответе есть оба, но каталог групп group.id не отдаёт, поэтому
// запасной путь нужен только ради устойчивости к смене формата.
func groupIDOf(p payload) string {
	if id := p.GroupID.String(); id != "" {
		return id
	}
	return p.Group.ID.String()
}

// lessonTimesByID — словарь slot'ов по id (как lesson_times в Python).
func lessonTimesByID(slots []rawLessonTime) map[string]rawLessonTime {
	result := make(map[string]rawLessonTime, len(slots))
	for _, slot := range slots {
		id := slot.ID.String()
		if id == "" {
			continue
		}
		result[id] = slot
	}
	return result
}

// lessonOrder — номер пары по времени начала, 1..N.
//
// Сортировка устойчивая по (минуты начала, id), поэтому два slot'а с одинаковым
// временем получают предсказуемые номера: у источника такие есть (факультативы
// вечером), и без tie-break'а нумерация менялась бы от запуска к запуску из-за
// порядка обхода карты.
func lessonOrder(slots []rawLessonTime) map[string]int {
	sorted := make([]rawLessonTime, 0, len(slots))
	for _, slot := range slots {
		if slot.ID.String() == "" {
			continue
		}
		sorted = append(sorted, slot)
	}

	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		am, bm := a.startMinutes(), b.startMinutes()
		if am != bm {
			return am < bm
		}
		return a.ID.String() < b.ID.String()
	})

	result := make(map[string]int, len(sorted))
	for index, slot := range sorted {
		result[slot.ID.String()] = index + 1
	}
	return result
}

func (s rawLessonTime) startMinutes() int {
	return s.HourFrom.Int()*60 + s.MinuteFrom.Int()
}

func (s rawLessonTime) start() string {
	return clock(s.HourFrom.Int(), s.MinuteFrom.Int())
}

func (s rawLessonTime) end() string {
	return clock(s.HourTo.Int(), s.MinuteTo.Int())
}

// timeText — «08:30 – 10:05»; пустой слот даёт пустую строку, а не «00:00 –
// 00:00»: пара без времени у источника бывает, и ноль на её месте выглядит
// как данные.
func (s rawLessonTime) timeText() string {
	if s.ID.String() == "" {
		return ""
	}
	return s.start() + " – " + s.end()
}

func clock(hour, minute int) string {
	return fmt.Sprintf("%02d:%02d", hour, minute)
}

// disciplineOf — что показать вместо названия предмета.
//
// Порядок проверок важен: is_empty идёт раньше self_work, как в Python. Если
// источник поднимет оба флага, пара станет «Пустая пара» — это безопасная
// формулировка, а не выдуманная самоподготовка.
func disciplineOf(raw rawLesson) string {
	if raw.IsEmpty.Bool() {
		return "Пустая пара"
	}
	if raw.SelfWork.Bool() {
		return "Самоподготовка"
	}
	return norm(raw.Discipline)
}

// determineSubgroup — каскад из parser.py, без изменений по смыслу.
func determineSubgroup(raw rawLesson, p *payload) string {
	if id := raw.SubgroupID.String(); id != "" {
		if name := norm(p.SubgroupNames[id]); name != "" {
			return name
		}
		if name := norm(p.GroupsList["Подгруппы"]["subgroup-"+id]); name != "" {
			return name
		}
	}

	if len(raw.SuperflowGroupsIds.Values()) > 2 {
		return "Поточка"
	}

	groupName := norm(raw.GroupName)
	if len(splitTokens(groupName)) > 2 {
		return "Поточка"
	}
	if strings.Contains(groupName, "/1") {
		return "1 подгруппа"
	}
	if strings.Contains(groupName, "/2") {
		return "2 подгруппа"
	}
	return "—"
}

// groupDisplay — имя группы плюс со-участники пары.
//
// Имена из superflowGroupsIds добавляются только те, которых ещё нет в строке
// группы: иначе «Б-ИСиТ-21, Б-ИСиТ-22» превратилось бы в «Б-ИСиТ-21,
// Б-ИСиТ-22, Б-ИСиТ-21».
func groupDisplay(raw rawLesson, p *payload) string {
	group := norm(raw.GroupName)
	parts := make([]string, 0, 1+len(raw.SuperflowGroupsIds)+len(raw.SuperflowSubgroupsIds))
	if group != "" {
		parts = append(parts, group)
	}

	for _, id := range raw.SuperflowGroupsIds.Values() {
		name := norm(p.GroupNames[id])
		if name != "" && !strings.Contains(group, name) {
			parts = append(parts, name)
		}
	}
	for _, id := range raw.SuperflowSubgroupsIds.Values() {
		if name := norm(p.SubgroupNames[id]); name != "" {
			parts = append(parts, name)
		}
	}

	return strings.Join(parts, ", ")
}

// splitTokens — части строки по разделителям списка (запятая, точка с
// запятой, перевод строки).
func splitTokens(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n'
	})

	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if trimmed := norm(field); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// dayKey — дата источника как YYYYMMDD.
func dayKey(date string) int {
	t, ok := parseDate(date)
	if !ok {
		return unknownDayKey
	}
	return t.Year()*10000 + int(t.Month())*100 + t.Day()
}

// weekday — подпись дня недели по дате источника ("" если дата не читается).
func weekday(date string) string {
	t, ok := parseDate(date)
	if !ok {
		return ""
	}
	// time.Weekday(): воскресенье = 0, а список WEEKDAYS начинается с понедельника.
	return weekdayNames[(int(t.Weekday())+6)%7]
}

// parseDate читает «ДД.ММ.ГГГГ» (и «Д.М.ГГГГ» — такое у источника тоже
// встречалось в подписях, поэтому формат слабее, чем кажется).
//
// Парсим сами, а не time.Parse("02.01.2006"): строгий формат отбросил бы
// «2.10.2026», и пара уехала бы в конец списка с пустым днём недели.
func parseDate(value string) (time.Time, bool) {
	trimmed := norm(value)
	if trimmed == "" {
		return time.Time{}, false
	}

	parts := strings.Split(trimmed, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}

	var numbers [3]int
	for i, part := range parts {
		n, ok := toInt(part)
		if !ok {
			return time.Time{}, false
		}
		numbers[i] = n
	}

	day, month, year := numbers[0], numbers[1], numbers[2]
	if year < 1000 || year > 9999 {
		return time.Time{}, false
	}

	// 0..32 вместо строгой календарной проверки: цель — не показать
	// выдуманную дату, а не отвергнуть ответ источника из-за неё.
	if month < 1 || month > 12 || day < 1 || day > 31 {
		return time.Time{}, false
	}

	t := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if t.Year() != year || int(t.Month()) != month || t.Day() != day {
		// 31.02 превратилось бы в 03.03 — это не дата источника, отбрасываем.
		return time.Time{}, false
	}
	return t, true
}

func toInt(value string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0, false
	}
	return n, true
}
