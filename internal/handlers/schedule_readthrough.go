package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/NumerR/SOS/internal/apeks"
	"github.com/NumerR/SOS/internal/store"
)

// Сквозное чтение расписания (ADR-0042): страница добывает месяц группы сама,
// если собственной копии этого месяца нет.
//
// Что это чинит. До этого /api/schedule/lessons умел только читать базу: 588
// групп каталога и расписание ровно у тех, которые админ успел нажать кнопкой.
// Выбор другой группы означал пустую неделю с текстом «ещё не импортировано», и
// починить её мог только админ ручным импортом — то есть страница показывала не
// расписание вуза, а следы чужих щелчков. Теперь запрос вида «группа + неделя»
// сам приводит недостающие месяцы из АПЭКС, а база остаётся единственной
// витриной: браузер токена источника не видит ни при каком раскладе.
//
// Почему база при этом обязана остаться базой, а не превратиться в прокси:
//
//   - ответ источника стоит ~260 КиБ и ~1 с, и отдавать его напрямую значит
//     повесить страницу на сайт вуза — ровно в те дни, когда его смотрят чаще
//     всего (начало семестра, день переноса занятия);
//   - пары уже лежат разобранными (подгруппа, номер пары, очищенная аудитория),
//     и второй разбор на каждый показ страницы означал бы два места, где живёт
//     правило из internal/apeks;
//   - дедупликация брифа возможна только относительно хранилища.
//
// Поэтому правило такое: показывается то, что лежит в базе, а источник
// спрашивается ровно в двух случаях — когда показывать нечего (месяц никогда не
// привозили) и когда копия есть, но устарела (тогда обновляется в фоне, и
// текущий запрос никто не задерживает).
//
// Чем это ограничено. Страницу читает кто угодно, включая гостя, а каждое
// недостающее поле — обращение к чужому сайту с токеном вуза. Границы, без
// которых этот путь был бы открытым ключом к DDoS источника:
//
//   - один месяц одной группы качается один раз, сколько бы людей его ни
//     попросили (scheduleReadCalls);
//   - одновременно идёт не больше scheduleReadWorkers обращений, лишние
//     ждут в пределах бюджета запроса и уходят, не дождавшись;
//   - за один запрос страницы проверяется не больше scheduleReadMaxMonths
//     месяцев, каким бы длинным ни оказался диапазон в строке запроса;
//   - прошлые месяцы больше не пересчитываются, а неудачная попытка
//     повторяется не раньше, чем через scheduleRetryDelay;
//   - весь путь укладывается в scheduleReadBudget — он заведомо короче
//     WriteTimeout сервера (30 с), поэтому страница отвечает всегда, даже
//     когда источник висит.
//
// Массовый импорт (schedule_import_all.go) идёт своим однопоточным шагом и эти
// счётчики не занимает: вместе они дают не больше пяти одновременных обращений к
// АПЭКС.

// Границы сквозного чтения.
const (
	// scheduleFreshRecent — насколько считается свежим пакет текущего и
	// следующего месяца. 6 часов: расписание правят рабочим днём, и гость,
	// открывший страницу утром, обязан увидеть перенос, сделанный после
	// обеда, — но не ради каждого обновления страницы, а раз в полдня на
	// группу.
	scheduleFreshRecent = 6 * time.Hour

	// scheduleFreshDistant — то же для месяцев дальше следующего. Неделя:
	// занятия декабря в ноябре уже известны, и каждые шесть часов переспрашивать
	// источник про далёкое будущее незачем.
	scheduleFreshDistant = 7 * 24 * time.Hour

	// scheduleRetryDelay — пауза после неудачной попытки. Без неё страница с
	// мёртвым источником била бы по нему на каждой загрузке, а гость ждал бы
	// таймаута вместо быстрого «показан архив».
	scheduleRetryDelay = 15 * time.Minute

	// scheduleReadBudget — потолок ожидания источника внутри одного запроса
	// страницы. 12 с при WriteTimeout 30 с: двух последовательных месяцев
	// хватает даже при медленном ответе, а ответ сервера остаётся возможным
	// до истечения своего срока.
	scheduleReadBudget = 12 * time.Second

	// scheduleReadWorkers — сколько обращений к источнику разрешено одновременно.
	// Четыре — это «несколько человек открыли разные группы», а не штурм:
	// источник уже держит последовательный прогон массового импорта.
	scheduleReadWorkers = 4

	// scheduleReadMaxMonths — сколько месяцев запрошенного периода готовы
	// добывать на одном показе страницы. Неделя задевает максимум два, и
	// длинный диапазон в query не должен превращать загрузку страницы в
	// прогон импорта.
	scheduleReadMaxMonths = 2
)

// source_state — чем отвечает страница. Пять значений покрывают все случаи,
// которые человек обязан видеть разными словами: данные свежие, данные
// привезли сейчас, данные из архива (обновляем), данные из архива (обновить не
// удалось) и показать нечего.
const (
	scheduleStateFresh       = "fresh"
	scheduleStateRefreshed   = "refreshed"
	scheduleStateCached      = "cached"
	scheduleStateStale       = "stale"
	scheduleStateUnavailable = "unavailable"
)

// scheduleReadCall — одно идущее обращение к источнику.
//
// done закрывается после того, как записан err, поэтому все, кто ждал этого же
// месяца, видят один и тот же итог и не повторяют запрос.
type scheduleReadCall struct {
	done chan struct{}
	err  error
}

// scheduleReadState — счётчики сквозного чтения. Нулевое значение рабочее: и
// карта, и семапер создаются при первом обращении под блокировкой, потому что
// Handlers собирается в cmd/server через структурный литерал и требовать от него
// конструктора значило бы однажды забыть make() и получить панику на живой
// странице.
type scheduleReadState struct {
	mu    sync.Mutex
	calls map[string]*scheduleReadCall
	slots chan struct{}
}

// scheduleReadOutcome — итог проверки периода для одного показа страницы.
type scheduleReadOutcome struct {
	fetched     int   // месяцев, привезённых и записанных, пока ждался этот запрос
	failed      int   // попыток, которые не дошли
	refreshing  int   // месяцев, отправленных на фоновую обновку
	closed      bool  // показать нечего: источника нет, или качали и не вышло
	lastFetched int64 // время последней успешной закачки (0 — её не было)
}

// sourceState — одно состояние на весь ответ; приоритет выбран по тому, что
// человек увидит на странице.
//
// «Нечего показать» важнее «привезли»: если из двух месяцев недели одного нет,
// расписание недели неполное, и сказать про это надо даже тогда, когда второй
// месяц дошёл. Следом идёт «привезли сейчас» — это обещание, что показанное не
// вчерашнее, и затмить его фоновой обновкой нельзя.
func (o scheduleReadOutcome) sourceState() string {
	switch {
	case o.closed:
		return scheduleStateUnavailable
	case o.fetched > 0:
		return scheduleStateRefreshed
	case o.refreshing > 0:
		return scheduleStateCached
	case o.failed > 0:
		return scheduleStateStale
	default:
		return scheduleStateFresh
	}
}

// scheduleEnsureMonths приводит период к состоянию «показывать есть что».
//
// Чтение и запись разнесены во времени намеренно: покрытие читается до, а пары
// — после, чтобы ListLessons видел привезённое. Отдельной транзакции тут нет, и
// это верно: импорт месяца сам атомарен (store.ReplaceMonth), а показ страницы
// обязан что-то отдать и при отказе источника.
func (h *Handlers) scheduleEnsureMonths(r *http.Request, group store.SourceGroup,
	fromKey, toKey int, coverage []store.MonthState) scheduleReadOutcome {

	var outcome scheduleReadOutcome

	// Клиента нет — ходить некуда: страница живёт тем, что уже лежит. Причина
	// «не настроен» не превращается в «источник не ответил»: админ обязан
	// видеть разные тексты, и подсказка про APEKS_TOKEN звучит из панели.
	if h.Apeks == nil {
		outcome.closed = !scheduleHasCopy(coverage, fromKey, toKey)
		return outcome
	}

	now := time.Now()
	known := scheduleCoverageIndex(coverage)
	budget, cancel := context.WithTimeout(r.Context(), scheduleReadBudget)
	defer cancel()

	for _, month := range scheduleMonthsOfRange(fromKey, toKey) {
		state, found := known[month]

		switch decideScheduleMonth(now, state, found) {
		case decisionFresh:
			// Ничего делать не нужно — и нельзя: это самый частый случай, и
			// любое обращение к источнику здесь было бы ударом впустую.

		case decisionStale:
			// Копия есть, а обновить её недавно не удалось: показываем архив и
			// молчим про источник — пауза scheduleRetryDelay ещё не истекла.
			outcome.failed++

		case decisionHold:
			// Ни копии, ни права лезть в источник: самая честная оценка недели —
			// «показать нечего».
			outcome.closed = true

		case decisionBackground:
			scheduleRefreshLater(h, group, month)
			outcome.refreshing++

		case decisionFetch:
			if outcome.fetched+outcome.failed >= scheduleReadMaxMonths {
				// Дальше по календарю — не на этой загрузке страницы.
				outcome.closed = true
				continue
			}

			err := h.scheduleFetchMonth(budget, group, month.Month, month.Year)
			if err != nil {
				outcome.failed++
				if !scheduleMonthHasCopy(state) {
					outcome.closed = true
				}
				h.logScheduleRead(group, month, "не привезён", err)
				continue
			}

			outcome.fetched++
			outcome.lastFetched = now.Unix()
			h.logScheduleRead(group, month, "привезён", nil)
		}
	}

	return outcome
}

// scheduleMonthDecision — что делать с одним месяцем периода.
type scheduleMonthDecision int

const (
	decisionFresh      scheduleMonthDecision = iota // копия свежая: источник не трогаем
	decisionStale                                   // копия есть, обновка не удалась: показываем архив
	decisionHold                                    // копии нет, в источник пока нельзя: показать нечего
	decisionBackground                              // копия есть, но устарела: обновляем в фоне
	decisionFetch                                   // копии нет: качаем, пока запрос ждёт
)

// decideScheduleMonth — правило «когда страницу спасает архив, а когда нужен
// источник».
//
// Порядок проверок здесь и есть смысл: сначала «точно свежо» (самый частый
// случай, и он не должен тратить ни одного обращения), затем пауза после
// неудачи, и только потом — чем мы располагаем: копией или ничего.
func decideScheduleMonth(now time.Time, state store.MonthState, found bool) scheduleMonthDecision {
	if !found {
		return decisionFetch
	}
	if state.OK && scheduleMonthFresh(now, state) {
		return decisionFresh
	}
	if now.Sub(time.Unix(state.AttemptedAt, 0)) < scheduleRetryDelay {
		// Недавно ходили и не дошли (или дошли, но пакет устарел внутри паузы):
		// второй удар по тому же месяцу на этой минуте ничего не изменит.
		if scheduleMonthHasCopy(state) {
			return decisionStale
		}
		return decisionHold
	}
	if scheduleMonthHasCopy(state) {
		return decisionBackground
	}
	return decisionFetch
}

// scheduleMonthHasCopy — есть ли что показать из этого месяца, даже если
// последняя проверка не удалась.
//
// ok=0 при lessons>0 — обычное дело: вчерашний пакет лежит, сегодняшняя
// обновка не дошла. Именно этот случай и требует различения «пусто» и «не
// прочитано», ради которого таблица покрытия и появилась.
func scheduleMonthHasCopy(state store.MonthState) bool { return state.OK || state.Lessons > 0 }

// scheduleHasCopy — есть ли что показать хоть в одном месяце периода.
//
// Этому предикату нужен именно период, а не месяц: страница показывает неделю
// (ADR-0040), а неделя 27 октября — 2 ноября задевает два месяца сразу.
// «Октябрь импортирован» ещё не значит, что про эту неделю что-то известно, а
// «ноябрь не импортирован» не значит, что она пуста — без этого поля честный
// текст «ещё не импортировано» на границе месяца пришлось бы выбирать наугад.
//
// Считаются только месяцы, по которым в базе действительно лежит пакет:
// успешно импортированный (в том числе пустой) или тот, что привезли раньше, а
// сегодняшняя проверка не дошла. Строка «спрашивали и ничего не получили» не
// даёт показать ничего, и период с такой строкой остаётся неимпортированным —
// страница имеет право сказать именно это, а не «занятий нет».
func scheduleHasCopy(coverage []store.MonthState, fromKey, toKey int) bool {
	for _, state := range coverage {
		first, last := scheduleMonthDays(state.Month, state.Year)
		if first <= toKey && last >= fromKey && scheduleMonthHasCopy(state) {
			return true
		}
	}
	return false
}

// scheduleMonthFresh — считается ли пакет месяца свежим.
//
// Прошлое не устаревает: перенос занятия задним числом — редкость, а пересчёт
// всех прошлых месяцев означал бы, что страница гостя каждый раз платит
// обращением к вузу за историю, которая не меняется. Дальнее будущее
// проверяется раз в неделю, текущий и следующий месяц — раз в полдня.
func scheduleMonthFresh(now time.Time, state store.MonthState) bool {
	target := scheduleMonth{Month: state.Month, Year: state.Year}
	current := scheduleMonth{Month: int(now.Month()), Year: now.Year()}

	switch {
	case monthIndex(target) < monthIndex(current):
		return true
	case monthIndex(target) <= monthIndex(current)+1:
		return now.Sub(time.Unix(state.RefreshedAt, 0)) < scheduleFreshRecent
	default:
		return now.Sub(time.Unix(state.RefreshedAt, 0)) < scheduleFreshDistant
	}
}

// monthIndex — месяц в непрерывную шкалу, чтобы не писать сравнение года и
// месяца в четырёх местах.
func monthIndex(m scheduleMonth) int { return m.Year*12 + m.Month - 1 }

// scheduleCoverageIndex — покрытие группы, доступное по (месяц, год).
//
// Повтор строки не возможен (PRIMARY KEY таблицы покрытия), но срез всё равно
// читается одним проходом, и последняя по порядку строка остаётся истинной —
// молча переписывать более свежую случайным порядком среза нельзя.
func scheduleCoverageIndex(coverage []store.MonthState) map[scheduleMonth]store.MonthState {
	index := make(map[scheduleMonth]store.MonthState, len(coverage))
	for _, state := range coverage {
		index[scheduleMonth{Month: state.Month, Year: state.Year}] = state
	}
	return index
}

// scheduleMonthsOfRange — месяцы, которых касается диапазон дней.
//
// Ключи YYYYMMDD, поэтому границы месяца берутся из них самих, а не из
// часовой зоны: тот же день, что видит фильтр ListLessons.
func scheduleMonthsOfRange(fromKey, toKey int) []scheduleMonth {
	if toKey < fromKey {
		fromKey, toKey = toKey, fromKey
	}

	months := make([]scheduleMonth, 0, scheduleReadMaxMonths+1)
	cursor := scheduleMonth{Month: fromKey / 100 % 100, Year: fromKey / 10000}
	last := scheduleMonth{Month: toKey / 100 % 100, Year: toKey / 10000}

	// 99999999 — «пара без даты»: у неё месяц 9, и в переборе она участвовать не
	// должна, иначе одна строка с битой датой заставила бы качать сентябрь.
	for guard := 0; guard <= 24 && monthIndex(cursor) <= monthIndex(last); guard++ {
		if cursor.Month >= 1 && cursor.Month <= 12 {
			months = append(months, cursor)
		}
		cursor = nextScheduleMonth(cursor)
	}

	return months
}

func nextScheduleMonth(m scheduleMonth) scheduleMonth {
	next := time.Date(m.Year, time.Month(m.Month)+1, 1, 0, 0, 0, 0, time.UTC)
	return scheduleMonth{Month: int(next.Month()), Year: next.Year()}
}

// scheduleMonthDays — первый и последний день месяца в ключах YYYYMMDD.
func scheduleMonthDays(month, year int) (int, int) {
	if month < 1 || month > 12 {
		return 0, 0
	}
	first := year*10000 + month*100 + 1
	return first, first + daysInMonth(year, time.Month(month)) - 1
}

// scheduleRefreshLater — обновка, ради которой гостя не задерживают.
//
// Контекст отвязан от запроса: ответ страницы уходит через мгновение, и снятие
// r.Context() убило бы закачку на первом же байте — ровно тот дефект, из-за
// которого массовый импорт тоже ходит со своим context.Background().
func scheduleRefreshLater(h *Handlers, group store.SourceGroup, month scheduleMonth) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), scheduleReadBudget)
		defer cancel()

		if err := h.scheduleFetchMonth(ctx, group, month.Month, month.Year); err != nil {
			h.logScheduleRead(group, month, "фоновая обновка не дошла", err)
			return
		}
		h.logScheduleRead(group, month, "фоновая обновка", nil)
	}()
}

// scheduleFetchMonth — один месяц группы, привезённый не более одного раза.
//
// Дедупликация по (источник, группа, месяц) нужна не для экономии трафика, а
// потому, что на старте семестра одну и ту же группу открывают десятки людей
// одновременно: без неё каждый из них был бы отдельным ударом по АПЭКС и
// отдельной же строкой покрытия «успешно» от чужого ответа.
//
// Ожидание идущего обращения — часть бюджета запроса, и истекший бюджет
// означает «отдадим то, что в базе»: ошибка возвращается вызывающему, и тот
// ставит в ответе архив, а не 502.
func (h *Handlers) scheduleFetchMonth(ctx context.Context, group store.SourceGroup, month, year int) error {
	key := fmt.Sprintf("%s|%s|%04d-%02d", apeks.Source, group.ExternalID, year, month)

	h.scheduleRead.mu.Lock()
	if h.scheduleRead.calls == nil {
		h.scheduleRead.calls = make(map[string]*scheduleReadCall)
	}
	if running, ok := h.scheduleRead.calls[key]; ok {
		h.scheduleRead.mu.Unlock()

		select {
		case <-running.done:
			return running.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	call := &scheduleReadCall{done: make(chan struct{})}
	h.scheduleRead.calls[key] = call
	if h.scheduleRead.slots == nil {
		h.scheduleRead.slots = make(chan struct{}, scheduleReadWorkers)
	}
	slots := h.scheduleRead.slots
	h.scheduleRead.mu.Unlock()

	defer h.finishScheduleRead(key, call)

	select {
	case slots <- struct{}{}:
	case <-ctx.Done():
		call.err = ctx.Err()
		return call.err
	}
	defer func() { <-slots }()

	// Отметку о неудаче пишет сам importGroupMonth: у неё те же границы, что у
	// любой другой закачки месяца, и своя копия здесь разошлась бы с ними при
	// первой же правке.
	_, call.err = h.importGroupMonth(ctx, group, month, year)
	return call.err
}

// finishScheduleRead закрывает обращение и снимает его из карты.
//
// Порядок важен: err записывается до close(done), а из карты ключ убирается
// после — иначе тот, кто вошёл между записью и close, увидел бы закрытый канал
// с ещё не записанной ошибкой, а новый пришедший завёл бы второе обращение к
// источнику вместо ожидания первого.
func (h *Handlers) finishScheduleRead(key string, call *scheduleReadCall) {
	h.scheduleRead.mu.Lock()
	if current, ok := h.scheduleRead.calls[key]; ok && current == call {
		delete(h.scheduleRead.calls, key)
	}
	h.scheduleRead.mu.Unlock()

	close(call.done)
}

// logScheduleRead — строка журнала об обращении, заведённом страницей.
//
// Аудит эти вызовы не пишут намеренно: журнал админских действий — это щелчки
// человека с правами, а не след каждого показа страницы. Текст без токена и без
// URL: обе гарантии даёт internal/apeks (см. Client.scrub), и здесь добавлять
// нечего.
func (h *Handlers) logScheduleRead(group store.SourceGroup, month scheduleMonth, what string, err error) {
	fields := []any{
		slog.String("group", group.ExternalID),
		slog.String("name", group.Name),
		slog.String("month", fmt.Sprintf("%02d.%d", month.Month, month.Year)),
		slog.String("what", what),
	}
	if err != nil {
		slog.Warn("расписание: сквозное чтение источника", append(fields, slog.Any("error", err))...)
		return
	}
	slog.Info("расписание: сквозное чтение источника", fields...)
}

// scheduleImportedMonths — месяцы, о которых база знает что показать.
//
// Сюда попадают и успешно импортированные пустые месяцы, и месяцы, чья
// последняя проверка не дошла, но пакет которых лежит: и то и другое — «в базе
// есть», а «спрашивали и ничего нет» остаётся за списком, иначе подсказка
// «в базе уже октябрь» соврёт про пустой импорт.
func scheduleImportedMonths(coverage []store.MonthState) []store.MonthState {
	months := make([]store.MonthState, 0, len(coverage))
	for _, state := range coverage {
		if scheduleMonthHasCopy(state) {
			months = append(months, state)
		}
	}
	return months
}

// scheduleLastRefresh — когда привезён самый свежий из лежащих пакетов группы
// (0 — пакетов нет).
//
// Считается по покрытию, а не по строкам пар: месяц, который качали и в котором
// занятий нет, — это тоже «обновлено тогда-то», и подпись «эта группа ещё не
// импортирована» над пустым сентябрём была бы неправдой.
func scheduleLastRefresh(coverage []store.MonthState) int64 {
	var last int64
	for _, state := range coverage {
		if state.OK && state.RefreshedAt > last {
			last = state.RefreshedAt
		}
	}
	return last
}
