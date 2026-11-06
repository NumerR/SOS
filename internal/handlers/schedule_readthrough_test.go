package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NumerR/SOS/internal/apeks"
	"github.com/NumerR/SOS/internal/store"
)

// Тесты сквозного чтения расписания (ADR-0042).
//
// Проверяется ровно то, из-за чего путь появился: «вписал группу — увидел её
// расписание», а не «увидел расписание ровно тех групп, которые админ успел
// нажать». Отсюда и набор: недостающий месяц привозится, уже лежащийся не
// трогается вообще, отказ источника не пустит страницу на 502, а архив
// переживает смерть источника.
//
// Вторая половина смысла — границы: обращений не больше, чем месяцев в показе,
// параллельные читатели не умножают удары по сайту вуза, и ни один ответ не
// содержит токена.

// apeksMonthSource — заглушка источника, отвечающая на любой month/year.
//
// Пары датированы 2-м и 6-м числами запрошенного месяца: «внутри недели» и
// «до неё» различимы по day_key, и тест видит, что привезённый пакет
// соответствует именно тому месяцу, о котором просили, а не вчерашнему ответу.
// Дисциплина несёт месяц: так же проверяется, что в базу лёг нужный пакет.
func apeksMonthSource(calls *atomic.Int64, delay time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if delay > 0 {
			time.Sleep(delay)
		}

		month, _ := strconv.Atoi(r.URL.Query().Get("month"))
		year, _ := strconv.Atoi(r.URL.Query().Get("year"))
		if month == 0 || year == 0 {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}

		_, _ = w.Write([]byte(apeksMonthPayloadOf(month, year)))
	}
}

func apeksMonthPayloadOf(month, year int) string {
	day := func(n int) string { return fmt.Sprintf("%02d.%02d.%d", n, month, year) }
	id := year*100 + month

	return fmt.Sprintf(`{"data":{
		"groupName":"Б-ИСиТ-22",
		"startDate":"%s","endDate":"%s",
		"lessonTimes":[{"id":1,"hour_from":8,"minute_from":30,"hour_to":10,"minute_to":0,"fullName":"08:30 – 10:00"}],
		"lessons":[
			{"id":%d1,"date":"%s","lesson_time_id":1,"groupName":"Б-ИСиТ-22",
			 "discipline":"Ранняя %02d","class_type_name":"Лекция","classroom":"201",
			 "staffNames":["Иванов И.И."],"subgroup_id":0,"subgroupNames":"","is_empty":0,"self_work":""},
			{"id":%d2,"date":"%s","lesson_time_id":1,"groupName":"Б-ИСиТ-22",
			 "discipline":"Поздняя %02d","class_type_name":"Практика","classroom":"203",
			 "staffNames":["Петров П.П."],"subgroup_id":0,"subgroupNames":"","is_empty":0,"self_work":""}
		]
	}}`, day(1), day(28), id, day(2), month, id, day(6), month)
}

// apeksLessonsOn — пакет месяца, в котором пары стоят на указанные числа.
//
// Заглушка выше ставит занятия на 2-е и 6-е число месяца, и для недели через
// границу месяцев этого мало: проверка должна видеть пару каждого месяца именно
// внутри показанных семи дней, иначе пустой ответ нельзя отличить от
// непривезённого месяца.
func apeksLessonsOn(month, year int, days ...int) string {
	lessons := make([]string, 0, len(days))
	for i, day := range days {
		lessons = append(lessons, fmt.Sprintf(`{"id":%d,"date":"%02d.%02d.%d","lesson_time_id":1,
			"groupName":"Б-ИСиТ-22","discipline":"Пара %02d.%02d","class_type_name":"Лекция",
			"classroom":"201","staffNames":["Иванов И.И."],"subgroup_id":0,"subgroupNames":"",
			"is_empty":0,"self_work":""}`, year*10000+month*100+i, day, month, year, day, month))
	}

	return fmt.Sprintf(`{"data":{
		"groupName":"Б-ИСиТ-22","startDate":"01.%02d.%d","endDate":"28.%02d.%d",
		"lessonTimes":[{"id":1,"hour_from":8,"minute_from":30,"hour_to":10,"minute_to":0,"fullName":"08:30 – 10:00"}],
		"lessons":[%s]
	}}`, month, year, month, year, strings.Join(lessons, ","))
}

// coverageOf — состояние месяцев группы так, его видит read-through.
func coverageOf(t *testing.T, e *scheduleSourceEnv, group string) []store.MonthState {
	t.Helper()

	states, err := e.src.MonthCoverage(context.Background(), apeks.Source, group)
	if err != nil {
		t.Fatalf("прочитать покрытие %s: %v", group, err)
	}
	return states
}

func coverageRow(states []store.MonthState, month, year int) (store.MonthState, bool) {
	for _, s := range states {
		if s.Month == month && s.Year == year {
			return s, true
		}
	}
	return store.MonthState{}, false
}

func stateOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()

	state, ok := decode(t, rec)["source_state"].(string)
	if !ok {
		t.Fatalf("source_state отсутствует в ответе: %s", rec.Body.String())
	}
	return state
}

// decodeBody — разбор без обращения к t: нужен горутине параллельного теста,
// где t.Fatalf из чужой горутины запрещён.
func decodeBody(rec *httptest.ResponseRecorder) map[string]any {
	var payload map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &payload)
	return payload
}

// --- Основной смысл: группа читается по требованию ---------------------------

// TestReadThroughFetchesGroupNobodyImported — «вписал группу — читается её таблица».
//
// Раньше этот же запрос отвечал пустой неделей и подсказкой «ещё не
// импортировано»: расписание существовало только для тех групп, которых админ
// притащил кнопкой. Проверка обязательной части: месяц привезён, пары показаны,
// и они же остались в базе.
func TestReadThroughFetchesGroupNobodyImported(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t, store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22", Department: "ИИТ", Course: "2"})

	calls := e.attachApeks(t, apeksMonthSource(new(atomic.Int64), 0))
	rec := lessonsQuery(e.h, "/api/schedule/lessons?group=494&month=10&year=2026")
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d: %s", rec.Code, rec.Body.String())
	}

	body := decode(t, rec)
	days, _ := body["days"].([]any)
	if len(days) != 2 {
		t.Fatalf("дней в ответе %d, ожидалось 2 привезённых: %v", len(days), days)
	}
	if got := stateOf(t, rec); got != "refreshed" {
		t.Errorf("source_state = %q, ожидалось refreshed — месяц привезли на этом запросе", got)
	}
	if body["range_imported"] != true {
		t.Errorf("range_imported = %v при только что привезённом месяце", body["range_imported"])
	}
	if body["course"] != "2" || body["department"] != "ИИТ" {
		t.Errorf("институт и курс в ответе: %v / %v — странице вкладок нужны оба", body["department"], body["course"])
	}
	if updated, _ := body["updated_at"].(float64); updated == 0 {
		t.Error("updated_at = 0 — подпись «обновлено» нечем построить")
	}
	if strings.Contains(rec.Body.String(), testApeksToken) {
		t.Fatal("токен источника ушёл браузеру: read-through обязан ходить к вузу сервером")
	}

	// Одиночный удар по сайту вуза — не два и не ноль.
	if calls.Load() != 1 {
		t.Errorf("обращений к источнику %d, ожидалось 1", calls.Load())
	}

	// Привезённое легло в базу: страница показывает разобранные пары, а не
	// пересланный ответ источника.
	stored := e.lessons(t, "494")
	if len(stored) != 2 {
		t.Fatalf("в базе %d пар, ожидалось 2", len(stored))
	}
	for _, lesson := range stored {
		if lesson.Month != 10 || lesson.Year != 2026 {
			t.Errorf("в пакет %02d.%d попала пара %+v", lesson.Month, lesson.Year, lesson)
		}
	}

	rows, had := coverageRow(coverageOf(t, e, "494"), 10, 2026)
	if !had {
		t.Fatal("месяц привезён, но строки покрытия нет: следующий читатель ударит в источник снова")
	}
	if !rows.OK || rows.Lessons != 2 || rows.RefreshedAt == 0 {
		t.Errorf("покрытие привезённого месяца: %+v", rows)
	}

	months, _ := body["imported_months"].([]any)
	if len(months) != 1 {
		t.Fatalf("imported_months = %v", body["imported_months"])
	}
	entry := months[0].(map[string]any)
	if entry["ok"] != true || entry["lessons"] != float64(2) {
		t.Errorf("месяц в ответе: %v", entry)
	}
}

// TestReadThroughWeekAcrossMonthBoundaryFetchesBoth — неделя через границу
// месяцев приводится целиком, а не половинкой.
//
// Недельный вид живёт семью днями, и на рубеже месяцев в показ попадают два
// пакета источника. Если бы сквозное чтение ограничилось «месяцем сегодняшнего
// дня», ровно на этой неделе страница показала бы пустые дни второй половины —
// и выглядело бы это как каникулы, а не как непроверенный месяц.
func TestReadThroughWeekAcrossMonthBoundaryFetchesBoth(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t, store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"})

	// Понедельник 30.03 и среда 01.04: по одной паре в каждом из двух месяцев
	// недели, чтобы день ответа нельзя было получить из одного пакета.
	calls := e.attachApeks(t, func(w http.ResponseWriter, r *http.Request) {
		switch month, _ := strconv.Atoi(r.URL.Query().Get("month")); month {
		case 3:
			_, _ = w.Write([]byte(apeksLessonsOn(3, 2026, 30)))
		case 4:
			_, _ = w.Write([]byte(apeksLessonsOn(4, 2026, 1)))
		default:
			w.WriteHeader(http.StatusUnprocessableEntity)
		}
	})

	from := time.Date(2026, 3, 30, 0, 0, 0, 0, time.Local).Unix()
	to := time.Date(2026, 4, 5, 23, 59, 59, 0, time.Local).Unix()
	rec := lessonsQuery(e.h, fmt.Sprintf("/api/schedule/lessons?group=494&from=%d&to=%d", from, to))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d: %s", rec.Code, rec.Body.String())
	}

	body := decode(t, rec)
	days, _ := body["days"].([]any)
	if len(days) != 2 {
		t.Fatalf("дней в неделе %d, ожидалось 2 — по одному из каждого месяца: %v", len(days), days)
	}
	keys := make([]int, 0, len(days))
	for _, raw := range days {
		day := raw.(map[string]any)
		key, _ := day["day_key"].(float64)
		keys = append(keys, int(key))
		if list, _ := day["lessons"].([]any); len(list) != 1 {
			t.Errorf("в дне %d пар %d, ожидалось 1 — неделя собрана не из обоих пакетов", int(key), len(list))
		}
	}
	// Порядок дней: неделя читается слева направо, и перепутанные месяцы
	// страница нарисовала бы как «сначала четверг, потом понедельник».
	if keys[0] != 20260330 || keys[1] != 20260401 {
		t.Errorf("дни недели: %v, ожидалось 20260330 и 20260401", keys)
	}

	if got := calls.Load(); got != 2 {
		t.Errorf("обращений к источнику %d, ожидалось 2 — по месяцу на каждый край недели", got)
	}
	if got := stateOf(t, rec); got != "refreshed" {
		t.Errorf("source_state = %q, ожидалось refreshed", got)
	}
	if months, _ := body["imported_months"].([]any); len(months) != 2 {
		t.Errorf("imported_months = %v, ожидалось два месяца недели", body["imported_months"])
	}

	// Оба пакета легли в базу: на следующем показе той же недели источник уже
	// не нужен.
	states := coverageOf(t, e, "494")
	for _, month := range []int{3, 4} {
		row, had := coverageRow(states, month, 2026)
		if !had || !row.OK || row.Lessons != 1 {
			t.Errorf("покрытие %02d.2026: %+v (есть строка: %v)", month, row, had)
		}
	}
}

// TestReadThroughArchiveOutlivesSource — «если расписание на сайте вуза
// перестанет работать, оно сохраняется в собственной базе».
//
// Источник здесь не просто отсутствует с самого начала: он был и пропал после
// того, как группа прочитана. Именно этот переход и проверяется — база должна
// отдавать то же расписание без единого обращения к вузу.
func TestReadThroughArchiveOutlivesSource(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t, store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"})

	e.attachApeks(t, apeksMonthSource(new(atomic.Int64), 0))
	first := lessonsQuery(e.h, "/api/schedule/lessons?group=494&month=10&year=2026")
	if len(decode(t, first)["days"].([]any)) != 2 {
		t.Fatalf("первый читатель не привёз месяц: %s", first.Body.String())
	}

	// Сайт вуза умер: клиента снимаем так, как это делает сборка без токена.
	e.h.Apeks = nil

	second := lessonsQuery(e.h, "/api/schedule/lessons?group=494&month=10&year=2026")
	if second.Code != http.StatusOK {
		t.Fatalf("без источника код %d: %s", second.Code, second.Body.String())
	}
	if got := stateOf(t, second); got != "fresh" {
		t.Errorf("source_state = %q, ожидалось fresh: в базе лежит полный пакет месяца", got)
	}
	days, _ := decode(t, second)["days"].([]any)
	if len(days) != 2 {
		t.Fatalf("архив отдал %d дней, ожидалось 2", len(days))
	}
}

// --- Границы: источник не трогаем лишний раз ---------------------------------

// TestReadThroughLeavesFreshMonthAlone — самый частый случай не должен стоить
// ни одного обращения к сайту вуза.
func TestReadThroughLeavesFreshMonthAlone(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t, store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"})

	now := time.Now()
	current := now.Month()
	e.seedMonth(t, "494", int(current), now.Year(),
		store.SourceLesson{ExternalID: "1", DayKey: int(now.Year()*10000 + int(current)*100 + 6),
			Date: now.Format("02.01.2006"), LessonNumber: 1, Discipline: "Легло заранее"})

	calls := e.attachApeks(t, apeksMonthSource(new(atomic.Int64), 0))
	rec := lessonsQuery(e.h, fmt.Sprintf("/api/schedule/lessons?group=494&month=%d&year=%d", int(current), now.Year()))

	if got := stateOf(t, rec); got != "fresh" {
		t.Errorf("source_state = %q, ожидалось fresh", got)
	}
	if calls.Load() != 0 {
		t.Errorf("свежий месяц вызвал %d обращений к источнику", calls.Load())
	}
}

// TestReadThroughNeverRefetchesPastMonth — прошлое не пересчитывается.
//
// Пакет октября лежит, и каким бы старым ни было время его закачки, месяц в
// прошлом уже не изменится: без этого правила каждый показ страницы за
// прошлую неделю платил бы обращением к вузу.
func TestReadThroughNeverRefetchesPastMonth(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t, store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"})
	e.seedMonth(t, "494", 10, 2026,
		store.SourceLesson{ExternalID: "1", DayKey: 20261006, Date: "06.10.2026", LessonNumber: 1, Discipline: "Базы данных"})

	if _, err := e.db.Exec(`UPDATE schedule_source_months SET refreshed_at = 1600000000, attempted_at = 1600000000`); err != nil {
		t.Fatalf("состарить покрытие: %v", err)
	}

	calls := e.attachApeks(t, apeksMonthSource(new(atomic.Int64), 0))
	rec := lessonsQuery(e.h, "/api/schedule/lessons?group=494&month=10&year=2026")
	if got := stateOf(t, rec); got != "fresh" {
		t.Errorf("source_state = %q, ожидалось fresh: прошлое не устаревает", got)
	}
	if calls.Load() != 0 {
		t.Errorf("прошлый месяц вызвал %d обращений", calls.Load())
	}
	days, _ := decode(t, rec)["days"].([]any)
	if len(days) != 1 {
		t.Fatalf("дней %d, ожидалось 1 из базы", len(days))
	}
}

// TestReadThroughParallelReadersMakeOneCall — дедупликация по (группа, месяц).
//
// На старте семестра одну группу открывают десятки людей: без single-flight
// каждый из них был бы отдельным ударом по АПЭКС и отдельной строкой «успешно»
// от чужого ответа. Источник здесь намеренно медленный: все гортины успели
// встать в очередь за одним обращением.
func TestReadThroughParallelReadersMakeOneCall(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t, store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"})

	const readers = 8
	calls := e.attachApeks(t, apeksMonthSource(new(atomic.Int64), 300*time.Millisecond))

	var wg sync.WaitGroup
	bodies := make([]map[string]any, readers)
	codes := make([]int, readers)
	for i := range readers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := lessonsQuery(e.h, "/api/schedule/lessons?group=494&month=11&year=2026")
			codes[i] = rec.Code
			bodies[i] = decodeBody(rec)
		}(i)
	}
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Errorf("обращений к источнику %d при %d читателях, ожидалось 1", got, readers)
	}
	for i := range readers {
		if codes[i] != http.StatusOK {
			t.Errorf("читатель %d получил код %d", i, codes[i])
			continue
		}
		if len(bodies[i]) == 0 {
			t.Errorf("читатель %d получил неразбираемый ответ", i)
			continue
		}
		if days, _ := bodies[i]["days"].([]any); len(days) != 2 {
			t.Errorf("читатель %d увидел %d дней, ожидалось 2", i, len(days))
		}
	}

	// Покрытие — одна строка на месяц: повторный импорт не должен плодить
	// «успешные» попытки, которых не было.
	rows := coverageOf(t, e, "494")
	if len(rows) != 1 || rows[0].Month != 11 || !rows[0].OK {
		t.Fatalf("покрытие после параллельного чтения: %+v", rows)
	}
	if stored := e.lessons(t, "494"); len(stored) != 2 {
		t.Errorf("пар в базе %d, ожидалось 2", len(stored))
	}
}

// --- Отказ источника: страница отвечает, а не врёт ---------------------------

// TestReadThroughSourceDownShowsNothingIsNotAnError — «показать нечего» — это
// состояние, а не 502.
//
// Гость не обязан видеть текст ошибки, и тем более нельзя оставлять его с
// пустой таблицей без объяснения: по source_state страница скажет «источник не
// ответил», а по отсутствию строк покрытия админ увидит, что месяц всё-таки
// проверяли.
func TestReadThroughSourceDownShowsNothingIsNotAnError(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t, store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"})

	down := new(atomic.Int64)
	e.attachApeks(t, func(w http.ResponseWriter, r *http.Request) {
		down.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})

	rec := lessonsQuery(e.h, "/api/schedule/lessons?group=494&month=10&year=2026")
	if rec.Code != http.StatusOK {
		t.Fatalf("при мёртвом источнике код %d, ожидалось 200: %s", rec.Code, rec.Body.String())
	}
	if got := stateOf(t, rec); got != "unavailable" {
		t.Errorf("source_state = %q, ожидалось unavailable", got)
	}
	body := decode(t, rec)
	if days, _ := body["days"].([]any); len(days) != 0 {
		t.Errorf("при отказе источника в ответе %d дней — страница показала бы выдуманное расписание", len(days))
	}
	if body["range_imported"] != false {
		t.Errorf("range_imported = %v при неудавшейся закачке", body["range_imported"])
	}
	if strings.Contains(rec.Body.String(), testApeksToken) {
		t.Fatal("токен источника в ответе при отказе")
	}

	// Ошибка источника не должна выглядеть как «месяц пуст»: строка покрытия
	// есть, но ok=0, и по ней read-through не побьёт по вузу на каждой
	// загрузке страницы.
	rows := coverageOf(t, e, "494")
	if len(rows) != 1 {
		t.Fatalf("покрытие после отказа: %+v", rows)
	}
	if rows[0].OK || rows[0].Lessons != 0 || rows[0].RefreshedAt != 0 || rows[0].AttemptedAt == 0 {
		t.Errorf("неудачная попытка записана как %+v", rows[0])
	}
}

// TestReadThroughNotConfiguredDiffersFromNotAnswering — у «показать нечего» два
// противоположных смысла, и словами их различает source_configured.
//
// source_state в обеих проверках один и тот же, потому что для читателя результат
// одинаков: недель нет. Причина же противоположная — во втором случае серверу
// вообще нечем ходить в вуз, и сказать гостю «сайт вуза не ответил» было бы
// врать: отвечать ему нечем, а не лень. Страница по этому полю выбирает,
// советовать ли «обновите неделю» или называть настройку APEKS_TOKEN.
func TestReadThroughNotConfiguredDiffersFromNotAnswering(t *testing.T) {
	down := newScheduleSourceEnv(t)
	down.seedCatalog(t, store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"})
	hits := down.attachApeks(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	recDown := lessonsQuery(down.h, "/api/schedule/lessons?group=494&month=10&year=2026")
	if got := stateOf(t, recDown); got != "unavailable" {
		t.Errorf("при отказе источника source_state = %q, ожидалось unavailable", got)
	}
	if body := decode(t, recDown); body["source_configured"] != true {
		t.Errorf("source_configured = %v: клиент источника есть, а страница выставила бы сервера слепым",
			body["source_configured"])
	}
	// Без этого проверка повисла бы в воздухе: 500 от заглушки засчитывается
	// только если запрос к источнику действительно состоялся.
	if hits.Load() == 0 {
		t.Error("источник не вызван — «не ответил» ничем не подтверждён")
	}

	bare := newScheduleSourceEnv(t)
	bare.seedCatalog(t, store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"})

	recBare := lessonsQuery(bare.h, "/api/schedule/lessons?group=494&month=10&year=2026")
	if recBare.Code != http.StatusOK {
		t.Fatalf("без источника код %d: %s", recBare.Code, recBare.Body.String())
	}
	if got := stateOf(t, recBare); got != "unavailable" {
		t.Errorf("без источника source_state = %q, ожидалось unavailable", got)
	}
	if body := decode(t, recBare); body["source_configured"] != false {
		t.Errorf("source_configured = %v без клиента: гостю пообещали бы «вуз не ответил», хотя ходить некуда",
			body["source_configured"])
	}
	if days, _ := decode(t, recBare)["days"].([]any); len(days) != 0 {
		t.Errorf("без источника в ответе %d дней — странице нечего показывать, но она показала бы что-то", len(days))
	}

	// Ходить некуда — значит и пытаться не было: пустое покрытие. Строка
	// попытки здесь означала бы, что следующая загрузка страницы молчала бы
	// пятнадцать минут из-за заведомо невозможного обращения.
	if rows := coverageOf(t, bare, "494"); len(rows) != 0 {
		t.Errorf("без клиента источника попытка записана в покрытие: %+v", rows)
	}
}

// TestReadThroughServesArchiveAfterFailedRefresh — вчерашний пакет переживает
// сегодняшнюю неудачу и не вызывает повторного удара в источник.
func TestReadThroughServesArchiveAfterFailedRefresh(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t, store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"})
	e.seedMonth(t, "494", 10, 2026,
		store.SourceLesson{ExternalID: "1", DayKey: 20261006, Date: "06.10.2026", LessonNumber: 1, Discipline: "Базы данных"})

	if err := e.src.MarkMonthAttempt(context.Background(), apeks.Source, "494", 10, 2026); err != nil {
		t.Fatalf("отметить попытку: %v", err)
	}

	calls := e.attachApeks(t, apeksMonthSource(new(atomic.Int64), 0))
	rec := lessonsQuery(e.h, "/api/schedule/lessons?group=494&month=10&year=2026")
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d: %s", rec.Code, rec.Body.String())
	}

	if got := stateOf(t, rec); got != "stale" {
		t.Errorf("source_state = %q, ожидалось stale: показан архив, обновка не дошла", got)
	}
	if calls.Load() != 0 {
		t.Errorf("в паузу после неудачи сделано %d обращений — страница била бы по вузу на каждой загрузке", calls.Load())
	}

	body := decode(t, rec)
	days, _ := body["days"].([]any)
	if len(days) != 1 {
		t.Fatalf("архив не показан: дней %d", len(days))
	}
	if body["range_imported"] != true {
		t.Error("неделя с лежашим пакетом помечена как неимпортированная")
	}
	months, _ := body["imported_months"].([]any)
	if len(months) != 1 {
		t.Errorf("imported_months = %v, ожидался месяц из архива", body["imported_months"])
	}
}

// TestReadThroughBackgroundsStaleCopy — устаревшая копия показывается сразу,
// обновка идёт фоном и текущего гостя не задерживает.
func TestReadThroughBackgroundsStaleCopy(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t, store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"})

	now := time.Now()
	distant := now.AddDate(0, 2, 0)
	distantKey := distant.Year()*10000 + int(distant.Month())*100 + 6
	e.seedMonth(t, "494", int(distant.Month()), distant.Year(),
		store.SourceLesson{ExternalID: "old", DayKey: distantKey,
			Date:         fmt.Sprintf("%02d.%02d.%d", 6, int(distant.Month()), distant.Year()),
			LessonNumber: 1, Discipline: "Из архива"})
	if _, err := e.db.Exec(`UPDATE schedule_source_months SET refreshed_at = ?, attempted_at = ?`,
		now.AddDate(0, 0, -8).Unix(), now.AddDate(0, 0, -8).Unix()); err != nil {
		t.Fatalf("состарить покрытие: %v", err)
	}

	// Источник заведомо медленнее ответа страницы: если бы обновка ждала
	// гостя, тест упал бы и по содержимому, и по времени.
	calls := e.attachApeks(t, apeksMonthSource(new(atomic.Int64), 400*time.Millisecond))
	started := time.Now()
	rec := lessonsQuery(e.h, fmt.Sprintf("/api/schedule/lessons?group=494&month=%d&year=%d",
		int(distant.Month()), distant.Year()))
	elapsed := time.Since(started)

	// Ответ уходит с архивом, а не с привезённым: гость платит за устаревшую
	// копию временем ожидания, которого у него нет.
	days, _ := decode(t, rec)["days"].([]any)
	if len(days) != 1 {
		t.Fatalf("дней %d, ожидалось 1 из архива", len(days))
	}
	first := days[0].(map[string]any)
	lessons := first["lessons"].([]any)
	if got := lessons[0].(map[string]any)["discipline"]; got != "Из архива" {
		t.Errorf("в ответе пара %v — фоновая обновка обогнала ответ", got)
	}
	if elapsed > 300*time.Millisecond {
		t.Errorf("страница ждала источник %v, хотя обязана отдать архив сразу", elapsed)
	}
	if got := stateOf(t, rec); got != "cached" {
		t.Errorf("source_state = %q, ожидалось cached", got)
	}

	// Фоновая закачка доходит: это её обещание и значит «обновляем».
	waitFor(t, 3*time.Second, "фоновая обновка не дошла", func() bool { return calls.Load() >= 1 })
	waitFor(t, 3*time.Second, "покрытие не обновилось фоном", func() bool {
		for _, row := range coverageOf(t, e, "494") {
			if row.Month == int(distant.Month()) && row.OK && row.Lessons == 2 {
				return true
			}
		}
		return false
	})
}

// waitFor — ожидание условия с понятным текстом сбоя.
//
// Фоновые закачки read-through нужны ровно для этого: без ожидания тест
// читал бы недописанное покрытие и падал бы не на правиле, а на гонке.
func waitFor(t *testing.T, limit time.Duration, what string, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s за %v", what, limit)
}

// --- Потолок обращений на один показ страницы --------------------------------

// TestReadThroughCapsMonthsPerRequest — длинный диапазон в строке запроса не
// должен превращать загрузку страницы в прогон импорта.
//
// Неделя задевает максимум два месяца, и потолок стоит именно там. На
// искусственном запросе за три месяца видно, что третий не качается: при этом
// ответ остаётся честным — привезённое показывается, а source_state говорит,
// что известен не весь период.
func TestReadThroughCapsMonthsPerRequest(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t, store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"})

	calls := e.attachApeks(t, apeksMonthSource(new(atomic.Int64), 0))
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.Local).Unix()
	to := time.Date(2026, 10, 31, 0, 0, 0, 0, time.Local).Unix()
	rec := lessonsQuery(e.h, fmt.Sprintf("/api/schedule/lessons?group=494&from=%d&to=%d", from, to))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d: %s", rec.Code, rec.Body.String())
	}

	if got := calls.Load(); got != scheduleReadMaxMonths {
		t.Errorf("обращений %d, ожидалось %d", got, scheduleReadMaxMonths)
	}
	body := decode(t, rec)
	if body["range_imported"] != true {
		t.Error("привезённые месяцы не признаны импортированными")
	}
	if got := stateOf(t, rec); got != "unavailable" {
		t.Errorf("source_state = %q: третий месяц периода остался непроверенным, страница обязана это отличить", got)
	}
	if months, _ := body["imported_months"].([]any); len(months) != scheduleReadMaxMonths {
		t.Errorf("imported_months = %v, ожидалось %d", body["imported_months"], scheduleReadMaxMonths)
	}
}

// TestReadThroughGuestCallLeavesNoAudit — журнал админских действий.
//
// Сквозное чтение пишет каждую закачку в журнал процесса, но не в аудит:
// журнал читают люди с правами, и «след каждого показа страницы» сделал бы его
// бесполезным — а гость тем временем оставался бы анонимным.
func TestReadThroughGuestCallLeavesNoAudit(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t, store.SourceGroup{ExternalID: "494", Name: "Б-ИСиТ-22"})
	e.attachApeks(t, apeksMonthSource(new(atomic.Int64), 0))

	rec := lessonsQuery(e.h, "/api/schedule/lessons?group=494&month=10&year=2026")
	if got := stateOf(t, rec); got != "refreshed" {
		t.Fatalf("source_state = %q, ожидалось refreshed: без закачки проверка бессмысленна", got)
	}

	events, err := e.h.Audit.List(context.Background(), store.AuditFilter{Limit: 50})
	if err != nil {
		t.Fatalf("прочитать журнал: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("гостевой показ страницы оставил %d записей аудита: %+v", len(events), events)
	}
}

// --- Чистые правила: таблица решений и разбор периода ------------------------

func TestDecideScheduleMonthTable(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-time.Minute).Unix()
	old := now.Add(-48 * time.Hour).Unix()
	// Для далёкого месяца окно свежести — неделя, поэтому «устарел» для него
	// значит не то же самое, что для текущего: проверка на 48 часах здесь
	// проходила бы, и правило семи дней осталось бы незащищённым.
	ancient := now.Add(-8 * 24 * time.Hour).Unix()

	cases := []struct {
		name   string
		state  store.MonthState
		found  bool
		want   scheduleMonthDecision
		reason string
	}{
		{"месяц никогда не спрашивали", store.MonthState{Month: 10, Year: 2026}, false, decisionFetch, "показывать нечего"},
		{"прошлый пакет лежит", store.MonthState{Month: 10, Year: 2026, OK: true, Lessons: 5, RefreshedAt: old, AttemptedAt: old}, true, decisionFresh, "прошлое не устаревает"},
		{"текущий пакет свежий", store.MonthState{Month: 11, Year: 2026, OK: true, Lessons: 5, RefreshedAt: recent, AttemptedAt: recent}, true, decisionFresh, "полдня ещё не прошло"},
		{"текущий пакет устарел", store.MonthState{Month: 11, Year: 2026, OK: true, Lessons: 5, RefreshedAt: old, AttemptedAt: old}, true, decisionBackground, "обновить фоном"},
		{"далёкое будущее свежо неделю", store.MonthState{Month: 2, Year: 2027, OK: true, Lessons: 3, RefreshedAt: now.Add(-24 * time.Hour).Unix(), AttemptedAt: now.Add(-24 * time.Hour).Unix()}, true, decisionFresh, "семь дней не прошли"},
		{"далёкое будущее устарело за неделю", store.MonthState{Month: 2, Year: 2027, OK: true, Lessons: 3, RefreshedAt: ancient, AttemptedAt: ancient}, true, decisionBackground, "пересчитать фоном"},
		{"недавняя неудача при лежашем пакете", store.MonthState{Month: 11, Year: 2026, Lessons: 5, RefreshedAt: old, AttemptedAt: recent}, true, decisionStale, "показать архив, не бить снова"},
		{"недавняя неудача при пустой базе", store.MonthState{Month: 11, Year: 2026, Lessons: 0, AttemptedAt: recent}, true, decisionHold, "в источник пока нельзя, показать нечего"},
		{"пустой месяц не переспрашивается", store.MonthState{Month: 10, Year: 2026, OK: true, Lessons: 0, RefreshedAt: old, AttemptedAt: old}, true, decisionFresh, "«занятий нет» — тоже ответ"},
	}

	for _, tc := range cases {
		if got := decideScheduleMonth(now, tc.state, tc.found); got != tc.want {
			t.Errorf("%s: решение %d, ожидалось %d (%s)", tc.name, got, tc.want, tc.reason)
		}
	}

	// Двухмесячная неделя, где первый месяц привезти не удалось: решение по
	// второму не зависит от первого — каждый месяц проверяется сам.
	if got := decideScheduleMonth(now, store.MonthState{Month: 12, Year: 2026, OK: true, Lessons: 1, RefreshedAt: old, AttemptedAt: old}, true); got != decisionBackground {
		t.Errorf("следующий месяц с устаревшим пакетом: %d, ожидалось background", got)
	}
}

func TestScheduleMonthsOfRange(t *testing.T) {
	cases := []struct {
		name     string
		from, to int
		want     []scheduleMonth
	}{
		{"одна неделя внутри месяца", 20261005, 20261011, []scheduleMonth{{Month: 10, Year: 2026}}},
		{"неделя через границу месяцев", 20261026, 20261101, []scheduleMonth{{Month: 10, Year: 2026}, {Month: 11, Year: 2026}}},
		{"неделя через Новый год", 20261228, 20270103, []scheduleMonth{{Month: 12, Year: 2026}, {Month: 1, Year: 2027}}},
		{"перепутанные границы", 20261011, 20261005, []scheduleMonth{{Month: 10, Year: 2026}}},
		{"целый семестр", 20260901, 20261231, []scheduleMonth{
			{Month: 9, Year: 2026}, {Month: 10, Year: 2026}, {Month: 11, Year: 2026}, {Month: 12, Year: 2026},
		}},
	}

	for _, tc := range cases {
		got := scheduleMonthsOfRange(tc.from, tc.to)
		if len(got) != len(tc.want) {
			t.Errorf("%s: месяцев %d, ожидалось %d: %+v", tc.name, len(got), len(tc.want), got)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: месяц %d = %v, ожидалось %v", tc.name, i, got[i], tc.want[i])
			}
		}
	}

	// Пара без даты (day_key = 99999999) не должна утянуть перебор за
	// пределы ограничителя: иначе одна строка с битой датой заставила бы
	// качать месяцы, которых в природе нет.
	broken := scheduleMonthsOfRange(20261001, 99999999)
	if len(broken) == 0 || len(broken) > 25 {
		t.Fatalf("перебор при битой дате дал %d месяцев", len(broken))
	}
	if broken[0] != (scheduleMonth{Month: 10, Year: 2026}) {
		t.Errorf("перебор начался не с запрошенного месяца: %+v", broken[0])
	}
	for _, m := range broken {
		if m.Month < 1 || m.Month > 12 {
			t.Errorf("в перебор попал месяц %v", m)
		}
	}
}

// TestScheduleHasCopyNeedsCopyNotRow — «месяц спрашивали» и «месяц есть» —
// разные вещи, и range_imported обязан показывать второе.
func TestScheduleHasCopyNeedsCopyNotRow(t *testing.T) {
	queried := []store.MonthState{{Month: 10, Year: 2026, OK: false, Lessons: 0, AttemptedAt: 1760000000}}
	if scheduleHasCopy(queried, 20261001, 20261031) {
		t.Error("неудачная попытка признана импортированным периодом")
	}
	if months := scheduleImportedMonths(queried); len(months) != 0 {
		t.Errorf("неудачная попытка попала в imported_months: %+v", months)
	}

	empty := []store.MonthState{{Month: 10, Year: 2026, OK: true, Lessons: 0, RefreshedAt: 1760000000}}
	if !scheduleHasCopy(empty, 20261001, 20261031) {
		t.Error("успешно импортированный пустой месяц — не «копия»: страница соврёт про «ещё не импортировано»")
	}
	if months := scheduleImportedMonths(empty); len(months) != 1 {
		t.Errorf("пустой месяц потерян из imported_months: %+v", months)
	}
	if last := scheduleLastRefresh(empty); last != 1760000000 {
		t.Errorf("updated_at по пустому месяцу = %d, ожидалось 1760000000", last)
	}

	// Период, который покрытие не касается, — не импортирован, даже если
	// месяцы в базе есть.
	if scheduleHasCopy([]store.MonthState{{Month: 10, Year: 2026, OK: true, Lessons: 9}}, 20261101, 20261130) {
		t.Error("ноябрьская неделя признана импортированной по октябрьскому пакету")
	}
}
