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
// th, контраст токенов, наличие и разрыв aria-describedby) дублируется в
// scripts/check-frontend.mjs — там, где они не требуют ни сервера, ни jsdom.
// Здесь то, что статикой не проверяется: ориентир «Навигация» рисует
// core/layout.js, ловушку фокуса держит modals.js в рантайме, и только в DOM
// видно, что после отказа текст появился именно в том элементе, который поле
// называет своим описанием (раздел 6, ADR-0018).
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
// theme.js стоит первым, как в <head>: он ставит data-theme до того, как
// layout.js нарисует кнопку, и именно этот порядок проверяют тесты ниже.
const LAYOUT_BUNDLE = ["core/theme.js", "config.js", "core/api-client.js", "core/format.js", "core/layout.js"];

// Открывает страницу, исполняет бандл и возвращает документ. `extra` — код,
// который исполняется в том же scope, что и бандл: ModalManager объявлен
// константой на уровне файла и наружу (в window) не виден, а тесту до него надо
// добраться. Так же, как в assignments-modal.cjs, это тестовый костыль, а не
// часть приложения.
function boot(page, scripts = LAYOUT_BUNDLE, extra = "", preboot = null) {
    const html = fs.readFileSync(path.join(REPO, "web", page), "utf8");
    const dom = new JSDOM(html, {
        url: BASE + "/" + page.replace(".html", ""),
        runScripts: "outside-only",
        pretendToBeVisual: true,
    });
    const { window } = dom;

    // preboot — то, что в браузере уже лежит в localStorage к моменту разбора
    // <head>: сюда тест кладёт сохранённый выбор темы, чтобы проверить, что
    // атрибут ставится до первой отрисовки.
    if (preboot) preboot(window);

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

function track(page, scripts, extra, preboot) {
    const opened = boot(page, scripts, extra, preboot);
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

    // Переключатель темы — отдельная кнопка рядом с <nav>, а не пункт меню:
    // внутри landmark'а «Навигация» действие читалось бы как ещё одна ссылка.
    const toggle = doc.getElementById("themeToggle");
    check(`${layout}: переключатель темы в шапке, вне <nav>`,
        Boolean(toggle) && Boolean(toggle.closest(".header-tools")) && !toggle.closest("nav"),
        toggle ? `в header-tools=${Boolean(toggle.closest(".header-tools"))}, в nav=${Boolean(toggle.closest("nav"))}` : "кнопки нет");
    check(`${layout}: переключатель — кнопка с доступным именем`,
        Boolean(toggle) && toggle.tagName === "BUTTON" && toggle.getAttribute("type") === "button"
            && (toggle.getAttribute("aria-label") || "").trim().length > 0,
        toggle ? `${toggle.tagName.toLowerCase()} type=${toggle.getAttribute("type")} label=${JSON.stringify(toggle.getAttribute("aria-label"))}` : "");
    check(`${layout}: aria-pressed объявляет состояние темы`,
        Boolean(toggle) && ["true", "false"].includes(toggle.getAttribute("aria-pressed")),
        toggle ? String(toggle.getAttribute("aria-pressed")) : "");

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
    // 404 намеренно живёт без скриптов (ADR-0013) — значит, и theme.js на ней
    // не исполняется. Мёртвая кнопка хуже её отсутствия: страница остаётся
    // светлой, зато работает тогда, когда приложение не работает.
    check("404: переключателя темы нет (страница без скриптов)",
        doc.getElementById("themeToggle") === null, "themeToggle найден");
}

// ---------- 3. тема: начальное состояние, переключение, сохранение ----------

async function themeChecks() {
    const { window, doc } = track("schedule.html");
    // DOMContentLoaded: theme.js на нём досинхронизирует подпись кнопки.
    await wait(20);

    check("тема: без сохранённого выбора страница открывается светлой",
        doc.documentElement.dataset.theme === "light", String(doc.documentElement.dataset.theme));
    check("тема: без выбора в localStorage ничего не записано",
        window.localStorage.getItem("sos.theme") === null,
        String(window.localStorage.getItem("sos.theme")));

    const toggle = doc.getElementById("themeToggle");
    toggle.click();
    check("тема: клик включает тёмную", doc.documentElement.dataset.theme === "dark",
        String(doc.documentElement.dataset.theme));
    check("тема: выбор сохранён в localStorage", window.localStorage.getItem("sos.theme") === "dark",
        String(window.localStorage.getItem("sos.theme")));
    check("тема: подпись кнопки называет обратное действие",
        toggle.getAttribute("aria-label") === "Включить светлую тему", toggle.getAttribute("aria-label"));
    check("тема: aria-pressed отражает включённую тёмную",
        toggle.getAttribute("aria-pressed") === "true", String(toggle.getAttribute("aria-pressed")));

    toggle.click();
    check("тема: повторный клик возвращает светлую",
        doc.documentElement.dataset.theme === "light", String(doc.documentElement.dataset.theme));
    check("тема: светлый выбор тоже сохранён", window.localStorage.getItem("sos.theme") === "light",
        String(window.localStorage.getItem("sos.theme")));
    check("тема: aria-pressed вернулся в false",
        toggle.getAttribute("aria-pressed") === "false", String(toggle.getAttribute("aria-pressed")));

    // Сохранённый выбор применяется до отрисовки: theme.js стоит в <head>, и
    // data-theme уже стоит к моменту, когда layout.js рисует шапку.
    const saved = track("schedule.html", LAYOUT_BUNDLE, "", (w) => w.localStorage.setItem("sos.theme", "dark"));
    check("тема: сохранённая тёмная применяется при загрузке",
        saved.doc.documentElement.dataset.theme === "dark", String(saved.doc.documentElement.dataset.theme));
    await wait(20);
    check("тема: подпись кнопки догнала сохранённое состояние",
        saved.doc.getElementById("themeToggle").getAttribute("aria-label") === "Включить светлую тему",
        saved.doc.getElementById("themeToggle").getAttribute("aria-label"));
    check("тема: GS.theme.current() читает применённую тему",
        Boolean(saved.window.GS && saved.window.GS.theme) && saved.window.GS.theme.current() === "dark",
        saved.window.GS && saved.window.GS.theme ? saved.window.GS.theme.current() : "GS.theme нет");
}

// ---------- 4. модальное окно: ловушка фокуса и возврат ----------

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

// ---------- 5. шапка не дублирует разметку при повторном render ----------

{
    const { window, doc } = track("dashboard.html");
    const before = doc.querySelectorAll("header nav").length;
    window.eval("GS.layout.render()");
    const after = doc.querySelectorAll("header nav").length;
    check("layout: повторный render не удваивает навигацию", before === 1 && after === 1, `${before} → ${after}`);
}

// ---------- 6. aria-describedby: поле знает про свою подсказку и строку сообщения ----------
//
// Остаток T0.5, закрытый ADR-0018: сообщения подписаны role="status", но поле
// ничего о своём сообщении не знало. check-frontend.mjs видит сам атрибут и его
// битую ссылку; здесь то, для чего нужен DOM и рантайм — что по ссылке лежит
// элемент, и в момент отказа именно в нём появляется текст, который читается
// вслух вместе с полем.
function describedByChecks() {
    let refs = 0;
    let broken = 0;
    let statusForms = 0;
    let unlinkedFields = 0;
    let unlinkedGroups = 0;
    const brokenList = [];

    for (const page of PAGES) {
        const html = fs.readFileSync(path.join(REPO, "web", page), "utf8");
        const dom = new JSDOM(html);
        const doc = dom.window.document;

        for (const el of doc.querySelectorAll("[aria-describedby]")) {
            for (const id of (el.getAttribute("aria-describedby") || "").trim().split(/\s+/)) {
                if (!id) continue;
                refs += 1;
                if (!doc.getElementById(id)) {
                    broken += 1;
                    brokenList.push(`${page}#${id}`);
                }
            }
        }

        for (const form of doc.querySelectorAll("form")) {
            const status = form.querySelector('[role="status"]');
            if (!status || !status.id) continue;
            statusForms += 1;

            for (const field of form.querySelectorAll("input.control, textarea.control, select.control")) {
                if (field.type === "hidden") continue;
                const ids = (field.getAttribute("aria-describedby") || "").split(/\s+/);
                if (!ids.includes(status.id)) unlinkedFields += 1;
            }

            // Радиогруппа: описание вешают на fieldset, а не на каждую метку —
            // иначе одна и та же строка читается четыре раза подряд.
            for (const group of form.querySelectorAll("fieldset")) {
                if (!group.querySelector('input[type="radio"], input[type="checkbox"]')) continue;
                const ids = (group.getAttribute("aria-describedby") || "").split(/\s+/);
                if (!ids.includes(status.id)) unlinkedGroups += 1;
            }
        }

        dom.window.close();
    }

    check("describedby: каждая ссылка ведёт на существующий элемент", broken === 0,
        `ссылок ${refs}, битых ${broken}${brokenList.length ? ": " + brokenList.join(", ") : ""}`);
    check("describedby: каждое поле формы со статус-строкой ссылается на неё",
        unlinkedFields === 0, `форм ${statusForms}, полей без ссылки ${unlinkedFields}`);
    check("describedby: радиогруппа ссылается через fieldset", unlinkedGroups === 0,
        `групп без ссылки ${unlinkedGroups}`);
}

// Рантайм: showMessage снимает hidden и пишет текст в тот самый элемент, который
// поле называет своим описанием. Если ссылка ведёт в никуда или в другой узел,
// проверка краснеет именно здесь — статика такого не видит.
function describedByRuntimeChecks() {
    const { window, doc } = track("schedule.html", [...LAYOUT_BUNDLE, "common.js"]);
    const titleField = doc.querySelector('#resourceForm input[name="title"]');
    const urlField = doc.querySelector('#resourceForm input[name="url"]');

    const titleRefs = ((titleField && titleField.getAttribute("aria-describedby")) || "").split(/\s+/);
    check("describedby: поле «Название» ссылкируется на resourceMsg", titleRefs.includes("resourceMsg"),
        titleField ? JSON.stringify(titleRefs) : "поля нет");

    window.eval('GS.showMessage(document.getElementById("resourceMsg"), "title is required — поле: Название", "error")');
    const shown = doc.getElementById("resourceMsg");
    const carriesText = titleRefs.some((id) => {
        const el = doc.getElementById(id);
        return el && el.textContent.includes("поле: Название");
    });
    check("describedby: после отказа текст стоит в элементе, на который ссылается поле",
        carriesText, JSON.stringify({ titleRefs, text: shown.textContent }));
    check("describedby: элемент описания перестал быть hidden и читается",
        !shown.classList.contains("hidden"), shown.className);

    const urlRefs = ((urlField && urlField.getAttribute("aria-describedby")) || "").split(/\s+/);
    const helper = doc.getElementById("urlHelper");
    check("describedby: поле «Ссылка» ссылается и на подсказку, и на сообщение",
        urlRefs.includes("urlHelper") && urlRefs.includes("resourceMsg"), JSON.stringify(urlRefs));
    check("describedby: текст подсказки непустой", Boolean(helper) && helper.textContent.trim().length > 10,
        helper ? JSON.stringify(helper.textContent.trim().slice(0, 40)) : "подсказки нет");

    window.eval('GS.hideMessage(document.getElementById("resourceMsg"))');
    check("describedby: после hideMessage описание пустое, а не залипшее",
        doc.getElementById("resourceMsg").textContent === "",
        JSON.stringify(doc.getElementById("resourceMsg").textContent));
}

// ---------- 7. анимации кнопок: ripple есть, но не при reduced-motion ----------
//
// Анимация нажатия — функциональная обратная связь, а не украшение; WCAG 2.3.3
// требует её выключать, когда человек попросил систему об этом. jsdom не считает
// вёрстку, поэтому кнопке подставляется прямоугольник: нулевой размер common.js
// пропускает намеренно (скрытая кнопка не должна «стрелять» волной).
function rippleChecks() {
    function press(win, doc) {
        const btn = doc.getElementById("currentMonth");
        btn.getBoundingClientRect = () => ({
            width: 120, height: 40, left: 0, top: 0, right: 120, bottom: 40, x: 0, y: 0,
        });
        btn.dispatchEvent(new win.MouseEvent("pointerdown", { bubbles: true, clientX: 20, clientY: 20 }));
        return { btn, ripple: btn.querySelector(".ripple") };
    }

    const normal = track("schedule.html", [...LAYOUT_BUNDLE, "common.js"]);
    const first = press(normal.window, normal.doc);
    check("анимации: нажатие создаёт ripple внутри кнопки", Boolean(first.ripple),
        first.ripple ? first.ripple.className : "ripple нет");
    check("анимации: ripple посажен на точку нажатия",
        Boolean(first.ripple) && /left:\s*-?\d/.test(first.ripple.getAttribute("style") || ""),
        first.ripple ? first.ripple.getAttribute("style") : "");

    if (first.ripple) {
        first.ripple.dispatchEvent(new normal.window.Event("animationend"));
    }
    check("анимации: ripple снимается после анимации",
        first.btn.querySelectorAll(".ripple").length === 0,
        String(first.btn.querySelectorAll(".ripple").length));

    const reduce = track("schedule.html", [...LAYOUT_BUNDLE, "common.js"], "", (w) => {
        w.matchMedia = () => ({
            matches: true,
            addEventListener() {}, removeEventListener() {},
            addListener() {}, removeListener() {},
        });
    });
    const reduced = press(reduce.window, reduce.doc);
    check("анимации: при prefers-reduced-motion ripple не создаётся", !reduced.ripple,
        reduced.ripple ? reduced.ripple.outerHTML : "ripple нет");
}

describedByChecks();
describedByRuntimeChecks();
rippleChecks();

themeChecks().then(modalChecks).then(() => {
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
