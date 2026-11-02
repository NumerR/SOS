// Приёмка T4.1: страница расписания на импортированных данных источника.
//
// Зачем отдельный тест: до T4.1 первый блок страницы был карточкой со ссылкой
// на сайт вуза — встроить его нельзя (X-Frame-Options), и страница честно
// говорила «открывайте снаружи». Теперь внутри платформы лежат разобранные
// пары из АПЭКС-ВУЗа, и проверять надо не «ссылка есть», а поведение витрины:
// что гость видит расписание (маршруты чтения публичные), что неделя листается,
// что пустые состояния различимы («не импортировано» ≠ «пар нет»), что панель
// импорта показана только админу и что запрос источника с браузера не делается
// вообще — токена здесь нет и быть не может.
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

const GROUPS = {
    source: "apeks",
    source_name: "АПЭКС-ВУЗ",
    synced_at: 1761200000,
    count: 2,
    groups: [
        { external_id: "494", name: "Б-ИСиТ-22", department: "Институт информационных технологий", course: "2", updated_at: 1761200000 },
        { external_id: "495", name: "ИСП-21", department: "Институт природных ресурсов", course: "3", updated_at: 1761200000 },
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
        imported_months: [{ month: 10, year: 2026, lessons: 4, imported_at: 1761300000 }],
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
    if (key === "POST /api/schedule/source/lessons") return json({ source: "apeks", group_id: "494", group_name: "Б-ИСиТ-22", months: [{ month: 10, year: 2026, lessons: 3, imported_at: 1761300000 }], lessons: 3 });
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
    check("метка «обновлено» есть", /Обновлено .*Б-ИСиТ-22/.test(text(guest.doc, "updatedNote")), text(guest.doc, "updatedNote"));
    check("подсказка datalist заполнена каталогом", guest.doc.querySelectorAll("#groupOptions option").length === 2);
    check("счётчик каталога по-русски", /В каталоге 2 группы/.test(text(guest.doc, "groupHelper")), text(guest.doc, "groupHelper"));
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
        imported_months: [{ month: new Date().getMonth() + 1, year: new Date().getFullYear(), lessons: 4, imported_at: 1761300000 }],
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

    // ---------- 4. пустые состояния ----------
    //
    // «Не импортировано» и «импортировано, но пар нет» различает сервер
    // (range_imported): неделя задевает два месяца, и по списку месяцев на
    // странице вывод был бы неверен в обе стороны.
    const notImported = boot({
        role: "guest",
        seed: { group: "494", name: "Б-ИСиТ-22" },
        routes: { "GET /api/schedule/lessons": () => json(lessons({
            days: [], updated_at: 0, range_imported: false, imported_months: [],
        })) },
    });
    await until(notImported, () => /не импортировано/i.test(text(notImported.doc, "timetable")));
    check("неделя без импорта названа неимпортированной", /ещё не импортировано/i.test(text(notImported.doc, "timetable")),
        text(notImported.doc, "timetable"));
    check("гостю назван тот, кто делает импорт", /администратор платформы/.test(text(notImported.doc, "timetable")),
        text(notImported.doc, "timetable"));
    check("в пустой неделе нет карточек дней", notImported.doc.querySelectorAll("#timetable .sched-day").length === 0);
    check("неимпортированная неделя не подписывается датой импорта", !/Обновлено/.test(text(notImported.doc, "updatedNote")),
        text(notImported.doc, "updatedNote"));

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

    // Та же пустая неделя, но у администратора: подсказка обязана указать на
    // его собственную панель, а не советовать «попросите администратора».
    const adminNotImported = boot({
        role: "admin",
        seed: { group: "494", name: "Б-ИСиТ-22" },
        routes: { "GET /api/schedule/lessons": () => json(lessons({
            days: [], updated_at: 0, range_imported: false, imported_months: [],
        })) },
    });
    await until(adminNotImported, () => /Источник расписания/.test(text(adminNotImported.doc, "timetable")));
    check("админу подсказана его панель, а не «попросите администратора»",
        /Импортируйте её кнопками панели/.test(text(adminNotImported.doc, "timetable")), text(adminNotImported.doc, "timetable"));

    const emptyCatalog = boot({ role: "guest", routes: { "GET /api/schedule/groups": json({ groups: [], count: 0, synced_at: 0 }) } });
    await until(emptyCatalog, () => /Каталог групп пуст/.test(text(emptyCatalog.doc, "groupHelper")));
    check("пустой каталог объяснён честно", /Каталог групп пуст/.test(text(emptyCatalog.doc, "groupHelper")), text(emptyCatalog.doc, "groupHelper"));
    check("без каталога расписание не запрашивается", find(emptyCatalog.calls, "GET /api/schedule/lessons").length === 0,
        emptyCatalog.calls.map((c) => c.key).join(", "));
    check("пустой каталог показывает выбор группы, а не спиннер", /Выберите группу/.test(text(emptyCatalog.doc, "timetable")));

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
    await until(gone, () => /нет в импортированном каталоге/.test(text(gone.doc, "timetable")));
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
