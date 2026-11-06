// Поведение прилипающей шапки при прокрутке (ADR-0039).
//
// Зачем отдельный сьют: складывание — это не вёрстка, а решение, которое
// принимается в рантайме по строке прокрутки. Его нельзя ни увидеть на
// скриншоте (шапка на нём всегда в одном состоянии), ни проверить статикой:
// scripts/check-frontend.mjs сверяет только то, что связь CSS ↔ JS не
// расцепилась, а пороги, гистерезис и «контролы целы в сложенном виде» живут
// ровно в одном месте — в обработчике scroll.
//
// Что здесь проверяется и почему этого не даёт никакой другой слой:
//   • пороги 24/12 работают как гистерезис, а не как два независимых условия:
//     с одним порогом складывание освобождало ~32px документа, прокрутка
//     оказывалась ниже порога, и шапка дёргалась под колесом мыши;
//   • в сложенном состоянии в шапке остаётся ровно тот же набор контролов, с
//     теми же aria-метками: «компактная полоса», где не видно «Выйти», — это
//     не компактная полоса, а потерянная кнопка;
//   • слушатель навешивается один раз и с passive: true (второй слушатель от
//     повторного render() удваивает работу на каждый кадр, а без passive
//     браузер обязан ждать обработчик до прокрутки — колесо начинает лагать);
//   • высоты в --header-h в jsdom не появляется: раскладки тут нет, и нулевой
//     замер должен быть отвергнут, иначе CSS получил бы отступ якоря 0px и
//     накрывал содержимое.
//
// Раскладку и переходы сьют не измеряет — для этого есть живая проверка
// браузером (scripts/check-focus.mjs и промер прилипания); здесь только те
// ветки, которые решают, поставить класс или нет.
//
// Тест полностью офлайновый: fetch подменяется заглушкой, сервер не нужен,
// лимитер входов (ADR-0006) не расходуется.
//
// Запуск: cd tests/ui && node header-collapse.cjs
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

// Как в accessibility.cjs: слушатель может вернуть отклонённый промис, и без
// сбора unhandledRejection Node роняет процесс вместо ИТОГА.
const rejections = [];
process.on("unhandledRejection", (reason) => {
    rejections.push(String((reason && reason.message) || reason));
});

function unauthorized() {
    return new Response(JSON.stringify({ error: { code: "unauthorized", message: "unauthorized", fields: {} } }), {
        status: 401,
        headers: { "Content-Type": "application/json" },
    });
}

const LAYOUT_BUNDLE = ["core/theme.js", "config.js", "core/api-client.js", "core/format.js", "core/layout.js"];

// Открывает страницу, исполняет бандл и считает, что именно он навесил на
// окно. Обёртка над addEventListener ставится до eval: посчитать слушатели
// post-factum в jsdom нечем, а именно двойной слушатель — класс поломки от
// повторного render().
function boot(page) {
    const html = fs.readFileSync(path.join(REPO, "web", page), "utf8");
    const dom = new JSDOM(html, {
        url: BASE + "/" + page.replace(".html", ""),
        runScripts: "outside-only",
        pretendToBeVisual: true,
    });
    const { window } = dom;

    const seen = { scroll: [], resize: 0 };
    const native = window.addEventListener.bind(window);
    window.addEventListener = (type, listener, options) => {
        if (type === "scroll") seen.scroll.push(options);
        if (type === "resize") seen.resize += 1;
        return native(type, listener, options);
    };

    window.fetch = () => Promise.resolve(unauthorized());
    window.document.cookie = "csrf_token=test-csrf-token";
    window.eval(LAYOUT_BUNDLE
        .map((file) => fs.readFileSync(path.join(REPO, "web/js", file), "utf8"))
        .join("\n;\n"));

    // Строка прокрутки: в jsdom её нельзя задать (нет окна прокрутки), поэтому
    // свойства подменяются геттером — ровно то, что читает обработчик.
    const scroll = { y: 0 };
    Object.defineProperty(window, "scrollY", { configurable: true, get: () => scroll.y });

    return { window, doc: window.document, header: window.document.querySelector("header.site-header"), seen, scroll };
}

// Один кадр прокрутки: обработчик ставит работу в requestAnimationFrame, и
// без ожидания следующего кадра класс бы ещё не проставился.
async function roll(env, y) {
    env.scroll.y = y;
    env.window.dispatchEvent(new env.window.Event("scroll"));
    await new Promise((r) => env.window.requestAnimationFrame(r));
    return env.header.classList.contains("is-stuck");
}

const stuck = (env) => env.header.classList.contains("is-stuck");

// Снимок контролов: тег, id, aria-метка и классы. Сортировка не нужна —
// порядок обхода детерминирован, и его изменение тоже было бы поломкой.
const controls = (header) => Array.from(header.querySelectorAll("a, button"))
    .map((el) => `${el.tagName.toLowerCase()}${el.id ? "#" + el.id : ""}|${el.getAttribute("aria-label") || ""}|${el.className}`);

// 404.html здесь нет намеренно: у неё статическая шапка без layout.js, значит
// и складывания быть не может. map.html входит: шапка у неё общий компонент,
// а страница ниже — единственный случай, где прилипание проверяется на
// полноэкранной раскладке.
const PAGES = [
    "index.html", "login.html", "register.html", "dashboard.html",
    "schedule.html", "profile.html", "admin.html", "map.html",
];

const windows = [];

async function collapseChecks() {
    for (const page of PAGES) {
        const env = boot(page);
        windows.push(env.window);
        const header = env.header;
        if (!header || !header.dataset.filled) {
            check(`${page}: шапка отрисована layout.js`, false, "шапки нет или она не заполнена");
            continue;
        }

        const before = controls(header);
        check(`${page}: в шапке есть контролы`, before.length > 0, `${before.length} штук`);

        // Порог складывания (STUCK_ON = 24): 100px — гарантированно ниже.
        check(`${page}: прокрутка 100px складывает`, await roll(env, 100) === true, `y=100 stuck=${stuck(env)}`);
        // 16px — между порогами: сложенная шапка обязана остаться сложенной,
        // иначе освобождённые 32px вернут прокрутку «наверх» и замкнут цикл.
        check(`${page}: 16px держит сложенной (гистерезис)`, (await roll(env, 16), stuck(env)) === true, `stuck=${stuck(env)}`);
        // Ниже STUCK_OFF = 12 — расправляем.
        check(`${page}: 4px расправляет шапку`, (await roll(env, 4), stuck(env)) === false, `stuck=${stuck(env)}`);
        // Обратная ветка того же порога: с расправленной шапки 16px складывать
        // не должен (с одним порогом дёрганье начиналось именно здесь).
        check(`${page}: 16px поверх расправленной не складывает`, (await roll(env, 16), stuck(env)) === false, `stuck=${stuck(env)}`);
        check(`${page}: 60px снова складывает`, (await roll(env, 60), stuck(env)) === true, `stuck=${stuck(env)}`);

        // Сложенное состояние не имеет права ничего прятать: в шапке тот же
        // набор ссылок и кнопок, тот же порядок, те же aria-метки.
        const after = controls(header);
        const lost = before.filter((c) => !after.includes(c));
        check(`${page}: сложенная полоса держит все контролы`, after.length === before.length && !lost.length,
            `было ${before.length}, стало ${after.length}${lost.length ? "; пропало: " + lost.join(", ") : ""}`);
        check(`${page}: складывание не прячет контролы классом hidden`,
            !after.some((c) => /\bhidden\b/.test(c) && !before.includes(c)),
            after.filter((c) => /\bhidden\b/.test(c)).join(", "));

        // Обёртка-<nav> с aria-label обязана пережить переход в компактное
        // состояние: ориентир не должен исчезать на середине страницы.
        check(`${page}: ориентир «Навигация» не зависит от прокрутки`,
            header.querySelectorAll('nav[aria-label="Навигация страницы"]').length === 1,
            `${header.querySelectorAll("nav").length} nav`);

        // Раскладки в jsdom нет: offsetHeight = 0, и высота в переменную не
        // пишется — fallback из style.css остаётся единственным (то, что он
        // там есть, сверяет scripts/check-frontend.mjs).
        check(`${page}: без раскладки --header-h не перезаписывается нулём`,
            env.doc.documentElement.style.getPropertyValue("--header-h") === "",
            `значение: ${JSON.stringify(env.doc.documentElement.style.getPropertyValue("--header-h"))}`);

        // Слушатель ровно один, passive, и рядом resize — иначе перерисовка
        // шапки (другое имя, другое число пунктов) не переснимет высоту.
        const opts = env.seen.scroll;
        check(`${page}: слушатель прокрутки единственный`, opts.length === 1, `навешано ${opts.length}`);
        check(`${page}: слушатель прокрутки passive`, opts.length === 1 && opts[0] && opts[0].passive === true,
            JSON.stringify(opts[0]));
        check(`${page}: смена размера переснимает высоту`, env.seen.resize === 1, `слушателей resize: ${env.seen.resize}`);

        // Повторный render() — то, что делает hot reload или тест: шапка уже
        // заполнена, и второй слушатель появился бы именно здесь.
        env.window.GS.layout.render();
        check(`${page}: второй render() не удваивает слушателей`, env.seen.scroll.length === 1, `их ${env.seen.scroll.length}`);
        await roll(env, 0);
        check(`${page}: после второго render() складывание работает`,
            (await roll(env, 90), stuck(env)) === true, `stuck=${stuck(env)}`);
    }
}

// ---------- состояние, которого в разметке быть не должно ----------

function wiringChecks() {
    // Класс сложенного состояния ставит только JS. Останется он в статичном
    // HTML — страница откроется уже собранной: на верхней строке расписания
    // компактная полоса, и раскроется она только после первой прокрутки вниз.
    const htmlFiles = fs.readdirSync(path.join(REPO, "web")).filter((f) => f.endsWith(".html"));
    const hardcoded = htmlFiles.filter((f) => fs.readFileSync(path.join(REPO, "web", f), "utf8").includes("is-stuck"));
    check("ни одна страница не содержит is-stuck в разметке", hardcoded.length === 0, hardcoded.join(", "));

    // 404.html скриптов не подключает: шапка там статическая, слушателя быть не
    // может, и складывания на странице ошибки тоже. Ищем именно <script>, а не
    // имя файла: в комментарии страницы core/layout.js помянут словом.
    const notFound = fs.readFileSync(path.join(REPO, "web", "404.html"), "utf8");
    check("404.html остаётся без складывания (шапка статическая)",
        !/<script/.test(notFound) && !notFound.includes("is-stuck"),
        `скриптов: ${(notFound.match(/<script/g) || []).length}`);
}

collapseChecks().then(wiringChecks).then(() => {
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
