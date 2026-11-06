// Приёмка T4.1: страница расписания на данных источника.
//
// Зачем отдельный тест: до T4.1 первый блок страницы был карточкой со ссылкой
// на сайт вуза — встроить его нельзя (X-Frame-Options), и страница честно
// говорила «открывайте снаружи». Теперь внутри платформы лежат разобранные
// пары из АПЭКС-ВУЗа, и проверять надо не «ссылка есть», а поведение витрины:
// что гость видит расписание (маршруты чтения публичные), что неделя листается,
// что пустые состояния различимы («пар нет» ≠ «не привезлось» ≠ «доступа к вузу
// нет»), что панель импорта показана только админу и что запрос источника с
// браузера не делается вообще — токена здесь нет и быть не может.
//
// После ADR-0042 к этому добавились два новых права страницы: навигация по
// каталогу «институт → курс → группа» (ряды вкладок, как у виджета вуза) и
// строка состояния источника под таблицей. Второе важнее первого: показывать
// вчерашний архив надписью «обновлено сегодня» — значит врать человеку, а
// именно это различие и несёт source_state.
//
// Тест полностью офлайновый: fetch подменяется роутером-заглушкой с фикстурами,
// похожими на живой ответ /api/schedule/lessons. Сервер на :8080 не нужен.
//
// Запуск: cd tests/ui && node schedule-timetable.cjs
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const REPO = path.resolve(__dirname, "..", "..");
const BASE = "http://localhost:8080";
const SOURCE_PAGE = "https://tksu.ru/bitrix/js/api-apeks-vuz/rasp-stud/rasp-stud.html";

const results = [];
function check(name, ok, detail = "") {
    results.push({ name, ok, detail });
    console.log(`${ok ? "PASS" : "FAIL"}  ${name}${detail ? " — " + detail : ""}`);
}
function wait(ms) { return new Promise((r) => setTimeout(r, ms)); }

// необработанное отклонение = баг страницы, а не теста: собираем и проверяем
// отдельным пунктом (см. комментарий в schedule-forms.cjs).
const rejections = [];
process.on("unhandledRejection", (reason) => rejections.push(String((reason && reason.message) || reason)));

const BUNDLE = ["config.js", "core/api-client.js", "core/format.js", "core/layout.js", "common.js"];

function json(body, status = 200) {
    return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

function splitUrl(url) {
    const clean = String(url).replace(BASE, "");
    const i = clean.indexOf("?");
    return { pathname: i === -1 ? clean : clean.slice(0, i), params: new URLSearchParams(i === -1 ? "" : clean.slice(i + 1)) };
}

// ---------- фикстуры: тот же набор полей, что отдаёт сервер ----------

// Курс — строка источника («1 курс»), а не число: страница обязана подписывать
// вкладку дословно и в том же порядке, что дал вуз.
const GROUPS = {
    source: "apeks",
    source_name: "АПЭКС-ВУЗ",
    synced_at: 1761200000,
    count: 3,
    groups: [
        { external_id: "494", name: "Б-ИСиТ-22", department: "Институт информационных технологий", course: "2 курс", updated_at: 1761200000 },
        { external_id: "495", name: "ИСП-21", department: "Институт природных ресурсов", course: "3 курс", updated_at: 1761200000 },
        { external_id: "496", name: "Б-ИСиТ-21", department: "Институт информационных технологий", course: "1 курс", updated_at: 1761200000 },
    ],
    source_page_url: SOURCE_PAGE,
};

function lesson(number, discipline, extra) {
    return Object.assign({
        external_id: "l" + number,
        group_ext_id: "494",
        lesson_number: number,
        time: "08:30 – 10:00",
        start_time: "08:30",
        end_time: "10:00",
        group_name: "Б-ИСиТ-22",
        subgroup: "",
        discipline,
        lesson_type: "Лк",
        room: "201",
        teacher: "Иванов И.И.",
        date: "06.10.2026",
        weekday: "Вт",
        day_key: 20261006,
        imported_at: 1761300000,
    }, extra || {});
}

function lessons(payload) {
    return Object.assign({
        source: "apeks",
        source_name: "АПЭКС-ВУЗ",
        group_id: "494",
        group_name: "Б-ИСиТ-22",
        department: "Институт информационных технологий",
        course: "2 курс",
        range: { from: 20261001, to: 20261031 },
        updated_at: 1761300000,
        days: [
            {
                day_key: 20261006, date: "06.10.2026", weekday: "Вт",
                lessons: [
                    lesson(1, "Базы данных"),
                    lesson(2, "Математика", { lesson_type: "Пр", time: "10:15 – 11:45", subgroup: "Б-ИСиТ-22/1", room: "", teacher: "" }),
                    lesson(3, "Физкультура", { subgroup: "—", room: "без ауд.", time: "12:20 – 13:55" }),
                ],
            },
            {
                day_key: 20261007, date: "07.10.2026", weekday: "Ср",
                // разметка от источника вырезается на импорте; если она всё же
                // приедет, страница обязана экранировать её, а не вставлять в DOM.
                lessons: [lesson(1, "<img src=x onerror=alert(1)>Физика", { day_key: 20261007, date: "07.10.2026", weekday: "Ср" })],
            },
        ],
        // range_imported — ответ сервера о том, лежит ли показанный период в
        // базе. По умолчанию «лежит»: пустота тогда означает «пар нет».
        range_imported: true,
        // imported_months — покрытие из schedule_source_months: у месяца есть
        // ok (довёл ли последний заход) и lessons (сколько пар легло). Список
        // пустых месяцев страница в подсказку не берёт именно поэтому.
        imported_months: [{ month: 10, year: 2026, lessons: 4, ok: true, refreshed_at: 1761300000, attempted_at: 1761300000 }],
        // source_state (ADR-0042) — чем именно отвечает сервер: копия свежая.
        source_state: "fresh",
        source_configured: true,
        source_page_url: SOURCE_PAGE,
    }, payload || {});
}

const STATUS = {
    source: "apeks",
    source_name: "АПЭКС-ВУЗ",
    configured: true,
    missing_config: "APEKS_TOKEN",
    stats: { groups: 2, lessons: 3, groups_synced_at: 1761200000, last_import_at: 1761300000, groups_with_lessons: 1 },
};

// boot сеет localStorage до исполнения скриптов страницы: выбор группы
// переживает перезагрузку, и без посева страница стартовала бы с первой группы
// каталога — проверки навигации зависели бы от порядка в фикстуре.
// Неделя, в отличие от группы, не запоминается: она всегда сегодняшняя.
function boot({ role = "teacher", routes = {}, seed = null, now = null } = {}) {
    const html = fs.readFileSync(path.join(REPO, "web", "schedule.html"), "utf8");
    const dom = new JSDOM(html, { url: BASE + "/schedule", runScripts: "outside-only", pretendToBeVisual: true });
    const { window } = dom;

    if (seed) window.localStorage.setItem("sos.schedule.choice", JSON.stringify(seed));
    // Часы страницы фиксируются до первого рендера: состояния «идёт/не начата»
    // иначе зависели бы от времени прогона.
    if (now) window.Date.now = () => now;

    const calls = [];
    const hits = {};

    window.fetch = (input, init = {}) => {
        const method = String(init.method || "GET").toUpperCase();
        const { pathname, params } = splitUrl(input);
        const key = method + " " + pathname;
        hits[key] = (hits[key] || 0) + 1;
        const search = params.toString();

        const record = { key, method, pathname, search, params, body: init.body, headers: init.headers || {} };
        calls.push(record);

        const rule = routes[key];
        const decided = typeof rule === "function" ? rule({ record, hits }) : rule;
        return Promise.resolve(decided === undefined ? defaultBody(key, role, hits[key]) : decided);
    };

    window.document.cookie = "csrf_token=test-csrf-token";

    const source = BUNDLE.concat(["schedule.js"]).map((f) => fs.readFileSync(path.join(REPO, "web/js", f), "utf8")).join("\n;\n");
    window.eval(source);

    const navigations = [];
    window.SOS_CONFIG.navigateTo = (target) => navigations.push(target);

    return { window, doc: window.document, calls, hits, navigations };
}

function defaultBody(key, role, hit) {
    if (key === "GET /api/auth/me") {
        return role === "guest" ? json({ error: { code: "unauthorized", message: "session expired" } }, 401)
            : json({ id: 7, name: "Проверка", email: "p@example.test", role });
    }
    if (key === "GET /api/schedule/groups") return json(hit === 1 ? GROUPS : GROUPS);
    if (key === "GET /api/schedule/lessons") return json(hit === 1 ? lessons() : lessons());
    if (key === "GET /api/schedule/source/status") return json(STATUS);
    if (key === "POST /api/schedule/source/groups") return json({ source: "apeks", fetched: 2, added: 2 });
    if (key === "POST /api/schedule/source/lessons") return json({ source: "apeks", group_id: "494", group_name: "Б-ИСиТ-22", months: [{ month: 10, year: 2026, lessons: 3, ok: true, refreshed_at: 1761300000, attempted_at: 1761300000 }], lessons: 3 });
    // Массовая задача: по умолчанию её нет — иначе каждый boot с админом
    // запускал бы двухсекундный опрос и тянул сьют.
    if (key === "GET /api/schedule/source/import-all") return json({ source: "apeks", job: { running: false, months: [] } });
    if (key === "POST /api/schedule/source/import-all") return json({ source: "apeks", job: { running: true, groups: 2, steps: 2, done: 0, lessons: 0, failed: 0, months: [{ month: 10, year: 2026 }] } }, 202);
    if (key === "GET /api/schedule") return json([]);
    if (key === "GET /api/resources") return json([]);
    return json({});
}

function text(doc, id) {
    const el = doc.getElementById(id);
    return el ? el.textContent.replace(/\s+/g, " ").trim() : "";
}
function hidden(doc, id) {
    const el = doc.getElementById(id);
    return !el || el.classList.contains("hidden");
}
function get(doc, selector) {
    return doc.querySelector(selector);
}
function find(calls, key) {
    return calls.filter((c) => c.key === key);
}

// ждём, пока страница дойдёт до состояния (запросы идут цепочкой через await)
async function until(page, predicate, tries = 60) {
    for (let i = 0; i < tries; i++) {
        if (predicate(page)) return true;
        await wait(20);
    }
    return predicate(page);
}

(async () => {
    // ---------- 1. гость: расписание публичное ----------
    //
    // Главная проверка задачи: маршруты чтения /api/schedule/groups и
    // /api/schedule/lessons объявлены Public, и страница обязана показать
    // таймтейбл до входа. До T4.1 любой 401 обрывал инициализацию — гость
    // видел только баннер.
    const guest = boot({ role: "guest", seed: { group: "494", name: "Б-ИСиТ-22", month: 10, year: 2026 } });
    await until(guest, () => find(guest.calls, "GET /api/schedule/lessons").length > 0);
    await wait(40);

    check("гость не уходит на вход", guest.navigations.length === 0, JSON.stringify(guest.navigations));
    check("гостю загружен каталог групп", find(guest.calls, "GET /api/schedule/groups").length === 1);
    check("гостю загружено расписание", find(guest.calls, "GET /api/schedule/lessons").length === 1,
        guest.calls.map((c) => c.key).join(", "));
    check("расписание гостя отрисовано", guest.doc.querySelectorAll("#timetable .sched-day").length === 2,
        String(guest.doc.querySelectorAll("#timetable .sched-day").length));
    check("пары внутри дня отрисованы", guest.doc.querySelectorAll("#timetable .lesson").length === 4,
        String(guest.doc.querySelectorAll("#timetable .lesson").length));
    check("дисциплина видна", /Базы данных/.test(text(guest.doc, "timetable")), text(guest.doc, "timetable").slice(0, 60));
    check("подгруппа показана отдельной строкой", /подгруппа Б-ИСиТ-22\/1/.test(text(guest.doc, "timetable")));
    // «ауд. к. 3/314» — тавтология из живых данных: источник сам подписывает
    // аудиторию, поэтому страница печатает её как есть.
    check("аудитория и преподаватель в строке пары", /201/.test(text(guest.doc, "timetable")) && /Иванов И\.И\./.test(text(guest.doc, "timetable")));
    // «без ауд.» — собственный текст источника, он печатается как есть;
    // проверяем, что к номеру аудитории страница ничего не приклеивает.
    check("префикс «ауд.» номеру группы не добавляется", !/ауд\.\s*201/.test(text(guest.doc, "timetable")),
        text(guest.doc, "timetable").slice(0, 80));
    check("текст аудитории из источника виден целиком", /без ауд\./.test(text(guest.doc, "timetable")));
    check("счёт пар склоняется по-русски", /3 пары/.test(text(guest.doc, "timetable")) && /1 пара/.test(text(guest.doc, "timetable")),
        text(guest.doc, "timetable").slice(0, 120));
    // «подгруппа —» и пустая подгруппа означают «её нет»; печатать это нельзя,
    // потому что тире читается как оборванная строка.
    check("пустая и «тире» подгруппа не печатаются", !/подгруппа\s*(—|-|$)/.test(text(guest.doc, "timetable")),
        text(guest.doc, "timetable").slice(0, 120));
    check("строка состояния называет группу и срок копии",
        /Б-ИСиТ-22 · Показана свежая копия: обновлена/.test(text(guest.doc, "updatedNote")),
        text(guest.doc, "updatedNote"));
    check("свежие данные помечены пилюлей «свежее»",
        get(guest.doc, "#updatedNote .sched-source-state.is-fresh") !== null, text(guest.doc, "updatedNote"));
    check("подсказка datalist заполнена каталогом", guest.doc.querySelectorAll("#groupOptions option").length === 3);
    check("счётчик каталога по-русски", /В каталоге 3 группы/.test(text(guest.doc, "groupHelper")), text(guest.doc, "groupHelper"));
    check("ссылка на первоисточник живёт в данных, а не в разметке", get(guest.doc, "#sourceLink").getAttribute("href") === SOURCE_PAGE);
    check("гостю показан баннер входа", !hidden(guest.doc, "guestNotice"));
    check("у гостя нет панели источника", hidden(guest.doc, "sourceManageSection")
        && find(guest.calls, "GET /api/schedule/source/status").length === 0);
    check("гость не обращается к приватной ленте", find(guest.calls, "GET /api/schedule").length === 0);
    check("ни одного запроса к источнику с браузера",
        guest.calls.every((c) => !/tksu\.ru|apeks/i.test(c.pathname)),
        guest.calls.map((c) => c.pathname).join(", "));

    // ---------- 2. экранирование ----------
    //
    // Импортер вырезает HTML из аудитории, но страница не должна полагаться на
    // это: неизвестное поле с разметкой обязано остаться текстом.
    check("разметка в дисциплине не становится элементом",
        guest.doc.querySelectorAll("#timetable img, #timetable script").length === 0
        && /Физика/.test(text(guest.doc, "timetable")));

    // ---------- 3. сегодня, счёт по номерам, состояния с таймером ----------
    //
    // Часы страницы — полдень сегодняшнего дня: так «прошедшая», «идущая» и
    // «будущая» пары детерминированы, а карточка «сегодня» существует в любом
    // месяце. Вчерашний день нужен для состояния «Закончена».
    const noon = new Date();
    noon.setHours(12, 0, 0, 0);
    const dayKeyOf = (date) => date.getFullYear() * 10000 + (date.getMonth() + 1) * 100 + date.getDate();
    const dateOf = (date) => `${String(date.getDate()).padStart(2, "0")}.${String(date.getMonth() + 1).padStart(2, "0")}.${date.getFullYear()}`;
    const yesterday = new Date(noon.getTime() - 24 * 3600 * 1000);

    const today = dayKeyOf(noon);
    const statusDays = [
        {
            day_key: dayKeyOf(yesterday), date: dateOf(yesterday), weekday: "Вчера",
            lessons: [
                lesson(2, "Прошедшая пара", {
                    external_id: "done-2", day_key: dayKeyOf(yesterday), date: dateOf(yesterday), weekday: "Вчера",
                    time: "09:00 – 10:00", start_time: "09:00", end_time: "10:00",
                }),
            ],
        },
        {
            day_key: today, date: dateOf(noon), weekday: "Сегодня",
            // Две подгруппы одной четвёртой пары — это одна пара, поэтому вместе
            // с пятой их две, а не три (страница считает номера, не строки).
            lessons: [
                lesson(4, "Четвёртая, первая подгруппа", {
                    external_id: "active-4a", day_key: today, date: dateOf(noon),
                    time: "11:30 – 13:30", start_time: "11:30", end_time: "13:30", subgroup: "/1",
                }),
                lesson(4, "Четвёртая, вторая подгруппа", {
                    external_id: "active-4b", day_key: today, date: dateOf(noon),
                    time: "11:30 – 13:30", start_time: "11:30", end_time: "13:30", subgroup: "/2",
                }),
                lesson(5, "Пятая, ещё не началась", {
                    external_id: "pending-5", day_key: today, date: dateOf(noon),
                    time: "14:00 – 15:30", start_time: "14:00", end_time: "15:30",
                }),
            ],
        },
    ];
    const statusFixture = lessons({
        days: statusDays,
        imported_months: [{
            month: new Date().getMonth() + 1, year: new Date().getFullYear(),
            lessons: 4, ok: true, refreshed_at: 1761300000, attempted_at: 1761300000,
        }],
    });

    const statusPage = boot({
        role: "guest",
        seed: { group: "494", name: "Б-ИСиТ-22" },
        now: noon.getTime(),
        routes: { "GET /api/schedule/lessons": json(statusFixture) },
    });
    await until(statusPage, () => /Сегодня/.test(text(statusPage.doc, "timetable")));

    const todayCard = statusPage.doc.querySelector(".sched-day.is-today");
    check("сегодняшний день помечен карточкой и чипом",
        statusPage.doc.querySelectorAll(".sched-day.is-today").length === 1
        && todayCard.querySelector(".sched-today-chip") !== null,
        todayCard ? todayCard.className : "карточки нет");
    check("счёт пар по номерам: две четвёртые и пятая — две пары",
        /2 пары/.test(todayCard.textContent.replace(/\s+/g, " ")),
        todayCard.textContent.replace(/\s+/g, " ").slice(0, 120));
    check("идущая пара помечена «Идёт» и показывает остаток",
        todayCard.querySelectorAll(".lesson-status.is-active").length === 2
        && /Идёт/.test(todayCard.textContent) && /осталось 1 ч 30 мин/.test(todayCard.textContent),
        todayCard.textContent.replace(/\s+/g, " ").slice(0, 200));
    check("будущая пара помечена «Не начата» и считает до начала",
        todayCard.querySelectorAll(".lesson-status.is-pending").length === 1
        && /Не начата/.test(todayCard.textContent) && /через 2 ч/.test(todayCard.textContent),
        todayCard.textContent.replace(/\s+/g, " ").slice(0, 200));

    const doneBadge = statusPage.doc.querySelector(".lesson-status.is-done");
    check("прошедшая пара помечена «Закончена»",
        doneBadge !== null && /Закончена/.test(doneBadge.textContent),
        doneBadge ? doneBadge.textContent : "плашки нет");
    check("у законченной пары нет таймера",
        doneBadge !== null && doneBadge.closest(".lesson-state").querySelector(".lesson-status-timer").textContent === "");

    // ---------- 3b. имя корпуса рядом с аудиторией ----------
    //
    // Подпись приносит сервер (ADR-0045): страница не разбирает строку
    // аудитории сама, иначе «карта показывает корпус, а расписание нет» стало
    // бы расходжением двух копий одного правила. Проверяются здесь следствия
    // этого поля — где подпись появляется, где сознательно молчит, и куда она
    // ведёт. Границы недели берутся из запроса самой страницы, чтобы прогон не
    // зависел от даты запуска.
    const namedDays = [{
        day_key: 20261006, date: "06.10.2026", weekday: "Вт",
        lessons: [
            lesson(1, "Базы данных", { room: "к. 1/710" }),
            lesson(2, "Матанализ", { room: "к. 3/21" }),
            lesson(3, "Физра", { room: "201" }),
            lesson(4, "Корпус без имени", { room: "к. 8/5" }),
            lesson(5, "Две аудитории", { room: "к. 1/710, к. 1/711" }),
        ],
    }];
    const namedRooms = {
        "к. 1/710": { building: "1", name: "Главный корпус" },
        "к. 3/21": { building: "3", name: "Биофак" },
        // Корпус заведён, но без имени: ссылку дать можно, подписывать словами
        // — нет, пустое место в тексте строки читается как обрезанная фраза.
        "к. 8/5": { building: "8" },
        "к. 1/710, к. 1/711": { building: "1", name: "Главный корпус" },
    };
    const lessonCell = (page, room) => Array.from(page.doc.querySelectorAll("#timetable .lesson"))
        .find((row) => row.textContent.includes(room));

    const named = boot({
        routes: { "GET /api/schedule/lessons": () => json(lessons({ days: namedDays, room_buildings: namedRooms })) },
    });
    await until(named, () => named.doc.querySelectorAll("#timetable .lesson-building").length > 0);
    const shownPeriod = find(named.calls, "GET /api/schedule/lessons")[0];

    check("имя корпуса стоит рядом с аудиторией, а не вместо неё",
        /к\. 1\/710/.test(lessonCell(named, "к. 1/710").textContent)
        && /Главный корпус/.test(lessonCell(named, "к. 1/710").textContent),
        lessonCell(named, "к. 1/710").textContent.replace(/\s+/g, " "));
    check("подписей ровно столько, сколько аудиторий с названным корпусом",
        named.doc.querySelectorAll("#timetable .lesson-building").length === 3,
        String(named.doc.querySelectorAll("#timetable .lesson-building").length));
    check("аудитория, не назвавшая корпус, подписи не получила",
        lessonCell(named, "201").querySelector(".lesson-building") === null,
        lessonCell(named, "201").textContent.replace(/\s+/g, " "));
    check("корпус без имени не подписан пустой строкой",
        lessonCell(named, "к. 8/5").querySelector(".lesson-building") === null,
        lessonCell(named, "к. 8/5").textContent.replace(/\s+/g, " "));
    check("две аудитории одного корпуса подписаны один раз",
        lessonCell(named, "к. 1/710, к. 1/711").querySelectorAll(".lesson-building").length === 1);
    // Номер корпуса в подпись не попадает: человек просил название, а «Главный
    // корпус (1)» рядом с «к. 1/710» значило бы «1» трижды в одной строке.
    check("подпись называет корпус словом, а не номером",
        named.doc.querySelector(".lesson-building").textContent === "Главный корпус",
        named.doc.querySelector(".lesson-building").textContent);

    const link = named.doc.querySelector("a.lesson-building");
    check("вошедшему подпись — ссылка на карту этого корпуса", !!link
        && /^\/map\?building=1&from=\d+&to=\d+$/.test(link.getAttribute("href")),
        link ? link.getAttribute("href") : "ссылки нет");
    check("ссылка ведёт на ту же неделю, что показана",
        !!link && link.getAttribute("href").includes(`from=${shownPeriod.params.get("from")}`)
        && link.getAttribute("href").includes(`to=${shownPeriod.params.get("to")}`),
        `${link ? link.getAttribute("href") : "ссылки нет"} против ${shownPeriod.search}`);
    check("ссылка называет, куда она ведёт",
        !!link && link.getAttribute("title") === "Открыть карту и показать корпус «Главный корпус»",
        link ? link.getAttribute("title") : "ссылки нет");
    check("подпись стоит вплотную к своей аудитории, а не отдельной строкой",
        !!link && link.previousElementSibling.textContent === "к. 1/710"
        && link.parentElement === lessonCell(named, "к. 1/710").querySelector(".lesson-meta"),
        link ? `${link.parentElement.className} · ${link.previousElementSibling.textContent}` : "ссылки нет");

    // Гость видит то же имя, но без ссылки: /map требует входа, и ссылка
    // привела бы его к форме входа вместо карты — обещание, которое страница
    // не может выполнить.
    const namedGuest = boot({
        role: "guest",
        seed: { group: "494", name: "Б-ИСиТ-22" },
        routes: { "GET /api/schedule/lessons": () => json(lessons({ days: namedDays, room_buildings: namedRooms })) },
    });
    await until(namedGuest, () => namedGuest.doc.querySelectorAll("#timetable .lesson-building").length > 0);
    check("гость видит имя корпуса", /Главный корпус/.test(text(namedGuest.doc, "timetable")));
    check("но не ссылку на страницу, куда его нет",
        namedGuest.doc.querySelectorAll("a.lesson-building").length === 0,
        String(namedGuest.doc.querySelectorAll("a.lesson-building").length));

    // Имя корпуса — строка человека из справочника мест, и она не имеет права
    // стать разметкой: тот же хранящийся XSS, что чинился в T0.1.
    const injected = boot({
        routes: {
            "GET /api/schedule/lessons": () => json(lessons({
                days: [{ day_key: 20261006, date: "06.10.2026", weekday: "Вт",
                    lessons: [lesson(1, "Базы данных", { room: "к. 1/710" })] }],
                room_buildings: { "к. 1/710": { building: "1", name: "<img src=x onerror=alert(1)>" } },
            })),
        },
    });
    await until(injected, () => injected.doc.querySelectorAll("#timetable .lesson-building").length > 0);
    check("разметка в имени корпуса осталась текстом",
        injected.doc.querySelectorAll("#timetable img").length === 0
        && /onerror/.test(injected.doc.querySelector(".lesson-building").textContent),
        injected.doc.querySelector(".lesson-building").innerHTML);
    check("и не попала в адрес ссылки",
        /^\/map\?building=1&from=\d+&to=\d+$/.test(injected.doc.querySelector("a.lesson-building").getAttribute("href")),
        injected.doc.querySelector("a.lesson-building").getAttribute("href"));

    // Поля нет вовсе — сборка без справочника мест (сервер отвечает пустым
    // объектом). Расписание от этого не имеет права изменяться.
    const noNames = boot();
    await until(noNames, () => noNames.doc.querySelectorAll("#timetable .lesson").length > 0);
    check("без подписей расписание рисуется как раньше",
        noNames.doc.querySelectorAll("#timetable .lesson-building").length === 0
        && noNames.doc.querySelectorAll("#timetable .lesson").length === 4,
        `${noNames.doc.querySelectorAll("#timetable .lesson-building").length} подписей, `
        + `${noNames.doc.querySelectorAll("#timetable .lesson").length} пар`);

    // ---------- 4. пустые состояния ----------
    //
    // «Пар нет» и «не привезлось» различает сервер (range_imported): неделя
    // задевает два месяца, и по списку месяцев на странице вывод был бы неверен
    // в обе стороны. Третье состояние — «доступа к вузу на сервере нет» —
    // появилось после ADR-0042: это единственный случай, где человеку без
    // панели правда нечем помочь, и именно поэтому он отделён от «вуз молчит».
    const notImported = boot({
        role: "guest",
        seed: { group: "494", name: "Б-ИСиТ-22" },
        routes: { "GET /api/schedule/lessons": () => json(lessons({
            days: [], updated_at: 0, range_imported: false, imported_months: [],
            source_state: "unavailable",
        })) },
    });
    await until(notImported, () => /не привезлось/i.test(text(notImported.doc, "timetable")));
    check("неделя без копии названа непривезённой", /Расписание на эту неделю не привезлось/.test(text(notImported.doc, "timetable")),
        text(notImported.doc, "timetable"));
    check("гость сам может обновить неделю", /Попробуйте обновить/.test(text(notImported.doc, "timetable")),
        text(notImported.doc, "timetable"));
    // Прежняя фраза «это делает администратор платформы» после ADR-0042 врёт:
    // недостающий месяц приводит сам запрос гостя.
    check("гостю не обещан администратор", !/администратор платформы/.test(text(notImported.doc, "timetable")),
        text(notImported.doc, "timetable"));
    check("в пустой неделе нет карточек дней", notImported.doc.querySelectorAll("#timetable .sched-day").length === 0);
    check("пустая неделя помечена пилюлей «показать нечего»",
        get(notImported.doc, "#updatedNote .sched-source-state.is-none") !== null, text(notImported.doc, "updatedNote"));
    check("при отказе источника никто не называет дату копии",
        !/Копия от|Обновлено/.test(text(notImported.doc, "updatedNote")), text(notImported.doc, "updatedNote"));

    const importedEmpty = boot({
        role: "admin",
        seed: { group: "494", name: "Б-ИСиТ-22" },
        routes: { "GET /api/schedule/lessons": () => json(lessons({ days: [] })) },
    });
    await until(importedEmpty, () => /пар нет/.test(text(importedEmpty.doc, "timetable")));
    check("импортированная пустая неделя отличается от неимпортированной",
        /На этой неделе пар нет/.test(text(importedEmpty.doc, "timetable")), text(importedEmpty.doc, "timetable"));
    check("подсказка называет месяцы, уже лежащие в базе",
        /в базе уже октябрь 2026/.test(text(importedEmpty.doc, "timetable")), text(importedEmpty.doc, "timetable"));

    // Месяц с нулём занятий — покрытие есть, расписания нет. «В базе уже
    // октябрь» про него было бы правдой про покрытие и ложью про пары.
    const onlyEmptyMonths = boot({
        role: "guest",
        seed: { group: "494", name: "Б-ИСиТ-22" },
        routes: { "GET /api/schedule/lessons": () => json(lessons({
            days: [],
            imported_months: [{ month: 10, year: 2026, lessons: 0, ok: true, refreshed_at: 1761300000, attempted_at: 1761300000 }],
        })) },
    });
    await until(onlyEmptyMonths, () => /пар нет/.test(text(onlyEmptyMonths.doc, "timetable")));
    check("пустой месяц в подсказку не попадает", !/в базе уже/.test(text(onlyEmptyMonths.doc, "timetable")),
        text(onlyEmptyMonths.doc, "timetable"));

    // Та же пустая неделя, но у администратора: подсказка обязана указать на
    // его собственную панель, а не советовать «попросите администратора».
    const adminNotImported = boot({
        role: "admin",
        seed: { group: "494", name: "Б-ИСиТ-22" },
        routes: { "GET /api/schedule/lessons": () => json(lessons({
            days: [], updated_at: 0, range_imported: false, imported_months: [],
            source_state: "unavailable",
        })) },
    });
    await until(adminNotImported, () => /Источник расписания/.test(text(adminNotImported.doc, "timetable")));
    check("админу подсказана его панель, а не «попросите администратора»",
        /панель «Источник расписания»/.test(text(adminNotImported.doc, "timetable")), text(adminNotImported.doc, "timetable"));

    // Пустота по причине «доступа нет на сервере» — единственная, где фраза про
    // администратора остаётся правдой.
    const noAccess = boot({
        role: "guest",
        seed: { group: "494", name: "Б-ИСиТ-22" },
        routes: { "GET /api/schedule/lessons": () => json(lessons({
            days: [], updated_at: 0, range_imported: false, imported_months: [],
            source_state: "unavailable", source_configured: false,
        })) },
    });
    await until(noAccess, () => /не попадало в базу/.test(text(noAccess.doc, "timetable")));
    check("пустота без доступа к вузу названа настройкой сервера",
        /Доступ к сайту вуза настраивает администратор/.test(text(noAccess.doc, "timetable")), text(noAccess.doc, "timetable"));
    check("источник без токена не называет вуз молчаливым", !/не ответил/.test(text(noAccess.doc, "timetable")),
        text(noAccess.doc, "timetable"));

    const emptyCatalog = boot({ role: "guest", routes: { "GET /api/schedule/groups": json({ groups: [], count: 0, synced_at: 0 }) } });
    await until(emptyCatalog, () => /Каталог групп пуст/.test(text(emptyCatalog.doc, "groupHelper")));
    check("пустой каталог объяснён честно", /Каталог групп пуст/.test(text(emptyCatalog.doc, "groupHelper")), text(emptyCatalog.doc, "groupHelper"));
    check("без каталога расписание не запрашивается", find(emptyCatalog.calls, "GET /api/schedule/lessons").length === 0,
        emptyCatalog.calls.map((c) => c.key).join(", "));
    check("пустой каталог показывает выбор группы, а не спиннер", /Выберите группу/.test(text(emptyCatalog.doc, "timetable")));

    // ---------- 4b. строка состояния источника ----------
    //
    // Read-through умеет показывать архив вместо свежих данных, и подпись под
    // таблицей — единственное место, где об этом можно узнать. Поэтому здесь
    // держится соответствие между source_state, цветом пилюли и словом в
    // тексте: «обновлено 3 ноября в 14:20» над вчерашней копией при недоступном
    // вузе было бы главным враньём страницы.
    const stateCases = [
        { state: "fresh", pill: "свежее", cls: "is-fresh", note: /Показана свежая копия: обновлена/ },
        { state: "refreshed", pill: "привезено сейчас", cls: "is-fresh", note: /привезли с сайта вуза, пока вы ждали/ },
        { state: "cached", pill: "архив · обновляем", cls: "is-archive", note: /обновляется в фоне\. Копия от/ },
        { state: "stale", pill: "архив", cls: "is-archive", note: /Сайт вуза не ответил — показан архив\. Копия от/ },
    ];
    for (const testCase of stateCases) {
        const page = boot({
            role: "guest",
            seed: { group: "494", name: "Б-ИСиТ-22" },
            routes: { "GET /api/schedule/lessons": () => json(lessons({ source_state: testCase.state })) },
        });
        await until(page, () => text(page.doc, "updatedNote").includes(testCase.pill));
        const pill = get(page.doc, "#updatedNote .sched-source-state");
        check(`состояние ${testCase.state}: пилюля «${testCase.pill}»`,
            pill !== null && pill.classList.contains(testCase.cls), text(page.doc, "updatedNote"));
        check(`состояние ${testCase.state}: текст объясняет, что показано`,
            testCase.note.test(text(page.doc, "updatedNote")), text(page.doc, "updatedNote"));
    }

    // «Показать нечего» при уже нарисованных парах — это неполная неделя, и
    // серверное слово здесь не подходит: часть недели на экране всё-таки есть.
    const partialWeek = boot({
        role: "guest",
        seed: { group: "494", name: "Б-ИСиТ-22" },
        routes: { "GET /api/schedule/lessons": () => json(lessons({ source_state: "unavailable" })) },
    });
    await until(partialWeek, () => /неполно/.test(text(partialWeek.doc, "updatedNote")));
    check("неполная неделя помечена «неполно», а не «показать нечего»",
        get(partialWeek.doc, "#updatedNote .sched-source-state.is-archive") !== null
        && !/показать нечего/.test(text(partialWeek.doc, "updatedNote")), text(partialWeek.doc, "updatedNote"));
    check("при неполной неделе показанное не исчезло",
        partialWeek.doc.querySelectorAll("#timetable .sched-day").length === 2,
        String(partialWeek.doc.querySelectorAll("#timetable .sched-day").length));
    check("неполная неделя называет пропущенную часть",
        /показаны не все занятия/.test(text(partialWeek.doc, "updatedNote")), text(partialWeek.doc, "updatedNote"));

    // ---------- 4c. вкладки каталога: институт → курс → группа ----------
    //
    // Требование владельца: расписание вуза устроено вкладками по институтам и
    // курсам. Проверяем три вещи: что ряды строятся из каталога, что выбранная
    // вкладка подсвечена и подписана для скринридера, и что выбор группы
    // вкладкой — это тот же запрос, что выбор из поля ввода.
    const tabs = boot({ role: "guest", seed: { group: "494", name: "Б-ИСиТ-22" } });
    await until(tabs, () => get(tabs.doc, "#instituteChips .sched-chip") !== null);

    check("ряды вкладок показаны", get(tabs.doc, "#groupNav") !== null
        && !get(tabs.doc, "#groupNav").hasAttribute("hidden"));
    const instituteChips = [...tabs.doc.querySelectorAll("#instituteChips .sched-chip")];
    check("институтов ровно столько, сколько в каталоге", instituteChips.length === 2,
        instituteChips.map((n) => n.dataset.key).join(" | "));
    const selectedInstitute = instituteChips.filter((n) => n.getAttribute("aria-pressed") === "true");
    check("подсвечен институт показанной группы",
        selectedInstitute.length === 1 && /информационных/.test(selectedInstitute[0].textContent),
        text(tabs.doc, "instituteChips"));
    check("вкладка института несёт счёт групп", /2/.test(selectedInstitute[0].textContent),
        selectedInstitute[0] ? selectedInstitute[0].textContent : "");

    const courseChips = [...tabs.doc.querySelectorAll("#courseChips .sched-chip")];
    check("курсы этого института отсортированы по номеру",
        courseChips.map((n) => n.dataset.key).join(",") === "1 курс,2 курс",
        courseChips.map((n) => n.dataset.key).join(","));
    check("подпись курса дословная, как её дал вуз",
        courseChips.every((n) => /курс/.test(n.textContent)), text(tabs.doc, "courseChips"));
    check("выбран курс тот, у которого показанная группа",
        [...tabs.doc.querySelectorAll("#courseChips .sched-chip")]
            .filter((n) => n.getAttribute("aria-pressed") === "true").map((n) => n.dataset.key).join() === "2 курс",
        text(tabs.doc, "courseChips"));
    check("ряд групп ограничен выбранным курсом",
        [...tabs.doc.querySelectorAll("#navGroupChips .sched-chip")].map((n) => n.textContent).join() === "Б-ИСиТ-22",
        text(tabs.doc, "navGroupChips"));

    const requestsBefore = find(tabs.calls, "GET /api/schedule/lessons").length;
    courseChips[0].click();
    await until(tabs, () => get(tabs.doc, "#navGroupChips .sched-chip") !== null
        && tabs.doc.querySelectorAll("#navGroupChips .sched-chip").length === 1);
    check("переход по вкладке курса ничего не запрашивает",
        find(tabs.calls, "GET /api/schedule/lessons").length === requestsBefore,
        String(find(tabs.calls, "GET /api/schedule/lessons").length));
    check("ряд групп перестроился под курс",
        tabs.doc.querySelector("#navGroupChips .sched-chip").textContent === "Б-ИСиТ-21",
        text(tabs.doc, "navGroupChips"));

    const groupChip = tabs.doc.querySelector("#navGroupChips .sched-chip");
    groupChip.focus();
    groupChip.click();
    await until(tabs, () => find(tabs.calls, "GET /api/schedule/lessons").some((c) => c.params.get("group") === "496"));
    // Ждём не запроса, а его обработки: подсветка вкладки двигается уже по
    // ответу, и без этого проверка читала бы DOM раньше страницы.
    await until(tabs, () => groupChip.classList.contains("is-selected"));
    const viaTab = find(tabs.calls, "GET /api/schedule/lessons").pop();
    check("вкладка группы отправляет её id", viaTab.params.get("group") === "496", viaTab.search);
    check("поле ввода после вкладки показывает человеческое имя",
        tabs.doc.getElementById("groupInput").value === "Б-ИСиТ-21", tabs.doc.getElementById("groupInput").value);
    check("выбранная вкладка группы помечена и не выброшена перерисовкой",
        groupChip.isConnected === true && groupChip.getAttribute("aria-current") === "true"
        && groupChip.classList.contains("is-selected"), groupChip.className);
    check("фокус остался на нажатой вкладке", tabs.doc.activeElement === groupChip,
        tabs.doc.activeElement ? tabs.doc.activeElement.className : "фокуса нет");

    // Свой ввод человека ряды обязаны догонять: иначе «выбранный институт»
    // расходится с показанной неделей.
    tabs.doc.getElementById("groupInput").value = "ИСП-21";
    tabs.doc.getElementById("groupInput").dispatchEvent(new tabs.window.Event("change", { bubbles: true }));
    await until(tabs, () => find(tabs.calls, "GET /api/schedule/lessons").some((c) => c.params.get("group") === "495"));
    await until(tabs, () => [...tabs.doc.querySelectorAll("#instituteChips .sched-chip")]
        .some((n) => n.getAttribute("aria-pressed") === "true" && /природных/.test(n.textContent)));
    check("смена группы вводом передвигает ряд институтов",
        [...tabs.doc.querySelectorAll("#instituteChips .sched-chip")]
            .some((n) => n.getAttribute("aria-pressed") === "true" && /природных/.test(n.dataset.key)),
        [...tabs.doc.querySelectorAll("#instituteChips .sched-chip")]
            .map((n) => `${n.dataset.key}=${n.getAttribute("aria-pressed")}`).join(" | "));

    // Каталог без институтов — не три пустые строки над таблицей, а ничего.
    const noDepartments = boot({
        role: "guest",
        routes: { "GET /api/schedule/groups": json({ groups: [{ external_id: "494", name: "Б-ИСиТ-22" }], count: 1, synced_at: 0 }) },
    });
    await until(noDepartments, () => /Б-ИСиТ-22/.test(text(noDepartments.doc, "groupOptions"))
        || find(noDepartments.calls, "GET /api/schedule/lessons").length > 0);
    check("каталог без институтов ряды вкладок не рисует",
        get(noDepartments.doc, "#groupNav").hasAttribute("hidden"), text(noDepartments.doc, "groupNav"));


    // ---------- 4d. двойники одного имени ----------
    //
    // Каталог источника носит один и тот же код группы в строках двух курсов:
    // у «Б-ПРЛ-21» есть id 36 (3 курс) и id 737 (2 курс), и живой АПЭКС по
    // одному из них отдаёт пустой месяц, а по другому — настоящую таблицу.
    // Проверяем оба права, которые владелец выбрал для этого случая: поле
    // различает варианты подписью, а показ называет, чей id на экране. Молча
    // брать первую строку каталога нельзя — половина каталога при этом
    // показывает пустоту там, где у вуза расписание есть.
    const TWIN = "Б-ПРЛ-21";
    const TWIN_GROUPS = {
        source: "apeks",
        source_name: "АПЭКС-ВУЗ",
        synced_at: 1761200000,
        count: 3,
        groups: [
            { external_id: "36", name: TWIN, department: "Институт природных ресурсов", course: "3 курс", updated_at: 1761200000 },
            { external_id: "737", name: TWIN, department: "Институт природных ресурсов", course: "2 курс", updated_at: 1761200000 },
            { external_id: "494", name: "Б-ИСиТ-22", department: "Институт информационных технологий", course: "2 курс", updated_at: 1761200000 },
        ],
        source_page_url: SOURCE_PAGE,
    };
    const twinEmpty = (id) => lessons({ group_id: id, group_name: TWIN, days: [] });
    const twinFull = lessons({
        group_id: "737",
        group_name: TWIN,
        days: [{
            day_key: 20261006, date: "06.10.2026", weekday: "Вт",
            lessons: [lesson(1, "Пара живого двойника", { group_ext_id: "737", group_name: TWIN })],
        }],
    });
    const twinRoutes = (answer) => ({
        "GET /api/schedule/groups": json(TWIN_GROUPS),
        "GET /api/schedule/lessons": ({ record }) => json(answer(record.params.get("group"))),
    });
    // Из этого каталога спрашивается только пара двойников: любой другой id
    // отвечает обычная непустая фикстура, чтобы на него не приходилось смотреть.
    const twinAnswer = (id) => (id === "737" ? twinFull : id === "36" ? twinEmpty("36") : lessons());
    const twinCalls = (page, from = 0) => find(page.calls, "GET /api/schedule/lessons")
        .slice(from).map((c) => c.params.get("group")).join(",");

    const twin = boot({ role: "guest", seed: { name: TWIN }, routes: twinRoutes(twinAnswer) });
    await until(twin, () => /Пара живого двойника/.test(text(twin.doc, "timetable")));

    const captions = [...twin.doc.querySelectorAll("#groupOptions option")].map((o) => o.value);
    check("подсказки поля различают двойников институтом и курсом",
        captions.includes(`${TWIN} — Институт природных ресурсов, 2 курс`)
        && captions.includes(`${TWIN} — Институт природных ресурсов, 3 курс`), captions.join(" | "));
    check("имя без двойника подсказка не уточняет", captions.includes("Б-ИСиТ-22"), captions.join(" | "));

    check("выбранное голое имя опрашивает соседа по пустому варианту",
        twinCalls(twin) === "36,737", twinCalls(twin));
    check("показана таблица того id, у которого данные есть",
        /Пара живого двойника/.test(text(twin.doc, "timetable")), text(twin.doc, "timetable").slice(0, 90));
    check("строка состояния называет показанный id и тот, что пуст",
        /Показан вариант id 737 — у id 36 источник за этот период пуст/.test(text(twin.doc, "updatedNote")),
        text(twin.doc, "updatedNote"));
    check("показ подписан вариантом с экрана, а не первым из каталога",
        text(twin.doc, "updatedNote").includes(`${TWIN} — Институт природных ресурсов, 2 курс`),
        text(twin.doc, "updatedNote"));

    const both = boot({ role: "guest", seed: { name: TWIN }, routes: twinRoutes((id) => twinEmpty(id)) });
    await until(both, () => /Ни у одного из/.test(text(both.doc, "updatedNote")));
    check("оба пустых варианта названы обоими, а не выдан один за другой",
        /Ни у одного из 2 опрошенных вариантов/.test(text(both.doc, "updatedNote"))
        && text(both.doc, "updatedNote").includes("(id 36, 737)"), text(both.doc, "updatedNote"));

    // Пустота без отметки о прочитанном периоде (range_imported: false) — это
    // «мы не спрашивали», а не «вуз молчал». Тот же разбор, что у пустоты
    // таблицы, обязан быть и в строке про соседей: на сборке без токена хода к
    // вузу нет вовсе, и свалить пустоту на источник значило бы соврать.
    const notAsked = (id) => lessons({ group_id: id, group_name: TWIN, days: [], range_imported: false });
    const blankSlate = boot({ role: "guest", seed: { name: TWIN }, routes: twinRoutes(notAsked) });
    await until(blankSlate, () => /Ни у одного из/.test(text(blankSlate.doc, "updatedNote")));
    check("неопрошенный период назван пустотой базы, а не молчанием источника",
        /в базе за этот период ничего нет/.test(text(blankSlate.doc, "updatedNote"))
        && !/источник за этот период/.test(text(blankSlate.doc, "updatedNote")),
        text(blankSlate.doc, "updatedNote"));

    // И при удачном переключении на живого соседа первому id сказано ровно то,
    // что о нём известно: сосед дал таблицу, а у первого период не качали.
    const mixedTwin = (id) => (id === "737" ? twinFull
        : lessons({ group_id: id, group_name: TWIN, days: [], range_imported: false }));
    const blankFirst = boot({ role: "guest", seed: { name: TWIN }, routes: twinRoutes(mixedTwin) });
    await until(blankFirst, () => /Пара живого двойника/.test(text(blankFirst.doc, "timetable")));
    check("живому соседу назван id, а неопрошенному первому — пустота базы",
        /Показан вариант id 737 — у id 36 в базе за этот период ничего нет/.test(text(blankFirst.doc, "updatedNote")),
        text(blankFirst.doc, "updatedNote"));

    // Выбор подписи из подсказки — это ровно один id: сосед не опрашивается,
    // даже когда выбранный вариант пустой. Иначе человек, который знает свой
    // курс, получил бы чужую таблицу вместо честного «пар нет».
    const chosenVariant = boot({ role: "guest", seed: { name: "Б-ИСиТ-22" }, routes: twinRoutes(twinAnswer) });
    await until(chosenVariant, () => find(chosenVariant.calls, "GET /api/schedule/lessons").length >= 1);
    const ask = (value) => {
        chosenVariant.doc.getElementById("groupInput").value = value;
        chosenVariant.doc.getElementById("groupInput")
            .dispatchEvent(new chosenVariant.window.Event("change", { bubbles: true }));
    };
    let marker = find(chosenVariant.calls, "GET /api/schedule/lessons").length;
    ask(`${TWIN} — Институт природных ресурсов, 2 курс`);
    await until(chosenVariant, () => twinCalls(chosenVariant, marker) !== "");
    await wait(60);
    check("выбранный вариант запрошен своим id", twinCalls(chosenVariant, marker) === "737",
        twinCalls(chosenVariant, marker));
    check("у варианта с данными соседа не спрашивают",
        !/Показан вариант/.test(text(chosenVariant.doc, "updatedNote")), text(chosenVariant.doc, "updatedNote"));

    marker = find(chosenVariant.calls, "GET /api/schedule/lessons").length;
    ask(`${TWIN} — Институт природных ресурсов, 3 курс`);
    await until(chosenVariant, () => twinCalls(chosenVariant, marker) !== "");
    await wait(60);
    check("пустой вариант — один запрос, без опроса соседа", twinCalls(chosenVariant, marker) === "36",
        twinCalls(chosenVariant, marker));
    check("пустому варианту сказано «пар нет», а не чужая таблица",
        !/Пара живого двойника/.test(text(chosenVariant.doc, "timetable"))
        && !/Показан вариант/.test(text(chosenVariant.doc, "updatedNote")), text(chosenVariant.doc, "updatedNote"));

    // Вкладка группы обязан вести ровно тот id, который под ней лежит: у двух
    // вариантов одного имени ряды разные (разные курсы), и голое имя в поле
    // выбрало бы первую строку каталога, а не нажатую вкладку.
    const twinTab = boot({ role: "guest", seed: { group: "737", name: TWIN }, routes: twinRoutes(twinAnswer) });
    await until(twinTab, () => twinTab.doc.querySelectorAll("#courseChips .sched-chip").length >= 2);
    check("вкладка выбранного двойника подсвечена своим id",
        [...twinTab.doc.querySelectorAll("#navGroupChips .sched-chip")]
            .some((n) => n.dataset.key === "737" && n.getAttribute("aria-current") === "true"),
        text(twinTab.doc, "navGroupChips"));
    const course3 = [...twinTab.doc.querySelectorAll("#courseChips .sched-chip")].find((n) => n.dataset.key === "3 курс");
    course3.click();
    await until(twinTab, () => twinTab.doc.querySelector("#navGroupChips .sched-chip") !== null);
    marker = find(twinTab.calls, "GET /api/schedule/lessons").length;
    twinTab.doc.querySelector("#navGroupChips .sched-chip").click();
    await until(twinTab, () => twinCalls(twinTab, marker) !== "");
    await wait(60);
    check("вкладка группы-двойника ведёт ровно свой id", twinCalls(twinTab, marker) === "36",
        twinCalls(twinTab, marker));
    check("поле ввода после вкладки показывает подпись варианта",
        twinTab.doc.getElementById("groupInput").value === `${TWIN} — Институт природных ресурсов, 3 курс`,
        twinTab.doc.getElementById("groupInput").value);


    //
    // ---------- 5. навигация по неделям ----------
    //
    // Границы периода считает страница, и проверять их надо без привязки к дате
    // прогона: неделя начинается с понедельника, длится ровно семь дней и
    // содержит сегодняшний день — это и есть требование «открывается на
    // сегодняшней дате». Месяц для этого не годился: стрелку надо было крутить
    // столько раз, какой на календаре месяц.
    const nav = boot({ role: "guest", seed: { group: "494", name: "Б-ИСиТ-22" } });
    await until(nav, () => find(nav.calls, "GET /api/schedule/lessons").length >= 1);

    const period = (call) => ({ from: Number(call.params.get("from")), to: Number(call.params.get("to")) });
    const first = period(find(nav.calls, "GET /api/schedule/lessons")[0]);
    const midnight = new Date();
    midnight.setHours(0, 0, 0, 0);
    const todayUnix = Math.floor(midnight.getTime() / 1000);
    const labelStart = text(nav.doc, "weekLabel");

    check("запрошен ровно недельный период", first.to - first.from + 1 === 7 * 86400, JSON.stringify(first));
    check("неделя начинается с понедельника", new Date(first.from * 1000).getDay() === 1,
        new Date(first.from * 1000).toString());
    check("сегодняшний день входит в показанный период", first.from <= todayUnix && todayUnix <= first.to,
        JSON.stringify(first));
    check("подпись недели содержит число", /\d/.test(labelStart), labelStart);

    nav.doc.getElementById("nextWeek").click();
    await until(nav, () => find(nav.calls, "GET /api/schedule/lessons").length >= 2);
    const forward = period(find(nav.calls, "GET /api/schedule/lessons").pop());
    check("стрелка вперёд сдвигает период ровно на семь дней", forward.from - first.from === 7 * 86400,
        JSON.stringify({ first, forward }));
    check("подпись недели изменилась вместе с периодом", text(nav.doc, "weekLabel") !== labelStart,
        `${labelStart} → ${text(nav.doc, "weekLabel")}`);

    nav.doc.getElementById("prevWeek").click();
    nav.doc.getElementById("prevWeek").click();
    await until(nav, () => find(nav.calls, "GET /api/schedule/lessons").length >= 4);
    const backward = period(find(nav.calls, "GET /api/schedule/lessons").pop());
    check("стрелка назад отмеряет семь дней обратно", backward.from === first.from - 7 * 86400,
        JSON.stringify(backward));

    nav.doc.getElementById("currentWeek").click();
    await until(nav, () => find(nav.calls, "GET /api/schedule/lessons").length >= 5);
    const backToNow = period(find(nav.calls, "GET /api/schedule/lessons").pop());
    check("«Текущая неделя» возвращает к сегодняшней", backToNow.from === first.from, JSON.stringify(backToNow));
    check("подпись вернулась к исходной", text(nav.doc, "weekLabel") === labelStart, text(nav.doc, "weekLabel"));

    // ---------- 5b. опоздавший ответ не перерисовывает показанную неделю ----------
    //
    // Неделя листается кликами, а ответы приходят не в порядке запросов: если
    // дорисовывать любой пришедший, подпись остаётся на одной неделе, а таблица
    // под ней рисуется по другой — человек видит чужое расписание под своей
    // подписью. Здесь опоздавший ответ той недели, которую уже сменили, обязан
    // не оставить на странице ни одной своей пары.
    let homeFrom = null;
    const fresh = lessons({
        days: [{ day_key: 20261006, date: "06.10.2026", weekday: "Вт", lessons: [lesson(1, "Свежая неделя")] }],
    });
    const late = lessons({
        days: [{ day_key: 20261013, date: "13.10.2026", weekday: "Вт",
            lessons: [lesson(1, "Опоздавшая неделя", { day_key: 20261013, date: "13.10.2026" })] }],
    });
    const raced = boot({
        role: "guest",
        seed: { group: "494", name: "Б-ИСиТ-22" },
        routes: {
            "GET /api/schedule/lessons": ({ record }) => {
                // Первая загрузка задаёт «домашнюю» неделю: ответы на неё
                // мгновенные, любая другая неделя приходит с задержкой — так
                // опоздавший ответ гарантированно прилетает после свежего.
                const from = Number(record.params.get("from"));
                if (homeFrom === null) homeFrom = from;
                if (from === homeFrom) return json(fresh);
                return new Promise((resolve) => setTimeout(() => resolve(json(late)), 250));
            },
        },
    });
    await until(raced, () => /Свежая неделя/.test(text(raced.doc, "timetable")));
    const homeLabel = text(raced.doc, "weekLabel");

    raced.doc.getElementById("nextWeek").click();
    await until(raced, () => find(raced.calls, "GET /api/schedule/lessons").length === 2);
    raced.doc.getElementById("prevWeek").click();
    await until(raced, () => /Свежая неделя/.test(text(raced.doc, "timetable")));
    check("возврат к своей неделе нарисован до опоздавшего ответа",
        text(raced.doc, "weekLabel") === homeLabel && /Свежая неделя/.test(text(raced.doc, "timetable")),
        text(raced.doc, "weekLabel"));

    // 400 мс больше задержки: к этому моменту ответ чужой недели точно получен.
    // Инвариант один и тот же с обеих сторон таблицы: и подпись, и пары обязаны
    // остаться своей недели.
    await wait(400);
    check("опоздавший ответ чужой недели отброшен",
        !/Опоздавшая неделя/.test(text(raced.doc, "timetable")) && /Свежая неделя/.test(text(raced.doc, "timetable"))
        && text(raced.doc, "weekLabel") === homeLabel,
        `${text(raced.doc, "weekLabel")} | ${text(raced.doc, "timetable").slice(0, 100)}`);

    // смена группы: имя из каталога превращается в id (однозначный), а живой
    // ввод вне каталога уходит как есть — сервер умеет искать и по имени.
    nav.doc.getElementById("groupInput").value = "ИСП-21";
    nav.doc.getElementById("groupInput").dispatchEvent(new nav.window.Event("change", { bubbles: true }));
    await until(nav, () => find(nav.calls, "GET /api/schedule/lessons").some((c) => c.params.get("group") === "495"));
    const chosen = find(nav.calls, "GET /api/schedule/lessons").pop();
    check("имя из каталога отправлено как id", chosen.params.get("group") === "495", chosen.search);

    nav.doc.getElementById("groupInput").value = "  б-исит-22  ";
    nav.doc.getElementById("groupInput").dispatchEvent(new nav.window.Event("change", { bubbles: true }));
    await until(nav, () => find(nav.calls, "GET /api/schedule/lessons").some((c) => c.params.get("group") === "494"));
    check("тот же ввод в чужом регистре с пробелами найден в каталоге",
        find(nav.calls, "GET /api/schedule/lessons").pop().params.get("group") === "494");

    // ---------- 5. состояния ошибок ----------
    const broken = boot({
        role: "guest",
        seed: { group: "494", name: "Б-ИСиТ-22", month: 10, year: 2026 },
        routes: { "GET /api/schedule/lessons": json({ error: { code: "source_unavailable", message: "Источник АПЭКС-ВУЗ не ответил (пары 10.2026)." } }, 502) },
    });
    await until(broken, () => /не ответил/.test(text(broken.doc, "timetable")));
    check("сбой источника показан текстом сервера", /не ответил/.test(text(broken.doc, "timetable")), text(broken.doc, "timetable").slice(0, 80));
    check("при сбое нет фантомной метки «обновлено»", text(broken.doc, "updatedNote") === "", text(broken.doc, "updatedNote"));

    const gone = boot({
        role: "guest",
        seed: { group: "999", name: "НЕТ-ТАКОЙ-22", month: 10, year: 2026 },
        routes: { "GET /api/schedule/lessons": json({ error: { code: "not_found", message: "group not found in the group catalogue: sync it first", fields: { group: "group not found in the group catalogue: sync it first" } } }, 404) },
    });
    await until(gone, () => /нет в каталоге вуза/.test(text(gone.doc, "timetable")));
    check("несуществующая группа объяснена по-русски", /Нет такой группы|такой группы нет/i.test(text(gone.doc, "timetable")),
        text(gone.doc, "timetable"));

    // ---------- 6. панель источника ----------
    const admin = boot({ role: "admin", seed: { group: "494", name: "Б-ИСиТ-22", month: 10, year: 2026 } });
    await until(admin, () => find(admin.calls, "GET /api/schedule/source/status").length > 0);
    check("админу показана панель источника", !hidden(admin.doc, "sourceManageSection"));
    check("цифры источника на панели", text(admin.doc, "statGroups") === "2" && text(admin.doc, "statLessons") === "3"
        && text(admin.doc, "statImportedGroups") === "1",
        [text(admin.doc, "statGroups"), text(admin.doc, "statLessons"), text(admin.doc, "statImportedGroups")].join("|"));
    check("настроенный источник не требует токена", !/не настроен/.test(text(admin.doc, "sourceHint")), text(admin.doc, "sourceHint"));

    admin.doc.getElementById("syncGroupsBtn").click();
    await until(admin, () => find(admin.calls, "POST /api/schedule/source/groups").length > 0);
    const sync = find(admin.calls, "POST /api/schedule/source/groups")[0];
    check("синхронизация каталога отправлена POST'ом", sync.method === "POST");
    check("синхронизация несёт CSRF-заголовок", sync.headers["X-CSRF-Token"] === "test-csrf-token",
        JSON.stringify(sync.headers));
    check("тело у синхронизации пустое", sync.body === undefined, String(sync.body));
    await until(admin, () => /Каталог обновлён/.test(text(admin.doc, "sourceMsg")));
    check("результат синхронизации показан числами", /получено 2, добавлено 2/.test(text(admin.doc, "sourceMsg")), text(admin.doc, "sourceMsg"));
    check("после синхронизации каталог перечитан", find(admin.calls, "GET /api/schedule/groups").length === 2,
        String(find(admin.calls, "GET /api/schedule/groups").length));

    // импорт одной группы: месяц берётся из показанной недели (понедельника),
    // а не из памяти о прошлом просмотре; count = 1 не отправляется (сервер
    // отвергает неизвестные поля, а 1 — его собственный default).
    const shownFrom = Number(find(admin.calls, "GET /api/schedule/lessons")[0].params.get("from"));
    const shown = new Date(shownFrom * 1000);

    admin.doc.getElementById("importGroupBtn").click();
    await until(admin, () => find(admin.calls, "POST /api/schedule/source/lessons").length > 0);
    const imp = JSON.parse(find(admin.calls, "POST /api/schedule/source/lessons")[0].body);
    check("импорт просит показанную неделю и группу",
        imp.group === "494" && imp.month === shown.getMonth() + 1 && imp.year === shown.getFullYear(),
        `${JSON.stringify(imp)} | показана неделя с ${shown.toLocaleDateString("ru-RU")}`);
    check("count=1 не улетает на сервер", !("count" in imp), JSON.stringify(imp));
    await until(admin, () => /Импортировано/.test(text(admin.doc, "sourceMsg")));
    check("импорт подтверждён числом пар", /Импортировано пар: 3/.test(text(admin.doc, "sourceMsg")), text(admin.doc, "sourceMsg"));

    admin.doc.getElementById("importCount").value = "3";
    admin.doc.getElementById("importGroupBtn").click();
    await until(admin, () => find(admin.calls, "POST /api/schedule/source/lessons").length > 1);
    const imp3 = JSON.parse(find(admin.calls, "POST /api/schedule/source/lessons").pop().body);
    check("выбор «3 месяца» доехал до сервера", imp3.count === 3, JSON.stringify(imp3));

    // ---------- 6b. массовый импорт: запуск, прогресс, завершение ----------
    //
    // Запрос владельца — «парситься должны все группы и все даты». Задача
    // фоновая, поэтому страница обязана: отправить тело без группы, показать
    // живой прогресс, перестать опрашивать после завершения и перечитать то,
    // что импорт изменил.
    const running = {
        running: true, actor: "root", started_at: 1761300000, finished_at: 0,
        months: [{ month: 10, year: 2026 }], groups: 588, steps: 588, done: 12,
        lessons: 340, failed: 1, current: "Б-ИСиТ-22 · 10.2026", failed_last_group: "",
    };
    const finished = {
        running: false, actor: "root", started_at: 1761300000, finished_at: 1761303600,
        months: [{ month: 10, year: 2026 }], groups: 588, steps: 588, done: 588,
        lessons: 14800, failed: 3, current: "", failed_last_group: "ИСП-21 · 10.2026",
    };

    const mass = boot({
        role: "admin",
        seed: { group: "494", name: "Б-ИСиТ-22" },
        routes: {
            // Ответы отдаются функцией: Response читается один раз, а эту
            // фикстуру страница опрашивает несколько раз подряд.
            "POST /api/schedule/source/import-all": () => json({ source: "apeks", job: running }, 202),
            "GET /api/schedule/source/import-all": ({ hits }) =>
                json({ source: "apeks", job: hits["GET /api/schedule/source/import-all"] === 1 ? running : finished }),
        },
    });

    // Первый опрос — инициализация панели: задача идёт, и панель это видит ещё
    // до всякой кнопки.
    await until(mass, () => /Массовый импорт идёт/.test(text(mass.doc, "sourceJobNote")));
    check("прогресс задачи показан числами шагов", /12 из 588/.test(text(mass.doc, "sourceJobNote")), text(mass.doc, "sourceJobNote"));
    check("прогресс называет группу, которую читает", /Б-ИСиТ-22/.test(text(mass.doc, "sourceJobNote")),
        text(mass.doc, "sourceJobNote"));

    mass.doc.getElementById("importAllBtn").click();
    await until(mass, () => find(mass.calls, "POST /api/schedule/source/import-all").length > 0);
    const allCall = find(mass.calls, "POST /api/schedule/source/import-all")[0];
    check("массовый импорт отправлен POST'ом с CSRF", allCall.method === "POST"
        && allCall.headers["X-CSRF-Token"] === "test-csrf-token", JSON.stringify(allCall.headers));
    const all = JSON.parse(allCall.body);
    const massMonday = new Date(Number(find(mass.calls, "GET /api/schedule/lessons")[0].params.get("from")) * 1000);
    check("массовый импорт просит период и не просит группу",
        !("group" in all) && typeof all.month === "number" && typeof all.year === "number", JSON.stringify(all));
    check("месяц массовой задачи — показанная неделя",
        all.month === massMonday.getMonth() + 1 && all.year === massMonday.getFullYear(),
        `${JSON.stringify(all)} | понедельник ${massMonday.toLocaleDateString("ru-RU")}`);
    await until(mass, () => /запущен/.test(text(mass.doc, "sourceMsg")));
    check("запуск подтверждён числом групп", /588 групп/.test(text(mass.doc, "sourceMsg")), text(mass.doc, "sourceMsg"));

    // Второй опрос по таймеру отдаёт завершённую задачу.
    await until(mass, () => /завершён/.test(text(mass.doc, "sourceJobNote")), 200);
    check("итог задачи подписан счётчиками", /завершён/.test(text(mass.doc, "sourceJobNote"))
        && /пар 14800/.test(text(mass.doc, "sourceJobNote")) && /сбоев 3/.test(text(mass.doc, "sourceJobNote")),
        text(mass.doc, "sourceJobNote"));
    check("сбойная группа названа по имени, без путей и стека",
        /ИСП-21/.test(text(mass.doc, "sourceJobNote")) && !/\//.test(text(mass.doc, "sourceJobNote")),
        text(mass.doc, "sourceJobNote"));
    check("после завершения статистика источника перечитана",
        find(mass.calls, "GET /api/schedule/source/status").length >= 2,
        String(find(mass.calls, "GET /api/schedule/source/status").length));

    const pollsBefore = find(mass.calls, "GET /api/schedule/source/import-all").length;
    await wait(2600);
    check("завершённая задача больше не опрашивается",
        find(mass.calls, "GET /api/schedule/source/import-all").length === pollsBefore,
        `${pollsBefore} → ${find(mass.calls, "GET /api/schedule/source/import-all").length}`);

    // 409 — не тупик: импорт уже идёт (с другой вкладки), и панель обязана
    // показать его прогресс вместо того чтобы оставить админа на тексте конфликта.
    const conflict = boot({
        role: "admin",
        seed: { group: "494", name: "Б-ИСиТ-22" },
        routes: {
            "POST /api/schedule/source/import-all": () => json({ error: "массовый импорт уже выполняется", job: running }, 409),
            "GET /api/schedule/source/import-all": () => json({ source: "apeks", job: running }),
        },
    });
    await until(conflict, () => find(conflict.calls, "GET /api/schedule/source/status").length > 0);
    conflict.doc.getElementById("importAllBtn").click();
    await until(conflict, () => /уже выполняется/.test(text(conflict.doc, "sourceMsg")));
    check("повторный запуск назван конфликтом, а не поломкой", /уже выполняется/.test(text(conflict.doc, "sourceMsg")),
        text(conflict.doc, "sourceMsg"));
    await until(conflict, () => find(conflict.calls, "GET /api/schedule/source/import-all").length >= 2, 200);
    check("на 409 опрос прогресса всё равно идёт",
        find(conflict.calls, "GET /api/schedule/source/import-all").length >= 2,
        String(find(conflict.calls, "GET /api/schedule/source/import-all").length));
    check("конфликт не оставил кнопку в is-loading",
        !conflict.doc.getElementById("importAllBtn").classList.contains("is-loading")
        && conflict.doc.getElementById("importAllBtn").disabled === false);

    // не настроен: состояние рабочее, и страница обязана назвать причину —
    // именно переменную окружения, а не «что-то пошло не так».
    const unconfigured = boot({
        role: "admin",
        seed: { group: "494", name: "Б-ИСиТ-22", month: 10, year: 2026 },
        routes: { "GET /api/schedule/source/status": json(Object.assign({}, STATUS, { configured: false })) },
    });
    await until(unconfigured, () => /APEKS_TOKEN/.test(text(unconfigured.doc, "sourceHint")));
    check("ненастроенный источник назван переменной", /APEKS_TOKEN/.test(text(unconfigured.doc, "sourceHint")), text(unconfigured.doc, "sourceHint"));
    check("при ненастроенном источнике цифры всё равно видно", text(unconfigured.doc, "statGroups") === "2");

    // Причина «токена нет» — единственная, которую панель формулирует сама. С
    // ADR-0021 их стало несколько, и настоящую знает только сборка сервера: она
    // приходит полем hint, а прежняя фраза панели обязана уступить — иначе
    // админ с уже заведённым токеном получает совет завести его заново.
    //
    // Текст hint здесь повторяет строку из cmd/server/main.go: проверка
    // держит контракт поля, а не фантазию фронтенда.
    const noParser = boot({
        role: "admin",
        seed: { group: "494", name: "Б-ИСиТ-22", month: 10, year: 2026 },
        routes: {
            "GET /api/schedule/source/status": json(Object.assign({}, STATUS, {
                configured: false,
                missing_config: "PYTHON_BIN",
                hint: "токен APEKS_TOKEN задан, но парсер расписания не запускается: проверьте PYTHON_BIN " +
                    "(по умолчанию python3) и SCHEDULE_PARSER (по умолчанию tools/schedule-parser/fetch.py); " +
                    "запасной путь без python — SCHEDULE_SOURCE=go",
            })),
        },
    });
    await until(noParser, () => /PYTHON_BIN/.test(text(noParser.doc, "sourceHint")));
    const parserHint = text(noParser.doc, "sourceHint");
    check("панель называет причину сервера", /PYTHON_BIN/.test(parserHint) && /SCHEDULE_SOURCE=go/.test(parserHint), parserHint);
    check("панель не советует завести уже заданный токен", !/не задан APEKS_TOKEN/.test(parserHint), parserHint);
    check("при чужой причине цифры и обещание чтения остаются", /Чтение уже импортированных/.test(parserHint)
        && text(noParser.doc, "statGroups") === "2", parserHint);

    // источник падает: 502 от сервера, текст сервера, а не выдуманный перевод.
    const failed = boot({
        role: "admin",
        seed: { group: "494", name: "Б-ИСиТ-22", month: 10, year: 2026 },
        routes: { "POST /api/schedule/source/lessons": json({ error: { code: "source_unavailable", message: "Источник АПЭКС-ВУЗ не ответил (пары 10.2026). Проверьте APEKS_TOKEN и попробуйте позже." } }, 502) },
    });
    // ждём статуса источника: он приходит в самом конце инициализации, и клик
    // раньше этого момента уткнулся бы в кнопку без обработчика (страница
    // навешивает его на DOMContentLoaded).
    await until(failed, () => find(failed.calls, "GET /api/schedule/source/status").length > 0);
    failed.doc.getElementById("importGroupBtn").click();
    await until(failed, () => hidden(failed.doc, "sourceMsg") === false);
    check("сбой импорта показан в панели", /не ответил/.test(text(failed.doc, "sourceMsg")),
        failed.doc.getElementById("sourceMsg").className + "|" + text(failed.doc, "sourceMsg"));
    check("кнопка импорта разблокирована после сбоя", failed.doc.getElementById("importGroupBtn").disabled === false);
    check("после сбоя импорта страница не осталась в is-loading",
        !failed.doc.getElementById("importGroupBtn").classList.contains("is-loading"));

    // преподаватель — не админ: панели нет, и запросов к ней тоже нет.
    const teacher = boot({ role: "teacher", seed: { group: "494", name: "Б-ИСиТ-22", month: 10, year: 2026 } });
    await until(teacher, () => find(teacher.calls, "GET /api/schedule/lessons").length > 0);
    check("преподавателю панель источника скрыта", hidden(teacher.doc, "sourceManageSection"));
    check("преподаватель не трогает статус источника", find(teacher.calls, "GET /api/schedule/source/status").length === 0,
        teacher.calls.map((c) => c.key).join(", "));
    check("расписание преподавателю тоже показано", teacher.doc.querySelectorAll("#timetable .sched-day").length === 2);

    // лента и ресурсы не сломаны перестройкой инициализации
    check("авторизованному лента загружена", find(teacher.calls, "GET /api/schedule").length === 1
        && find(teacher.calls, "GET /api/resources").length === 1);
    check("после входа баннер скрыт", hidden(teacher.doc, "guestNotice"));

    check("ни один обработчик не оставил необработанного отклонения", rejections.length === 0, JSON.stringify(rejections));

    const failed2 = results.filter((r) => !r.ok);
    console.log(`\nИТОГ: ${results.length - failed2.length}/${results.length} проверок пройдено`);
    for (const f of failed2) console.log("  FAIL " + f.name + (f.detail ? " — " + f.detail : ""));
    process.exit(failed2.length ? 1 : 0);
})();
