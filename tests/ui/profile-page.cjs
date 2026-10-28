// Приёмка страницы профиля (остаток T3.1): блок идентичности и перенесённая
// панель доступа на /profile, офлайново.
//
// Зачем отдельный тест: страница новая, и её главный риск — тот же класс, что и
// у модалок в T0.1 и у ленты в T3.1: «Загрузка…» остаётся навсегда, когда API
// ответил не 200. Плюс панель смены пароля переехала с dashboard на profile, и
// нужно закрепить, что на новом месте она по-прежнему обслуживается account.js,
// а инлайновых обработчиков в разметке нет.
//
// Тест полностью офлайновый: fetch подменяется заглушкой, сервер на :8080 не
// нужен и лимитер входов (20 попыток на IP за 10 минут, ADR-0006) не расходуется.
//
// Ограничение harness'а, о котором надо помнить: скрипты исполняются из списка
// BUNDLE, а не из тегов <script>, поэтому поведение панели доказывает работу
// кода, но не то, что страница его подключает. За это отвечают две статические
// проверки ниже — порядок тегов и наличие js/account.js.
//
// Запуск: cd tests/ui && node profile-page.cjs
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const REPO = path.resolve(__dirname, "..", "..");
const BASE = "http://localhost:8080";

const results = [];
function check(name, ok, detail = "") {
    results.push({ name, ok, detail });
    console.log(`${ok ? "PASS" : "FAIL"}  ${name}${detail ? " — " + detail : ""}`);
}
function wait(ms) { return new Promise((r) => setTimeout(r, ms)); }

// Как в schedule-forms.cjs: async-слушатель не бросает в dispatchEvent, а
// возвращает отклонённый промис; без сбора unhandledRejection Node роняет
// процесс вместо того, чтобы показать ИТОГ.
const rejections = [];
process.on("unhandledRejection", (reason) => {
    rejections.push(String((reason && reason.message) || reason));
});

const BUNDLE = ["config.js", "core/api-client.js", "core/format.js", "core/layout.js", "common.js", "account.js", "profile.js"];

function json(body, status = 200) {
    return new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
    });
}

const DEFAULT_USER = { id: 3, username: "s_stud", email: "s1@example.com", full_name: "Sidorov S. S.", role: "student" };

// Загружаем /profile с заданным ответом на GET /api/auth/me.
// me — функция (record) => Response | готовое тело, либо само тело/Response.
function boot({ me = DEFAULT_USER, fail = null } = {}) {
    const html = fs.readFileSync(path.join(REPO, "web/profile.html"), "utf8");
    const dom = new JSDOM(html, { url: BASE + "/profile", runScripts: "outside-only", pretendToBeVisual: true });
    const { window } = dom;

    const calls = [];
    window.fetch = (input, init = {}) => {
        const method = String(init.method || "GET").toUpperCase();
        const pathname = String(input).replace(BASE, "").split("?")[0];
        const record = { key: method + " " + pathname, method, pathname, body: init.body, headers: init.headers };
        calls.push(record);

        if (fail) return Promise.reject(fail instanceof Error ? fail : new TypeError("Failed to fetch"));

        const decided = typeof me === "function" ? me(record) : me;
        // Объект — «счастливый» ответ теста, Response — то, что тест заготовил
        // сам (в том числе 401/500). Разница видна здесь и только здесь.
        if (decided instanceof Response) return Promise.resolve(decided);
        return Promise.resolve(json(decided === undefined ? DEFAULT_USER : decided));
    };

    window.document.cookie = "csrf_token=test-csrf-token";

    const source = BUNDLE.map((file) => fs.readFileSync(path.join(REPO, "web/js", file), "utf8")).join("\n;\n");
    window.eval(source);

    const navigations = [];
    window.SOS_CONFIG.navigateTo = (target) => navigations.push(target);

    const doc = window.document;
    return { window, doc, calls, navigations };
}

function cells(doc) {
    return {
        name: doc.getElementById("profileName").textContent.trim(),
        username: doc.getElementById("profileUsername").textContent.trim(),
        email: doc.getElementById("profileEmail").textContent.trim(),
        role: doc.getElementById("profileRole").textContent.trim(),
        roleClass: doc.getElementById("profileRole").className,
        statusClass: doc.getElementById("profileStatus").className,
        statusText: doc.getElementById("profileStatus").textContent.trim(),
    };
}

// Ждём, пока страница не перестанет показывать «Загрузка…» или не выдаст
// сообщение — иначе ассерты зависели бы от скорости таймера.
async function settled(page, limit = 160) {
    for (let i = 0; i < limit; i++) {
        const current = cells(page.doc);
        if (current.username !== "Загрузка…" || current.statusText) return current;
        await wait(10);
    }
    return cells(page.doc);
}

(async () => {
    // --- 1. Штатный ответ: поля заполнены, шапка тоже, запрос ровно один. ---
    const ok = boot();
    const shown = await settled(ok);
    check("логин из ответа API", shown.username === "s_stud", shown.username);
    check("имя из ответа API", shown.name === "Sidorov S. S.", shown.name);
    check("почта из ответа API", shown.email === "s1@example.com", shown.email);
    check("роль подписана по-русски", shown.role === "студент", shown.role);
    check("чип роли использует класс из format.js", shown.roleClass === "chip chip-muted", shown.roleClass);
    check("чтение профиля — ровно один запрос", ok.calls.length === 1 && ok.calls[0].key === "GET /api/auth/me", JSON.stringify(ok.calls.map((c) => c.key)));
    check("имя и инициалы попали в шапку", ok.doc.getElementById("userName").textContent === "Sidorov S. S."
        && ok.doc.getElementById("userAvatar").textContent === "SS"
        && ok.doc.getElementById("userRole").textContent === "студент",
    [ok.doc.getElementById("userName").textContent, ok.doc.getElementById("userAvatar").textContent, ok.doc.getElementById("userRole").textContent].join(" / "));
    check("сообщение об ошибке скрыто", /hidden/.test(shown.statusClass), shown.statusClass);

    // --- 2. Пустое имя: подставляем логин, а не оставляем пустую ячейку. ---
    const noName = boot({ me: { id: 4, username: "t_teacher", email: "", full_name: "", role: "teacher" } });
    const noNameCells = await settled(noName);
    check("без имени показан логин", noNameCells.name === "t_teacher", noNameCells.name);
    check("без почты — прочерк, а не пустота", noNameCells.email === "—", noNameCells.email);
    check("роль преподавателя подписана", noNameCells.role === "преподаватель", noNameCells.role);
    check("чип преподавателя отличается от студенческого", noNameCells.roleClass === "chip chip-outline", noNameCells.roleClass);

    // --- 3. 500: «Загрузка…» не остаётся, текст ошибки виден. ---
    const broken = boot({ me: () => json({ error: { code: "internal", message: "server exploded" } }, 500) });
    const brokenCells = await settled(broken);
    check("при 500 надписи «Загрузка…» нет", brokenCells.username === "—" && !/Загрузка/.test(broken.doc.getElementById("profileIdentity").textContent), brokenCells.username);
    check("при 500 показан текст ошибки", Boolean(brokenCells.statusText) && /error/.test(brokenCells.statusClass), brokenCells.statusClass + " · " + brokenCells.statusText);
    check("при 500 редиректа нет", broken.navigations.length === 0, JSON.stringify(broken.navigations));

    // --- 4. Сетевой сбой (сервер не отвечает) — тот же путь. ---
    const offline = boot({ fail: new TypeError("Failed to fetch") });
    const offlineCells = await settled(offline);
    check("при сетевом сбое таблица не «загружается»", offlineCells.username === "—", offlineCells.username);
    check("при сетевом сбое есть сообщение", /Нет связи/.test(offlineCells.statusText), offlineCells.statusText);

    // --- 5. 401 без сессии: уводим на вход ровно один раз. ---
    const expired = boot({ me: () => json({ error: { code: "unauthorized", message: "unauthorized" } }, 401) });
    await settled(expired);
    check("при 401 навигация ровно одна", expired.navigations.length === 1, JSON.stringify(expired.navigations));
    check("при 401 уходим на вход", expired.navigations[0] === "login", String(expired.navigations[0]));

    // --- 6. Мусорный ответ (нет username): понятная ошибка, а не пустая таблица. ---
    const garbage = boot({ me: () => json({ ok: true }) });
    const garbageCells = await settled(garbage);
    check("при неожиданном ответе поля не пусты", garbageCells.username === "—", garbageCells.username);
    check("при неожиданном ответе есть сообщение", /обновите страницу/i.test(garbageCells.statusText), garbageCells.statusText);

    // --- 7. Панель доступа на новом месте работает: клиентские проверки до запроса. ---
    const panel = boot();
    await settled(panel);
    const form = panel.doc.getElementById("passwordForm");
    const message = panel.doc.getElementById("passwordMessage");
    check("форма смены пароля на месте", Boolean(form) && Boolean(panel.doc.getElementById("logoutAllBtn")));
    check("инлайновых обработчиков в панели нет", !form.innerHTML.includes("onclick=") && !panel.doc.body.innerHTML.includes("onclick="));

    const beforeMismatch = panel.calls.length;
    form.querySelector('[name="current_password"]').value = "seed-pass";
    form.querySelector('[name="new_password"]').value = "new-pass-1234";
    form.querySelector('[name="repeat_password"]').value = "other-pass-1234";
    form.dispatchEvent(new panel.window.Event("submit", { bubbles: true, cancelable: true }));
    await wait(120);
    check("несовпадение повтора отклонено на клиенте", /не совпада/.test(message.textContent), message.textContent.trim());
    check("на клиентском отказе запроса не было", panel.calls.length === beforeMismatch, panel.calls.length + " запросов");

    const beforeShort = panel.calls.length;
    form.querySelector('[name="new_password"]').value = "short";
    form.querySelector('[name="repeat_password"]').value = "short";
    form.dispatchEvent(new panel.window.Event("submit", { bubbles: true, cancelable: true }));
    await wait(120);
    check("короткий пароль отклонён на клиенте", /8 символов/.test(message.textContent), message.textContent.trim());
    check("слабый пароль не ушёл на сервер", panel.calls.length === beforeShort, panel.calls.length + " запросов");

    // Кнопка «выйти везде» подтверждает первым кликом, а не выходит сразу.
    const beforeLogout = panel.calls.length;
    panel.doc.getElementById("logoutAllBtn").click();
    await wait(60);
    check("первый клик просит подтверждение", /точно/i.test(panel.doc.getElementById("logoutAllBtn").textContent), panel.doc.getElementById("logoutAllBtn").textContent.trim());
    check("первый клик не отправил запрос", panel.calls.length === beforeLogout, panel.calls.length + " запросов");

    // --- 8. Порядок загрузки и навигация страницы. ---
    // В адресах статики есть ?v=дата (ADR-0037): порядок и состав проверяем по
    // путям, а не по полной строке src — версия меняется при каждой правке.
    const scriptSrcs = [...panel.doc.querySelectorAll("script")].map((s) => s.getAttribute("src").split("?")[0]);
    check("layout.js раньше common.js и profile.js", scriptSrcs.indexOf("js/core/layout.js") < scriptSrcs.indexOf("js/common.js")
        && scriptSrcs.indexOf("js/common.js") < scriptSrcs.indexOf("js/profile.js"), JSON.stringify(scriptSrcs));
    check("account.js подключён на странице профиля", scriptSrcs.includes("js/account.js"), JSON.stringify(scriptSrcs));
    check("ссылки навигации относительные", [...panel.doc.querySelectorAll(".top-actions a")].every((a) => !a.getAttribute("href").startsWith("/")),
    JSON.stringify([...panel.doc.querySelectorAll(".top-actions a")].map((a) => a.getAttribute("href"))));
    check("в шапке есть ссылка на кабинет", Boolean(panel.doc.querySelector('.top-actions a[href="dashboard"]')));
    check("подвал отрисован атрибутом страницы", /Account · sessions · password/.test(panel.doc.querySelector(".footer-meta").textContent), panel.doc.querySelector(".footer-meta").textContent);

    check("обработчики не падали с необработанным исключением", rejections.length === 0, rejections.join(" | "));

    const failed = results.filter((r) => !r.ok);
    console.log(`\nИТОГ: ${results.length - failed.length}/${results.length} проверок пройдено`);
    process.exit(failed.length ? 1 : 0);
})().catch((error) => {
    console.error("СБОЙ ПРОВЕРКИ:", error.stack || error.message || error);
    process.exit(2);
});
