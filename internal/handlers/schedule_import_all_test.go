package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NumerR/SOS/internal/authz"
	"github.com/NumerR/SOS/internal/store"
)

// Тесты массового импорта всего каталога (запрос владельца: «парситься должны
// все группы и все даты»).
//
// Здесь проверяется то, чего не видно в тестах одиночного импорта:
//
//  1. Обход доходит до каждой группы и каждого месяца периода, а не до первой
//     группы — ровно та ошибка, из-за которой в живой базе из 588 групп
//     расписание имела одна.
//  2. Задача живёт в отдельной горутине, поэтому POST отвечает сразу, а второй
//     щелчок не заводит вторую проходку: 409 с живым прогрессом в теле.
//  3. Сбой одной группы не рвёт каталог и не уносит причину в браузер — у
//     Python-движка в тексте бывают абсолютные пути процесса.
//  4. Права и отказ источника работают на новой кнопке так же, как на старой:
//     проверка стоит до обращения к чужому сайту.
//
// Состояние задачи читается через snapshotScheduleImport, а не через внутреннее
// поле: это ровно тот же путь с мьютексом, которым пользуется панель, и гонка
// -race на нём находит настоящую проблему, а не выдуманный прямой доступ.

const importAllPath = "/api/schedule/source/import-all"

// callImportAll запускает массовый импорт от имени роли через те же middleware,
// что заявлены в модуле: право schedule.source.manage и CSRF для POST.
func (e *scheduleSourceEnv) callImportAll(t *testing.T, role, body string) *httptest.ResponseRecorder {
	t.Helper()

	return e.callAdmin(t, role, importAllPath, body,
		http.HandlerFunc(e.h.ScheduleImportAllAPI), true)
}

// importAllStatus читает прогресс задачи админским GET'ом.
func (e *scheduleSourceEnv) importAllStatus(t *testing.T, role string) *httptest.ResponseRecorder {
	t.Helper()

	return e.callAdminGET(t, role, importAllPath,
		http.HandlerFunc(e.h.ScheduleImportAllStatusAPI))
}

// waitImportJob дожидается конца проходки и возвращает её итог.
//
// Ждать обязательно, а не читать состояние один раз: горутина пишет в ту же
// базу, которую t.Cleanup закрывает по окончании теста. Без ожидания база
// умирала бы под работающим импортом, а первой ошибкой в логе была бы
// «no such table», а не то, что тест проверял.
//
// Отдельного сигнала о завершении у задачи нет намеренно — панель опрашивает
// состояние, и тест смотрит теми же глазами.
func waitImportJob(t *testing.T, h *Handlers) scheduleImportView {
	t.Helper()

	for range 500 {
		view, found := h.snapshotScheduleImport()
		if found && !view.Running {
			// Чтение срезов после этого момента безопасно: запись в них шла
			// под тем же мьютексом, под которым читается снимок выше.
			return view
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("массовый импорт не завершился за 5 секунд")
	return scheduleImportView{}
}

// jobOf вынимает снимок задачи из JSON-ответа.
func jobOf(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	job, ok := decode(t, rec)["job"].(map[string]any)
	if !ok {
		t.Fatalf("в ответе нет снимка задачи: %s", rec.Body.String())
	}
	return job
}

func jobNumber(t *testing.T, job map[string]any, key string) int {
	t.Helper()

	value, ok := job[key].(float64)
	if !ok {
		t.Fatalf("поле %s не число: %#v", key, job[key])
	}
	return int(value)
}

func jobFlag(t *testing.T, job map[string]any, key string) bool {
	t.Helper()

	value, ok := job[key].(bool)
	if !ok {
		t.Fatalf("поле %s не булево: %#v", key, job[key])
	}
	return value
}

// apeksStepPayload — месяц одной группы, собранный из её запроса.
//
// Id пары выводится из группы и месяца, а не берётся постоянным, как в
// apeksMonthPayload: источник отдаёт id, уникальный в масштабе выгрузки, а
// store.ReplaceMonth склеивает строки по (source, group_ext_id, external_id) и
// при повторе делает ON CONFLICT DO UPDATE. Два месяца с одним id — это не «обе
// пары в базе», а одна поверх другой, и фикстура с постоянным id молча бы это
// скрыла.
func apeksStepPayload(groupID string, month, year int) string {
	number, err := strconv.Atoi(groupID)
	if err != nil {
		number = 1
	}

	return fmt.Sprintf(`{"data":{
		"groupName":"%s",
		"startDate":"01.%02d.%d","endDate":"28.%02d.%d",
		"lessonTimes":[{"id":1,"hour_from":8,"minute_from":30,"hour_to":10,"minute_to":0,"fullName":"08:30 – 10:00"}],
		"lessons":[{
			"id":%d,"date":"06.%02d.%d","lesson_time_id":1,
			"groupName":"%s","discipline":"Пара %s/%02d","class_type_name":"Лекция",
			"classroom":"201","staffNames":["Иванов И.И."],
			"subgroup_id":0,"subgroupNames":"","is_empty":0,"self_work":""
		}]
	}}`, groupID, month, year, month, year,
		number*100+month, month, year, groupID, groupID, month)
}

// recordingApeks пишет каждый запрос источника в список и отвечает валидным
// месяцем. Список читается только после waitImportJob: запись шла до ответа
// клиенту, а ответ — до шага задачи, который закрыт под мьютексом снимка.
func (e *scheduleSourceEnv) recordingApeks(t *testing.T) *[]string {
	t.Helper()

	var seen []string
	var mu sync.Mutex

	e.attachApeks(t, func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		group, month, year := query.Get("group_id"), query.Get("month"), query.Get("year")

		mu.Lock()
		seen = append(seen, group+"/"+month+"/"+year)
		mu.Unlock()

		_, _ = w.Write([]byte(apeksStepPayload(group, mustInt(month), mustInt(year))))
	})

	return &seen
}

func mustInt(raw string) int {
	value, _ := strconv.Atoi(raw)
	return value
}

// --- Обход каталога ----------------------------------------------------------

// Главная проверка всего запроса: после одного запуска расписание есть у каждой
// группы каталога, а не у той, которую админ случайно выбрал на странице.
func TestImportAllWalksEveryGroup(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t,
		store.SourceGroup{ExternalID: "1", Name: "А-1"},
		store.SourceGroup{ExternalID: "2", Name: "Б-2"},
		store.SourceGroup{ExternalID: "3", Name: "В-3"},
	)
	seen := e.recordingApeks(t)

	rec := e.callImportAll(t, "admin", `{"month":10,"year":2026}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("код %d, ожидалось 202: %s", rec.Code, rec.Body.String())
	}

	// POST обязан ответить до конца проходки: держать под 588 обращениями к
	// чужому сайту HTTP-соединение нельзя, а обрыв вкладки оборвал бы импорт.
	started := jobOf(t, rec)
	if !jobFlag(t, started, "running") {
		t.Error("ответ о запуске говорит, что задача уже завершена")
	}
	if jobNumber(t, started, "groups") != 3 || jobNumber(t, started, "steps") != 3 {
		t.Errorf("на старте groups=%v steps=%v, ожидалось 3 и 3",
			started["groups"], started["steps"])
	}

	view := waitImportJob(t, e.h)
	if view.Groups != 3 || view.Total != 3 || view.Done != 3 {
		t.Errorf("итог: groups=%d steps=%d done=%d, ожидалось 3/3/3",
			view.Groups, view.Total, view.Done)
	}
	if view.Failed != 0 {
		t.Errorf("сбоев %d (%q), ожидалось 0", view.Failed, view.FailedLast)
	}
	if view.Lessons != 3 {
		t.Errorf("пар записано %d, ожидалось 3", view.Lessons)
	}
	if view.Actor == "" || view.StartedAt == 0 || view.FinishedAt < view.StartedAt {
		t.Errorf("задача без подписи: actor=%q start=%d finish=%d",
			view.Actor, view.StartedAt, view.FinishedAt)
	}
	if len(view.Months) != 1 || view.Months[0].Month != 10 || view.Months[0].Year != 2026 {
		t.Errorf("период задачи: %+v", view.Months)
	}

	// Ровно одно обращение к группе: веер по месяцам или повтор одного шага
	// были бы видны по длине списка.
	if len(*seen) != 3 {
		t.Fatalf("источник спросили %d раз: %v", len(*seen), *seen)
	}
	for _, step := range *seen {
		if !strings.HasSuffix(step, "/10/2026") {
			t.Errorf("запрошен не тот месяц: %q", step)
		}
	}

	// Счётчики — не самоцель: строки обязаны лежать в базе именно по группам,
	// иначе панель врёт, а страница показывает пусто.
	for _, id := range []string{"1", "2", "3"} {
		lessons := e.lessons(t, id)
		if len(lessons) != 1 {
			t.Fatalf("группа %s: пар %d, ожидалось 1", id, len(lessons))
		}
		if lessons[0].Month != 10 || lessons[0].Year != 2026 {
			t.Errorf("группа %s: месяц строки %+v", id, lessons[0])
		}
		if !strings.Contains(lessons[0].Discipline, id+"/10") {
			t.Errorf("группе %s записана чужая пара %q", id, lessons[0].Discipline)
		}
	}

	event := lastAudit(t, e.h, store.AuditScheduleImport)
	if !event.OK {
		t.Error("удачный массовый импорт записан как неудачный")
	}
	if !strings.Contains(event.Detail, "all start groups=3 steps=3") {
		t.Errorf("в деталях аудита не видно размера прогона: %q", event.Detail)
	}
	if strings.Contains(rec.Body.String(), testApeksToken) {
		t.Fatal("токен источника в ответе запуска")
	}
	assertNoSecretInAudit(t, e.h)
}

// Период «несколько месяцев» обязан проходиться для каждой группы, а не только
// для первой: «все даты» из запроса владельца — про это.
func TestImportAllWalksEveryMonthOfEveryGroup(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t,
		store.SourceGroup{ExternalID: "1", Name: "А-1"},
		store.SourceGroup{ExternalID: "2", Name: "Б-2"},
	)
	seen := e.recordingApeks(t)

	rec := e.callImportAll(t, "admin", `{"month":10,"year":2026,"count":2}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("код %d: %s", rec.Code, rec.Body.String())
	}

	view := waitImportJob(t, e.h)
	if view.Total != 4 || view.Done != 4 || view.Failed != 0 {
		t.Errorf("итог: steps=%d done=%d failed=%d, ожидалось 4/4/0",
			view.Total, view.Done, view.Failed)
	}
	if len(view.Months) != 2 {
		t.Errorf("в период задачи попало %d месяцев, ожидалось 2", len(view.Months))
	}

	// Порядок — группа крупной: вся первая группа, потом вторая. При обратном
	// («сначала месяц у всех») админ видел бы кривую покрытия: у половины групп
	// октябрь, ни у одной — ноябрь.
	if got := strings.Join(*seen, ","); got != "1/10/2026,1/11/2026,2/10/2026,2/11/2026" {
		t.Errorf("обход идёт не по группам: %s", got)
	}

	// Два месяца одной группы сосуществуют: id пары выводится из месяца именно
	// поэтому, и проверка «1 пара» тут поймала бы ON CONFLICT.
	for _, id := range []string{"1", "2"} {
		if lessons := e.lessons(t, id); len(lessons) != 2 {
			t.Errorf("группа %s: пар %d, ожидалось 2 (октябрь и ноябрь)", id, len(lessons))
		}
	}
}

// --- Одна задача одновременно ------------------------------------------------

func TestImportAllSecondStartIs409WithLiveProgress(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t, store.SourceGroup{ExternalID: "1", Name: "А-1"})

	reached := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once

	// Первый шаг ставится на паузу: без неё задача успевала бы завершиться
	// между двумя POST'ами (второй POST сам по себе медленный — вход и bcrypt),
	// и тест ловил бы 202 вместо 409 на пустом месте.
	calls := e.attachApeks(t, func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(reached) })
		select {
		case <-release:
		case <-time.After(30 * time.Second):
		}
		query := r.URL.Query()
		_, _ = w.Write([]byte(apeksStepPayload(query.Get("group_id"), mustInt(query.Get("month")), mustInt(query.Get("year")))))
	})
	// Освободить нужно до server.Close в t.Cleanup: иначе сервер ждал бы бы
	// висящий запрос и тест упёрся бы в общий таймаут.
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})

	first := e.callImportAll(t, "admin", `{"month":10,"year":2026}`)
	if first.Code != http.StatusAccepted {
		t.Fatalf("первый запуск: код %d: %s", first.Code, first.Body.String())
	}

	// Ждём шаг у источника, но не вечно: если горутина до него не дошла, это
	// собственная поломка задачи, и тест обязан сказать про неё, а не упёреться
	// в общий таймаут go test.
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("массовый импорт за 5 секунд не обратился к источнику")
	}

	second := e.callImportAll(t, "admin", `{"month":11,"year":2026}`)
	if second.Code != http.StatusConflict {
		t.Fatalf("повторный запуск: код %d, ожидалось 409: %s", second.Code, second.Body.String())
	}

	conflict := decode(t, second)
	message, _ := conflict["error"].(string)
	if !strings.Contains(message, "уже") {
		t.Errorf("ответ 409 не объясняет, что уже идёт прогон: %q", message)
	}
	// 409 отдаёт живой прогресс, а не пустую заглушку: панель показывает те же
	// цифры, что и обычный опрос, и двойной клик выглядит идущим импортом.
	job := jobOf(t, second)
	if !jobFlag(t, job, "running") {
		t.Error("в ответе 409 задача не выполняется")
	}
	if jobNumber(t, job, "groups") != 1 || jobNumber(t, job, "steps") != 1 {
		t.Errorf("прогресс в 409: groups=%v steps=%v", job["groups"], job["steps"])
	}
	months, ok := job["months"].([]any)
	if !ok || len(months) != 1 {
		t.Errorf("месяц в 409 не тот: %v", job["months"])
	}

	close(release)
	view := waitImportJob(t, e.h)

	// Отказанная попытка не должна ничего пройти заново: один шаг, один месяц.
	if view.Done != 1 || view.Total != 1 || view.Failed != 0 {
		t.Errorf("итог после 409: done=%d steps=%d failed=%d", view.Done, view.Total, view.Failed)
	}
	if got := view.Months[0].Month; got != 10 {
		t.Errorf("задача ушла на месяц %d из отвергнутого запроса", got)
	}
	if calls.Load() != 1 {
		t.Errorf("источник тронут %d раз, ожидалось 1", calls.Load())
	}

	event := lastAudit(t, e.h, store.AuditScheduleImport)
	if event.OK {
		t.Error("отказанная повторная задача записана как удачная")
	}
	if !strings.Contains(event.Detail, "already_running") {
		t.Errorf("в деталях отказа нет причины: %q", event.Detail)
	}
	assertNoSecretInAudit(t, e.h)
}

// После конца задачи её прогресс остаётся читаемым: панель перезагружается, и
// «последний прогон завершён, 3 группы» — это ответ, а не «задачи не было».
func TestImportAllKeepsFinishedProgressReadable(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t, store.SourceGroup{ExternalID: "1", Name: "А-1"})
	e.recordingApeks(t)

	if rec := e.callImportAll(t, "admin", `{"month":10,"year":2026}`); rec.Code != http.StatusAccepted {
		t.Fatalf("код %d: %s", rec.Code, rec.Body.String())
	}
	waitImportJob(t, e.h)

	rec := e.importAllStatus(t, "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d: %s", rec.Code, rec.Body.String())
	}

	job := jobOf(t, rec)
	if jobFlag(t, job, "running") {
		t.Error("завершённая задача отдаётся выполняющейся")
	}
	if jobNumber(t, job, "done") != 1 || jobNumber(t, job, "finished_at") == 0 {
		t.Errorf("снимок по GET не похож на итог: %v", job)
	}
	if current, _ := job["current"].(string); current != "" {
		t.Errorf("у завершённой задачи осталось «читаю %q»", current)
	}
}

// --- Состояния каталога и источника -----------------------------------------

// Пустой каталог — пропущенный шаг «синхронизируйте группы», и ответ обязан
// называть именно это: 503 или тихий ноль отправляли бы админа искать NETWORK
// вместо кнопки над ним.
func TestImportAllRejectsEmptyCatalogue(t *testing.T) {
	e := newScheduleSourceEnv(t)
	calls := e.attachApeks(t, func(http.ResponseWriter, *http.Request) {
		t.Error("при пустом каталоге к источнику идти нельзя")
	})

	rec := e.callImportAll(t, "admin", `{"month":10,"year":2026}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("код %d, ожидалось 422: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "каталог групп пуст") {
		t.Errorf("ответ не называет причину: %s", rec.Body.String())
	}
	if calls.Load() != 0 {
		t.Errorf("источник тронут %d раз при пустом каталоге", calls.Load())
	}

	// Задача не заведена: иначе панель показала бы «идёт прогон 0 групп».
	status := jobOf(t, e.importAllStatus(t, "admin"))
	if jobFlag(t, status, "running") || jobNumber(t, status, "steps") != 0 {
		t.Errorf("после отказа каталога прогресс: %v", status)
	}
}

func TestImportAllWithoutSourceClientIs503(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t, store.SourceGroup{ExternalID: "1", Name: "А-1"})

	// Apeks == nil: сервер поднят без APEKS_TOKEN.
	rec := e.callImportAll(t, "admin", `{"month":10,"year":2026}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("код %d, ожидалось 503: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "APEKS_TOKEN") {
		t.Errorf("ответ не говорит, чего не хватает: %s", rec.Body.String())
	}
	if decoded := decodeError(t, rec); decoded.Code != ErrSourceUnavailable {
		t.Errorf("код отказа %q, ожидалось %q", decoded.Code, ErrSourceUnavailable)
	}

	event := lastAudit(t, e.h, store.AuditScheduleImport)
	if event.OK || !strings.Contains(event.Detail, "not_configured") {
		t.Errorf("отказ ненастроенного источника записан так: ok=%v %q", event.OK, event.Detail)
	}
	assertNoSecretInAudit(t, e.h)
}

// Битый ответ одной группы не должен ронять весь каталог: 588 групп — это 588
// шансов встретить чужой HTML, и админ обязан получить расписание остальных.
func TestImportAllSkipsBrokenGroupAndKeepsOthers(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t,
		store.SourceGroup{ExternalID: "1", Name: "А-1"},
		store.SourceGroup{ExternalID: "2", Name: "Б-2"},
	)

	e.attachApeks(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("group_id") == "2" {
			_, _ = w.Write([]byte(`{"data":{"lessons":"не массив"}}`))
			return
		}
		query := r.URL.Query()
		_, _ = w.Write([]byte(apeksStepPayload(query.Get("group_id"), mustInt(query.Get("month")), mustInt(query.Get("year")))))
	})

	rec := e.callImportAll(t, "admin", `{"month":10,"year":2026}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("код %d: %s", rec.Code, rec.Body.String())
	}

	view := waitImportJob(t, e.h)
	if view.Done != 2 || view.Failed != 1 || view.Lessons != 1 {
		t.Errorf("итог: done=%d failed=%d lessons=%d, ожидалось 2/1/1",
			view.Done, view.Failed, view.Lessons)
	}
	if !strings.Contains(view.FailedLast, "Б-2") {
		t.Errorf("в failed_last_group не видно имя сбоящей группы: %q", view.FailedLast)
	}

	if lessons := e.lessons(t, "1"); len(lessons) != 1 {
		t.Errorf("здоровая группа потеряна из-за соседки: пар %d", len(lessons))
	}
	if lessons := e.lessons(t, "2"); len(lessons) != 0 {
		t.Errorf("сбойная группа всё-таки записана: %+v", lessons)
	}

	// Наружу уходит только имя группы. Текст разбора Python-движка содержит
	// абсолютные пути процесса (см. ADR-0023), а журнал действий показывают.
	status := e.importAllStatus(t, "admin")
	body := status.Body.String()
	if strings.Contains(body, "не массив") {
		t.Errorf("текст ошибки разбора ушёл браузеру: %s", body)
	}
	if strings.Contains(body, testApeksToken) {
		t.Error("токен источника в прогрессе импорта")
	}
	if !strings.Contains(body, "Б-2") {
		t.Errorf("прогресс не называет группу, на которой сбой: %s", body)
	}
	assertNoSecretInAudit(t, e.h)
}

// --- Права и разбор запроса ---------------------------------------------------

func TestImportAllDeniedWithoutPermission(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t, store.SourceGroup{ExternalID: "1", Name: "А-1"})
	calls := e.attachApeks(t, func(http.ResponseWriter, *http.Request) {
		t.Error("отказанный по правам запрос дошёл до источника")
	})

	for _, role := range []string{"teacher", "student"} {
		rec := e.callImportAll(t, role, `{"month":10,"year":2026}`)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: код %d, ожидалось 403: %s", role, rec.Code, rec.Body.String())
		}
		// Прогресс — тоже админская строка: в нём имена групп и кто запускал.
		status := e.importAllStatus(t, role)
		if status.Code != http.StatusForbidden {
			t.Errorf("%s читает прогресс: код %d", role, status.Code)
		}
	}

	if calls.Load() != 0 {
		t.Errorf("источник тронут %d раз отказанными запросами", calls.Load())
	}
}

func TestImportAllDeniedWithoutCSRF(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t, store.SourceGroup{ExternalID: "1", Name: "А-1"})
	calls := e.attachApeks(t, func(http.ResponseWriter, *http.Request) {
		t.Error("запрос без CSRF-заголовка дошёл до источника")
	})

	cookies := e.loginAs(t, "admin")
	handler := e.h.RequireCSRF(e.h.RequirePerm(authz.ScheduleSourceManage, "нет права",
		http.HandlerFunc(e.h.ScheduleImportAllAPI)))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, request(http.MethodPost, importAllPath, `{"month":10,"year":2026}`, "10.0.0.9:1", cookies))

	if rec.Code != http.StatusForbidden {
		t.Errorf("код %d, ожидалось 403 — %s", rec.Code, rec.Body.String())
	}
	if calls.Load() != 0 {
		t.Errorf("источник тронут %d раз при отказе CSRF", calls.Load())
	}
}

func TestImportAllRequiresSession(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t, store.SourceGroup{ExternalID: "1", Name: "А-1"})
	e.attachApeks(t, func(http.ResponseWriter, *http.Request) {
		t.Error("анонимный запрос дошёл до источника")
	})

	rec := httptest.NewRecorder()
	http.HandlerFunc(e.h.ScheduleImportAllAPI).ServeHTTP(rec,
		request(http.MethodPost, importAllPath, `{"month":10,"year":2026}`, "10.0.0.9:1", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("запуск без пользователя: код %d, ожидалось 401", rec.Code)
	}

	status := httptest.NewRecorder()
	http.HandlerFunc(e.h.ScheduleImportAllStatusAPI).ServeHTTP(status,
		request(http.MethodGet, importAllPath, "", "10.0.0.9:1", nil))
	if status.Code != http.StatusOK {
		t.Errorf("прогресс без пользователя: код %d", status.Code)
	}
}

func TestImportAllValidatesRequest(t *testing.T) {
	e := newScheduleSourceEnv(t)
	e.seedCatalog(t, store.SourceGroup{ExternalID: "1", Name: "А-1"})
	calls := e.attachApeks(t, func(http.ResponseWriter, *http.Request) {
		t.Error("непринятый запрос дошёл до источника")
	})

	cases := []struct {
		name string
		body string
		code int
	}{
		{"не JSON", "not json", http.StatusBadRequest},
		// Группы у массовой задачи нет намеренно: поле «group» породило бы
		// соблазн качать одну кнопку в обход счётчиков прогресса.
		{"поле группы отвергнуто", `{"group":"1","month":10,"year":2026}`, http.StatusBadRequest},
		{"месяц вне диапазона", `{"month":13,"year":2026}`, http.StatusUnprocessableEntity},
		{"месяц без года", `{"month":10}`, http.StatusUnprocessableEntity},
		{"подозрительный год", `{"month":10,"year":1999}`, http.StatusUnprocessableEntity},
		{"слишком длинный период", `{"month":10,"year":2026,"count":4}`, http.StatusUnprocessableEntity},
	}

	for _, testCase := range cases {
		rec := e.callImportAll(t, "admin", testCase.body)
		if rec.Code != testCase.code {
			t.Errorf("%s: код %d, ожидалось %d — %s", testCase.name, rec.Code, testCase.code, rec.Body.String())
		}
	}

	// Ни один отказ не должен оставить половину задачи: проверка периода стоит
	// до запуска горутины.
	if calls.Load() != 0 {
		t.Errorf("источник тронут %d раз отказанными запросами", calls.Load())
	}
	if _, found := e.h.snapshotScheduleImport(); found {
		t.Error("после отказа разбора задача всё равно заведена")
	}
}

func TestImportAllStatusShapeWithoutJob(t *testing.T) {
	e := newScheduleSourceEnv(t)

	// «Массовый импорт ещё не запускался» — рабочее состояние, а не 404:
	// страница должна показать его без разбора кода.
	rec := e.importAllStatus(t, "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d, ожидалось 200: %s", rec.Code, rec.Body.String())
	}

	job := jobOf(t, rec)
	if jobFlag(t, job, "running") {
		t.Error("без задачи прогресс отдаётся выполняющимся")
	}
	// months обязан быть [], а не null: страница берёт months.length, и на null
	// это падает так же, как на пустом каталоге групп.
	months, ok := job["months"].([]any)
	if !ok {
		t.Fatalf("months не массив: %#v", job["months"])
	}
	if len(months) != 0 {
		t.Errorf("без задачи в months %d записей", len(months))
	}

	// POST в чтение прогресса — не «случайно сработало»: метод запрещён, и
	// страница не должна иметь возможности завести задачу этим путём.
	wrong := httptest.NewRecorder()
	http.HandlerFunc(e.h.ScheduleImportAllStatusAPI).ServeHTTP(wrong,
		request(http.MethodPost, importAllPath, "", "10.0.0.1:1", nil))
	if wrong.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST в прогресс: код %d, ожидалось 405", wrong.Code)
	}
}

func TestImportAllRejectsWrongMethod(t *testing.T) {
	e := newScheduleSourceEnv(t)

	rec := httptest.NewRecorder()
	http.HandlerFunc(e.h.ScheduleImportAllAPI).ServeHTTP(rec,
		request(http.MethodGet, importAllPath, "", "10.0.0.1:1", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("код %d, ожидалось 405: %s", rec.Code, rec.Body.String())
	}
}
