document.addEventListener("DOMContentLoaded", async () => {
    const feedSection = document.getElementById("feedSection");
    const feedContainer = document.getElementById("feed");
    const resourcesSection = document.getElementById("resourcesSection");
    const resourcesContainer = document.getElementById("resources");
    const createResourceSection = document.getElementById("createResourceSection");
    const resourceForm = document.getElementById("resourceForm");
    const resourceMsg = document.getElementById("resourceMsg");
    const eventsManageSection = document.getElementById("eventsManageSection");
    const eventForm = document.getElementById("eventForm");
    const eventMsg = document.getElementById("eventMsg");
    const guestNotice = document.getElementById("guestNotice");
    const loginLink = document.getElementById("loginLink");
    const refreshButton = document.getElementById("refreshBtn");
    const refreshResources = document.getElementById("refreshResources");
    const applyFilter = document.getElementById("applyFilter");
    const fromDate = document.getElementById("fromDate");
    const toDate = document.getElementById("toDate");

    // Официальное расписание (T4.1) и панель источника.
    const groupInput = document.getElementById("groupInput");
    const groupOptions = document.getElementById("groupOptions");
    const groupHelper = document.getElementById("groupHelper");
    const weekLabel = document.getElementById("weekLabel");
    const prevWeek = document.getElementById("prevWeek");
    const nextWeek = document.getElementById("nextWeek");
    const currentWeekBtn = document.getElementById("currentWeek");
    const refreshTimetable = document.getElementById("refreshTimetable");
    const timetableContainer = document.getElementById("timetable");
    const updatedNote = document.getElementById("updatedNote");
    const sourceName = document.getElementById("sourceName");
    const sourceNameNote = document.getElementById("sourceNameNote");
    const sourceLink = document.getElementById("sourceLink");
    const sourceSection = document.getElementById("sourceManageSection");
    const sourceMsg = document.getElementById("sourceMsg");
    const sourceHint = document.getElementById("sourceHint");
    const sourceJobNote = document.getElementById("sourceJobNote");
    const statGroups = document.getElementById("statGroups");
    const statLessons = document.getElementById("statLessons");
    const statImportedGroups = document.getElementById("statImportedGroups");
    const syncGroupsBtn = document.getElementById("syncGroupsBtn");
    const importGroupBtn = document.getElementById("importGroupBtn");
    const importAllBtn = document.getElementById("importAllBtn");
    const importCount = document.getElementById("importCount");

    const canManage = GS.can.manageBoard;
    const canManageSource = GS.can.manageScheduleSource;

    const stateMeta = {
        none:      { label: "не сдано",    cls: "feed-state-none" },
        submitted: { label: "сдано",       cls: "feed-state-submitted" },
        graded:    { label: "оценено",     cls: "feed-state-graded" },
        own:       { label: "моё задание", cls: "feed-state-own" },
    };

    let user = null;

    // Даты — общий модуль core/format.js: раньше эти же пять функций были
    // написаны здесь заново (и «Без дедлайна»/«без точной даты» расходились в
    // формулировках с дашбордом).
    const toISODate = GS.toISODate;
    const dayStartUnix = GS.dayStartUnix;
    const dayEndUnix = GS.dayEndUnix;
    const formatDue = GS.formatDateTime;

    const today = new Date();
    const weekLater = addDays(today, 7);
    if (fromDate) fromDate.value = toISODate(today);
    if (toDate) toDate.value = toISODate(weekLater);

    // Состояние таймтейбла.
    //
    // group — ровно то, что уйдёт в параметр group: id выбранной группы, если
    // ввод совпал с каталогом, и иначе сам ввод (сервер умеет разрешать имя,
    // и живая строка «Б-ИСиТ-22» из поиска — штатный случай).
    //
    // Неделя, а не месяц (запрос владельца): человек смотрит расписание на
    // «эту неделю», а месячный список — 30+ карточек прокрутки, чтобы найти
    // вторник. weekStart — локальный понедельник, от него считаются границы
    // периода в запросе: сервер фильтрует по дням источника (YYYYMMDD) и
    // одинаково понимает и неделю, и месяц, поэтому граница посреди месяца для
    // него не особая.
    const timetable = {
        group: "",
        groupName: "",
        weekStart: startOfWeek(today),
        rangeImported: false,
        importedMonths: [],
    };

    // Каталог групп держим в переменной: он нужен и чтобы превратить имя из
    // поля ввода в однозначный id, и после синхронизации админом, когда список
    // на странице меняется прямо во время просмотра.
    let catalogue = [];

    // Группа переживает перезагрузку страницы: студент открывает расписание
    // утром и вечером, и каждый раз вводить группу заново — значит сделать
    // страницу хуже, чем бумажное расписание на стенде.
    //
    // Неделя, в отличие от группы, не запоминается: страница всегда открывается
    // на сегодняшнем дне. Иначе вчерашний просмотр декабря показывал бы декабрь
    // и в октябре, а «сегодня» приходилось бы искать вручную.
    const CHOICE_KEY = "sos.schedule.choice";

    function saveChoice() {
        try {
            localStorage.setItem(CHOICE_KEY, JSON.stringify({
                group: timetable.group,
                name: timetable.groupName,
            }));
        } catch (error) {
            // Приватный режим и переполненное хранилище — не причина ронять
            // страницу: выбор группы это удобство, а не данные расписания.
        }
    }

    function readChoice() {
        try {
            const raw = localStorage.getItem(CHOICE_KEY);
            if (!raw) return null;
            const parsed = JSON.parse(raw);
            return parsed && typeof parsed === "object" ? parsed : null;
        } catch (error) {
            return null;
        }
    }

    // ---------- лента (дедлайны + события) ----------
    function renderFeed(items) {
        if (!feedContainer) return;
        if (!items.length) {
            feedContainer.innerHTML = `
                <div class="empty-state" style="border:0;box-shadow:none;background:transparent;">
                    <span class="empty-text">В этом диапазоне ничего нет</span>
                    <span class="empty-hint">Расширьте период или добавьте задания/события.</span>
                </div>`;
            return;
        }
        feedContainer.innerHTML = items.map((it) => {
            if (it.kind === "event") {
                return `
                    <article class="feed-item is-event">
                        <div class="feed-top">
                            <div class="feed-info">
                                <h4>${GS.escapeHtml(it.title)}</h4>
                                <span class="feed-course">Событие</span>
                            </div>
                            <span class="feed-state feed-state-event">событие</span>
                        </div>
                        ${it.description ? `<p class="feed-desc">${GS.escapeHtml(it.description)}</p>` : ""}
                        <div class="feed-meta">
                            <span class="feed-due">${it.due_date > 0 ? formatDue(it.due_date) : "без точной даты"}</span>
                        </div>
                    </article>`;
            }
            const meta = stateMeta[it.state] || stateMeta.none;
            const scoreTag = it.state === "graded" && it.score >= 0
                ? `<span class="feed-score">${it.score.toFixed(1)}</span>` : "";
            const overdue = it.due_date > 0 && (Date.now() / 1000) > it.due_date && it.state === "none";
            return `
                <article class="feed-item ${overdue ? "is-overdue" : ""}">
                    <div class="feed-top">
                        <div class="feed-info">
                            <h4>${GS.escapeHtml(it.title)}</h4>
                            <span class="feed-course">${GS.escapeHtml(it.course_title || "")}</span>
                        </div>
                        <span class="feed-state ${meta.cls}">${GS.escapeHtml(meta.label)}${scoreTag}</span>
                    </div>
                    ${it.description ? `<p class="feed-desc">${GS.escapeHtml(it.description)}</p>` : ""}
                    <div class="feed-meta">
                        <span class="feed-due">${formatDue(it.due_date)}${overdue ? " · просрочено" : ""}</span>
                        <span class="dot-sep"></span>
                        <span>задание #${GS.escapeHtml(it.assignment_id)}</span>
                    </div>
                </article>`;
        }).join("");
    }

    async function loadFeed() {
        if (!feedContainer) return;
        feedContainer.innerHTML = `<div class="empty-state" style="border:0;box-shadow:none;background:transparent;"><span class="spinner"></span><span class="empty-hint">Загрузка ленты…</span></div>`;

        const from = dayStartUnix(fromDate.value || toISODate(today));
        const to = dayEndUnix(toDate.value || toISODate(weekLater));

        try {
            const items = await GS.fetchJSON(`/api/schedule?from=${from}&to=${to}`, { on401: "throw" });
            renderFeed(items);
        } catch (error) {
            if (error.status === 401) { showGuestMode(); return; }
            feedContainer.innerHTML = `<p class="message message-error">${GS.escapeHtml(error.message || "Не удалось загрузить ленту")}</p>`;
        }
    }

    // ---------- официальное расписание ----------
    //
    // Источник (АПЭКС-ВУЗ) к странице не подключается: страница читает только
    // импортированные строки через /api/schedule/*, а токен источника остаётся
    // на сервере. Поэтому здесь нет ни одного состояния «введите токен», и
    // гость видит то же расписание, что и студент.

    function plural(count, one, few, many) {
        const mod10 = count % 10;
        const mod100 = count % 100;
        if (mod10 === 1 && mod100 !== 11) return one;
        if (mod10 >= 2 && mod10 <= 4 && (mod100 < 10 || mod100 >= 20)) return few;
        return many;
    }

    // Понедельник недели, в которую попадает дата. Локальное время, не UTC:
    // «эта неделя» считается по календарю человека, который смотрит страницу, и
    // в 00:30 воскресенья по Гринвичу этот календарь ещё суббота.
    function startOfWeek(date) {
        const shift = (date.getDay() + 6) % 7;
        return new Date(date.getFullYear(), date.getMonth(), date.getDate() - shift);
    }

    function monthTitle(month, year) {
        const date = new Date(year, month - 1, 1);
        // «г.» в конце убран: в перечислении («в базе уже октябрь 2026, ноябрь
        // 2026») он шум, а точка фразы после него превращалась бы в «г..».
        return date.toLocaleDateString("ru-RU", { month: "long", year: "numeric" }).replace(/\s*г\.$/, "");
    }

    // Родительный падеж месяца: «8 ноября» -> «ноября».
    //
    // Считается датой, а не словарём названий: {month: "long"} сам по себе
    // даёт именительный («ноябрь»), и подписать «2–8 ноябрь» было бы грубой
    // ошибкой в языке, которую видно на каждой второй неделе.
    function monthGenitive(date) {
        return date.toLocaleDateString("ru-RU", { day: "numeric", month: "long" }).replace(/^\d+\s+/, "");
    }

    // Прибавка дней через календарь, а не через миллисекунды: сутки бывают 23
    // и 25 часа, и «+6*24 ч» через переход в летнее время дал бы воскресенье
    // вместо субботы — конец недели уехал бы на день.
    function addDays(date, days) {
        return new Date(date.getFullYear(), date.getMonth(), date.getDate() + days);
    }

    // Подпись недели: «2–8 ноября», «26 октября – 1 ноября»,
    // «29 декабря 2026 – 4 января 2027».
    //
    // Три формы, а не одна: достаточно месяца, когда неделя внутри него, два
    // — когда пересекает границу, и год — когда пересекает новогоднюю. «29
    // декабря – 4 января» без года читается наугад («это же прошлая неделя?»),
    // и именно на такой неделе человек планирует поездку.
    function weekTitle(start) {
        const end = addDays(start, 6);

        if (start.getMonth() === end.getMonth()) {
            return `${start.getDate()}–${end.getDate()} ${monthGenitive(end)}`;
        }

        const day = (date) => `${date.getDate()} ${monthGenitive(date)}`;
        if (start.getFullYear() === end.getFullYear()) {
            return `${day(start)} – ${day(end)}`;
        }
        return `${day(start)} ${start.getFullYear()} – ${day(end)} ${end.getFullYear()}`;
    }

    function setWeekLabel() {
        if (weekLabel) weekLabel.textContent = weekTitle(timetable.weekStart);
    }

    // Период показа в unix-секундах: от полуночи понедельника до конца
    // воскресенья. Границы считает общий с лентой модуль core/format.js —
    // dayStartUnix/dayEndUnix, чтобы «эта неделя» в расписании и «эта неделя» в
    // ленте дедлайнов были одним и тем же отрезком.
    function weekRange(start) {
        return {
            from: dayStartUnix(toISODate(start)),
            to: dayEndUnix(toISODate(addDays(start, 6))),
        };
    }

    // Дата из toLocaleDateString кончается «г.», и своя точка поверх неё —
    // «г..». Одна проверка на оба случая.
    function sentenceEnd(value) {
        return /[.!?]$/.test(value) ? value : `${value}.`;
    }

    // Что уже лежит в базе по этой группе — подсказка к пустой неделе.
    //
    // Три месяца максимум: список «в базе уже 14 месяцев» человек не читает, а
    // направление («вот сюда импорт уже приходил») из него видно и по трём.
    function monthList(months) {
        if (!Array.isArray(months) || !months.length) return "";
        const titles = months.slice(0, 3).map((m) => monthTitle(m.month, m.year));
        return ` — в базе уже ${titles.join(", ")}`;
    }

    // Пустая неделя без импорта обязана называть путь наружу, и путь зависит от
    // роли: админу — кнопка своей панели, студенту — что он бессилен и к кому
    // идти. Одна фраза на обеих ролях всегда врёт бы хотя бы одной из них.
    function importHint(role) {
        return canManageSource(role)
            ? "Импортируйте её кнопками панели «Источник расписания» ниже: «все группы» закрывает каталог за один прогон."
            : "Расписание этой группы ещё не скачано с сайта вуза — это делает администратор платформы.";
    }

    function renderTimetableEmpty(text, hint) {
        if (!timetableContainer) return;
        timetableContainer.innerHTML = `
            <div class="empty-state plain">
                <span class="empty-text">${GS.escapeHtml(text)}</span>
                ${hint ? `<span class="empty-hint">${GS.escapeHtml(hint)}</span>` : ""}
            </div>`;
    }

    // Живые данные источника подсказали две мелочи, которые на фикстурах из
    // юнит-тестов не видны: аудитория приходит уже с префиксом («к. 3/314»,
    // «без ауд.»), поэтому «ауд. к. 3/314» было бы тавтологией; а подгруппа
    // без подгруппы кодируется длинным тире, и «подгруппа —» человеку ничего
    // не объясняет.
    const NO_SUBGROUP = new Set(["", "—", "-", "нет"]);

    // ---------- состояние пары ----------
    //
    // День приходит как day_key (YYYYMMDD), время — отдельными start_time и
    // end_time. Поле time («08:30 – 10:05») остаётся запасным: строки, которые
    // импортировали до появления этих полей, всё ещё лежат в базе.
    //
    // Сравнение идёт с часами браузера: источник называет день в местном
    // времени вуза, а студент смотрит расписание там же. Хранить здесь UTC
    // значило бы сдвинуть «пару в 08:30» на часовой пояс сервера.
    const STATUS_LABELS = { pending: "Не начата", active: "Идёт", done: "Закончена" };
    const STATUS_ICONS = {
        pending: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/></svg>',
        active: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><circle cx="12" cy="12" r="9"/><circle class="status-dot" cx="12" cy="12" r="3.5" fill="currentColor" stroke="none"/></svg>',
        done: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="m8 12 3 3 5-6"/></svg>',
    };

    function clock(value) {
        const match = String(value || "").match(/(\d{1,2}):(\d{2})/);
        if (!match) return null;
        const hours = Number(match[1]);
        const minutes = Number(match[2]);
        if (hours > 23 || minutes > 59) return null;
        return { hours, minutes };
    }

    function lessonBoundary(lesson, field, fallbackIndex) {
        const key = String(lesson.day_key || "");
        // 99999999 — служебный ключ строк без даты (см. formatDayKey): у такой
        // пары нет дня, значит и состояния «началась/закончилась» нет.
        if (!/^\d{8}$/.test(key) || key === "99999999") return 0;

        let time = clock(lesson[field]);
        if (!time) {
            const parts = String(lesson.time || "").split(/[–—-]/);
            time = clock(parts[fallbackIndex] || parts[0]);
        }
        if (!time) return 0;

        const year = Number(key.slice(0, 4));
        const month = Number(key.slice(4, 6));
        const day = Number(key.slice(6, 8));
        return new Date(year, month - 1, day, time.hours, time.minutes, 0, 0).getTime();
    }

    function stateAt(start, end, now) {
        if (now < start) return "pending";
        if (end && now >= end) return "done";
        return "active";
    }

    // Отдельно дни/часы/минуты: «через 2 ч 15 мин» читается, а «через 15360 с»
    // заставляет считать в уме. Секунды остаются только на последней минуте,
    // где именно они и нужны.
    function countdown(ms) {
        const total = Math.max(0, Math.ceil(ms / 1000));
        if (total >= 86400) {
            const days = Math.floor(total / 86400);
            const hours = Math.floor((total % 86400) / 3600);
            return `${days} ${plural(days, "день", "дня", "дней")}${hours ? ` ${hours} ч` : ""}`;
        }
        if (total >= 3600) {
            const hours = Math.floor(total / 3600);
            const minutes = Math.floor((total % 3600) / 60);
            return `${hours} ч ${minutes} мин`;
        }
        if (total >= 60) return `${Math.floor(total / 60)} мин`;
        return `${total} с`;
    }

    function timerText(state, start, end, now) {
        if (state === "pending") return `через ${countdown(start - now)}`;
        if (state === "active" && end) return `осталось ${countdown(end - now)}`;
        return "";
    }

    function statusHtml(state) {
        return `${STATUS_ICONS[state]}<span>${STATUS_LABELS[state]}</span>`;
    }

    function renderLessonStatus(start, end, now) {
        const state = stateAt(start, end, now);
        const timer = timerText(state, start, end, now);
        return {
            active: state === "active",
            html: `
                <div class="lesson-state" data-start="${start}"${end ? ` data-end="${end}"` : ""}>
                    <span class="lesson-status is-${state}">${statusHtml(state)}</span>
                    <span class="lesson-status-timer">${GS.escapeHtml(timer)}</span>
                </div>`,
        };
    }

    // Тик раз в секунду. Плашку перерисовываем только при смене состояния:
    // иначе значок «идёт» перезапускал бы анимацию каждый раз, когда меняется
    // подпись таймера, и выглядел бы дёрганым.
    function refreshLessonStates() {
        if (!timetableContainer) return;
        const now = Date.now();

        timetableContainer.querySelectorAll(".lesson-state[data-start]").forEach((box) => {
            const start = Number(box.dataset.start);
            if (!start) return;
            const end = box.dataset.end ? Number(box.dataset.end) : 0;
            const state = stateAt(start, end, now);
            const lesson = box.closest(".lesson");
            if (lesson) lesson.classList.toggle("is-active", state === "active");

            const badge = box.querySelector(".lesson-status");
            if (badge && !badge.classList.contains(`is-${state}`)) {
                badge.className = `lesson-status is-${state}`;
                badge.innerHTML = statusHtml(state);
            }

            const timer = box.querySelector(".lesson-status-timer");
            if (timer) timer.textContent = timerText(state, start, end, now);
        });
    }

    let lessonTicker = null;
    function startLessonTicker() {
        if (lessonTicker) return;
        lessonTicker = setInterval(refreshLessonStates, 1000);
    }

    function renderLesson(lesson) {
        const parts = [];
        if (lesson.time) parts.push(`<span class="lesson-time">${GS.escapeHtml(lesson.time)}</span>`);
        if (lesson.lesson_type) parts.push(`<span class="lesson-type">${GS.escapeHtml(lesson.lesson_type)}</span>`);
        if (lesson.room) parts.push(`<span>${GS.escapeHtml(lesson.room)}</span>`);
        if (lesson.teacher) parts.push(`<span>${GS.escapeHtml(lesson.teacher)}</span>`);
        const subgroup = String(lesson.subgroup || "").trim();
        const sub = NO_SUBGROUP.has(subgroup) ? "" : subgroup;

        const start = lessonBoundary(lesson, "start_time", 0);
        const end = start ? lessonBoundary(lesson, "end_time", 1) : 0;
        const status = start ? renderLessonStatus(start, end, Date.now()) : null;

        return `
            <div class="lesson${status && status.active ? " is-active" : ""}">
                <span class="lesson-num">${GS.escapeHtml(lesson.lesson_number || "?")}</span>
                <div class="lesson-body">
                    <span class="lesson-title">${GS.escapeHtml(lesson.discipline || "Дисциплина не указана")}</span>
                    <span class="lesson-meta">${parts.join('<span class="dot-sep"></span>')}</span>
                    ${sub ? `<span class="lesson-sub">подгруппа ${GS.escapeHtml(sub)}</span>` : ""}
                </div>
                ${status ? status.html : ""}
            </div>`;
    }

    // Пар в дне — это уникальные номера, а не строки: две подгруппы одной
    // четвёртой пары и пятая пара дают две пары, а не три. Строки без номера
    // (старый импорт) считаем построчно, иначе день обнулился бы.
    function pairCount(lessons) {
        const numbers = new Set();
        for (const lesson of lessons) {
            const number = Number(lesson.lesson_number);
            if (Number.isInteger(number) && number > 0) numbers.add(number);
        }
        return numbers.size || lessons.length;
    }

    function localDayKey(date = new Date()) {
        return date.getFullYear() * 10000 + (date.getMonth() + 1) * 100 + date.getDate();
    }

    // Страница открывается на сегодняшнем дне: неделя уже сегодняшняя, но
    // начинается она с понедельника, и без прокрутки четверг стоит третьим
    // карточкой под заголовком. Если сегодня пар нет, показываем ближайший
    // будущий день — именно там начнётся учёба.
    function revealToday() {
        if (!timetableContainer) return;
        const today = localDayKey();
        const cards = Array.from(timetableContainer.querySelectorAll(".sched-day[data-day-key]"));
        const card = cards.find((el) => Number(el.dataset.dayKey) === today)
            || cards.find((el) => Number(el.dataset.dayKey) > today);
        if (!card || typeof card.scrollIntoView !== "function") return;

        const reduce = typeof window.matchMedia === "function"
            && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
        card.scrollIntoView({ block: "center", behavior: reduce ? "auto" : "smooth" });
    }

    let pendingTodayScroll = true;

    function renderTimetable(days) {
        if (!timetableContainer) return;
        const today = localDayKey();

        timetableContainer.innerHTML = days.map((day, index) => {
            const lessons = day.lessons || [];
            const count = pairCount(lessons);
            const isToday = Number(day.day_key) === today;
            const head = `
                <header class="sched-day-head">
                    <span class="sched-day-weekday">${GS.escapeHtml(day.weekday || "")}</span>
                    <span class="sched-day-date">${GS.escapeHtml(day.date || formatDayKey(day.day_key))}</span>
                    ${isToday ? '<span class="sched-today-chip">Сегодня</span>' : ""}
                    <span class="sched-day-count">${count} ${plural(count, "пара", "пары", "пар")}</span>
                </header>`;

            return `
                <article class="sched-day${isToday ? " is-today" : ""}"
                         data-day-key="${GS.escapeHtml(String(day.day_key || ""))}"
                         style="--i: ${index}">
                    ${head}
                    <div class="lesson-list">${lessons.map(renderLesson).join("")}</div>
                </article>`;
        }).join("");

        if (pendingTodayScroll) {
            pendingTodayScroll = false;
            revealToday();
        }
    }

    // День без даты от источника: у таких пар day_key = 99999999 (см. store),
    // и печатать его как дату «99.99.9999» нельзя — это ровно тот случай, где
    // честнее сказать «без точной даты».
    function formatDayKey(dayKey) {
        const raw = String(dayKey || "");
        if (raw === "99999999") return "без точной даты";
        if (raw.length !== 8) return "дата не указана";
        return `${raw.slice(6, 8)}.${raw.slice(4, 6)}.${raw.slice(0, 4)}`;
    }

    // Подпись под таблицей. Отдельная функция: состояние «обновлено тогда-то»
    // и состояние «данных нет» обязаны объясняться одной строкой на одном и том
    // же месте, а не спорить друг с другом текстами из разных веток.
    function setNote(text) {
        if (updatedNote) updatedNote.textContent = text || "";
    }

    function updatedText(unix) {
        if (!unix) return "Расписание этой группы ещё не импортировано из источника.";

        const time = new Date(unix * 1000).toLocaleTimeString("ru-RU", { hour: "2-digit", minute: "2-digit" });
        const who = timetable.groupName || (groupInput ? groupInput.value.trim() : "");
        return `Обновлено ${GS.formatDate(unix)} в ${time}${who ? ` · ${who}` : ""}`;
    }

    // Текст для группы: имя всегда человеческое, id — только в каталоге.
    function fillGroupOptions(groups) {
        if (!groupOptions) return;
        groupOptions.innerHTML = groups
            .map((g) => `<option value="${GS.escapeHtml(g.name)}"></option>`)
            .join("");
    }

    // Ввод совпадает с каталогом (в любом регистре и с лишними пробелами) —
    // отправляем id: он однозначен, а имя из каталога однажды может
    // повториться у двух институтов, и сервер честно откажет 422.
    function resolveGroupRef(input, groups) {
        const needle = String(input || "").trim().toLowerCase();
        if (!needle) return "";
        const exact = groups.find((g) => String(g.name).trim().toLowerCase() === needle);
        return exact ? exact.external_id : String(input).trim();
    }

    async function loadGroups() {
        try {
            const data = await GS.fetchJSON("/api/schedule/groups", { on401: "throw" });
            const groups = Array.isArray(data.groups) ? data.groups : [];

            fillGroupOptions(groups);
            if (sourceName && data.source_name) sourceName.textContent = data.source_name;
            if (sourceNameNote) sourceNameNote.textContent = `источник: ${data.source_name || "—"}`;
            if (sourceLink && data.source_page_url) sourceLink.href = data.source_page_url;

            const synced = data.synced_at ? ` · каталог обновлён ${GS.formatDate(data.synced_at)}` : "";
            if (groupHelper) {
                groupHelper.textContent = groups.length
                    ? `В каталоге ${groups.length} ${plural(groups.length, "группа", "группы", "групп")}${synced}`
                    : `Каталог групп пуст${synced}`;
            }

            return groups;
        } catch (error) {
            if (groupHelper) groupHelper.textContent = error.message || "Не удалось загрузить каталог групп";
            return [];
        }
    }

    async function loadTimetable(groups) {
        if (!timetableContainer) return;

        setWeekLabel();
        if (Array.isArray(groups)) catalogue = groups;
        const ref = resolveGroupRef(groupInput ? groupInput.value : timetable.group, catalogue);
        if (!ref) {
            renderTimetableEmpty("Выберите группу", "Начните вводить название — страница подсказывает группы из каталога.");
            return;
        }

        timetable.group = ref;
        if (groupInput && catalogue.length) {
            const found = catalogue.find((g) => g.external_id === ref);
            if (found) timetable.groupName = found.name;
        }

        timetableContainer.innerHTML = `<div class="empty-state plain"><span class="spinner"></span><span class="empty-hint">Загрузка расписания…</span></div>`;

        const period = weekRange(timetable.weekStart);
        const query = `/api/schedule/lessons?group=${encodeURIComponent(timetable.group)}`
            + `&from=${period.from}&to=${period.to}`;

        try {
            const data = await GS.fetchJSON(query, { on401: "throw" });
            timetable.importedMonths = Array.isArray(data.imported_months) ? data.imported_months : [];
            timetable.rangeImported = data.range_imported === true;
            if (data.group_name) timetable.groupName = data.group_name;

            const days = Array.isArray(data.days) ? data.days : [];

            if (!days.length) {
                // Два разных «пусто»: период ещё не импортирован (данных нет в
                // базе) и импортирован, но пар в нём нет. Сваливать их в одну
                // фразу нельзя: в первом случае нужен импорт, во втором — искать
                // дальше по календарю.
                //
                // Поле «импортирован ли период» читается из ответа сервера
                // (range_imported), а не выводится из списка месяцев на
                // странице: неделя легко задевает два месяца, и по одному из
                // них вывод был бы неверен в обе стороны.
                renderTimetableEmpty(
                    timetable.rangeImported ? "На этой неделе пар нет" : "Расписание на эту неделю ещё не импортировано",
                    timetable.rangeImported
                        ? `Посмотрите другую неделю${monthList(timetable.importedMonths)}.`
                        : importHint(user && user.role),
                );
            } else {
                renderTimetable(days);
            }

            setNote(updatedText(data.updated_at));
            saveChoice();
        } catch (error) {
            timetable.importedMonths = [];
            timetable.rangeImported = false;
            renderTimetableEmpty(
                error.status === 404
                    ? "Такой группы нет в импортированном каталоге"
                    : error.message || "Не удалось загрузить расписание",
                error.status === 404
                    ? "Синхронизируйте каталог групп — возможно, её добавили в вузе позже."
                    : "Попробуйте обновить страницу через минуту.",
            );
            // Пустая строка вместо «обновлено тогда-то»: при сбое загрузки мы не
            // знаем, актуальны ли данные, и утверждать это нельзя.
            setNote("");
        }
    }

    // Неделя шагается по датам, а не по миллисекундам: +7*24 часа через переход
    // в летнее время сдвинули бы понедельник на день, и «эта неделя» съехала бы
    // без всякой вины человека.
    function shiftWeek(delta) {
        const start = timetable.weekStart;
        timetable.weekStart = new Date(start.getFullYear(), start.getMonth(), start.getDate() + 7 * delta);
        return loadTimetable();
    }

    // Кнопки: стрелки листают неделю, «Текущая неделя» возвращает к сегодняшнему
    // дню (и прокручивает список к нему), Enter и «смена» поля группы ищут
    // расписание выбранной группы.
    function bindWeekControls() {
        if (prevWeek) prevWeek.addEventListener("click", () => shiftWeek(-1));
        if (nextWeek) nextWeek.addEventListener("click", () => shiftWeek(1));
        if (currentWeekBtn) currentWeekBtn.addEventListener("click", () => {
            timetable.weekStart = startOfWeek(new Date());
            pendingTodayScroll = true;
            return loadTimetable();
        });
        if (refreshTimetable) refreshTimetable.addEventListener("click", () => loadTimetable());

        if (groupInput) {
            // change срабатывает и на Enter, и на выборе из datalist — этого
            // достаточно; отдельная кнопка «найти» добавила бы второй путь к
            // тому же запросу.
            groupInput.addEventListener("change", () => loadTimetable());
        }
    }

    // ---------- ресурсы ----------
    function isHttpUrl(u) { return /^https?:\/\//i.test(String(u || "").trim()); }

    function renderResources(resources) {
        if (!resourcesContainer) return;
        if (!resources.length) {
            resourcesContainer.innerHTML = `
                <div class="empty-state">
                    <span class="empty-text">Пока нет ни одного ресурса</span>
                    <span class="empty-hint">${canManage(user.role) ? "Добавьте первый через форму ниже." : "Ресурсы появятся, когда их добавят преподаватели."}</span>
                </div>`;
            return;
        }
        resourcesContainer.innerHTML = resources.map((r) => {
            const cat = r.category ? `<span class="chip chip-muted">${GS.escapeHtml(r.category)}</span>` : "";
            const link = isHttpUrl(r.url)
                ? `<a class="btn btn-outline res-link" target="_blank" rel="noopener noreferrer" href="${GS.escapeHtml(r.url)}">Открыть
                     <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/><path d="M15 3h6v6M10 14 21 3"/></svg></a>`
                : "";
            return `
                <article class="card">
                    <div class="card-top">
                        <span class="card-icon" aria-hidden="true">
                            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 19.5A2.5 2.5 0 0 1 6.5 17H20"/><path d="M6.5 2H20v20H6.5A2.5 2.5 0 0 1 4 19.5v-15A2.5 2.5 0 0 1 6.5 2z"/></svg>
                        </span>
                        ${cat}
                    </div>
                    <h3 class="card-title">${GS.escapeHtml(r.title)}</h3>
                    <p class="card-text">${GS.escapeHtml(r.description || "Описание не указано.")}</p>
                    <div class="card-actions">${link}</div>
                </article>`;
        }).join("");
    }

    async function loadResources() {
        if (!resourcesContainer) return;
        resourcesContainer.innerHTML = `<div class="empty-state"><span class="spinner"></span><span class="empty-hint">Загрузка ресурсов…</span></div>`;
        try {
            const resources = await GS.fetchJSON("/api/resources", { on401: "throw" });
            renderResources(resources);
        } catch (error) {
            if (error.status === 401) { showGuestMode(); return; }
            resourcesContainer.innerHTML = `<p class="message message-error">${GS.escapeHtml(error.message || "Не удалось загрузить ресурсы")}</p>`;
        }
    }

    // ---------- режимы ----------
    function showGuestMode() {
        if (feedSection) feedSection.classList.add("hidden");
        if (resourcesSection) resourcesSection.classList.add("hidden");
        if (eventsManageSection) eventsManageSection.classList.add("hidden");
        if (sourceSection) sourceSection.classList.add("hidden");
        if (guestNotice) guestNotice.classList.remove("hidden");
        if (loginLink) loginLink.classList.remove("hidden");
    }

    function showAuthedMode() {
        if (feedSection) feedSection.classList.remove("hidden");
        if (resourcesSection) resourcesSection.classList.remove("hidden");
        if (guestNotice) guestNotice.classList.add("hidden");
        if (loginLink) loginLink.classList.add("hidden");

        const manage = canManage(user.role);
        if (createResourceSection) createResourceSection.classList.toggle("hidden", !manage);
        if (eventsManageSection) eventsManageSection.classList.toggle("hidden", !manage);
        if (sourceSection) sourceSection.classList.toggle("hidden", !canManageSource(user.role));
    }

    // Сбой профиля, отличный от 401 (сервер 500, сети нет): расписание уже
    // нарисовано, поэтому показываем беду там, где ей и место — в разделе ленты,
    // — и не выдаём это за «войдите, чтобы увидеть больше».
    function showProfileFailure(error) {
        if (guestNotice) guestNotice.classList.add("hidden");
        if (feedSection) feedSection.classList.remove("hidden");
        if (feedContainer) {
            feedContainer.innerHTML = `<p class="message message-error">${GS.escapeHtml(error.message || "Не удалось загрузить профиль")}</p>`;
        }
    }

    // ---------- панели источника (админ) ----------
    //
    // Кнопки синхронизации и импорта делают исходящий запрос к сайту вуза,
    // поэтому они под правом schedule.source.manage и под CSRF. Сами вызовы
    // идут через withBusyButton: запрос к источнику идёт секунды, и не
    // заблокировать кнопку — значит позволить админу настать очередь из пяти
    // импортов одним щелчком мыши.
    async function withBusyButton(button, message, task, ok) {
        if (!button) return;
        GS.hideMessage(message);

        const original = button.innerHTML;
        button.classList.add("is-loading");
        button.disabled = true;
        try {
            const data = await task();
            GS.showMessage(message, typeof ok === "function" ? ok(data) : ok, "success");
        } catch (error) {
            GS.showMessage(message, error.message || "Действие не выполнено", "error");
        } finally {
            button.classList.remove("is-loading");
            button.disabled = false;
            button.innerHTML = original;
        }
    }

    function renderSourceStatus(data) {
        if (!data || typeof data !== "object") return;

        const stats = data.stats || {};
        if (statGroups) statGroups.textContent = numberOrDash(stats.groups);
        if (statLessons) statLessons.textContent = numberOrDash(stats.lessons);
        if (statImportedGroups) statImportedGroups.textContent = numberOrDash(stats.groups_with_lessons);

        if (!sourceHint) return;
        if (!data.configured) {
            // Состояние «не настроен» рабочее: сервер поднимается и без токена,
            // и админ должен видеть причину, по которой импорт не сработает.
            // С ADR-0021 причин стало больше одной (токен, интерпретатор и
            // путь парсера, адрес, выбор движка), и настоящую знает только
            // сборка сервера — поэтому панель берёт её текст (hint, ADR-0023),
            // а своё предложение оставляет для случая, когда причину не назвали.
            const reason = data.hint
                ? `${data.hint}.`
                : `на сервере не задан ${data.missing_config || "APEKS_TOKEN"}.`;
            sourceHint.textContent = `Источник не настроен: ${reason} Чтение уже импортированных данных при этом работает.`;
        } else {
            const at = stats.last_import_at ? GS.formatDate(stats.last_import_at) : "ни разу";
            sourceHint.textContent = sentenceEnd(`Токен источника задан на сервере. Последний импорт: ${at}`)
                + " «Импортировать эту группу» качает показанный месяц одной группы, «все группы» — весь каталог фоновой задачей.";
        }
    }

    function numberOrDash(value) {
        return typeof value === "number" ? String(value) : "—";
    }

    async function loadSourceStatus() {
        try {
            renderSourceStatus(await GS.fetchJSON("/api/schedule/source/status", { on401: "throw" }));
        } catch (error) {
            if (sourceHint) sourceHint.textContent = error.message || "Не удалось прочитать состояние источника";
        }
    }

    // Месяц, который человек сейчас видит. Стартуем от понедельника: неделя
    // может задевать два месяца, и «импортировать этот» должен означать «тот,
    // что начался на экране», а не «тот, что посередине».
    function visibleMonth() {
        const start = timetable.weekStart;
        return { month: start.getMonth() + 1, year: start.getFullYear() };
    }

    function importPayload() {
        const period = visibleMonth();
        const body = {
            group: timetable.group || (groupInput ? groupInput.value.trim() : ""),
            month: period.month,
            year: period.year,
        };
        // count = 1 отправлять не надо: сервер по умолчанию берёт ровно один
        // месяц, а лишнее поле в теле — ещё один способ разойтись с сервером
        // (readJSON отвергает неизвестные поля).
        const count = importCount ? Number(importCount.value) : 1;
        if (count > 1) body.count = count;
        return body;
    }

    // Тело массовой задачи: группы в нём нет намеренно — она вся про каталог,
    // и неизвестное поле сервер отвергает вместе с запросом.
    function importAllPayload() {
        const period = visibleMonth();
        const body = { month: period.month, year: period.year };
        const count = importCount ? Number(importCount.value) : 1;
        if (count > 1) body.count = count;
        return body;
    }

    // ---------- прогресс массового импорта ----------
    //
    // Проход по каталогу идёт на сервере десятки минут, панель опрашивает его
    // раз в две секунды. Частота выбрана по смыслу: цифра «12 из 588»,
    // обновляющаяся раз в двадцать секунд, выглядит зависшей, а чаще — значит
    // долбить свой же сервер ради строчки текста.
    const JOB_POLL_MS = 2000;

    let jobTimer = null;
    let jobWatching = false;
    let jobSawRunning = false;

    function jobMonths(job) {
        const months = Array.isArray(job.months) ? job.months : [];
        return months.map((m) => monthTitle(m.month, m.year)).join(", ");
    }

    function renderImportJob(job) {
        if (!sourceJobNote) return;
        if (!job || (!job.running && !job.started_at)) {
            sourceJobNote.textContent = "Массовый импорт ещё не запускался.";
            return;
        }

        if (job.running) {
            const percent = job.steps ? Math.round((job.done / job.steps) * 100) : 0;
            sourceJobNote.textContent =
                `Массовый импорт идёт (${jobMonths(job)}): ${job.done} из ${job.steps} шагов, ${percent}%`
                + ` · пар записано ${job.lessons}`
                + `${job.failed ? ` · сбоев ${job.failed}` : ""}`
                + `${job.current ? ` · читаю «${job.current}»` : ""}.`;
            return;
        }

        sourceJobNote.textContent =
            `Последний массовый импорт (${jobMonths(job)}) завершён ${GS.formatDate(job.finished_at)}`
            + ` · групп ${job.groups}, шагов ${job.steps}, пар ${job.lessons}`
            + `${job.failed ? ` · сбоев ${job.failed}${job.failed_last_group ? `, последний — «${job.failed_last_group}»` : ""}` : ""}.`;
    }

    async function pollImportJob() {
        jobTimer = null;

        let job = null;
        try {
            const data = await GS.fetchJSON("/api/schedule/source/import-all", { on401: "throw" });
            job = data && data.job ? data.job : null;
        } catch (error) {
            // Сессия истекла или прав не стало: опрос больше нечем закрыть, и
            // крутить его каждые две секунды — значит долбить сервер ответами,
            // которые заранее отвергнуты.
            if (error.status === 401 || error.status === 403) {
                jobWatching = false;
                if (sourceJobNote) sourceJobNote.textContent = "Прогресс импорта виден только администратору после входа.";
                return;
            }

            // Сбой опроса — не конец задачи: импорт на сервере идёт и без
            // нашей вкладки. Пробуем снова, только реже, чтобы не долбить
            // сервер, который и так не отвечает.
            if (sourceJobNote) {
                sourceJobNote.textContent = `Прогресс импорта не читается: ${error.message || "сервер не отвечает"}. Пробую снова.`;
            }
            jobTimer = setTimeout(pollImportJob, JOB_POLL_MS * 5);
            return;
        }

        renderImportJob(job);

        if (job && job.running) {
            jobSawRunning = true;
            jobTimer = setTimeout(pollImportJob, JOB_POLL_MS);
            return;
        }

        // Задача закончилась (или её никогда не было): опрос гасем, иначе
        // вкладка будет слать запросы вечно. Обновляем то, что импорт изменил,
        // — статистику источника и показанное расписание.
        const finished = jobSawRunning;
        jobWatching = false;
        jobSawRunning = false;
        if (finished) {
            await Promise.all([loadSourceStatus(), loadTimetable()]);
        }
    }

    function watchImportJob() {
        if (jobWatching) return Promise.resolve();
        jobWatching = true;
        return pollImportJob();
    }

    function bindSourcePanel() {
        if (syncGroupsBtn) {
            syncGroupsBtn.addEventListener("click", () => withBusyButton(
                syncGroupsBtn,
                sourceMsg,
                () => GS.fetchJSON("/api/schedule/source/groups", { method: "POST" }),
                (data) => `Каталог обновлён: получено ${data.fetched}, добавлено ${data.added}`,
            ).then(() => reloadCatalogue()));
        }

        if (importGroupBtn) {
            importGroupBtn.addEventListener("click", () => withBusyButton(
                importGroupBtn,
                sourceMsg,
                () => GS.fetchJSON("/api/schedule/source/lessons", {
                    method: "POST",
                    body: JSON.stringify(importPayload()),
                }),
                (data) => `Импортировано пар: ${data.lessons} (${data.group_name})`,
            ).then(() => loadTimetable()));
        }

        if (importAllBtn) {
            importAllBtn.addEventListener("click", () => withBusyButton(
                importAllBtn,
                sourceMsg,
                () => GS.fetchJSON("/api/schedule/source/import-all", {
                    method: "POST",
                    body: JSON.stringify(importAllPayload()),
                }),
                (data) => `Массовый импорт запущен: ${data.job.groups} групп × ${data.job.months.length} мес.`,
            ).then(() => {
                // withBusyButton не бросает: он уже показал ошибку. Значит
                // сюда приходит и 409 «импорт уже выполняется» — и это ровно
                // тот случай, когда опрос обязан включиться: задача идёт, и
                // панель должна показывать её прогресс, а не текст конфликта.
                watchImportJob();
                return loadSourceStatus();
            }));
        }
    }

    async function reloadCatalogue() {
        catalogue = await loadGroups();
        await loadSourceStatus();
        await loadTimetable(catalogue);
    }

    // ---------- формы создания ----------
    //
    // Общий механизм — GS.bindCreateForm в common.js. Здесь он был приватной
    // копией с 8b3a027 (ADR-0013), пока третья такая же копия не нашлась в
    // dashboard.js: тогда хелпер переехал в GS без изменения вызовов, как этот
    // переезд и был запланирован комментарием на месте. Разбор даты события
    // (toEventDate) остаётся страничным — он нужен только здесь.

    // Дата события: поле свободное ввода. Пустая строка остаётся пустой — это
    // «без даты»; нечитаемое значение уходит серверу как есть, потому что
    // прежняя подмена пустой строкой создавала событие без даты и без жалобы,
    // а в ленте такое событие не видно (ADR-0032).
    function toEventDate(raw) {
        const value = String(raw || "").trim();
        if (!value) return "";

        const date = new Date(value);
        return isNaN(date.getTime()) ? value : date.toISOString();
    }

    GS.bindCreateForm({
        form: resourceForm,
        message: resourceMsg,
        endpoint: "/api/resources",
        buildPayload: (fd) => ({
            title: String(fd.get("title") || "").trim(),
            category: String(fd.get("category") || "").trim(),
            url: String(fd.get("url") || "").trim(),
            description: String(fd.get("description") || "").trim(),
        }),
        successText: "Ресурс добавлен",
        errorText: "Ошибка при добавлении ресурса",
        reload: loadResources,
    });

    GS.bindCreateForm({
        form: eventForm,
        message: eventMsg,
        endpoint: "/api/events",
        buildPayload: (fd) => ({
            title: String(fd.get("title") || "").trim(),
            description: String(fd.get("description") || "").trim(),
            event_date: toEventDate(fd.get("event_date_raw")),
        }),
        successText: "Событие добавлено",
        errorText: "Ошибка при добавлении события",
        reload: loadFeed,
    });

    // ---------- инициализация ----------
    //
    // Профиль читаем первым, но БЕЗ раннего return. Раньше ответ 401 обрывал
    // инициализацию всей страницы, и гость, кроме баннера «войдите», не видел
    // ничего — хотя оба маршрута чтения расписания публичные, и официальное
    // расписание групп на странице теперь доступно всем.

    // Стартовая группа: первая из каталога. Страница с заголовком «выберите
    // группу» на полностью импортированном каталоге выглядит пустой, хотя
    // данных достаточно для показа. Это только выбор для отрисовки: он ни на
    // что не подписывает и меняется вводом человека.
    function startGroupName(remembered, groups) {
        if (remembered && remembered.group) {
            const byId = groups.find((g) => g.external_id === remembered.group);
            if (byId) return String(byId.name);
        }
        if (remembered && remembered.name) return String(remembered.name);
        return groups.length ? String(groups[0].name) : "";
    }

    setWeekLabel();
    bindWeekControls();
    bindSourcePanel();
    startLessonTicker();

    let profileError = null;
    try {
        user = await GS.fetchJSON("/api/auth/me", { on401: "throw" });
    } catch (error) {
        // 401 здесь — штатный гость, а не сбой: редиректа не делаем (on401) и
        // сообщение об ошибке не показываем.
        if (error.status !== 401) profileError = error;
    }

    if (user) showAuthedMode();
    else showGuestMode();

    catalogue = await loadGroups();
    if (groupInput && !groupInput.value.trim()) {
        groupInput.value = startGroupName(readChoice(), catalogue);
    }
    await loadTimetable(catalogue);

    if (!user) {
        if (profileError) showProfileFailure(profileError);
        return;
    }

    await Promise.all([loadFeed(), loadResources()]);
    if (canManageSource(user.role)) {
        await loadSourceStatus();
        // Один опрос при открытии: он же и показывает последнюю завершённую
        // задачу, и запускает непрерывный, если импорт идёт прямо сейчас —
        // например запущенный с другой вкладки.
        await watchImportJob();
    }

    if (refreshButton) refreshButton.addEventListener("click", loadFeed);
    if (refreshResources) refreshResources.addEventListener("click", loadResources);
    if (applyFilter) applyFilter.addEventListener("click", loadFeed);
});
