// Проверка админ-контура ролей (admin.html + admin.js) в jsdom против живого сервера.
//
// Требования: сервер на :8080 с админ-сидом и применённый ../seed.sh
// (ADMIN_USERNAME / ADMIN_PASSWORD должны совпадать с теми, под которыми запущен сервер).
// Запуск: cd tests/ui && npm i && npm run test:admin
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const REPO = path.resolve(__dirname, "..", "..");
const BASE = process.env.SOS_BASE_URL || "http://localhost:8080";
const ADMIN_USER = process.env.ADMIN_USERNAME;
const ADMIN_PASS = process.env.ADMIN_PASSWORD;

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
    const res = await fetch(BASE + pathname, { method, headers, body: body ? JSON.stringify(body) : undefined });
    const text = await res.text();
    let data = null;
    try { data = JSON.parse(text); } catch { data = text; }
    return { status: res.status, data, setCookie: res.headers.getSetCookie?.() || [] };
}

async function login(identifier, password) {
    const res = await api("/api/auth/login", { method: "POST", body: { identifier, password } });
    if (res.status !== 200) throw new Error("login " + identifier + ": " + res.status + " " + JSON.stringify(res.data));
    const cookie = res.setCookie.map((c) => c.split(";")[0]).join("; ");
    const csrf = (cookie.match(/csrf_token=([0-9a-f]+)/) || [])[1] || "";
    return { cookie, csrf, user: res.data };
}

(async () => {
    if (!ADMIN_USER || !ADMIN_PASS) {
        throw new Error("нужны ADMIN_USERNAME и ADMIN_PASSWORD — те же, что у запущенного сервера");
    }

    const admin = await login(ADMIN_USER, ADMIN_PASS);
    const student = await login("s_stud", "password123");

    const before = await api("/api/admin/users", { cookie: admin.cookie });
    const sstud = before.data.find((u) => u.username === "s_stud");
    if (!sstud) throw new Error("s_stud не найден — сначала выполните ./seed.sh");

    const html = fs.readFileSync(path.join(REPO, "web/admin.html"), "utf8");
    const dom = new JSDOM(html, { url: BASE + "/admin", runScripts: "outside-only", pretendToBeVisual: true });
    const { window } = dom;
    const doc = window.document;

    window.fetch = (input, init = {}) => {
        const url = String(input).startsWith("http") ? String(input) : BASE + String(input);
        const headers = new window.Headers(init.headers || {});
        headers.set("Cookie", admin.cookie);
        return fetch(url, { method: (init.method || "GET").toUpperCase(), headers, body: init.body });
    };
    window.alert = (m) => { window.__alerts = (window.__alerts || []).concat(m); };
    doc.cookie = "csrf_token=" + admin.csrf;

    const bundle = ["config.js", "core/api-client.js", "core/format.js", "common.js", "admin.js"]
        .map((f) => fs.readFileSync(path.join(REPO, "web/js", f), "utf8"))
        .join("\n;\n");
    window.eval(bundle);
    doc.dispatchEvent(new window.Event("DOMContentLoaded", { bubbles: true }));

    for (let i = 0; i < 60 && !doc.querySelector("[data-action='save-role']"); i++) await wait(50);

    const rows = Array.from(doc.querySelectorAll("#usersBody tr"));
    check("таблица пользователей отрисована", rows.length >= 2, rows.length + " строк");

    const selects = doc.querySelectorAll("[data-role-select]");
    const selfButton = doc.querySelector("[data-action='save-role']");
    check("у каждой чужой учётки есть селект роли", selects.length >= 2, selects.length + " селектов");

    const selfRow = rows.find((r) => r.textContent.includes(admin.user.username));
    check("собственную роль нельзя менять в интерфейсе",
        selfRow && !selfRow.querySelector("[data-action='save-role']"),
        selfRow ? selfRow.textContent.replace(/\s+/g, " ").trim().slice(0, 60) : "строка не найдена");
    check("кнопки сохранения найдены", Boolean(selfButton));

    // ---------- журнал действий: доступ и отрисовка (T1.2) ----------
    //
    // Проверки идут до смены роли: дальше сессия студента отзывается, и
    // сравнить 403 с 401 станет невозможно.

    const journal = await api("/api/admin/audit?limit=50", { cookie: admin.cookie });
    check("администратору журнал доступен", journal.status === 200 && Array.isArray(journal.data),
        "код " + journal.status);

    const forbidden = await api("/api/admin/audit", { cookie: student.cookie });
    check("студенту журнал закрыт", forbidden.status === 403,
        "код " + forbidden.status + " " + JSON.stringify(forbidden.data));

    const auditRows = Array.from(doc.querySelectorAll("#auditBody tr"));
    check("таблица журнала отрисована", auditRows.length >= 1, auditRows.length + " строк");
    check("в журнале не осталось заглушки загрузки",
        !doc.getElementById("auditBody").innerHTML.includes("spinner"),
        doc.getElementById("auditBody").textContent.replace(/\s+/g, " ").trim().slice(0, 60));

    const hasRefresh = Boolean(doc.getElementById("refreshAuditBtn"));
    check("кнопка обновления журнала есть", hasRefresh);

    // Меняем роль студента на преподавателя и обратно — через интерфейс.
    const targetRow = rows.find((r) => r.textContent.includes("s_stud"));
    const targetSelect = targetRow.querySelector("[data-role-select]");
    targetSelect.value = "teacher";
    targetRow.querySelector("[data-action='save-role']").click();

    for (let i = 0; i < 60 && !(doc.getElementById("adminMsg").textContent || "").trim(); i++) await wait(50);
    const status = doc.getElementById("adminMsg");
    check("сервер принял изменение роли через UI", /обновлен/i.test(status.textContent), status.textContent.trim());
    check("сообщение предупреждает об отзыве сессий", /сесс/i.test(status.textContent));

    const after = await api("/api/admin/users", { cookie: admin.cookie });
    const promoted = after.data.find((u) => u.username === "s_stud");
    check("роль изменилась на сервере", promoted.role === "teacher", promoted.role);

    const meAfter = await api("/api/auth/me", { cookie: student.cookie });
    check("старая сессия студента отозвана", meAfter.status === 401, "код " + meAfter.status);

    // Возврат: интерфейс должен уметь и понижать роль.
    const reraised = Array.from(doc.querySelectorAll("#usersBody tr")).find((r) => r.textContent.includes("s_stud"));
    reraised.querySelector("[data-role-select]").value = "student";
    reraised.querySelector("[data-action='save-role']").click();
    for (let i = 0; i < 60; i++) {
        const u = (await api("/api/admin/users", { cookie: admin.cookie })).data.find((x) => x.username === "s_stud");
        if (u.role === "student") break;
        await wait(50);
    }
    const restored = (await api("/api/admin/users", { cookie: admin.cookie })).data.find((u) => u.username === "s_stud");
    check("роль возвращена через тот же интерфейс", restored.role === "student", restored.role);

    // ---------- журнал действий: запись сделанного ----------
    //
    // Смена роли выполнена кнопкой на странице, поэтому событие обязано быть
    // видено и в API, и в таблице — иначе администратор не узнает, что
    // действие дошло до сервера.

    const journalAfter = await api("/api/admin/audit?limit=50", { cookie: admin.cookie });
    const roleEvents = (journalAfter.data || []).filter((e) => e.action === "admin.role_change");
    check("повышение записано в журнал",
        roleEvents.some((e) => e.ok === true && /from=student to=teacher/.test(e.detail)),
        roleEvents.map((e) => e.detail).join(" | ").slice(0, 120));
    check("понижение записано в журнал",
        roleEvents.some((e) => e.ok === true && /from=teacher to=student/.test(e.detail)),
        roleEvents.map((e) => e.detail).join(" | ").slice(0, 120));
    check("журнал подписывает автора действия",
        roleEvents.every((e) => e.actor_username === admin.user.username),
        roleEvents.map((e) => e.actor_username).join(", "));
    // Свежие две записи — те, что сделала страница; третья осталась от seed.sh
    // (она про t_teacher), поэтому id сверяем только со свежими.
    check("объект действия — пользователь",
        roleEvents.every((e) => e.target_type === "user") &&
            roleEvents.slice(0, 2).every((e) => String(e.target_id) === String(sstud.id)),
        roleEvents.map((e) => e.target_type + "#" + e.target_id).join(", "));

    const auditText = doc.getElementById("auditBody").textContent.replace(/\s+/g, " ");
    check("в таблице действия по-русски", /смена роли/.test(auditText), auditText.slice(0, 80));
    check("итог показан чипом", /выполнено/.test(auditText));

    if (hasRefresh) {
        doc.getElementById("refreshAuditBtn").click();
        for (let i = 0; i < 60 && doc.getElementById("auditBody").innerHTML.includes("spinner"); i++) await wait(50);
        const reRendered = Array.from(doc.querySelectorAll("#auditBody tr"));
        check("обновление журнала перерисовывает таблицу",
            reRendered.length >= 1 && !doc.getElementById("auditBody").innerHTML.includes("message-error"),
            reRendered.length + " строк");
    }

    // Журнал не должен содержать ничего, чем можно воспользоваться для входа.
    const dump = JSON.stringify(journalAfter.data || {}) + auditText;
    const leaks = ["$2a$", "$2b$", ADMIN_PASS, "password123", admin.csrf].filter((secret) => dump.includes(secret));
    check("в журнале нет паролей, хешей и токенов", leaks.length === 0, leaks.join(", "));

    check("инлайновых onclick в таблице нет",
        doc.getElementById("usersBody").innerHTML.includes("onclick=") === false);
    check("alert не используется", !window.__alerts, JSON.stringify(window.__alerts || []));

    const failed = results.filter((r) => !r.ok);
    console.log(`\nИТОГ: ${results.length - failed.length}/${results.length} проверок пройдено`);
    window.close();
    process.exit(failed.length ? 1 : 0);
})().catch((error) => {
    console.error("СБОЙ ПРОВЕРКИ:", error.message || error);
    process.exit(2);
});
