// Приёмка T4.1: страница расписания на импортированных данных источника.
//
// Зачем отдельный тест: до T4.1 первый блок страницы был карточкой со ссылкой
// на сайт вуза — встроить его нельзя (X-Frame-Options), и страница честно
// говорила «открывайте снаружи». Теперь внутри платформы лежат разобранные
// пары из АПЭКС-ВУЗа, и проверять надо не «ссылка есть», а поведение витрины:
// что гость видит расписание (маршруты чтения публичные), что месяц листается,
// что пустые состояния различимы («не импортирован» ≠ «пар нет»), что панель
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

// boot сеет localStorage до исполнения скриптов страницы: выбор группы и
// месяца переживает перезагрузку, и без этого нельзя получить детерминированный
// «декабрь 2026» для проверки перехода в январь.
function boot({ role = "teacher", routes = {}, seed = null } = {}) {
    const html = fs.readFileSync(path.join(REPO, "web", "schedule.html"), "utf8");
    const dom = new JSDOM(html, { url: BASE + "/schedule", runScripts: "outside-only", pretendToBeVisual: true });
    const { window } = dom;

    if (seed) window.localStorage.setItem("sos.schedule.choice", JSON.stringify(seed));

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

    // ---------- 3. пустые состояния ----------
    const notImported = boot({
        role: "guest",
        seed: { group: "494", name: "Б-ИСиТ-22", month: 12, year: 2026 },
        routes: { "GET /api/schedule/lessons": json(lessons({ days: [], updated_at: 0 })) },
    });
    await until(notImported, () => /не импортирован/.test(text(notImported.doc, "timetable")));
    check("месяц без импорта назван неимпортированным", /ещё не импортирован/i.test(text(notImported.doc, "timetable")),
        text(notImported.doc, "timetable"));
    check("в пустом месяце нет карточек дней", notImported.doc.querySelectorAll("#timetable .sched-day").length === 0);
    check("неимпортированный месяц не подписывается датой импорта", !/Обновлено/.test(text(notImported.doc, "updatedNote")),
        text(notImported.doc, "updatedNote"));

    const importedEmpty = boot({
        role: "admin",
        seed: { group: "494", name: "Б-ИСиТ-22", month: 10, year: 2026 },
        routes: { "GET /api/schedule/lessons": json(lessons({ days: [] })) },
    });
    await until(importedEmpty, () => /пар нет/.test(text(importedEmpty.doc, "timetable")));
    check("импортированный пустой месяц отличается от неимпортированного",
        /пар нет/.test(text(importedEmpty.doc, "timetable")), text(importedEmpty.doc, "timetable"));

    const emptyCatalog = boot({ role: "guest", routes: { "GET /api/schedule/groups": json({ groups: [], count: 0, synced_at: 0 }) } });
    await until(emptyCatalog, () => /Каталог групп пуст/.test(text(emptyCatalog.doc, "groupHelper")));
    check("пустой каталог объяснён честно", /Каталог групп пуст/.test(text(emptyCatalog.doc, "groupHelper")), text(emptyCatalog.doc, "groupHelper"));
    check("без каталога расписание не запрашивается", find(emptyCatalog.calls, "GET /api/schedule/lessons").length === 0,
        emptyCatalog.calls.map((c) => c.key).join(", "));
    check("пустой каталог показывает выбор группы, а не спиннер", /Выберите группу/.test(text(emptyCatalog.doc, "timetable")));

    // ---------- 4. навигация по месяцам ----------
    //
    // Декабрь 2026 -> январь 2027: границы месяца считает сервер (month/year),
    // поэтому от клиента нужен только корректный переход через год.
    const dec = boot({ role: "guest", seed: { group: "494", name: "Б-ИСиТ-22", month: 12, year: 2026 } });
    await until(dec, () => find(dec.calls, "GET /api/schedule/lessons").length > 0);
    dec.doc.getElementById("nextMonth").click();
    await until(dec, () => find(dec.calls, "GET /api/schedule/lessons").length > 1);
    const january = find(dec.calls, "GET /api/schedule/lessons").pop();
    check("стрелка вперёд перелистывает через год", january.params.get("month") === "1" && january.params.get("year") === "2027",
        january.search);
    check("подпись месяца обновилась", /январь 2027/i.test(text(dec.doc, "monthLabel")), text(dec.doc, "monthLabel"));

    dec.doc.getElementById("currentMonth").click();
    await until(dec, () => find(dec.calls, "GET /api/schedule/lessons").length > 2);
    const backToNow = find(dec.calls, "GET /api/schedule/lessons").pop();
    const realMonth = String(new Date().getMonth() + 1);
    check("кнопка текущего месяца возвращает к сегодняшнему", backToNow.params.get("month") === realMonth
        && backToNow.params.get("year") === String(new Date().getFullYear()), backToNow.search);

    // смена группы: имя из каталога превращается в id (однозначный), а живой
    // ввод вне каталога уходит как есть — сервер умеет искать и по имени.
    dec.doc.getElementById("groupInput").value = "ИСП-21";
    dec.doc.getElementById("groupInput").dispatchEvent(new dec.window.Event("change", { bubbles: true }));
    await until(dec, () => find(dec.calls, "GET /api/schedule/lessons").some((c) => c.params.get("group") === "495"));
    const chosen = find(dec.calls, "GET /api/schedule/lessons").pop();
    check("имя из каталога отправлено как id", chosen.params.get("group") === "495", chosen.search);

    dec.doc.getElementById("groupInput").value = "  б-исит-22  ";
    dec.doc.getElementById("groupInput").dispatchEvent(new dec.window.Event("change", { bubbles: true }));
    await until(dec, () => find(dec.calls, "GET /api/schedule/lessons").some((c) => c.params.get("group") === "494"));
    check("тот же ввод в чужом регистре с пробелами найден в каталоге",
        find(dec.calls, "GET /api/schedule/lessons").pop().params.get("group") === "494");

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

    // импорт: только выбранный месяц и группа; поле count = 1 не отправляется
    // (сервер отвергает неизвестные поля, а 1 — его собственный default).
    admin.doc.getElementById("importMonthBtn").click();
    await until(admin, () => find(admin.calls, "POST /api/schedule/source/lessons").length > 0);
    const imp = JSON.parse(find(admin.calls, "POST /api/schedule/source/lessons")[0].body);
    check("импорт просит ровно показанные месяц и группу", imp.group === "494" && imp.month === 10 && imp.year === 2026,
        JSON.stringify(imp));
    check("count=1 не улетает на сервер", !("count" in imp), JSON.stringify(imp));
    await until(admin, () => /Импортировано/.test(text(admin.doc, "sourceMsg")));
    check("импорт подтверждён числом пар", /Импортировано пар: 3/.test(text(admin.doc, "sourceMsg")), text(admin.doc, "sourceMsg"));

    admin.doc.getElementById("importCount").value = "3";
    admin.doc.getElementById("importMonthBtn").click();
    await until(admin, () => find(admin.calls, "POST /api/schedule/source/lessons").length > 1);
    const imp3 = JSON.parse(find(admin.calls, "POST /api/schedule/source/lessons").pop().body);
    check("выбор «3 месяца» доехал до сервера", imp3.count === 3, JSON.stringify(imp3));

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
    failed.doc.getElementById("importMonthBtn").click();
    await until(failed, () => hidden(failed.doc, "sourceMsg") === false);
    check("сбой импорта показан в панели", /не ответил/.test(text(failed.doc, "sourceMsg")),
        failed.doc.getElementById("sourceMsg").className + "|" + text(failed.doc, "sourceMsg"));
    check("кнопка импорта разблокирована после сбоя", failed.doc.getElementById("importMonthBtn").disabled === false);
    check("после сбоя импорта страница не осталась в is-loading",
        !failed.doc.getElementById("importMonthBtn").classList.contains("is-loading"));

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
