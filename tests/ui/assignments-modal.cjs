// Проверка UI заданий (modals.js) в jsdom против запущенного Go-сервера.
// Те же web/js/*.js, что и в браузере, но без вмешательства в сессию пользователя.
//
// Требования: сервер на :8080 и применённый сид (пользователи t_teacher / s_stud
// с паролем password123, курс #1 с заданием, в котором есть сдача).
// Запуск: cd tests/ui && npm i && npm test
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
    if (res.status !== 200) throw new Error("login failed: " + res.status + " " + JSON.stringify(res.data));
    // CSRF-токен сервер выдаёт вместе с сессией, отдельный GET не нужен.
    let cookie = res.setCookie.map((c) => c.split(";")[0]).join("; ");
    const csrf = (cookie.match(/csrf_token=([0-9a-f]+)/) || [])[1] || "";
    if (!csrf) throw new Error("сервер не выдал csrf_token вместе с сессией");
    return { cookie, csrf, user: res.data };
}

function wait(ms) { return new Promise((r) => setTimeout(r, ms)); }

async function openDashboard(account) {
    const html = fs.readFileSync(path.join(REPO, "web/dashboard.html"), "utf8");
    const dom = new JSDOM(html, {
        url: BASE + "/dashboard",
        runScripts: "outside-only",
        pretendToBeVisual: true,
    });
    const { window } = dom;

    window.fetch = (input, init = {}) => {
        const url = String(input).startsWith("http") ? String(input) : BASE + String(input);
        const headers = new window.Headers(init.headers || {});
        headers.set("Cookie", account.cookie);
        const method = (init.method || "GET").toUpperCase();
        if (method !== "GET" && account.csrf) headers.set("X-CSRF-Token", account.csrf);
        return fetch(url, { method, headers, body: init.body });
    };
    // alert в тестовой среде не должен молча съедать ошибки UI
    window.alert = (message) => { window.__alerts = (window.__alerts || []).concat(message); };
    window.confirm = () => true;

    window.document.cookie = "csrf_token=" + account.csrf;

    // Один eval на все скрипты: в браузере классические <script> делят global lexical
    // environment, а window.eval() в jsdom создаёт отдельную область на каждый вызов.
    const bundle = ["config.js", "core/api-client.js", "core/format.js", "core/layout.js", "common.js", "modals.js", "dashboard.js"]
        .map((file) => fs.readFileSync(path.join(REPO, "web/js", file), "utf8"))
        .join("\n;\n");
    window.eval(bundle);
    window.document.dispatchEvent(new window.Event("DOMContentLoaded", { bubbles: true }));

    // ждём, пока дашборд отрисует карточки с действиями
    for (let i = 0; i < 60; i++) {
        if (window.document.querySelector("[data-action]")) break;
        await wait(50);
    }
    return { dom, window, doc: window.document };
}

const DANGEROUS = `HW 'tests' <img src=x onerror=alert(1)>`;

(async () => {
    const student = await login("s_stud", "password123");
    const teacher = await login("t_teacher", "password123");
    console.log("вход: студент id=" + student.user.id + ", преподаватель id=" + teacher.user.id);

    // ---------- студент: список заданий ----------
    {
        const { window, doc } = await openDashboard(student);
        const button = doc.querySelector('[data-action="view-assignments-student"]');
        check("карточка курса показывает кнопку «Задания»", Boolean(button));
        button.click();
        await wait(400);

        const overlay = doc.getElementById("modalContainer");
        check("модалка открыта (нет .hidden, есть .active)",
            overlay && !overlay.classList.contains("hidden") && overlay.classList.contains("active"));

        const body = doc.getElementById("modalBody");
        check("название задания показано как текст", body.textContent.includes("HW 'tests'"),
            JSON.stringify(body.textContent.trim().slice(0, 60)));
        check("XSS не исполняется: в модалке нет элемента <img>", body.querySelector("img") === null);
        check("в разметке модалки нет инлайновых onclick",
            body.innerHTML.includes("onclick=") === false);

        const dialog = doc.getElementById("modalDialog");
        check("диалог размечен для скринридера",
            dialog.getAttribute("role") === "dialog" && dialog.getAttribute("aria-modal") === "true");
        check("фокус перешёл внутрь диалога", dialog.contains(doc.activeElement),
            doc.activeElement && doc.activeElement.tagName);

        // сдача работы
        const submitBtn = body.querySelector('[data-modal-action="open-submit"]');
        check("в списке есть кнопка «Сдать работу»", Boolean(submitBtn));
        submitBtn.click();
        await wait(250);

        const form = doc.querySelector('form[data-modal-form="submit-work"]');
        check("форма сдачи открылась", Boolean(form));
        form.querySelector('textarea[name="content"]').value = "https://example.com/repo Решение";
        form.dispatchEvent(new window.Event("submit", { bubbles: true, cancelable: true }));
        await wait(900);

        const status = doc.getElementById("modalStatus");
        check("сдача принята сервером", status && /отправлена/i.test(status.textContent), status && status.textContent);

        // Escape закрывает окно
        doc.dispatchEvent(new window.KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
        await wait(350);
        check("Escape закрывает модалку", doc.getElementById("modalContainer").classList.contains("hidden"));

        window.close();
    }

    // ---------- преподаватель: сдачи и оценка ----------
    {
        const { window, doc } = await openDashboard(teacher);
        const manage = doc.querySelector('[data-action="manage-assignments"]');
        check("преподавателю доступна кнопка «Управление заданиями»", Boolean(manage));
        manage.click();
        await wait(400);

        const createBtn = doc.querySelector('#modalBody [data-modal-action="open-create"]');
        check("в списке заданий есть кнопка «+ Новое задание»", Boolean(createBtn));

        // создаём задание через форму
        createBtn.click();
        await wait(250);
        const cform = doc.querySelector('form[data-modal-form="create-assignment"]');
        check("форма создания задания открылась", Boolean(cform));
        const runTag = "HW run " + Date.now().toString().slice(-6);
        cform.querySelector('input[name="title"]').value = runTag;
        cform.querySelector('textarea[name="description"]').value = "Вторая работа";
        cform.dispatchEvent(new window.Event("submit", { bubbles: true, cancelable: true }));
        await wait(1400);
        const list = doc.getElementById("modalBody");
        check("новое задание появилось в списке (список пересобрался)", list.textContent.includes(runTag),
            list.textContent.replace(/\s+/g, " ").trim().slice(0, 70));

        // открыть сдачи задания #1: сервер отдаёт список по created DESC, поэтому берём по id
        const checkBtn = doc.querySelector('#modalBody [data-modal-action="open-submissions"][data-assignment-id="1"]');
        check("есть кнопка «Проверить сдачи»", Boolean(checkBtn));
        checkBtn.click();
        await wait(400);

        const row = doc.querySelector(".submissions-table tbody tr");
        check("сдача студента видна преподавателю", Boolean(row), row ? row.textContent.trim().slice(0, 40) : "");

        const scoreInput = row.querySelector("[data-score-for]");
        scoreInput.value = "4.5";
        row.querySelector("[data-comment-for]").value = "Хорошо";
        row.querySelector('[data-modal-action="save-grade"]').click();
        await wait(700);

        const badge = row.querySelector(".grade-badge");
        check("оценка отразилась в интерфейсе", badge && badge.textContent.trim() === "4.5", badge && badge.textContent);
        check("оценка сохранилась на сервере",
            (await api("/api/submissions?assignment_id=1", { cookie: teacher.cookie })).data[0].score === 4.5);
        check("alert не использован (заменён на inline-сообщение)", !window.__alerts, JSON.stringify(window.__alerts || []));

        window.close();
    }

    const failed = results.filter((r) => !r.ok);
    console.log(`\nИТОГ: ${results.length - failed.length}/${results.length} проверок пройдено`);
    process.exit(failed.length ? 1 : 0);
})().catch((error) => {
    console.error("СБОЙ ПРОВЕРКИ:", error);
    process.exit(2);
});
