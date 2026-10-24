// Приёмка T0.5: доступность структуры страниц и модального окна.
//
// Зачем отдельный тест: правки T0.5 невидимы на скриншоте. У страницы, у
// которой нет <h1>, нет и имени для ассистивных технологий — видно это только
// запросом к DOM; `<div class="top-actions">` оставался бы вёрсткой, если бы не
// стал `<nav>`; skip-link, спрятанный до фокуса, на картинке не появляется
// вовсе. Всё это ловится ровно одним способом — запросом к DOM, и ровно так же
// молча теряется, если проверку не оставить в репозитории.
//
// Часть проверок (lang, один h1, разрыв уровня заголовков, skip-link, scope у
// th, контраст токенов) дублируется в scripts/check-frontend.mjs — там, где они
// не требуют ни сервера, ни jsdom. Здесь то, что статикой не проверяется:
// ориентир «Навигация» рисует core/layout.js, а ловушку фокуса держит modals.js
// в рантайме.
//
// Тест полностью офлайновый: fetch подменяется заглушкой, сервер на :8080 не
// нужен, лимитер входов (20 попыток на IP за 10 минут, ADR-0006) не расходуется.
//
// Запуск: cd tests/ui && node accessibility.cjs
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

// Как в profile-page.cjs: слушатель может вернуть отклонённый промис, и без
// сбора unhandledRejection Node роняет процесс вместо ИТОГА.
const rejections = [];
process.on("unhandledRejection", (reason) => {
    rejections.push(String((reason && reason.message) || reason));
});

// Заглушка транспорта: страницам хватает 401 на любой запрос — с ним common.js
// не уходит в бесконечный spinner и не пытается читать сеть.
function unauthorized() {
    return new Response(JSON.stringify({ error: { code: "unauthorized", message: "unauthorized", fields: {} } }), {
        status: 401,
        headers: { "Content-Type": "application/json" },
    });
}

// Транспортный минимум: то, что нужно core/layout.js, чтобы отрисовать шапку.
// common.js и скрипты страниц здесь не нужны — навигация от них не зависит.
const LAYOUT_BUNDLE = ["config.js", "core/api-client.js", "core/format.js", "core/layout.js"];

// Открывает страницу, исполняет бандл и возвращает документ. `extra` — код,
// который исполняется в том же scope, что и бандл: ModalManager объявлен
// константой на уровне файла и наружу (в window) не виден, а тесту до него надо
// добраться. Так же, как в assignments-modal.cjs, это тестовый костыль, а не
// часть приложения.
function boot(page, scripts = LAYOUT_BUNDLE, extra = "") {
    const html = fs.readFileSync(path.join(REPO, "web", page), "utf8");
    const dom = new JSDOM(html, {
        url: BASE + "/" + page.replace(".html", ""),
        runScripts: "outside-only",
        pretendToBeVisual: true,
    });
    const { window } = dom;

    window.fetch = () => Promise.resolve(unauthorized());
    window.document.cookie = "csrf_token=test-csrf-token";

    const source = scripts
        .map((file) => fs.readFileSync(path.join(REPO, "web/js", file), "utf8"))
        .join("\n;\n");
    window.eval(source + (extra ? "\n;" + extra : ""));

    return { window, doc: window.document };
}

// ---------- 1. структура каждой страницы ----------

const PAGES = [
    "index.html", "login.html", "register.html", "dashboard.html",
    "schedule.html", "profile.html", "admin.html", "404.html",
];

const windows = [];

function track(page, scripts, extra) {
    const opened = boot(page, scripts, extra);
    windows.push(opened.window);
    return opened;
}

for (const page of PAGES) {
    // 404.html скриптов не подключает вовсе: страница раздаётся как статичный
    // ответ, и запускать на ней layout.js в тесте означало бы проверять разметку,
    // которой в браузере не бывает (он затёр бы статическую шапку).
    const { doc } = track(page, page === "404.html" ? [] : LAYOUT_BUNDLE);

    const h1 = doc.querySelectorAll("h1");
    check(`${page}: один <h1>`, h1.length === 1, `найдено ${h1.length}`);
    check(`${page}: <h1> не пустой`, h1.length === 1 && h1[0].textContent.trim().length > 3,
        h1.length ? JSON.stringify(h1[0].textContent.trim()) : "");

    const skip = doc.querySelector("a.skip-link");
    const firstAnchor = doc.querySelector("a");
    check(`${page}: skip-link есть и это первая ссылка`, Boolean(skip) && skip === firstAnchor,
        firstAnchor ? firstAnchor.getAttribute("href") : "ссылок нет вовсе");

    const target = skip ? skip.getAttribute("href").slice(1) : "";
    const targetEl = target ? doc.getElementById(target) : null;
    check(`${page}: цель skip-link #${target || "?"} существует`, Boolean(targetEl), "");
    check(`${page}: цель skip-link фокусируемая (tabindex="-1")`,
        Boolean(targetEl) && targetEl.getAttribute("tabindex") === "-1",
        targetEl ? `tag=${targetEl.tagName.toLowerCase()}` : "");

    // banner и main обязательны: без них страницу нельзя ни начать с
    // содержимого, ни обойти. Футер — оформление, и на страницах входа его нет
    // намеренно, поэтому он в детали, а не в условие.
    const present = ["header", "main", "footer"].filter((tag) => doc.querySelector(tag));
    check(`${page}: ориентир содержимого и шапка на месте`,
        present.includes("header") && present.includes("main"), present.join(","));

    // Порядок заголовков: h1 → h3 без h2 ломает обход по структуре.
    const levels = Array.from(doc.querySelectorAll("h1,h2,h3,h4,h5,h6"))
        .map((node) => Number(node.tagName.slice(1)));
    const jumps = [];
    for (let i = 1; i < levels.length; i += 1) {
        if (levels[i] - levels[i - 1] > 1) jumps.push(`h${levels[i - 1]}→h${levels[i]}`);
    }
    check(`${page}: уровни заголовков без разрывов`, jumps.length === 0, `${levels.join(",")} ${jumps.join(" ")}`);

    const ths = Array.from(doc.querySelectorAll("th"));
    check(`${page}: у всех <th> стоит scope`, ths.every((th) => th.hasAttribute("scope")),
        `${ths.filter((th) => !th.hasAttribute("scope")).length} из ${ths.length} без scope`);
}

// ---------- 2. ориентир «Навигация» рисует core/layout.js ----------

// Классы обёрток различаются (nav.nav у гостевых страниц, nav.top-actions у
// рабочих), тег с T0.5 один — <nav>. Именно он раньше был <div>, и на
// «Кабинете», «Расписании», «Профиле» и в админке ориентира не было вовсе.
const LAYOUT_PAGES = ["index", "login", "register", "schedule", "dashboard", "profile", "admin"];

for (const layout of LAYOUT_PAGES) {
    const { doc } = track(`${layout}.html`);
    const nav = doc.querySelector("header nav");
    check(`${layout}: шапка содержит <nav>`, Boolean(nav), nav ? `${nav.tagName.toLowerCase()}.${nav.className}` : "нет nav");
    check(`${layout}: у <nav> есть доступное имя`, Boolean(nav) && nav.hasAttribute("aria-label"),
        nav ? JSON.stringify(nav.getAttribute("aria-label") || "") : "");

    const links = nav ? Array.from(nav.querySelectorAll("a")) : [];
    check(`${layout}: ссылки навигации внутри <nav>, а не снаружи`,
        links.length > 0 && links.every((a) => a.closest("nav") === nav), `ссылок ${links.length}`);

    const avatar = doc.getElementById("userAvatar");
    if (avatar) {
        check(`${layout}: аватар скрыт от чтения (инициалы дублируют имя)`,
            avatar.getAttribute("aria-hidden") === "true", avatar.outerHTML.slice(0, 60));
    }
}

// 404 собирается без layout.js: шапка там статическая и ориентира «Навигация» в
// ней нет — проверяем, что отсутствие landmark'а не маскирует пустой <nav>.
{
    const { doc } = track("404.html", []);
    check("404: нет пустого <nav> (шапка статическая)", doc.querySelectorAll("header nav").length === 0,
        `nav найдено ${doc.querySelectorAll("header nav").length}`);
    check("404: есть ссылки, по которым можно уйти со страницы",
        doc.querySelectorAll("main a").length >= 3,
        Array.from(doc.querySelectorAll("main a")).map((a) => a.getAttribute("href")).join(" "));
}

// ---------- 3. модальное окно: ловушка фокуса и возврат ----------

async function modalChecks() {
    const { window, doc } = track(
        "dashboard.html",
        LAYOUT_BUNDLE.concat(["common.js", "modals.js"]),
        "window.__Modal = ModalManager;",
    );

    const dialog = doc.getElementById("modalDialog");
    check("модалка: role=dialog", dialog.getAttribute("role") === "dialog", dialog.getAttribute("role"));
    check("модалка: aria-modal=true", dialog.getAttribute("aria-modal") === "true", dialog.getAttribute("aria-modal"));

    const labelId = dialog.getAttribute("aria-labelledby");
    const labelEl = doc.getElementById(labelId);
    check("модалка: aria-labelledby указывает на существующий заголовок",
        Boolean(labelEl) && labelEl.textContent.trim().length > 0,
        `${labelId} → ${labelEl ? labelEl.tagName.toLowerCase() + " " + JSON.stringify(labelEl.textContent.trim()) : "нет элемента"}`);
    check("модалка: у кнопки закрытия есть доступное имя",
        doc.getElementById("modalCloseBtn").hasAttribute("aria-label"),
        doc.getElementById("modalCloseBtn").getAttribute("aria-label") || "");

    // Открываем из реальной кнопки страницы, чтобы было что возвращать.
    const opener = doc.getElementById("refreshBtn");
    opener.focus();
    const opened = window.__Modal.open("Проверка", `
        <button id="firstBtn" type="button">Первый</button>
        <button id="lastBtn" type="button">Последний</button>`);
    await wait(10);

    check("модалка: open() вернул true (контейнер на странице есть)", opened === true, String(opened));
    check("модалка: фокус ушёл внутрь диалога",
        dialog.contains(doc.activeElement), active(doc));

    const closeBtn = doc.getElementById("modalCloseBtn");
    const last = doc.getElementById("lastBtn");

    last.focus();
    doc.dispatchEvent(new window.KeyboardEvent("keydown", { key: "Tab", bubbles: true }));
    check("модалка: Tab с последней кнопки возвращается к кнопке закрытия",
        doc.activeElement === closeBtn, active(doc));

    closeBtn.focus();
    doc.dispatchEvent(new window.KeyboardEvent("keydown", { key: "Tab", shiftKey: true, bubbles: true }));
    check("модалка: Shift+Tab с первой позиции уходит в конец списка",
        doc.activeElement === last, active(doc));

    doc.dispatchEvent(new window.KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    await wait(250);
    check("модалка: Escape закрывает окно", window.__Modal.isOpen() === false, String(window.__Modal.isOpen()));
    check("модалка: фокус вернулся в кнопку, которая открыла окно",
        doc.activeElement === opener, active(doc));
    check("модалка: после закрытия контейнер скрыт",
        doc.getElementById("modalContainer").classList.contains("hidden"),
        doc.getElementById("modalContainer").className);

    // Подписи полей внутри тела модалки приходят из modals.js: без <label for>
    // поле оценки читалось бы вслух как «редактировать» без имени.
    window.__Modal.open("Сдача", `
        <label class="sr-only" for="score-1">Оценка</label>
        <input id="score-1" name="score">`);
    await wait(10);
    check("модалка: поле оценки подписано (for/id совпали)",
        doc.getElementById("score-1") !== null && doc.querySelector('label[for="score-1"]') !== null, "");
    window.__Modal.close();
    await wait(250);
}

function active(doc) {
    return `activeElement=${doc.activeElement.id || doc.activeElement.tagName}`;
}

// ---------- 4. шапка не дублирует разметку при повторном render ----------

{
    const { window, doc } = track("dashboard.html");
    const before = doc.querySelectorAll("header nav").length;
    window.eval("GS.layout.render()");
    const after = doc.querySelectorAll("header nav").length;
    check("layout: повторный render не удваивает навигацию", before === 1 && after === 1, `${before} → ${after}`);
}

modalChecks().then(() => {
    check("ни одна проверка не оставила необработанного отклонения", rejections.length === 0, JSON.stringify(rejections));

    const failed = results.filter((r) => !r.ok);
    console.log(`\nИТОГ: ${results.length - failed.length}/${results.length} проверок пройдено`);
    for (const f of failed) console.log(`  FAIL  ${f.name}${f.detail ? " — " + f.detail : ""}`);
    for (const w of windows) w.close();
    process.exit(failed.length ? 1 : 0);
}).catch((error) => {
    console.error("СБОЙ ПРОВЕРКИ:", error && error.stack ? error.stack : error);
    for (const w of windows) w.close();
    process.exit(2);
});
