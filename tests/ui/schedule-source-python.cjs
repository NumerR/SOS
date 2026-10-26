// Сквозная приёмка источника расписания: страница + сервер + парсер + база.
//
// Зачем отдельный тест, если разборы покрыты юнитами, а страница — офлайновым
// schedule-timetable.cjs: ни одна из тех проверок не проходит весь путь целиком.
// Юниты сверяют разбор с фикстурой, офлайновая приёмка рисует страницу из
// подставного ответа API. Живая связка «дочерний процесс -> строки в базе ->
// разметка, которую видит гость» до этого не проверялась никогда, и именно на
// ней впервые всплыло, что пустой уровень дерева групп источник отдаёт списком.
//
// Сервер запускается с подставным парсером (tests/fixtures/schedule-source.sh):
// проверка не ходит в чужой API и не требует токена вуза, поэтому может стоять
// в CI. Договорённость о токенe при этом проверяется настоящая — заглушка
// отказывается запускаться, если APEKS_TOKEN пуст или уехал в аргументы.
//
// Требования: запущенный сервер с SCHEDULE_SOURCE=python и админ-сидом.
// Запуск вручную: bash tests/schedule-source-python.sh
// Запуск против своего сервера: cd tests/ui && SOS_BASE_URL=... ADMIN_USERNAME=...
//   ADMIN_PASSWORD=... node schedule-source-python.cjs
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const REPO = path.resolve(__dirname, "..", "..");
const BASE = process.env.SOS_BASE_URL || "http://localhost:8080";
const ADMIN_USER = process.env.ADMIN_USERNAME;
const ADMIN_PASS = process.env.ADMIN_PASSWORD;
const GROUP = process.env.SCHEDULE_GROUP || "494";
const MONTH = Number(process.env.SCHEDULE_MONTH || 10);
const YEAR = Number(process.env.SCHEDULE_YEAR || 2026);
const SOURCE_HOST = /apeks\.tksu\.ru|tksu\.ru/i;

const results = [];
function check(name, ok, detail = "") {
    results.push({ name, ok, detail });
    console.log(`${ok ? "PASS" : "FAIL"}  ${name}${detail ? " — " + detail : ""}`);
}
function wait(ms) { return new Promise((r) => setTimeout(r, ms)); }

async function api(pathname, { method = "GET", body, cookie = "", csrf = "" } = {}) {
    const headers = { "Content-Type": "application/json" };
    if (cookie) headers.Cookie = cookie;
    if (csrf && method !== "GET") headers["X-CSRF-Token"] = csrf;
    const res = await fetch(BASE + pathname, {
        method,
        headers,
        body: body ? JSON.stringify(body) : undefined,
    });
    const text = await res.text();
    let data = null;
    try { data = JSON.parse(text); } catch { data = text; }
    return { status: res.status, data, setCookie: res.headers.getSetCookie?.() || [] };
}

async function login(identifier, password) {
    const res = await api("/api/auth/login", { method: "POST", body: { identifier, password } });
    if (res.status !== 200) {
        throw new Error(`вход ${identifier}: ${res.status} ${JSON.stringify(res.data)}`);
    }
    const cookie = res.setCookie.map((c) => c.split(";")[0]).join("; ");
    const csrf = (cookie.match(/csrf_token=([0-9a-f]+)/) || [])[1] || "";
    return { cookie, csrf };
}

// boot рисует /schedule так же, как это делает гость браузера: скрипты страницы
// исполняются в jsdom, а fetch уходит на настоящий сервер. Различия против
// офлайновой приёмки ровно два: ответы не подставные, и куки не передаются —
// маршруты чтения публичные, гость их видит без сессии.
async function bootPage({ seed }) {
    const html = fs.readFileSync(path.join(REPO, "web", "schedule.html"), "utf8");
    const dom = new JSDOM(html, { url: BASE + "/schedule", runScripts: "outside-only", pretendToBeVisual: true });
    const { window } = dom;
    const doc = window.document;

    if (seed) window.localStorage.setItem("sos.schedule.choice", JSON.stringify(seed));

    const calls = [];
    window.fetch = (input, init = {}) => {
        const url = String(input).startsWith("http") ? String(input) : BASE + String(input);
        calls.push(new URL(url).pathname);
        return fetch(url, { method: (init.method || "GET").toUpperCase(), body: init.body });
    };

    const bundle = ["config.js", "core/api-client.js", "core/format.js", "core/layout.js", "common.js", "schedule.js"]
        .map((f) => fs.readFileSync(path.join(REPO, "web/js", f), "utf8"))
        .join("\n;\n");
    window.eval(bundle);
    // DOMContentLoaded диспатчит сам jsdom на следующем тике после разбора
    // (приём из session-expiry.cjs): ручной диспатч запускает страницу дважды,
    // и каждый запрос источника уходит в двух экземплярах.

    return { window, doc, calls };
}

async function until(page, predicate, tries = 60) {
    for (let i = 0; i < tries; i++) {
        if (predicate(page)) return true;
        await wait(20);
    }
    return predicate(page);
}

function text(doc, id) {
    const el = doc.getElementById(id);
    return el ? el.textContent.replace(/\s+/g, " ").trim() : "";
}

(async () => {
    if (!ADMIN_USER || !ADMIN_PASS) {
        throw new Error("нужны ADMIN_USERNAME и ADMIN_PASSWORD — те же, под которыми запущен сервер");
    }

    const admin = await login(ADMIN_USER, ADMIN_PASS);
    const importBody = { group: GROUP, month: MONTH, year: YEAR };

    // ---------- 1. конфигурация источника ----------
    const statusBefore = await api("/api/schedule/source/status", { cookie: admin.cookie });
    check("статус источника отвечает", statusBefore.status === 200, String(statusBefore.status));
    const engine = statusBefore.data.engine;
    check("движок источника объявлен", typeof engine === "string" && engine !== "",
        `engine=${JSON.stringify(engine)}`);
    check("движок — приложенный парсер", engine === "python",
        `получено ${JSON.stringify(engine)}`);
    check("настроенность читается из ответа, а не из кода страницы",
        statusBefore.data.configured === true, `подсказка в ответе: ${JSON.stringify(statusBefore.data.missing_config)}`);

    // ---------- 2. синхронизация каталога ----------
    const groups = await api("/api/schedule/source/groups", {
        method: "POST", body: {}, cookie: admin.cookie, csrf: admin.csrf,
    });
    check("каталог групп импортирован", groups.status === 200 && groups.data.added > 0,
        `${groups.status} ${JSON.stringify(groups.data)}`);

    const catalogue = await api(`/api/schedule/groups`);
    check("каталог доступен гостю", catalogue.status === 200 && catalogue.data.count > 0,
        `${catalogue.status}, групп ${catalogue.data && catalogue.data.count}`);
    check("группа из импорта есть в каталоге",
        (catalogue.data.groups || []).some((g) => g.external_id === GROUP),
        `искали ${GROUP}`);

    // ---------- 3. импорт месяца ----------
    const first = await api("/api/schedule/source/lessons", {
        method: "POST", body: importBody, cookie: admin.cookie, csrf: admin.csrf,
    });
    check("месяц импортирован", first.status === 200 && first.data.lessons > 0,
        `${first.status} ${JSON.stringify(first.data)}`);

    const read = await api(`/api/schedule/lessons?group=${GROUP}&month=${MONTH}&year=${YEAR}`);
    const rows = (read.data.days || []).reduce((n, day) => n + day.lessons.length, 0);
    check("строки читаются публично", read.status === 200 && rows === first.data.lessons,
        `${read.status}, строк ${rows}, импортировано ${first.data.lessons}`);
    check("HTML из аудитории вырезан на импорте",
        (read.data.days || []).every((day) => day.lessons.every((lesson) => !/[<>]/.test(lesson.room))),
        JSON.stringify(((read.data.days || [])[0] || {}).lessons?.[0]?.room));

    // ---------- 4. повторный импорт не плодит копии ----------
    const again = await api("/api/schedule/source/lessons", {
        method: "POST", body: importBody, cookie: admin.cookie, csrf: admin.csrf,
    });
    const after = await api(`/api/schedule/lessons?group=${GROUP}&month=${MONTH}&year=${YEAR}`);
    const rowsAfter = (after.data.days || []).reduce((n, day) => n + day.lessons.length, 0);
    check("повторный импорт заменяет строки, а не удваивает",
        again.status === 200 && rowsAfter === rows, `строк ${rows} -> ${rowsAfter}`);

    // ---------- 5. страница гостя на импортированных данных ----------
    const page = await bootPage({ seed: { group: GROUP, name: GROUP, month: MONTH, year: YEAR } });
    const drawn = await until(page, () => page.doc.querySelectorAll("#timetable .lesson").length > 0);
    const timetableText = text(page.doc, "timetable");
    check("гость видит таймтейбл без входа", drawn,
        `дней ${page.doc.querySelectorAll("#timetable .sched-day").length}, пар ${page.doc.querySelectorAll("#timetable .lesson").length}`);
    check("пар отрисовано ровно столько, сколько в базе",
        page.doc.querySelectorAll("#timetable .lesson").length === rowsAfter,
        `${page.doc.querySelectorAll("#timetable .lesson").length} против ${rowsAfter}`);
    check("подписи источника дошли до страницы",
        /Базы данных/.test(timetableText) && /без ауд\./.test(timetableText),
        `фрагмент: ${JSON.stringify(timetableText.slice(0, 60))}`);
    check("разметка источника не стала элементом DOM",
        page.doc.querySelectorAll("#timetable img, #timetable script").length === 0);
    check("подгруппа показана там, где она есть",
        /подгруппа/.test(timetableText), `фрагмент: ${JSON.stringify(timetableText.slice(0, 60))}`);
    check("каталог заполнил подсказку выбора группы",
        page.doc.querySelectorAll("#groupOptions option").length > 0,
        String(page.doc.querySelectorAll("#groupOptions option").length));
    check("метка «обновлено» показывает импортированную группу",
        /Обновлено/.test(text(page.doc, "updatedNote")), text(page.doc, "updatedNote"));
    check("страница не обращается к источнику напрямую",
        page.calls.every((p) => !SOURCE_HOST.test(p)), page.calls.join(", "));
    check("панель источника спрятана от гостя",
        page.doc.getElementById("sourceManageSection").classList.contains("hidden"));

    // ---------- 6. месяц без пар ----------
    const empty = await bootPage({ seed: { group: GROUP, name: GROUP, month: 12, year: YEAR } });
    const told = await until(empty, () => /пар нет|не импортирован/.test(text(empty.doc, "timetable")));
    check("пустой месяц различим", told, text(empty.doc, "timetable").slice(0, 120));

    const failed = results.filter((r) => !r.ok);
    console.log(`\n${results.length - failed.length}/${results.length} проверок пройдено`);
    if (failed.length) process.exitCode = 1;
})().catch((err) => {
    console.error("ПРОГОН УПАЛ: " + (err && err.stack ? err.stack : err));
    process.exitCode = 1;
});
