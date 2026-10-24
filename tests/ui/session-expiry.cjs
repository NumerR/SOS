// Приёмка T3.1: «при истечении сессии фронтенд не показывает вечный spinner, а
// уводит на вход».
//
// Проверяет политику 401 из core/api-client.js на живом сервере: сессия
// действительно отзывается (POST /api/auth/logout), а не подделывается моком —
// иначе тест проверял бы собственную фантазию.
//
// Навигацию наблюдаем через SOS_CONFIG.navigateTo: в jsdom переход между
// документами не реализован, и без хука мы бы видели только предупреждение
// «Not implemented: navigation».
//
// Требования: сервер на :8080 и выполненный ../seed.sh (студент s_stud / password123).
// Запуск: cd tests/ui && SOS_BASE_URL=http://localhost:8080 node session-expiry.cjs
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const REPO = path.resolve(__dirname, "..", "..");
const BASE = process.env.SOS_BASE_URL || "http://localhost:8080";

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

function cookieOf(setCookieHeaders) {
    return setCookieHeaders.map((c) => c.split(";")[0]).join("; ");
}
function csrfOf(cookieHeader) {
    return (cookieHeader.match(/csrf_token=([0-9a-f]+)/) || [])[1] || "";
}

async function login(identifier, password) {
    const res = await api("/api/auth/login", { method: "POST", body: { identifier, password } });
    if (res.status !== 200) return null;
    const cookie = cookieOf(res.setCookie);
    return { cookie, csrf: csrfOf(cookie) };
}

const BUNDLE = ["config.js", "core/api-client.js", "core/format.js", "common.js"];

// Собирает страницу в jsdom: подключаем бандл + скрипт страницы, ставим хук
// навигации и fetch с cookie указанной сессии.
function page(htmlFile, scripts, { url, cookie, csrf }) {
    const html = fs.readFileSync(path.join(REPO, "web", htmlFile), "utf8");
    const dom = new JSDOM(html, { url: BASE + url, runScripts: "outside-only", pretendToBeVisual: true });
    const { window } = dom;

    window.fetch = (input, init = {}) => {
        const target = String(input).startsWith("http") ? String(input) : BASE + String(input);
        const headers = new window.Headers(init.headers || {});
        headers.set("Cookie", cookie);
        return fetch(target, {
            method: (init.method || "GET").toUpperCase(),
            headers,
            body: init.body,
        });
    };
    window.alert = (m) => { window.__alerts = (window.__alerts || []).concat(m); };
    window.document.cookie = "csrf_token=" + csrf;

    const source = BUNDLE.concat(scripts)
        .map((f) => fs.readFileSync(path.join(REPO, "web/js", f), "utf8"))
        .join("\n;\n");
    window.eval(source);

    const navigations = [];
    window.SOS_CONFIG.navigateTo = (target) => navigations.push(target);

    return { window, doc: window.document, navigations };
}

(async () => {
    const dead = await login("s_stud", "password123");
    if (!dead) throw new Error("s_stud не входит — выполните ./seed.sh");

    // Отзывываем сессию по-настоящему: тест должен ловить именно 401 от сервера.
    const logout = await api("/api/auth/logout", {
        method: "POST",
        cookie: dead.cookie,
        csrf: dead.csrf,
    });
    check("сессия отозвана на сервере", logout.status === 200, "код " + logout.status);

    const probe = await api("/api/auth/me", { cookie: dead.cookie });
    check("отозванная сессия получает 401", probe.status === 401, "код " + probe.status);

    // --- Личный кабинет: 401 уводит на вход, текст ошибки не остаётся. ---
    // DOMContentLoaded не диспатчим руками: jsdom сам стреляет им на следующем
    // тике после разбора (проверено), а ручной диспатч запускает обработчик
    // страницы дважды и ломает счётчик навигаций ниже.
    const dash = page("dashboard.html", ["modals.js", "dashboard.js", "account.js"], {
        url: "/dashboard",
        cookie: dead.cookie,
        csrf: dead.csrf,
    });
    for (let i = 0; i < 60 && dash.navigations.length === 0; i++) await wait(50);

    check(
        "истёкшая сессия уводит на вход, а не крутит spinner",
        dash.navigations.includes("login"),
        JSON.stringify(dash.navigations),
    );
    check("навигация ровно одна, без петли", dash.navigations.length === 1, JSON.stringify(dash.navigations));

    const courses = dash.doc.getElementById("courses");
    const leftover = (courses ? courses.textContent : "").trim();
    check(
        "placeholder со spinner заменён на явное сообщение",
        /Сессия истекла/.test(leftover) && !courses.querySelector(".spinner"),
        JSON.stringify(leftover.slice(0, 80)),
    );
    check("текста про 401 или «не удалось» там нет", !/401|unauthorized|Не удалось/i.test(leftover),
        JSON.stringify(leftover.slice(0, 80)));
    check("alert для истёкшей сессии не использовался", !dash.window.__alerts, JSON.stringify(dash.window.__alerts || []));

    // --- Вход: 401 здесь штатный ответ, редирект устроил бы петлю. ---
    const loginPage = page("login.html", ["auth.js"], {
        url: "/login",
        cookie: dead.cookie,
        csrf: dead.csrf,
    });
    await wait(500); // ждём DOMContentLoaded от jsdom (см. комментарий выше)

    check("на странице входа нет навигации", loginPage.navigations.length === 0, JSON.stringify(loginPage.navigations));

    // Тот же запрос с политикой по умолчанию, уже на /login: защита от цикла
    // /login -> /login должна сработать и здесь.
    loginPage.window.eval(
        "window.__probe='pending';GS.fetchJSON('/api/auth/me').then(() => { window.__probe='ok'; }, (e) => { window.__probe='err:'+e.status; });",
    );
    for (let i = 0; i < 60 && loginPage.window.__probe === "pending"; i++) await wait(50);
    check("401 на странице входа не редиректит",
        loginPage.window.__probe === "err:401" && loginPage.navigations.length === 0,
        loginPage.window.__probe + " навигации: " + JSON.stringify(loginPage.navigations));

    // --- Гость на расписании: публичные эндпоинты не должны вызывать навигацию. ---
    const guest = page("schedule.html", ["schedule.js"], { url: "/schedule", cookie: "", csrf: "" });
    await wait(500); // тот же механизм: событие стреляет само
    check(
        "гостевой режим расписания не уводит на вход",
        guest.navigations.length === 0,
        JSON.stringify(guest.navigations),
    );

    // --- Куда ведут относительные пути: под каталогом Pages они обязаны
    // разрешиться в страницы этого каталога, а не в корень домена. ---
    const fresh = await login("s_stud", "password123");
    check("seed-ная сессия восстановима для следующих прогонов", Boolean(fresh));

    const paths = await loginPage.window.eval(
        "JSON.stringify([GS.api.loginPath(), GS.api.dashboardPath(), GS.api.homePath(), GS.api.sessionRevokedPath()])",
    );
    const [loginPath, dashboardPath, homePath, revokedPath] = JSON.parse(paths);
    check("пути страниц относительные (работают под каталогом Pages)",
        loginPath === "login" && dashboardPath === "dashboard" && revokedPath === "login?revoked=1", paths);
    check("главная задаётся через './', а не '/index.html'", homePath === "./", homePath);

    // Относительный путь из /login действительно ведёт в /login, а не в /SOS/login.
    const resolved = new URL(dashboardPath, BASE + "/login").pathname;
    check("разрешение относительного пути даёт страницу кабинета", resolved === "/dashboard", resolved);

    const failed = results.filter((r) => !r.ok);
    console.log(`\nИТОГ: ${results.length - failed.length}/${results.length} проверок пройдено`);
    for (const w of [dash, loginPage, guest]) w.window.close();
    process.exit(failed.length ? 1 : 0);
})().catch((error) => {
    console.error("СБОЙ ПРОВЕРКИ:", error.message || error);
    process.exit(2);
});
