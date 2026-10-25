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
    const monthLabel = document.getElementById("monthLabel");
    const prevMonth = document.getElementById("prevMonth");
    const nextMonth = document.getElementById("nextMonth");
    const currentMonthBtn = document.getElementById("currentMonth");
    const refreshTimetable = document.getElementById("refreshTimetable");
    const timetableContainer = document.getElementById("timetable");
    const updatedNote = document.getElementById("updatedNote");
    const sourceName = document.getElementById("sourceName");
    const sourceNameNote = document.getElementById("sourceNameNote");
    const sourceLink = document.getElementById("sourceLink");
    const sourceSection = document.getElementById("sourceManageSection");
    const sourceMsg = document.getElementById("sourceMsg");
    const sourceHint = document.getElementById("sourceHint");
    const statGroups = document.getElementById("statGroups");
    const statLessons = document.getElementById("statLessons");
    const statImportedGroups = document.getElementById("statImportedGroups");
    const syncGroupsBtn = document.getElementById("syncGroupsBtn");
    const importMonthBtn = document.getElementById("importMonthBtn");
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
    const weekLater = new Date(today.getTime() + 7 * 24 * 3600 * 1000);
    if (fromDate) fromDate.value = toISODate(today);
    if (toDate) toDate.value = toISODate(weekLater);

    // Состояние таймтейбла.
    //
    // group — ровно то, что уйдёт в параметр group: id выбранной группы, если
    // ввод совпал с каталогом, и иначе сам ввод (сервер умеет разрешать имя,
    // и живая строка «Б-ИСиТ-22» из поиска — штатный случай).
    const timetable = {
        group: "",
        groupName: "",
        month: today.getMonth() + 1,
        year: today.getFullYear(),
        importedMonths: [],
    };

    // Каталог групп держим в переменной: он нужен и чтобы превратить имя из
    // поля ввода в однозначный id, и после синхронизации админом, когда список
    // на странице меняется прямо во время просмотра.
    let catalogue = [];

    // Выбранные группа и месяц переживают перезагрузку страницы: студент
    // открывает расписание утром и вечером, и каждый раз вводить группу заново
    // — значит сделать страницу хуже, чем бумажное расписание на стенде.
    const CHOICE_KEY = "sos.schedule.choice";

    function saveChoice() {
        try {
            localStorage.setItem(CHOICE_KEY, JSON.stringify({
                group: timetable.group,
                name: timetable.groupName,
                month: timetable.month,
                year: timetable.year,
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

    function monthTitle(month, year) {
        const date = new Date(year, month - 1, 1);
        return date.toLocaleDateString("ru-RU", { month: "long", year: "numeric" });
    }

    // toLocaleDateString даёт «октябрь 2026» со строчной, а в начале фразы нужна
    // заглавная. Отдельная функция, а не replace в двух местах: второе
    // предложение было бы первым кандидатом на «забыли поправить».
    function capitalize(text) {
        const value = String(text || "");
        return value.charAt(0).toUpperCase() + value.slice(1);
    }

    function setMonthLabel() {
        if (monthLabel) monthLabel.textContent = monthTitle(timetable.month, timetable.year);
    }

    function isImported(month, year) {
        return (timetable.importedMonths || []).some((m) => m.month === month && m.year === year);
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

    function renderLesson(lesson) {
        const parts = [];
        if (lesson.time) parts.push(`<span class="lesson-time">${GS.escapeHtml(lesson.time)}</span>`);
        if (lesson.lesson_type) parts.push(`<span class="lesson-type">${GS.escapeHtml(lesson.lesson_type)}</span>`);
        if (lesson.room) parts.push(`<span>${GS.escapeHtml(lesson.room)}</span>`);
        if (lesson.teacher) parts.push(`<span>${GS.escapeHtml(lesson.teacher)}</span>`);
        const subgroup = String(lesson.subgroup || "").trim();
        const sub = NO_SUBGROUP.has(subgroup) ? "" : subgroup;

        return `
            <div class="lesson">
                <span class="lesson-num">${GS.escapeHtml(lesson.lesson_number || "?")}</span>
                <div class="lesson-body">
                    <span class="lesson-title">${GS.escapeHtml(lesson.discipline || "Дисциплина не указана")}</span>
                    <span class="lesson-meta">${parts.join('<span class="dot-sep"></span>')}</span>
                    ${sub ? `<span class="lesson-sub">подгруппа ${GS.escapeHtml(sub)}</span>` : ""}
                </div>
            </div>`;
    }

    function renderTimetable(days) {
        if (!timetableContainer) return;

        timetableContainer.innerHTML = days.map((day) => {
            const lessons = day.lessons || [];
            const head = `
                <header class="sched-day-head">
                    <span class="sched-day-weekday">${GS.escapeHtml(day.weekday || "")}</span>
                    <span class="sched-day-date">${GS.escapeHtml(day.date || formatDayKey(day.day_key))}</span>
                    <span class="sched-day-count">${lessons.length} ${plural(lessons.length, "пара", "пары", "пар")}</span>
                </header>`;

            return `
                <article class="sched-day">
                    ${head}
                    <div class="lesson-list">${lessons.map(renderLesson).join("")}</div>
                </article>`;
        }).join("");
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

        setMonthLabel();
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

        const query = `/api/schedule/lessons?group=${encodeURIComponent(timetable.group)}`
            + `&month=${timetable.month}&year=${timetable.year}`;

        try {
            const data = await GS.fetchJSON(query, { on401: "throw" });
            timetable.importedMonths = Array.isArray(data.imported_months) ? data.imported_months : [];
            if (data.group_name) timetable.groupName = data.group_name;

            const days = Array.isArray(data.days) ? data.days : [];
            const imported = isImported(timetable.month, timetable.year);

            if (!days.length) {
                // Два разных «пусто»: месяц ещё не импортирован (данных нет в
                // базе) и импортирован, но пар в нём нет. Сваливать их в одну
                // фразу нельзя: в первом случае нужен импорт, во втором — искать
                // дальше по календарю.
                renderTimetableEmpty(
                    imported
                        ? `В ${monthTitle(timetable.month, timetable.year)} пар нет`
                        : `${capitalize(monthTitle(timetable.month, timetable.year))} ещё не импортирован`,
                    imported
                        ? "Посмотрите другой месяц или обновите импорт."
                        : (canManageSource(user && user.role)
                            ? "Импортируйте его кнопкой в панели «Источник расписания» ниже."
                            : "Попросите администратора импортировать этот месяц."),
                );
            } else {
                renderTimetable(days);
            }

            setNote(updatedText(data.updated_at));
            saveChoice();
        } catch (error) {
            timetable.importedMonths = [];
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

    function shiftMonth(delta) {
        const date = new Date(timetable.year, timetable.month - 1 + delta, 1);
        timetable.month = date.getMonth() + 1;
        timetable.year = date.getFullYear();
        return loadTimetable();
    }

    // Кнопки: стрелки листают месяц, «Текущий месяц» возвращает к сегодняшнему,
    // Enter и «смена» поля группы ищут расписание выбранной группы.
    function bindMonthControls() {
        if (prevMonth) prevMonth.addEventListener("click", () => shiftMonth(-1));
        if (nextMonth) nextMonth.addEventListener("click", () => shiftMonth(1));
        if (currentMonthBtn) currentMonthBtn.addEventListener("click", () => {
            const now = new Date();
            timetable.month = now.getMonth() + 1;
            timetable.year = now.getFullYear();
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
            sourceHint.textContent = `Токен источника задан на сервере. Последний импорт: ${at}. Кнопка ниже импортирует показанный месяц выбранной группы.`;
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

    function importPayload() {
        const body = {
            group: timetable.group || (groupInput ? groupInput.value.trim() : ""),
            month: timetable.month,
            year: timetable.year,
        };
        // count = 1 отправлять не надо: сервер по умолчанию берёт ровно один
        // месяц, а лишнее поле в теле — ещё один способ разойтись с сервером
        // (readJSON отвергает неизвестные поля).
        const count = importCount ? Number(importCount.value) : 1;
        if (count > 1) body.count = count;
        return body;
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

        if (importMonthBtn) {
            importMonthBtn.addEventListener("click", () => withBusyButton(
                importMonthBtn,
                sourceMsg,
                () => GS.fetchJSON("/api/schedule/source/lessons", {
                    method: "POST",
                    body: JSON.stringify(importPayload()),
                }),
                (data) => `Импортировано пар: ${data.lessons} (${data.group_name})`,
            ).then(() => loadTimetable()));
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

    // Дата события: поле свободное ввода, поэтому пустая строка и нечитаемая
    // дата дают одинаковый результат — пустое значение, сервер трактует его как
    // «без даты».
    function toEventDate(raw) {
        const value = String(raw || "").trim();
        if (!value) return "";

        const date = new Date(value);
        return isNaN(date.getTime()) ? "" : date.toISOString();
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

    function applyRememberedMonth(remembered) {
        if (!remembered) return;
        const month = Number(remembered.month);
        const year = Number(remembered.year);
        if (month >= 1 && month <= 12 && year >= 2000 && year <= 2100) {
            timetable.month = month;
            timetable.year = year;
        }
    }

    setMonthLabel();
    bindMonthControls();
    bindSourcePanel();

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

    applyRememberedMonth(readChoice());
    setMonthLabel();

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
    if (canManageSource(user.role)) await loadSourceStatus();

    if (refreshButton) refreshButton.addEventListener("click", loadFeed);
    if (refreshResources) refreshResources.addEventListener("click", loadResources);
    if (applyFilter) applyFilter.addEventListener("click", loadFeed);
});
