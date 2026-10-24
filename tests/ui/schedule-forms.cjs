// Приёмка T3.1: формы расписания (ресурс и событие) живут на одном общем
// обработчике отправки.
//
// Зачем отдельный тест: до T3.1 в schedule.js было два дословно одинаковых
// submit-хэндлера, и правка, внесённая в один, молча оставляла второй старым —
// ровно тот класс багов, из-за которого модалки в T0.1 не работали. Теперь
// механизм один (bindCreateForm), а тест закрепляет его поведение с обеих сторон:
// валидация до запроса, блокировка кнопки, сброс формы только при успехе,
// перезагрузка списка, восстановление подписи кнопки с иконкой.
//
// Тест полностью офлайновый: fetch подменяется роутером-заглушкой. Сервер на
// :8080 не нужен, и лимитер входов (20 попыток на IP за 10 минут, ADR-0006) не
// расходуется — этот набор можно гонять хоть каждые пять минут.
//
// Запуск: cd tests/ui && node schedule-forms.cjs
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

// Async-обработчик страницы не бросает исключение в dispatchEvent: он возвращает
// уже отклонённый промис, и без этого слушателя Node роняет процесс с --unhandled-
// rejections=throw — тест заканчивался стеком вместо ИТОГА. Собираем такое в
// список и проверяем отдельным пунктом: падение обработчика это баг, и он должен
// быть виден как FAIL.
const rejections = [];
process.on("unhandledRejection", (reason) => {
    rejections.push(String((reason && reason.message) || reason));
});

const BUNDLE = ["config.js", "core/api-client.js", "core/format.js", "core/layout.js", "common.js"];

function json(body, status = 200) {
    return new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
    });
}

// Разбираем "/api/schedule?from=1&to=2" в "/api/schedule" + строку запроса.
function splitUrl(url) {
    const clean = String(url).replace(BASE, "");
    const queryIndex = clean.indexOf("?");
    return {
        pathname: queryIndex === -1 ? clean : clean.slice(0, queryIndex),
        search: queryIndex === -1 ? "" : clean.slice(queryIndex),
    };
}

// Страница расписания с подставленным API. Правила ответа задаёт вызывающий
// тест через routes: ключ "GET /api/resources", значение — функция или готовое
// тело. calls пишет историю запросов в порядке их фактической отправки.
function boot({ role = "teacher", routes = {} } = {}) {
    const html = fs.readFileSync(path.join(REPO, "web", "schedule.html"), "utf8");
    const dom = new JSDOM(html, { url: BASE + "/schedule", runScripts: "outside-only", pretendToBeVisual: true });
    const { window } = dom;

    const calls = [];
    // Счётчик вызовов по каждому ключу: первый GET /api/resources отдаёт один
    // список, повторный — уже расширенный. Так видно, что после сохранения
    // страница действительно перерисовала данные, а не оставила старые.
    const hits = {};

    window.fetch = (input, init = {}) => {
        const method = String(init.method || "GET").toUpperCase();
        const { pathname, search } = splitUrl(input);
        const key = method + " " + pathname;
        hits[key] = (hits[key] || 0) + 1;

        const record = { key, method, pathname, search, body: init.body, headers: init.headers };
        calls.push(record);

        const rule = routes[key];
        const decided = typeof rule === "function" ? rule({ record, hits }) : rule;
        return Promise.resolve(decided === undefined ? defaultBody(key, role, hits[key]) : decided);
    };

    window.document.cookie = "csrf_token=test-csrf-token";

    const source = BUNDLE.concat(["schedule.js"])
        .map((file) => fs.readFileSync(path.join(REPO, "web/js", file), "utf8"))
        .join("\n;\n");
    window.eval(source);

    const navigations = [];
    window.SOS_CONFIG.navigateTo = (target) => navigations.push(target);

    return { window, doc: window.document, calls, navigations, hits };
}

function defaultBody(key, role, hit) {
    if (key === "GET /api/auth/me") {
        if (role === "guest") {
            return json({ error: { code: "unauthorized", message: "session expired" } }, 401);
        }
        return json({ id: 7, name: "Проверка Форма", email: "f@example.test", role });
    }
    if (key === "GET /api/schedule") {
        return json(hit === 1 ? [] : [{
            kind: "event",
            title: "Событие из перезагрузки",
            description: "",
            due_date: 0,
        }]);
    }
    if (key === "GET /api/resources") {
        return json(hit === 1
            ? [{ id: 1, title: "Старый ресурс", category: "", url: "", description: "" }]
            : [
                { id: 1, title: "Старый ресурс", category: "", url: "", description: "" },
                { id: 2, title: "Новый ресурс", category: "Материалы", url: "", description: "" },
            ]);
    }
    if (key === "POST /api/resources" || key === "POST /api/events") {
        return json({ id: 99, ok: true });
    }
    return json({});
}

// Заполняет поля и отправляет форму; ждёт, пока обработчик дойдёт до сервера
// (или до отказа валидации, где запроса нет вовсе).
//
// Значение вида { type: "text", value: "..." } сначала переключает поле в
// другой тип. Это нужно для проверки ветки «дата не читается»: и jsdom, и
// браузеры поддерживают datetime-local как поле с валидацией — любое
// «31.12.2026» режется до пустой строки ещё до чтения FormData (проверено
// руками, defineProperty-обход тоже не помогает: FormData берёт внутреннее
// значение). Поле как text — это ровно то, что происходит в браузере без
// поддержки datetime-local (там тип откатывается к текстовому), и только так
// защитный isNaN в toEventDate вообще достижим из теста.
async function submit(page, formId, fields) {
    const form = page.doc.getElementById(formId);
    if (!form) throw new Error("нет формы " + formId);

    for (const [name, value] of Object.entries(fields)) {
        const input = form.querySelector(`[name="${name}"]`);
        if (!input) throw new Error(`у формы ${formId} нет поля ${name}`);

        if (value && typeof value === "object" && "value" in value) {
            if (value.type) input.setAttribute("type", value.type);
            input.value = value.value;
        } else {
            input.value = value;
        }
    }

    const postBefore = page.calls.filter((c) => c.method === "POST").length;
    // Обработчик страницы вызывается нашим же dispatchEvent синхронно, поэтому
    // его исключение прилетает сюда напрямую. Ловим и отдаём наружу: иначе баг
    // вида «RangeError в toEventDate» валил бы процесс без единой строки итога,
    // а тест обязан показывать FAIL с именем проверки, а не стек.
    let thrown = null;
    try {
        form.dispatchEvent(new page.window.Event("submit", { bubbles: true, cancelable: true }));
    } catch (error) {
        thrown = error;
    }

    // Микротаски заглушки резолвятся сразу, но wait нужен: обработчик async, и
    // без тика таймера мы увидим только первую половину его работы.
    await wait(30);
    for (let i = 0; i < 40 && page.calls.filter((c) => c.method === "POST").length === postBefore && !messageText(page.doc, formId); i++) {
        await wait(20);
    }
    await wait(30);

    return {
        form,
        thrown,
        message: messageText(page.doc, formId),
        messageType: messageType(page.doc, formId),
        posts: page.calls.filter((c) => c.method === "POST"),
    };
}

function messageText(doc, formId) {
    const msg = doc.getElementById(formId === "resourceForm" ? "resourceMsg" : "eventMsg");
    return msg ? msg.textContent : "";
}
function messageType(doc, formId) {
    const msg = doc.getElementById(formId === "resourceForm" ? "resourceMsg" : "eventMsg");
    return msg ? msg.className : "";
}
function button(page, formId) {
    return page.doc.getElementById(formId).querySelector('button[type="submit"]');
}
function bodyOf(post) {
    return post && post.body ? JSON.parse(post.body) : null;
}

(async () => {
    // --- Гостевой режим: формы создания скрыты, запрос профиля не уводит на вход. ---
    const guest = boot({ role: "guest" });
    await wait(150);
    check(
        "гость видит уведомление вместо ленты",
        !guest.doc.getElementById("guestNotice").classList.contains("hidden")
        && guest.doc.getElementById("feedSection").classList.contains("hidden"),
        guest.doc.getElementById("guestNotice").className,
    );
    check(
        "у гостя скрыты обе формы создания",
        guest.doc.getElementById("createResourceSection").classList.contains("hidden")
        && guest.doc.getElementById("eventsManageSection").classList.contains("hidden"),
    );
    check("гостевой запуск не редиректит", guest.navigations.length === 0, JSON.stringify(guest.navigations));

    // --- Студент: лента есть, форм создания нет (права на доску только у препода). ---
    const student = boot({ role: "student" });
    await wait(150);
    check(
        "студенту лента видна, формы — нет",
        !student.doc.getElementById("feedSection").classList.contains("hidden")
        && student.doc.getElementById("createResourceSection").classList.contains("hidden"),
    );

    // --- Преподаватель: обе формы открыты, дальше на них и проверяем механизм. ---
    const page = boot({ role: "teacher" });
    await wait(150);
    check(
        "преподавателю открыты формы ресурса и события",
        !page.doc.getElementById("createResourceSection").classList.contains("hidden")
        && !page.doc.getElementById("eventsManageSection").classList.contains("hidden"),
    );
    check("стартовый список ресурсов отрисован", /Старый ресурс/.test(page.doc.getElementById("resources").textContent),
        JSON.stringify(page.doc.getElementById("resources").textContent.trim().slice(0, 60)));

    // --- 0. Шапка и подвал: страница расписания — одна из шести, где их рисует
    // core/layout.js. Проверки живут здесь, потому что это самый дешёвый сьют
    // (без сервера), а регрессия «шапка пустая» была бы видна только глазами. ---
    const header = page.doc.querySelector("header.site-header");
    const footer = page.doc.querySelector("footer.site-footer");
    check("шапка отрисована компонентом, а не осталась пустой",
        Boolean(header.querySelector(".brand-title")) && header.querySelector(".brand-title").textContent === "SOS",
        JSON.stringify(header.textContent.replace(/\s+/g, " ").trim().slice(0, 60)));
    check("подпись бренда взята из data-brand-sub страницы",
        header.querySelector(".brand-sub").textContent === "Расписание · Ресурсы",
        JSON.stringify(header.querySelector(".brand-sub").textContent));
    // Ссылки в шапке относительные (не "/login"): это правило config.js и ADR
    // про GitHub Pages под каталогом. Путь страницы "/schedule" без слэша —
    // значит базовый каталог "/", и "login" разрешается в "/login". Проверка
    // фиксирует именно это разрешение: если в шапке появится "/login", ссылок
    // из-под каталога Pages не станет, а тут тест это поймает.
    const loginLink = header.querySelector("#loginLink");
    const dashboardLink = header.querySelector('a[href="dashboard"]');
    check("в шапке есть ссылка на кабинет и вход с id для скриптов страниц",
        Boolean(dashboardLink) && Boolean(loginLink),
        JSON.stringify([...header.querySelectorAll("a")].map((a) => a.getAttribute("href"))));
    check("относительные ссылки шапки разрешаются в страницы этого каталога",
        loginLink && loginLink.pathname === "/login" && dashboardLink && dashboardLink.pathname === "/dashboard",
        JSON.stringify([loginLink && loginLink.pathname, dashboardLink && dashboardLink.pathname]));
    check("подвал заполнен из data-footer-note",
        footer.querySelector(".footer-meta").textContent === "Schedule · events · resources — one place",
        JSON.stringify(footer.textContent.replace(/\s+/g, " ").trim().slice(0, 60)));

    // Повторный render() не должен затирать то, что страничные скрипты сделали с
    // шапкой после отрисовки: dashboard.js прячет #adminLink и подписывает имя
    // пользователя, и второй вызов (горячая перезагрузка, лишний include) эти
    // правки стер бы. Проверка моделирует именно это — правку после отрисовки.
    loginLink.classList.add("hidden");
    loginLink.textContent = "Вход по-новому";
    page.window.eval("GS.layout.render()");
    // Мишень ищем заново в DOM, а не по прежней ссылке: innerHTML при повторной
    // отрисовке создаёт новый элемент, и проверка старого узла никогда бы не
    // увидела подмены (первая версия этой проверки именно так и молчала).
    const reRendered = page.doc.querySelector("#loginLink");
    check("повторный GS.layout.render() не затирает правки шапки от скриптов",
        reRendered.classList.contains("hidden") && reRendered.textContent === "Вход по-новому",
        JSON.stringify([reRendered.className, reRendered.textContent]));

    // 1. Пустой заголовок: клиент отсекает запрос, сервер не дёргается.
    const emptyTitle = await submit(page, "resourceForm", { title: "   ", category: "Материалы" });
    check("пустой заголовок не уходит на сервер", emptyTitle.posts.length === 0, String(emptyTitle.posts.length));
    check("текст про обязательное название показан", emptyTitle.message === "Название обязательно", JSON.stringify(emptyTitle.message));
    check("сообщение помечено как ошибка", /error/.test(emptyTitle.messageType) && !/hidden/.test(emptyTitle.messageType), emptyTitle.messageType);
    const btnAfterReject = button(page, "resourceForm");
    check("кнопка не осталась заблокированной после отказа валидации",
        !btnAfterReject.disabled && !btnAfterReject.classList.contains("is-loading"),
        btnAfterReject.className + " disabled=" + btnAfterReject.disabled);

    // 2. Успешное добавление ресурса: payload, сброс, перезагрузка, состояние кнопки.
    const okResource = await submit(page, "resourceForm", {
        title: "  Moodle  ",
        category: " Материалы ",
        url: " https://moodle.example.test ",
        description: "Платформа курса",
    });
    const resourcePost = okResource.posts[okResource.posts.length - 1];
    check("запрос отправлен на /api/resources", resourcePost && resourcePost.pathname === "/api/resources",
        resourcePost ? resourcePost.key : "нет POST");
    const resourcePayload = bodyOf(resourcePost);
    check("значения полей обрезаются и уходят на сервер как JSON",
        resourcePayload && resourcePayload.title === "Moodle" && resourcePayload.category === "Материалы"
        && resourcePayload.url === "https://moodle.example.test" && resourcePayload.description === "Платформа курса",
        JSON.stringify(resourcePayload));
    check("у POST-запроса есть CSRF-токен", Boolean(resourcePost && resourcePost.headers && resourcePost.headers["X-CSRF-Token"]),
        JSON.stringify(resourcePost && resourcePost.headers));
    check("успех подтверждён текстом", okResource.message === "Ресурс добавлен", JSON.stringify(okResource.message));
    check("сообщение об успехе, а не об ошибке", /success/.test(okResource.messageType), okResource.messageType);
    check("форма очищена после сохранения",
        page.doc.querySelector('#resourceForm [name="title"]').value === "",
        JSON.stringify(page.doc.querySelector('#resourceForm [name="title"]').value));
    const postIndex = page.calls.findIndex((c) => c.method === "POST" && c.pathname === "/api/resources");
    const reloadIndex = page.calls.findIndex((c, i) => i > postIndex && c.method === "GET" && c.pathname === "/api/resources");
    check("список ресурсов перезапрошен после добавления", reloadIndex > postIndex, `POST=${postIndex} GET=${reloadIndex}`);
    check("перезагруженный список отрисован с новым ресурсом",
        /Новый ресурс/.test(page.doc.getElementById("resources").textContent));
    const btn = button(page, "resourceForm");
    check("кнопка снова активна", !btn.disabled && !btn.classList.contains("is-loading"),
        btn.className + " disabled=" + btn.disabled);
    check("подпись кнопки восстановлена вместе с иконкой", /<svg/.test(btn.innerHTML) && /Сохранить/.test(btn.innerHTML),
        JSON.stringify(btn.textContent.trim()));
    check("отправка ресурса не роняет обработчик", okResource.thrown === null,
        okResource.thrown ? okResource.thrown.message : "");

    // 3. Ошибка сервера: форма НЕ сбрасывается, список НЕ перезагружается.
    const failing = boot({
        role: "teacher",
        routes: { "POST /api/resources": () => json({ error: { code: "validation", message: "title is required", fields: { title: "title is required" } } }, 422) },
    });
    await wait(150);
    const badSubmit = await submit(failing, "resourceForm", { title: "Дубликат", category: "Материалы" });
    check("серверная ошибка показана текстом сервера",
        badSubmit.message.startsWith("title is required"), JSON.stringify(badSubmit.message));
    check("названное сервером поле подписано по-человечески",
        badSubmit.message.includes("Название"), JSON.stringify(badSubmit.message));
    check("ошибочный ответ не назван успехом", /error/.test(badSubmit.messageType) && !/success/.test(badSubmit.messageType),
        badSubmit.messageType);
    check("при ошибке форма не очищена — пользователь не теряет ввод",
        failing.doc.querySelector('#resourceForm [name="title"]').value === "Дубликат",
        JSON.stringify(failing.doc.querySelector('#resourceForm [name="title"]').value));
    const badPostIndex = failing.calls.findIndex((c) => c.method === "POST");
    const badReload = failing.calls.findIndex((c, i) => i > badPostIndex && c.method === "GET" && c.pathname === "/api/resources");
    check("при ошибке список не перезапрашивается", badReload === -1, `POST=${badPostIndex} GET=${badReload}`);
    check("после ошибки кнопка тоже разблокирована",
        !button(failing, "resourceForm").disabled && !button(failing, "resourceForm").classList.contains("is-loading"));
    check("серверная ошибка не роняет обработчик", badSubmit.thrown === null,
        badSubmit.thrown ? badSubmit.thrown.message : "");

    // 4. Форма события: тот же механизм, другие тексты + разбор даты.
    const events = boot({ role: "teacher" });
    await wait(150);

    const withDate = await submit(events, "eventForm", {
        title: "День открытых дверей",
        event_date_raw: "2026-11-05T09:30",
        description: "Приходите",
    });
    const datePost = withDate.posts[withDate.posts.length - 1];
    check("событие отправлено на /api/events", datePost && datePost.pathname === "/api/events",
        datePost ? datePost.key : "нет POST");
    const datePayload = bodyOf(datePost);
    const expectedIso = new Date("2026-11-05T09:30").toISOString();
    check("читаемая дата превращена в ISO", datePayload && datePayload.event_date === expectedIso,
        JSON.stringify(datePayload && datePayload.event_date) + " ожидалось " + JSON.stringify(expectedIso));
    check("заголовок и описание события доехали",
        datePayload && datePayload.title === "День открытых дверей" && datePayload.description === "Приходите",
        JSON.stringify(datePayload));
    check("успешное событие подтверждено своим текстом", withDate.message === "Событие добавлено", JSON.stringify(withDate.message));
    check("форма события очищена после сохранения",
        events.doc.querySelector('#eventForm [name="title"]').value === "");

    const eventPostIndex = events.calls.findIndex((c) => c.method === "POST" && c.pathname === "/api/events");
    const feedReload = events.calls.findIndex((c, i) => i > eventPostIndex && c.method === "GET" && c.pathname === "/api/schedule");
    check("лента перезапрошена после добавления события", feedReload > eventPostIndex,
        `POST=${eventPostIndex} GET=${feedReload}`);
    check("лента отрисована после перезагрузки",
        /Событие из перезагрузки/.test(events.doc.getElementById("feed").textContent),
        JSON.stringify(events.doc.getElementById("feed").textContent.trim().slice(0, 60)));
    const eventBtn = button(events, "eventForm");
    check("кнопка формы события восстановлена", /<svg/.test(eventBtn.innerHTML) && !eventBtn.disabled
        && !eventBtn.classList.contains("is-loading"), JSON.stringify(eventBtn.textContent.trim()));
    check("отправка события не роняет обработчик", withDate.thrown === null,
        withDate.thrown ? withDate.thrown.message : "");

    // 5. Дата: нечитаемая и пустая дают одинаковый результат — «без даты».
    const garbage = await submit(events, "eventForm", {
        title: "Гараж",
        event_date_raw: { type: "text", value: "31.12.2026" },
    });
    const garbagePayload = bodyOf(garbage.posts[garbage.posts.length - 1]);
    check("нечитаемая дата (поле без поддержки datetime-local) уходит пустой, а не NaN",
        garbagePayload && garbagePayload.event_date === "", JSON.stringify(garbagePayload));
    check("нечитаемая дата не считается ошибкой формы", garbage.message === "Событие добавлено",
        JSON.stringify(garbage.message));
    check("защита от нечитаемой даты работает — обработчик не упал", garbage.thrown === null,
        garbage.thrown ? garbage.thrown.constructor.name + ": " + garbage.thrown.message : "");

    const noDate = await submit(events, "eventForm", { title: "Без даты", event_date_raw: "" });
    const noDatePayload = bodyOf(noDate.posts[noDate.posts.length - 1]);
    check("пустая дата тоже уходит пустой строкой",
        noDatePayload && noDatePayload.event_date === "", JSON.stringify(noDatePayload));

    const emptyEvent = await submit(events, "eventForm", { title: "  " });
    check("форма события проверяет заголовок тем же механизмом",
        emptyEvent.posts.length === noDate.posts.length
        && emptyEvent.message === "Название обязательно", JSON.stringify(emptyEvent.message));

    // 6. Истёкшая сессия при сохранении: редирект на вход, но без зависшей кнопки.
    const expired = boot({
        role: "teacher",
        routes: { "POST /api/resources": () => json({ error: { code: "unauthorized", message: "unauthorized" } }, 401) },
    });
    await wait(150);
    const expiredSubmit = await submit(expired, "resourceForm", { title: "Когда сессия истекла" });
    check("истёкшая сессия при сохранении уводит на вход",
        expired.navigations.includes("login"), JSON.stringify(expired.navigations));
    // Текст берётся от сервера как есть (политика describe в core/api-client.js):
    // страница показывает «unauthorized», а не русскую формулировку. Это известный
    // gap — перевод 401/403 в транспортном слое не сделан; проверка закрепляет
    // текущее поведение, а не желаемое.
    check("текст ошибки 401 пришёл от сервера без выдуманного перевода",
        expiredSubmit.message === "unauthorized", JSON.stringify(expiredSubmit.message));
    check("ошибочный стиль у 401 сохранён", /error/.test(expiredSubmit.messageType), expiredSubmit.messageType);
    check("навигация одна — редирект не зацикливается", expired.navigations.length === 1,
        JSON.stringify(expired.navigations));
    check("после редиректа кнопка не осталась в is-loading",
        !button(expired, "resourceForm").disabled && !button(expired, "resourceForm").classList.contains("is-loading"));
    check("401 при сохранении не роняет обработчик", expiredSubmit.thrown === null,
        expiredSubmit.thrown ? expiredSubmit.thrown.message : "");

    check("ни одна отправка не оставила необработанного отклонения", rejections.length === 0,
        JSON.stringify(rejections));

    const failed = results.filter((r) => !r.ok);    console.log(`\nИТОГ: ${results.length - failed.length}/${results.length} проверок пройдено`);
    for (const p of [guest, student, page, failing, events, expired]) p.window.close();
    process.exit(failed.length ? 1 : 0);
})().catch((error) => {
    console.error("СБОЙ ПРОВЕРКИ:", error.message || error);
    process.exit(2);
});
