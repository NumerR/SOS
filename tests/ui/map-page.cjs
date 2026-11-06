// Приёмка страницы /map (ADR-0041): два слоя поверх одного справочника мест.
//
// Зачем отдельный сьют: карта рисует чужая библиотека, и ни Go-тест, ни
// check-frontend.mjs не видят того, что решает страница, — что встанет маркером,
// а что останется строкой текста, почему отказ одного слоя не гасит второй, и
// что происходит, когда подложки или самой библиотеки нет. Всё это — ветки кода,
// а не вёрстка.
//
// Leaflet здесь подменён двойником (makeLeaflet). Двойник нужен не ради
// скорости, а ради того, чтобы проверять наши решения: настоящий Leaflet
// уткнётся в контейнер нулевого размера (в jsdom раскладки нет), и единственный
// вывод из прогона был бы «библиотека не упала». Двойник записирует ровно то, о
// чём страница договорилась с библиотекой: какие координаты переданы, сколько раз
// пересоздан тайловый слой, сколько вызовов fitBounds, чем именно едет камера на
// выбранную точку и что попало в removeLayer. Саму библиотеку и настоящую
// плавность покрывает живой прогон браузером, а не этот файл.
//
// Что проверяется и почему этого не даёт никакой другой слой:
//   • страница под сессией, но права на неё нет, и ни одного адреса тайл-сервера
//     в статике: подложка приходит с сервера, и страница обязана взять именно её;
//   • маркер получает только строка с реальной координатой, а всё остальное
//     называется причиной по серверному словарю состояний;
//   • слои грузятся независимо: 503 источника расписания оставляет слой событий
//     живым, и каждый отказ подписан своей русской фразой, а не голым текстом
//     сервера (он английский);
//   • период карты — тот же отрезок, что у расписания: понедельник 00:00:00 —
//     воскресенье 23:59:59, и оба слоя просят именно его;
//   • список пар внутри корпуса — второй запрос по клику, а не при открытии;
//   • нажатие на точку или на строку пары едет к ней и обводит ровно её одну:
//     выбор живёт на перерисовках, а просьба убрать движение меняет перелёт на
//     мгновенную установку кадра;
//   • адрес /map?building=… принесённый из расписания: неделя берётся из ссылки,
//     общий кадр перед перелётом не показывается, а несостоявшийся переход
//     называется словами, а не молчанием;
//   • без Leaflet и без подложки страница не падает и не молчит.
//
// Тест полностью офлайновый: fetch подменяется роутером-заглушкой с фикстурами
// той же формы, что отдают /api/schedule/map и /api/events/map. Сервер не нужен.
//
// Запуск: cd tests/ui && node map-page.cjs
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const REPO = path.resolve(__dirname, "..", "..");
const BASE = "http://localhost:8080";

// Часы прогона фиксируются до исполнения скриптов страницы: неделя считается из
// «сегодня» при старте, без этого проверка подписи и параметров зависела бы от
// даты запуска. Среда — 4 ноября 2026, середина недели 2–8 ноября.
const FIXED = new Date(2026, 10, 4, 12, 0, 0, 0).getTime();

// Период ожидаем мы, а не страница: границы недели собраны здесь из локальных
// дат независимо, иначе проверка weekRange превратилась бы в самоцитату.
const EXPECT_FROM = Math.floor(new Date(2026, 10, 2, 0, 0, 0, 0).getTime() / 1000);
const EXPECT_TO = Math.floor(new Date(2026, 10, 8, 23, 59, 59, 0).getTime() / 1000);

const results = [];
function check(name, ok, detail = "") {
    results.push({ name, ok, detail });
    console.log(`${ok ? "PASS" : "FAIL"}  ${name}${detail ? " — " + detail : ""}`);
}
function wait(ms) { return new Promise((r) => setTimeout(r, ms)); }

// Необработанное отклонение = баг страницы, а не теста (см. schedule-forms.cjs).
const rejections = [];
process.on("unhandledRejection", (reason) => rejections.push(String((reason && reason.message) || reason)));

const BUNDLE = ["config.js", "core/api-client.js", "core/format.js", "core/layout.js", "common.js"];

function json(body, status = 200) {
    return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}
function apiError(status, message) {
    return json({ error: { code: "e", status, message, fields: {} } }, status);
}

function splitUrl(url) {
    const clean = String(url).replace(BASE, "");
    const i = clean.indexOf("?");
    return { pathname: i === -1 ? clean : clean.slice(0, i), params: new URLSearchParams(i === -1 ? "" : clean.slice(i + 1)) };
}

// ---------- фикстуры: тот же набор полей, что отдают серверы ----------

const TILE_URL = "https://tiles.example.test/{z}/{x}/{y}.png";
const CONFIG = { tiles: { url: TILE_URL, attribution: "Тестовый кампус" } };
const CONFIG_EMPTY = { tiles: null, problem: "MAP_TILES_URL" };

const USER = { id: 7, username: "iprof", email: "i@example.test", full_name: "Иванова Мария", role: "teacher" };

function place(name, lat, lon) { return { name, lat, lon }; }
function building(name, state, point, count, days) {
    return { building: name, place_state: state, place: point || null, lesson_count: count, days: days || [] };
}
function lessonRow(n) {
    return {
        external_id: "x" + n, lesson_number: (n % 6) + 1, time: "08:30 – 10:00",
        discipline: "Предмет " + n, lesson_type: "Лк", room: "301", group_name: "Б-ИСиТ-22",
        date: "02.11.2026", day_key: 20261102,
    };
}

// Сводка слоя «пары по корпусам»: у строки без координаты `place` обязан быть
// null — это договор с сервером (internal/handlers/map_state.go), и страница не
// имеет права дорисовывать точку там, где её нет.
function scheduleMap(payload) {
    return Object.assign({
        source: "apeks",
        source_name: "АПЭКС-ВУЗ",
        range: { from: EXPECT_FROM, to: EXPECT_TO },
        group: null,
        buildings: [
            building("3", "resolved", place("Корпус 3", 56.989, 60.576), 12, [
                { day_key: 20261102, date: "02.11.2026", lesson_count: 3 },
                { day_key: 20261104, date: "04.11.2026", lesson_count: 2 },
            ]),
            building("5", "unresolved", null, 4, [{ day_key: 20261103, date: "03.11.2026", lesson_count: 4 }]),
            building("1", "resolved", place("Главный корпус", 56.995, 60.59), 1, [
                { day_key: 20261106, date: "06.11.2026", lesson_count: 1 },
            ]),
        ],
        unplaced: {
            lesson_count: 11,
            reasons: [
                { reason: "no_building", lesson_count: 5, rooms: ["актовый зал", "без ауд."] },
                { reason: "several", lesson_count: 2, rooms: ["3"] },
                { reason: "unresolved", lesson_count: 4, rooms: ["512"] },
            ],
        },
        updated_at: 1761300000,
    }, payload || {});
}

// Тот же путь, но с building=: сводка сужена до одного корпуса, и к нему
// приложены строки пар. Это ответ на клик по маркеру, а не на открытие страницы.
function scheduleDetail(payload) {
    const lessons = [];
    for (let i = 1; i <= 15; i++) lessons.push(lessonRow(i));
    return Object.assign({
        source: "apeks",
        source_name: "АПЭКС-ВУЗ",
        range: { from: EXPECT_FROM, to: EXPECT_TO },
        group: null,
        buildings: [Object.assign(
            building("3", "resolved", place("Корпус 3", 56.989, 60.576), 15,
                [{ day_key: 20261102, date: "02.11.2026", lesson_count: 5 }]),
            { lessons },
        )],
        unplaced: { lesson_count: 0, reasons: [] },
        updated_at: 1761300000,
    }, payload || {});
}

function eventsMap(payload) {
    return Object.assign({
        range: { from: EXPECT_FROM, to: EXPECT_TO },
        places: [
            {
                place_id: 7, name: "Актовый зал", place_state: "resolved",
                place: place("Актовый зал", 56.991, 60.581), event_count: 2,
                events: [
                    { id: 11, title: "Концерт", category: "Культура", event_date: 1762070400 },
                    { id: 12, title: "День открытых дверей", category: "", event_date: 1762156800 },
                ],
            },
            {
                place_id: 9, name: "Библиотека", place_state: "resolved",
                place: place("Библиотека", 56.993, 60.584), event_count: 1,
                events: [{ id: 13, title: "Выставка", category: "Культура", event_date: 1762243200 }],
            },
            {
                place_id: 12, name: "Студклуб", place_state: "unresolved",
                place: null, event_count: 3,
                events: [{ id: 14, title: "Квиз", category: "Студенчество", event_date: 1762329600 }],
            },
        ],
        unplaced: {
            event_count: 6,
            reasons: [
                { reason: "no_date", event_count: 1, titles: ["Собрание"] },
                { reason: "no_place", event_count: 3, titles: ["Онлайн-встреча", "Ярмарка вакансий"] },
                { reason: "missing", event_count: 2, titles: [] },
            ],
        },
    }, payload || {});
}

// ---------- двойник Leaflet ----------

function newLog() {
    return {
        zoom: 12, maps: [], tiles: [], groups: [], markers: [], bounds: [],
        fitBounds: [], setView: [], flyTo: [], removed: [], added: [], cleared: [],
        scales: [], zoomControls: [],
    };
}

function stubElement() {
    const attrs = {};
    const classes = new Set();
    // У SVG-фигуры маркера в Leaflet есть и атрибуты, и classList: страница
    // вычитает фигуру из порядка Tab атрибутом и обводит выбранную точку
    // классом. Заглушка переживает оба вызова, но не притворяется живым
    // документом — навешивать стили и рисовать раскладку здесь незачем, а
    // настоящая фокусируемость и плавность проверяет браузер
    // (scripts/check-focus.mjs и живой прогон).
    return {
        attrs,
        classes,
        setAttribute(name, value) { attrs[name] = String(value); },
        getAttribute(name) { return name in attrs ? attrs[name] : null; },
        classList: {
            toggle(name, on) {
                if (on) classes.add(name); else classes.delete(name);
                return classes.has(name);
            },
            contains: (name) => classes.has(name),
        },
    };
}

function makeLeaflet(log) {
    // Двойник хранит подписки на moveend сам: страница открывает окно точки
    // только когда перелёт долетел, и без этого события окно в jsdom никогда
    // бы не открылось — проверка «окно открыто» молча стала бы проверкой
    // «окно не открыто».
    const handlers = { moveend: [] };
    const fireMoveEnd = () => {
        const list = handlers.moveend.splice(0, handlers.moveend.length);
        for (const fn of list) fn();
    };
    const mapObj = {
        fitBounds(bounds, opts) { log.fitBounds.push({ bounds, opts }); },
        setView(latlng, zoom) { log.setView.push({ latlng, zoom }); },
        // flyTo — то же перемещение камеры, но с анимацией. Двойник пишет его
        // отдельно: «плавный перелёт» и «мгновенный прыжок» — разный договор
        // со страницей, и подмена одного другим должна быть видна проверке.
        // moveend приходит асинхронно, как в живом Leaflet: до посадки окно
        // не должно открываться.
        flyTo(latlng, zoom, opts) {
            log.flyTo.push({ latlng, zoom, opts });
            setTimeout(fireMoveEnd, 0);
        },
        on(event, fn) { (handlers[event] = handlers[event] || []).push(fn); },
        once(event, fn) { (handlers[event] = handlers[event] || []).push(fn); },
        off(event, fn) {
            const list = handlers[event] || [];
            const at = list.indexOf(fn);
            if (at >= 0) list.splice(at, 1);
        },
        getZoom: () => log.zoom,
        // Живая карта отвечает ceiling по подложке; здесь подложки нет, и
        // двойник отдаёт то же, что отдаёт Leaflet без слоёв.
        getMaxZoom: () => Infinity,
        removeLayer(layer) { log.removed.push(layer); },
        addLayer(layer) { log.added.push(layer); },
    };

    return {
        map(el, opts) { log.maps.push({ el, opts }); return mapObj; },
        latLngBounds(points) { log.bounds.push(points); return { points }; },
        control: {
            scale(opts) { return { addTo(m) { log.scales.push({ opts, m }); return this; } }; },
            zoom(opts) { return { addTo(m) { log.zoomControls.push({ opts, m }); return this; } }; },
        },
        tileLayer(url, opts) {
            return {
                url, options: opts,
                addTo(m) { log.tiles.push(this); this.onMap = m; return this; },
            };
        },
        layerGroup() {
            return {
                layers: [],
                // Повторное addTo — это возврат слоя на карту чипом, а не вторая
                // группа: индекс группы в логе должен пережить выключение слоя.
                addTo(m) {
                    if (!log.groups.includes(this)) log.groups.push(this);
                    this.onMap = m;
                    log.added.push(this);
                    return this;
                },
                clearLayers() { log.cleared.push(this); this.layers.length = 0; return this; },
            };
        },
        circleMarker(latlng, opts) {
            const handlers = {};
            const marker = {
                latlng,
                options: opts || {},
                // Каждый setStyle запоминается: проверка читает последний, а
                // число записей говорит, не перекрашивается ли точка лишний раз.
                styles: [],
                group: null,
                opened: 0,
                popup: null,
                bindTooltip(title, o) { marker.tooltip = { title, o }; return marker; },
                bindPopup(fn, o) {
                    marker.popup = fn; marker.popupOptions = o;
                    // Живой Leaflet на bindPopup вешает на точку собственный
                    // обработчик click → _openPopup: окно раскрывается сразу
                    // по нажатию, посреди перелёта, и его автопан зовёт
                    // moveend, который съедает единственный обработчик
                    // посадки страницы (так и случилось в живом Chromium:
                    // окно осталось отрезано краем карты). Двойник моделирует
                    // именно этот обработчик — иначе страница могла бы его не
                    // снимать вовсе, и проверка не отличила бы снятие от
                    // его отсутствия.
                    marker._openPopup = function () { marker.opened += 1; };
                    (handlers.click = handlers.click || []).push(marker._openPopup);
                    return marker;
                },
                // Снятие по ссылке, как в библиотеке: off(type, fn, context).
                off(event, fn) {
                    const list = handlers[event] || [];
                    const at = list.indexOf(fn);
                    if (at >= 0) list.splice(at, 1);
                    return marker;
                },
                addTo(group) { marker.group = group; group.layers.push(marker); return marker; },
                getLatLng: () => latlng,
                // Leaflet отдаёт DOM-фигуру маркера, и страница вычитает её из
                // порядка Tab и навешивает на неё обводку. Заглушка переживает
                // этот вызов, не притворяясь живым SVG: настоящая фокусируемость
                // проверяется браузером (scripts/check-focus.mjs), здесь — сам
                // вызов и его аргументы.
                getElement() { return marker.el || (marker.el = stubElement()); },
                openPopup() { marker.opened += 1; return marker; },
                // setStyle в библиотеке сливает опции, а не заменяет их: без
                // этого выбора точка теряла бы радиус и цвет заливки.
                setStyle(options) {
                    marker.styles.push(options);
                    marker.options = Object.assign({}, marker.options, options);
                    return marker;
                },
                on(event, fn) { (handlers[event] = handlers[event] || []).push(fn); return marker; },
                fire(event) { for (const fn of handlers[event] || []) fn(); return marker; },
                // Окно точки живёт своей жизнью: страница после приезда строк
                // переспрашивает автопан вызовом getPopup().setLatLng(той же
                // точки). Заглушка считает эти вызовы — «окно не обрезано» в
                // браузере иначе как проверить, нечем.
                pans: [],
                getPopup() {
                    if (!marker.popupBox) {
                        marker.popupBox = {
                            isOpen: () => marker.opened > 0,
                            getLatLng: () => latlng,
                            setLatLng(where) { marker.pans.push(where); return marker.popupBox; },
                        };
                    }
                    return marker.popupBox;
                },
            };
            log.markers.push(marker);
            return marker;
        },
    };
}

// initMap создаёт группы в порядке «пары, события». Это не догадка, а предмет
// отдельной проверки ниже; здесь порядок только именуется, чтобы остальной файл
// не зависел от цифр.
const lessonsGroup = (log) => log.groups[0];
const eventsGroup = (log) => log.groups[1];
function markersIn(log, kind) {
    const group = kind === "lessons" ? lessonsGroup(log) : eventsGroup(log);
    return group ? group.layers : [];
}
function markerOf(log, kind, title) {
    return markersIn(log, kind).find((m) => m.tooltip && m.tooltip.title === title);
}
// Точки, лежащие на карте прямо сейчас: log.markers копит и снятые слои, а
// выбор судится по живым.
function drawnMarkers(log) {
    return markersIn(log, "lessons").concat(markersIn(log, "events"));
}
// Последний setStyle точки — это и есть её текущий вид: обводка выбирается
// вызовом страницы, а не классом CSS, и проверить «выделена ровно одна» без
// него нельзя.
function styleOf(marker) {
    return marker && marker.styles.length ? marker.styles.slice(-1)[0] : {};
}
const isFocusedStyle = (m) => styleOf(m).weight === 4;
function focusedMarkers(log) {
    return drawnMarkers(log).filter((m) => m.el && m.el.classes.has("map-marker-focus"));
}

// ---------- загрузка страницы ----------

function frozenDate(window) {
    const Real = window.Date;
    class Frozen extends Real {
        constructor(...args) { super(...(args.length ? args : [FIXED])); }
        static now() { return FIXED; }
    }
    window.Date = Frozen;
}

function boot({ routes = {}, config = CONFIG, leaflet = true, user = USER, search = "" } = {}) {
    const html = fs.readFileSync(path.join(REPO, "web", "map.html"), "utf8");
    const dom = new JSDOM(html, { url: BASE + "/map" + search, runScripts: "outside-only", pretendToBeVisual: true });
    const { window } = dom;

    frozenDate(window);
    const log = newLog();
    if (leaflet) window.L = makeLeaflet(log);

    const calls = [];
    const hits = {};

    window.fetch = (input, init = {}) => {
        const method = String(init.method || "GET").toUpperCase();
        const { pathname, params } = splitUrl(input);
        const key = method + " " + pathname;
        hits[key] = (hits[key] || 0) + 1;
        const record = { key, pathname, params, search: params.toString(), building: params.get("building") };
        calls.push(record);

        const rule = routes[key];
        const decided = typeof rule === "function" ? rule({ record, hits }) : rule;
        if (decided !== undefined) return Promise.resolve(decided);
        return Promise.resolve(defaultBody(key, record, config, user));
    };

    window.document.cookie = "csrf_token=test-csrf-token";
    window.eval(BUNDLE.concat(["map.js"]).map((f) => fs.readFileSync(path.join(REPO, "web/js", f), "utf8")).join("\n;\n"));

    const navigations = [];
    window.SOS_CONFIG.navigateTo = (target) => navigations.push(target);

    return { window, doc: window.document, log, calls, hits, navigations };
}

function defaultBody(key, record, config, user) {
    if (key === "GET /api/auth/me") return json(user);
    if (key === "GET /api/map/config") return json(config);
    if (key === "GET /api/schedule/map") return json(record.building ? scheduleDetail() : scheduleMap());
    if (key === "GET /api/events/map") return json(eventsMap());
    return json({});
}

// ---------- помощники ----------

function text(doc, id) {
    const el = doc.getElementById(id);
    return el ? el.textContent.replace(/\s+/g, " ").trim() : "";
}
function visible(doc, id) {
    const el = doc.getElementById(id);
    return !!el && !el.classList.contains("hidden");
}
function rows(doc, id) {
    const el = doc.getElementById(id);
    return el ? Array.from(el.children) : [];
}
function rowText(doc, id) {
    return rows(doc, id).map((r) => r.textContent.replace(/\s+/g, " ").trim());
}
// Строка списка — несколько span-ов, разведённых flex- или grid-раскладкой.
// textContent склеивает их без разделителя («4 пары» + «512» → «4 пары512»),
// поэтому содержимое сравнивается по детям: ровно так его видит человек.
function rowParts(doc, id) {
    return rows(doc, id).map((r) => Array.from(r.children)
        .map((c) => c.textContent.replace(/\s+/g, " ").trim())
        .filter((t) => t !== ""));
}
function findRow(doc, id, needle) {
    return rows(doc, id).find((r) => needle.test(r.textContent));
}
// Название строки лежит в отдельном span: искать его по склеенному textContent
// нельзя — «5» и «4 пары» дают «54 пары», и число попадает в заголовок.
function findRowByTitle(doc, id, title) {
    return rows(doc, id).find((r) => {
        const cell = r.querySelector(".map-row-title");
        return !!cell && cell.textContent.trim() === title;
    });
}
function find(calls, key) { return calls.filter((c) => c.key === key); }
function click(doc, id) {
    const el = doc.getElementById(id);
    el.dispatchEvent(new el.ownerDocument.defaultView.Event("click", { bubbles: true }));
    return el;
}
function toggle(doc, window, id, value) {
    const el = doc.getElementById(id);
    el.checked = value;
    el.dispatchEvent(new window.Event("change", { bubbles: true }));
    return el;
}

async function until(predicate, tries = 100) {
    for (let i = 0; i < tries; i++) {
        if (predicate()) return true;
        await wait(15);
    }
    return predicate();
}

// Скрипты страницы слушают DOMContentLoaded, и он наступает сам после разбора
// документа — вручную его не.dispatch (это проверяет десятая группа
// scripts/check-frontend.mjs).
async function ready(page) {
    await until(() => page.hits["GET /api/schedule/map"] && page.hits["GET /api/events/map"]);
    await wait(30);
}

(async () => {
    // ---------- 1. страница вошедшего ----------

    const main = boot();
    await ready(main);

    check("страница спросила сессию", main.hits["GET /api/auth/me"] === 1, JSON.stringify(main.hits));
    check("шапка заполнена из /api/auth/me", text(main.doc, "userName") === "Иванова Мария", text(main.doc, "userName"));
    check("роль подписана по-русски", text(main.doc, "userRole") === "преподаватель", text(main.doc, "userRole"));
    check("аватар — инициалы", text(main.doc, "userAvatar") === "ИМ", text(main.doc, "userAvatar"));
    check("вошедшего страница не уводит на вход", main.navigations.length === 0, JSON.stringify(main.navigations));
    check("оба слоя запрошены по одному разу",
        main.hits["GET /api/schedule/map"] === 1 && main.hits["GET /api/events/map"] === 1,
        JSON.stringify(main.hits));

    // ---------- 2. подложка ----------

    check("подложка спрошена у сервера", main.hits["GET /api/map/config"] === 1);
    check("групп слоёв ровно две", main.log.groups.length === 2, `${main.log.groups.length}`);
    check("первая группа — слой пар, вторая — событий",
        markerOf(main.log, "lessons", "3") !== undefined && markerOf(main.log, "events", "Актовый зал") !== undefined,
        `пары: ${markersIn(main.log, "lessons").map((m) => m.tooltip.title).join(",")}, ` +
        `события: ${markersIn(main.log, "events").map((m) => m.tooltip.title).join(",")}`);
    check("тайловый слой создан один раз", main.log.tiles.length === 1, `${main.log.tiles.length}`);
    check("адрес подложки взят ровно из ответа сервера",
        main.log.tiles[0].url === TILE_URL, main.log.tiles.map((t) => t.url).join(", "));
    check("подпись провайдера передана в Leaflet",
        main.log.tiles[0].options.attribution === "Тестовый кампус", JSON.stringify(main.log.tiles[0].options));
    check("подпись провайдера показана под картой", /Тестовый кампус/.test(text(main.doc, "tilesNote")),
        text(main.doc, "tilesNote"));
    check("масштабная линейка без имперских миль",
        main.log.scales.length === 1 && main.log.scales[0].opts.imperial === false);

    // «Центр кампуса не выдуман» — то, чего глазом на карте не видно: на пустом
    // фоне стартовая точка не отличается от правдоподобной.
    const start = main.log.maps[0].opts;
    check("стартовый центр — нейтральная постройка, а не координата",
        String(start.center) === "0,0" && start.zoom === 1,
        JSON.stringify({ center: start.center, zoom: start.zoom }));
    check("колесо мыши не отдаётся карте", start.scrollWheelZoom === false);
    // Оба свойства живут в одном месте: Tab-порядок страницы меряет живой
    // браузер (scripts/check-focus.mjs), и именно он нашёл английские подписи
    // контрола масштаба и ссылку библиотеки в порядке обхода. Здесь они
    // проверяются потому, что офлайновый прогон видит настройки, а браузер —
    // следствия.
    check("масштаб задаёт контрол страницы с русскими подписями",
        main.log.zoomControls.length === 1 &&
        main.log.zoomControls[0].opts.zoomInTitle === "Приблизить" &&
        main.log.zoomControls[0].opts.zoomOutTitle === "Отдалить",
        JSON.stringify(main.log.zoomControls.map((z) => z.opts)));
    check("стандартный контрол масштаба и подпись библиотеки выключены",
        start.zoomControl === false && start.attributionControl === false,
        JSON.stringify({ zoomControl: start.zoomControl, attributionControl: start.attributionControl }));
    check("карта села в контейнер из разметки", main.log.maps[0].el === main.doc.getElementById("mapCanvas"));

    // ---------- 3. точки ----------

    check("маркеров ровно по числу строк с координатой", main.log.markers.length === 4,
        main.log.markers.map((m) => m.tooltip.title).join(", "));
    check("корпус без координаты не стал маркером", !markerOf(main.log, "lessons", "5"));
    check("место без координаты не стало маркером", !markerOf(main.log, "events", "Студклуб"));
    const coords = main.log.markers.map((m) => m.latlng.join(",")).sort();
    check("в карту попали только координаты из places",
        coords.join("|") === "56.989,60.576|56.991,60.581|56.993,60.584|56.995,60.59", coords.join("|"));
    check("кадр посчитан один раз по найденным точкам",
        main.log.fitBounds.length === 1 && main.log.bounds.length === 1 && main.log.bounds[0].length === 4,
        `fitBounds=${main.log.fitBounds.length}, bounds=${JSON.stringify(main.log.bounds)}`);
    // Потолок общего кадра обязан быть ниже масштаба фокуса (17): живой
    // Chromium показал, что при 16 кампус умещается целиком, и перелёт к
    // корпусу не показывал приближения — «фокус» сводился к сдвигу картинки.
    check("общий кадр не дотягивается до масштаба фокуса",
        main.log.fitBounds[0].opts.maxZoom < 17,
        JSON.stringify(main.log.fitBounds[0].opts));
    // Порядок отрисовки — вопрос кликабельности, а не косметики. Точка события
    // лежит на той же координате, что корпус, и всегда меньше его окружности
    // (радиус считается от числа позиций), поэтому при обратном порядке клик
    // по событию доставался слою пар — это измерен живым Chromium, где под
    // курсором всякий раз оказывался путь слоя «пары».
    check("пары рисуются первыми, события — поверх них",
        main.log.markers.slice(0, 2).every((m) => m.group !== eventsGroup(main.log)) &&
        main.log.markers.slice(-2).every((m) => m.group === eventsGroup(main.log)),
        main.log.markers.map((m) => `${m.group === eventsGroup(main.log) ? "событие:" : "корпус:"}${m.tooltip.title}`).join(", "));
    // И у окна есть потолок высоты: без него список пар на 86 позиций
    // раздувался до 1340px при кадре 440px, и автопан утаскивал камеру на
    // 145м севернее выбранного корпуса сразу после посадки.
    const unboxed = main.log.markers.filter((m) => !m.popupOptions || !m.popupOptions.maxHeight).length;
    check("всплывающее окно точки ограничено по высоте", unboxed === 0,
        `${unboxed} из ${main.log.markers.length} маркеров без maxHeight`);

    const big = markerOf(main.log, "lessons", "3");
    const small = markerOf(main.log, "lessons", "1");
    check("радиус растёт с числом пар и имеет потолок",
        big.options.radius > small.options.radius && big.options.radius <= 22,
        `12 пар → ${big.options.radius}, 1 пара → ${small.options.radius}`);
    // Тот же Tab-порядок: у SVG-фигуры имени нет, и обход клавиатурой давал
    // безымянную остановку на каждую точку. Одной опции мало — Chromium считает
    // фокусными сами фигуры, поэтому страница вычитает их явно (проверено
    // живым обходом: cursor, pointer-events и aria-hidden не помогали, помог
    // только tabindex=-1). Кликабельность от этого не страдает, а список ниже
    // называет то же действие кнопкой.
    const untabbed = main.log.markers.filter((m) => m.options.keyboard === false
        && m.el && m.el.attrs.tabindex === "-1").length;
    check("маркеры не добавляют в Tab-порядок безымянные остановки",
        untabbed === main.log.markers.length,
        `${untabbed} из ${main.log.markers.length}; `
        + main.log.markers.map((m) => `${m.tooltip.title}:${m.options.keyboard}/`
            + (m.el ? m.el.attrs.tabindex : "нет фигура")).join(", "));

    // Два маркера одного слоя обязаны различаться размером, а не цветом, и
    // наоборот: цвет несёт принадлежность слою, обводка — общий токен текста.
    // Сравнение внутри одного слоя ничего бы не проверяло: там цвета равны по
    // замыслу.
    const eventsMarker = markerOf(main.log, "events", "Актовый зал");
    check("цвета маркеров читаются из токенов темы",
        big.options.fillColor !== eventsMarker.options.fillColor &&
        big.options.color === eventsMarker.options.color &&
        big.options.color === small.options.color,
        JSON.stringify({ пары: big.options, событие: eventsMarker.options }));

    // ---------- 4. период недели ----------

    const lessonCall = find(main.calls, "GET /api/schedule/map")[0];
    const eventCall = find(main.calls, "GET /api/events/map")[0];
    check("слой пар начинается понедельником", Number(lessonCall.params.get("from")) === EXPECT_FROM,
        `from=${lessonCall.params.get("from")}, ждали ${EXPECT_FROM}`);
    check("слой пар заканчивает воскресеньем", Number(lessonCall.params.get("to")) === EXPECT_TO,
        `to=${lessonCall.params.get("to")}, ждали ${EXPECT_TO}`);
    check("оба слоя просят один и тот же отрезок", lessonCall.search === eventCall.search,
        `пары: ${lessonCall.search} · события: ${eventCall.search}`);
    check("подпись недели — «2–8 ноября»", text(main.doc, "mapWeekLabel") === "2–8 ноября",
        text(main.doc, "mapWeekLabel"));
    check("строка состояния называет оба слоя",
        /Пары: 2 точки, вне кадра 11 позиций/.test(text(main.doc, "mapStatus")) &&
        /События: 2 точки, вне кадра 6 позиций/.test(text(main.doc, "mapStatus")),
        text(main.doc, "mapStatus"));

    click(main.doc, "mapPrevWeek");
    await until(() => find(main.calls, "GET /api/schedule/map").length >= 2);
    await wait(30);
    const prev = find(main.calls, "GET /api/schedule/map").slice(-1)[0];
    check("сдвиг назад — ровно семь суток", EXPECT_FROM - Number(prev.params.get("from")) === 7 * 86400,
        `from=${prev.params.get("from")}, дельта=${EXPECT_FROM - Number(prev.params.get("from"))}`);
    check("неделя через границу месяцев подписана двумя месяцами",
        text(main.doc, "mapWeekLabel") === "26 октября – 1 ноября", text(main.doc, "mapWeekLabel"));
    check("кадр пересчитан после переезда на другую неделю", main.log.fitBounds.length === 2,
        `${main.log.fitBounds.length}`);

    click(main.doc, "mapCurrentWeek");
    await until(() => find(main.calls, "GET /api/schedule/map").length >= 3);
    await wait(30);
    const back = find(main.calls, "GET /api/schedule/map").slice(-1)[0];
    check("«Текущая неделя» вернула тот же период",
        Number(back.params.get("from")) === EXPECT_FROM && Number(back.params.get("to")) === EXPECT_TO,
        back.search);
    check("возврат к текущей неделе не пересоздал подложку", main.log.tiles.length === 1);

    // «Обновить» на том же наборе точек кадр не двигает: человек мог отъехать
    // глазами, и возвращать его насильно — значит игнорировать его выбор.
    const before = main.log.fitBounds.length;
    click(main.doc, "mapRefresh");
    await until(() => find(main.calls, "GET /api/schedule/map").length >= 4);
    await wait(30);
    check("обновление на том же периоде не двигает кадр", main.log.fitBounds.length === before,
        `было ${before}, стало ${main.log.fitBounds.length}`);
    check("обновление не добавило тайловых слоёв", main.log.tiles.length === 1);

    // ---------- 5. во время загрузки карта не додумывает ----------

    const slow = boot({ routes: { "GET /api/events/map": () => new Promise((r) => setTimeout(() => r(json(eventsMap())), 120)) } });
    await wait(25);
    check("пока слой грузится, статус говорит о загрузке", text(slow.doc, "mapStatus") === "Загрузка слоёв…",
        text(slow.doc, "mapStatus"));
    check("и карта не утверждает, что рисовать нечего", !visible(slow.doc, "mapNotice"),
        text(slow.doc, "mapNotice"));
    // ready() считает вызовы fetch, а этот маршрут отвечает с задержкой:
    // ждать надо нарисованный маркер, а не факт запроса.
    await until(() => markersIn(slow.log, "events").length === 2);
    check("досчитав, слой событий нарисован", markersIn(slow.log, "events").length === 2,
        `${markersIn(slow.log, "events").length}`);
    check("досчитав, статус перестал говорить о загрузке",
        !/^Загрузка/.test(text(slow.doc, "mapStatus")), text(slow.doc, "mapStatus"));

    // ---------- 6. слои независимы ----------

    const noSource = boot({ routes: { "GET /api/schedule/map": apiError(503, "schedule source storage is not connected") } });
    await ready(noSource);
    check("отказ слоя пар не гасит слой событий", markersIn(noSource.log, "events").length === 2,
        `${markersIn(noSource.log, "events").length}`);
    check("503 источника назван состоянием сборки, а не текстом сервера",
        /Пары: слой не подключён в этой сборке/.test(text(noSource.doc, "mapStatus")), text(noSource.doc, "mapStatus"));
    check("английская строка сервера не показалась на странице",
        !/source storage/i.test(noSource.doc.body.textContent));
    check("список пар назвал причину",
        /Слой не ответил: слой не подключён в этой сборке/.test(rowText(noSource.doc, "mapLessonsList").join(" ")),
        rowText(noSource.doc, "mapLessonsList").join(" | "));
    check("отказ не оставил слой в состоянии «загрузка»",
        !/Загрузка…/.test(rowText(noSource.doc, "mapLessonsList").join(" ")));
    check("несмотря на отказ, слой был запрошен", noSource.hits["GET /api/schedule/map"] === 1);

    const broken = boot({ routes: { "GET /api/events/map": apiError(500, "internal") } });
    await ready(broken);
    check("сбой сервера у одного слоя назван кодом и журналом",
        /События: сбой сервера \(500\), причина в его журнале/.test(text(broken.doc, "mapStatus")),
        text(broken.doc, "mapStatus"));
    check("при этом слой пар нарисован", markersIn(broken.log, "lessons").length === 2,
        `${markersIn(broken.log, "lessons").length}`);

    const badPeriod = boot({ routes: { "GET /api/events/map": () => apiError(422, "from must be YYYY-MM-DD") } });
    await ready(badPeriod);
    check("422 периода сохранён дословно: это про данные, а не про сборку",
        /сервер не принял период \(from must be YYYY-MM-DD\)/.test(text(badPeriod.doc, "mapStatus")),
        text(badPeriod.doc, "mapStatus"));

    const bothDown = boot({
        routes: {
            "GET /api/schedule/map": apiError(503, "schedule source storage is not connected"),
            "GET /api/events/map": apiError(500, "internal"),
        },
    });
    await ready(bothDown);
    check("оба отказа названы прямо",
        /Оба слоя не ответили — причины в строке состояния ниже\./.test(text(bothDown.doc, "mapNotice")),
        text(bothDown.doc, "mapNotice"));
    check("пустая карта не притворяется готовой", visible(bothDown.doc, "mapNotice"));

    const noConfig = boot({ routes: { "GET /api/map/config": apiError(500, "internal") } });
    await ready(noConfig);
    // Смысл проверки именно в паре: строка обязана назвать сбой конфига и НЕ
    // обязана врать про переменную окружения — «подложку задаёт MAP_TILES_URL»
    // означало бы «оператор ничего не задал», хотя мы этого не знаем.
    check("сбой конфига отделён от «подложки нет»",
        /конфиг подложки не получен: сбой сервера \(500\)/.test(text(noConfig.doc, "mapStatus")) &&
        !/подложку задаёт/.test(text(noConfig.doc, "mapStatus")),
        text(noConfig.doc, "mapStatus"));
    check("подпись под картой не притворяется, что подложки нет",
        !/не настроена/.test(text(noConfig.doc, "tilesNote")), text(noConfig.doc, "tilesNote"));
    check("без конфига слои всё равно загружены", markersIn(noConfig.log, "lessons").length === 2,
        `${markersIn(noConfig.log, "lessons").length}`);
    check("без конфига тайловый слой не создан", noConfig.log.tiles.length === 0);

    // ---------- 7. подложки нет — сборка живая ----------

    const bare = boot({ config: CONFIG_EMPTY });
    await ready(bare);
    check("без тайлов тайловый слой не создаётся", bare.log.tiles.length === 0);
    check("подпись под картой называет отсутствие подложки",
        text(bare.doc, "tilesNote") === "подложка: не настроена", text(bare.doc, "tilesNote"));
    check("строка состояния называет переменную", /подложку задаёт MAP_TILES_URL/.test(text(bare.doc, "mapStatus")),
        text(bare.doc, "mapStatus"));
    check("без подложки точки всё равно нарисованы", bare.log.markers.length === 4, `${bare.log.markers.length}`);
    check("без подложки пояснения на карте нет", !visible(bare.doc, "mapNotice"), text(bare.doc, "mapNotice"));

    // ---------- 8. чего на карте нет ----------

    const unplacedKeys = rows(bare.doc, "mapUnplaced")
        .map((r) => (r.firstElementChild ? r.firstElementChild.className : ""));
    check("причины идут в серверном порядке состояний, а не как пришли",
        unplacedKeys.join(" ") === "map-key is-unresolved map-key is-missing map-key is-no_building " +
            "map-key is-several map-key is-no_place map-key is-no_date", unplacedKeys.join(" "));
    check("все шесть причин показаны", rows(bare.doc, "mapUnplaced").length === 6,
        `${rows(bare.doc, "mapUnplaced").length}`);
    check("итог вне кадра посчитан по обоим слоям", text(bare.doc, "unplacedTotal") === "17 позиций вне кадра",
        text(bare.doc, "unplacedTotal"));
    check("строка называет причину, слой и число",
        rowParts(bare.doc, "mapUnplaced")[0].join(" ") === "место заведено, координат нет — Пары 4 пары 512",
        rowParts(bare.doc, "mapUnplaced")[0].join(" | "));
    check("примеры аудиторий показаны рядом с причиной",
        /актовый зал · без ауд\./.test(rowText(bare.doc, "mapUnplaced").join(" | ")));
    check("названия событий показаны рядом с причиной",
        /Онлайн-встреча · Ярмарка вакансий/.test(rowText(bare.doc, "mapUnplaced").join(" | ")));
    const noExamplesRow = findRow(bare.doc, "mapUnplaced", /корпуса нет в справочнике — События/);
    check("у причины без примеров строка примеров не выдумана",
        !!noExamplesRow && noExamplesRow.querySelectorAll(".map-unplaced-examples").length === 0,
        noExamplesRow ? noExamplesRow.textContent : "строки нет");

    const single = boot({
        routes: {
            "GET /api/schedule/map": () => json(scheduleMap({
                buildings: [building("9", "resolved", place("Корпус 9", 56.9, 60.6), 1, [])],
                unplaced: { lesson_count: 1, reasons: [{ reason: "no_building", lesson_count: 1, rooms: ["спортзал"] }] },
            })),
            "GET /api/events/map": () => json(eventsMap({ places: [], unplaced: { event_count: 0, reasons: [] } })),
        },
    });
    await ready(single);
    check("одна позиция — «1 позиция вне кадра»", text(single.doc, "unplacedTotal") === "1 позиция вне кадра",
        text(single.doc, "unplacedTotal"));
    check("одна пара — «1 пара»", rowParts(single.doc, "mapLessonsList")[0].includes("1 пара"),
        rowParts(single.doc, "mapLessonsList").join(" | "));
    check("пустой слой событий назван словами, а не молчанием",
        /За этот период событий с привязанным местом нет\./.test(rowText(single.doc, "mapEventsList").join(" ")),
        rowText(single.doc, "mapEventsList").join(" | "));

    const clean = boot({
        routes: {
            "GET /api/schedule/map": () => json(scheduleMap({ unplaced: { lesson_count: 0, reasons: [] } })),
            "GET /api/events/map": () => json(eventsMap({ unplaced: { event_count: 0, reasons: [] } })),
        },
    });
    await ready(clean);
    check("пустой список причин говорит «всё названное — в кадре»",
        text(clean.doc, "unplacedTotal") === "всё названное — в кадре", text(clean.doc, "unplacedTotal"));
    check("и объясняет это словами", /Ни одна позиция не осталась без точки/.test(rowText(clean.doc, "mapUnplaced").join(" ")),
        rowText(clean.doc, "mapUnplaced").join(" | "));

    // ---------- 9. список под картой ----------

    check("список пар по числу корпусов", rows(main.doc, "mapLessonsList").length === 3,
        `${rows(main.doc, "mapLessonsList").length}`);
    check("список событий по числу мест", rows(main.doc, "mapEventsList").length === 3,
        `${rows(main.doc, "mapEventsList").length}`);
    const chips = Array.from(main.doc.querySelectorAll("#mapLessonsList .chip"))
        .map((c) => `${c.textContent.trim()}|${c.className}`);
    check("чип строки с точкой — «точка есть» и не заглушён",
        chips.some((c) => c === "точка есть|chip chip-outline"), chips.join(" · "));
    check("чип строки без координаты — пояснение приглушённым стилем",
        chips.some((c) => c === "место заведено, координат нет|chip chip-muted"), chips.join(" · "));
    check("кнопок «Показать» ровно по числу маркеров",
        main.doc.querySelectorAll("#mapLessonsList button, #mapEventsList button").length === 4,
        `${main.doc.querySelectorAll("#mapLessonsList button, #mapEventsList button").length}`);
    check("кнопка называет, что именно покажет",
        !!main.doc.querySelector('#mapLessonsList button[aria-label="Показать на карте: 3"]'));
    const noCoordRow = findRowByTitle(main.doc, "mapLessonsList", "5");
    check("у строки без координаты кнопки «Показать» нет",
        !!noCoordRow && noCoordRow.querySelectorAll("button").length === 0,
        noCoordRow ? rowParts(main.doc, "mapLessonsList").join(" | ") : "строки нет");

    const noPoints = boot({
        routes: {
            "GET /api/schedule/map": () => json(scheduleMap({
                buildings: [building("5", "unresolved", null, 4, [])],
                unplaced: { lesson_count: 4, reasons: [{ reason: "unresolved", lesson_count: 4, rooms: ["512"] }] },
            })),
        },
    });
    await ready(noPoints);
    check("слой без единой точки честно об этом пишет",
        /Точек на карте у этого слоя нет/.test(rowText(noPoints.doc, "mapLessonsList").join(" ")),
        rowText(noPoints.doc, "mapLessonsList").join(" | "));
    check("и не рисует маркеров пар", markersIn(noPoints.log, "lessons").length === 0,
        `${markersIn(noPoints.log, "lessons").length}`);
    check("кнопок «Показать» в таком слое нет",
        noPoints.doc.querySelectorAll("#mapLessonsList button").length === 0);

    // ---------- 10. нажатие наводит карту и обводит точку ----------
    //
    // Проверяется договор страницы с библиотекой, а не сама библиотека: каким
    // вызовом едет камера, куда, на какой масштаб, и что выбранная точка
    // получила яркую обводку, а остальные — нет. Плавность перелёта и живой SVG
    // судит браузер (scripts/check-focus.mjs и прогон headless Chromium);
    // подменять их здесь было бы проверкой двойника.

    const hallButton = main.doc.querySelector('#mapEventsList button[aria-label="Показать на карте: Актовый зал"]');
    hallButton.dispatchEvent(new main.window.Event("click", { bubbles: true }));
    const hall = markerOf(main.log, "events", "Актовый зал");
    // Окно не открывается до посадки: у всплывающего окна Leaflet есть
    // собственный автопан, и открытый в полёте он дотягивает кадр обратно —
    // живой Chromium показывал перелёт к корпусу, который кончился ровно там,
    // где начался. Поэтому сначала «ещё закрыто», и только потом «открыто».
    check("во время перелёта окно точки ещё закрыто", hall.opened === 0, `${hall.opened}`);
    await wait(30);
    const fly = main.log.flyTo.slice(-1)[0];
    check("кнопка повела карту плавным перелётом, а не мгновенным прыжком",
        !!fly && main.log.setView.length === 0,
        `flyTo=${main.log.flyTo.length}, setView=${main.log.setView.length}`);
    check("перелёт — к координате выбранного места", !!fly && fly.latlng.join(",") === "56.991,60.581",
        JSON.stringify(fly));
    // 17, а не «как было»: общий кадр кампуса живёт на 15 (см. потолок
    // fitBounds ниже на этой странице), и на 16 «фокус» был бы сдвигом картинки
    // без приближения — живой Chromium измерил, что кампус умещается на 16
    // целиком. Разница между общим видом и видом точки должна быть в масштабе.
    check("и минимум на уровень, где корпус виден целиком", !!fly && fly.zoom === 17,
        JSON.stringify(fly && fly.zoom));
    check("перелёт длится меньше секунды и тормозит в конце",
        !!fly && fly.opts.duration === 0.9 && fly.opts.easeLinearity === 0.25,
        JSON.stringify(fly && fly.opts));
    check("и открыла окно точки", !!hall && hall.opened === 1, `${hall && hall.opened}`);

    const selected = focusedMarkers(main.log);
    check("обведена ровно одна точка", selected.length === 1,
        drawnMarkers(main.log).map((m) => `${m.tooltip.title}:${isFocusedStyle(m)}`).join(", "));
    check("обведена именно та, по которой кликнули", selected.length === 1 && selected[0] === hall);
    const other = markerOf(main.log, "lessons", "3");
    // Обводка трогает ровно два свойства: размер точки несёт число пар, а её
    // заливка — принадлежность слою, и выбор не имеет права переписывать ни то,
    // ни другое.
    check("выбор касается только границы точки",
        Object.keys(styleOf(hall)).sort().join(",") === "color,weight",
        Object.keys(styleOf(hall)).join(","));
    check("толщина обводки растёт выбранной точке, а не всем",
        styleOf(hall).weight === 4 && styleOf(other).weight === 2,
        `${styleOf(hall).weight} против ${styleOf(other).weight}`);
    // Тем же setStyle выбирается и цвет: если обводка совпадёт с заливкой
    // любого из слоёв, выбранная точка на карте станет неотличима от
    // невыбранной — проверяется именно это, а не конкретная строчка CSS.
    check("цвет обводки не совпадает ни с одной заливкой слоя",
        styleOf(hall).color !== hall.options.fillColor &&
        styleOf(hall).color !== other.options.fillColor &&
        styleOf(hall).color !== styleOf(other).color,
        `${styleOf(hall).color} · ${hall.options.fillColor} · ${other.options.fillColor}`);

    // Выбор переживает перерисовку: пересбор точек создаёт новые объекты, и
    // без шага в drawMarkers подсветка гасла бы после смены темы или недели.
    click(main.doc, "mapRefresh");
    await until(() => find(main.calls, "GET /api/schedule/map").length >= 5);
    await wait(30);
    const afterRefresh = focusedMarkers(main.log);
    check("обновление данных не сбросило выбор", afterRefresh.length === 1 &&
        afterRefresh[0].tooltip.title === "Актовый зал",
        drawnMarkers(main.log).map((m) => m.tooltip.title).join(", "));

    // Перелёт к другой точке забирает обводку у первой: два выделения на карте
    // отвечают «выбрана», и человек не понял бы, куда он смотрит.
    markerOf(main.log, "lessons", "1").fire("click");
    await wait(30);
    check("нажатие на саму точку тоже выбирает её",
        focusedMarkers(main.log).length === 1 &&
        focusedMarkers(main.log)[0].tooltip.title === "1",
        focusedMarkers(main.log).map((m) => m.tooltip.title).join(", "));
    check("и снимает обводку с прежней точки",
        !markerOf(main.log, "events", "Актовый зал").el.classes.has("map-marker-focus"));

    // Нажатие само по себе окно не раскрывает. Живой Leaflet на bindPopup
    // вешает на точку свой click → _openPopup: он раскрывает окно посреди
    // перелёта, его автопан двигает карту, а наступивший от допана moveend
    // съедает единственный обработчик посадки страницы (живой Chromium:
    // маркер в центре кадра, а окно отрезано 69px сверху). Двойник
    // моделирует тот же обработчик, поэтому снятие видно проверке.
    const autoOpen = markerOf(main.log, "lessons", "1");
    const openedBeforeClick = autoOpen.opened;
    autoOpen.fire("click");
    check("нажатие на точку не раскрыло окно до посадки — обработчик bindPopup снят",
        autoOpen.opened === openedBeforeClick, `${autoOpen.opened} против ${openedBeforeClick}`);
    await wait(30);
    check("а посадка раскрыла его ровно один раз",
        autoOpen.opened === openedBeforeClick + 1, `${autoOpen.opened}`);

    // Быстрое нажатие догоняет предыдущее: перелёт прерывается, и окно
    // перебитой точки человек увидеть не должен — оно относится к виду, который
    // он уже отменил.
    const quickA = markerOf(main.log, "lessons", "1");
    const quickB = markerOf(main.log, "lessons", "3");
    const openedA = quickA.opened, openedB = quickB.opened;
    quickA.fire("click");
    quickB.fire("click");
    await wait(30);
    check("перебитый перелёт не открыл окно прежней точки", quickA.opened === openedA,
        `${quickA.opened} против ${openedA}`);
    check("а долетевший открыл окно новой", quickB.opened === openedB + 1,
        `${quickB.opened} против ${openedB}`);

    // Выбранная точка сильнее общего кадра. Пока слой догружался после нажатия,
    // пересбор точек пересчитывал кадр и утаскивал человека обратно на весь
    // кампус: обводка оставалась на корпусе, а камера — на общем виде
    // (замерено живым браузером на реальном расписании).
    const framesBefore = main.log.fitBounds.length;
    toggle(main.doc, main.window, "layerEvents", false);
    await wait(30);
    check("смена набора точек при выбранной точке не откатывает кадр",
        main.log.fitBounds.length === framesBefore,
        `fitBounds вызван ${main.log.fitBounds.length} раз`);
    check("и выбор после этого остаётся на той же точке",
        focusedMarkers(main.log).length === 1 && focusedMarkers(main.log)[0].tooltip.title === "3",
        focusedMarkers(main.log).map((m) => m.tooltip.title).join(", "));
    toggle(main.doc, main.window, "layerEvents", true);
    await wait(30);

    // Плавность — по просьбе человека: тот же reduce-motion, что учитывает
    // расписание. Перелёт остаётся перемещением камеры, но без движения
    // экрана.
    main.window.matchMedia = () => ({ matches: true });
    const flies = main.log.flyTo.length;
    markerOf(main.log, "events", "Библиотека").fire("click");
    await wait(30);
    check("просьба убрать движение не гоняет карту туда-сюда",
        main.log.flyTo.length === flies && main.log.setView.length === 1,
        `flyTo=${main.log.flyTo.length}, setView=${main.log.setView.length}`);
    check("и точка всё равно оказывается в кадре",
        main.log.setView[0].latlng.join(",") === "56.993,60.584" && main.log.setView[0].zoom === 17,
        JSON.stringify(main.log.setView[0]));
    delete main.window.matchMedia;

    // ---------- 11. содержимое окон ----------
    //
    // Leaflet вызывает функцию содержимого окна, когда окно открывается. Здесь
    // функция вызывается напрямую — это ровно то, что делает библиотека, и
    // позволяет проверить второй запрос, не дожидаясь настоящей разметки попапа.

    const buildingMarker = markerOf(main.log, "lessons", "3");
    check("окно корпуса строится функцией, а не строкой HTML", typeof buildingMarker.popup === "function");
    const beforeDetail = find(main.calls, "GET /api/schedule/map").length;
    // Проверяется до открытия окна: сам popup() и отправляет запрос за строками
    // корпуса, поэтому после него «сводка ничего не тянула» уже не увидеть.
    check("сводка страницы не тянула строки пар",
        find(main.calls, "GET /api/schedule/map").every((c) => !c.building),
        find(main.calls, "GET /api/schedule/map").map((c) => c.search).join(" | "));
    const box = buildingMarker.popup();
    check("в окне — название корпуса", /Корпус 3/.test(box.textContent), box.textContent.slice(0, 60));
    check("в окне — число пар за период", /12 пар за период/.test(box.textContent), box.textContent.slice(0, 80));
    check("дни показаны отдельными строками", box.querySelectorAll(".map-popup-day").length === 2,
        `${box.querySelectorAll(".map-popup-day").length}`);
    check("пока список грузится, окно говорит об этом", /Список пар этого корпуса…/.test(box.textContent));

    await until(() => find(main.calls, "GET /api/schedule/map").length > beforeDetail);
    await until(() => box.querySelectorAll(".map-popup-lesson").length > 0);
    const detailCalls = find(main.calls, "GET /api/schedule/map");
    const detail = detailCalls.slice(-1)[0];
    check("клик по маркеру спросил ровно один дополнительный запрос",
        detailCalls.length === beforeDetail + 1, `${detailCalls.length} против ${beforeDetail}`);
    check("и спросил именно этот корпус", detail.building === "3", detail.search);
    check("в том же периоде недели", Number(detail.params.get("from")) === EXPECT_FROM, detail.search);
    check("строк пар показано не больше двенадцати", box.querySelectorAll(".map-popup-lesson").length === 12,
        `${box.querySelectorAll(".map-popup-lesson").length}`);
    check("остальное сосчитано, а не обрезано молча", /и ещё 3 пары/.test(box.textContent),
        box.textContent.slice(-60));

    // Открытое окно переспрашивает автопан, когда строки приехали. Leaflet
    // считает пан один раз при открытии и пропускает пересчёт по своему флагу
    // keepInView, а окно к этому моменту ещё короткое: живой браузер показал
    // отрезанный краем карты заголовок (20px при карте 440px). Проверка
    // требует ровно один пересчёт и ровно ту же точку — двигать камеру «на
    // глаз» код не имеет права.
    buildingMarker.openPopup();
    const repanBox = buildingMarker.popup();
    await until(() => repanBox.querySelectorAll(".map-popup-lesson").length > 0);
    check("окно позвало автопан после приезда строк", buildingMarker.pans.length === 1,
        `${buildingMarker.pans.length} пересчётов`);
    check("и позвало его на той же точке, без выдуманного смещения",
        buildingMarker.pans[0] === buildingMarker.latlng,
        JSON.stringify(buildingMarker.pans[0]));

    const hallBox = hall.popup();
    check("окно места перечисляет события", hallBox.querySelectorAll(".map-popup-lesson").length === 2,
        `${hallBox.querySelectorAll(".map-popup-lesson").length}`);
    check("событие подписано категорией", /Концерт · Культура/.test(hallBox.textContent),
        hallBox.textContent.slice(0, 90));
    check("у события без категории пустого разделителя нет",
        /День открытых дверей/.test(hallBox.textContent) && !/undefined/.test(hallBox.textContent),
        hallBox.textContent.slice(0, 120));

    // Строка пары в окне — кнопка, и по просьбе владельца нажатие на пару
    // должно приближать карту к этому корпусу. Здесь проверяется ровно то, что
    // даёт офлайновый прогон: строка названа кнопкой, она зовёт тот же
    // focusMarker, что и маркер, и после неё точка корпуса обведена.
    const pairRow = box.querySelector("button.map-popup-lesson");
    check("строка пары — кнопка, а не текст", !!pairRow && pairRow.tagName === "BUTTON",
        pairRow ? pairRow.tagName : "строки нет");
    check("строка пары называет, что покажет",
        !!pairRow && /^Показать на карте корпус 3: /.test(pairRow.getAttribute("aria-label") || ""),
        pairRow ? pairRow.getAttribute("aria-label") : "строки нет");
    const lessonFly = main.log.flyTo.length;
    const openedBefore = buildingMarker.opened;
    pairRow.dispatchEvent(new main.window.Event("click", { bubbles: true }));
    await wait(30);
    check("нажатие на пару повело карту к её корпусу",
        main.log.flyTo.length === lessonFly + 1 &&
        main.log.flyTo.slice(-1)[0].latlng.join(",") === buildingMarker.latlng.join(","),
        JSON.stringify(main.log.flyTo.slice(-1)));
    check("и выбрал именно этот корпус",
        focusedMarkers(main.log).length === 1 && focusedMarkers(main.log)[0] === buildingMarker,
        focusedMarkers(main.log).map((m) => m.tooltip.title).join(", "));
    check("окно корпуса при этом осталось открытым",
        buildingMarker.opened === openedBefore + 1, `${buildingMarker.opened} против ${openedBefore}`);

    // Название корпуса — строка человека, и она не имеет права стать разметкой.
    const injected = boot({
        routes: {
            "GET /api/schedule/map": (ctx) => (ctx.record.building
                ? json(scheduleDetail({
                    buildings: [Object.assign(
                        building("<img src=x onerror=alert(1)>", "resolved", place("Корпус", 1, 1), 1, []),
                        { lessons: [lessonRow(1)] }),
                    ],
                }))
                : json(scheduleMap({
                    buildings: [building("<img src=x onerror=alert(1)>", "resolved", place("Корпус", 1, 1), 1, [])],
                }))),
        },
    });
    await ready(injected);
    const danger = markersIn(injected.log, "lessons").find((m) => /onerror/.test(m.tooltip.title));
    check("разметка из названия корпуса дошла до списка текстом", !!danger,
        markersIn(injected.log, "lessons").map((m) => m.tooltip.title).join(", "));
    const dangerBox = danger.popup();
    await wait(60);
    check("и не стала узлом DOM ни в окне, ни в списке",
        dangerBox.querySelectorAll("img").length === 0 &&
        injected.doc.querySelectorAll("#mapLessonsList img").length === 0,
        `в окне ${dangerBox.querySelectorAll("img").length}, в списке ${injected.doc.querySelectorAll("#mapLessonsList img").length}`);

    const detailFailed = boot({
        routes: {
            "GET /api/schedule/map": (ctx) => (ctx.record.building
                ? apiError(503, "schedule source storage is not connected")
                : json(scheduleMap())),
        },
    });
    await ready(detailFailed);
    const failedBox = markerOf(detailFailed.log, "lessons", "3").popup();
    await until(() => /Пары не загрузились/.test(failedBox.textContent));
    check("отказ второго запроса назван в окне, а не пустотой",
        /Пары не загрузились: слой не подключён в этой сборке/.test(failedBox.textContent),
        failedBox.textContent.slice(0, 90));

    // ---------- 12. переход, принесённый с другой страницы ----------
    //
    // Имя корпуса в расписании — ссылка на /map?building=&from=&to=. То, что
    // страница делает с этим адресом, нельзя проверить ничем другим: ни Go
    // (там только форма ответа), ни браузер (он видит картинку, а не то, что
    // неделя взята из ссылки, а не из «сегодня»).

    const entry = boot({ search: `?building=3&from=${EXPECT_FROM}&to=${EXPECT_TO}` });
    await ready(entry);
    check("принесённый корпус выбран",
        focusedMarkers(entry.log).length === 1 &&
        focusedMarkers(entry.log)[0].tooltip.title === "3",
        focusedMarkers(entry.log).map((m) => m.tooltip.title).join(", "));
    const entryFly = entry.log.flyTo.slice(-1)[0];
    check("к нему приехал именно тот корпус, а не общий вид",
        !!entryFly && entryFly.latlng.join(",") === "56.989,60.576", JSON.stringify(entryFly));
    // Смысл проверки в паре к предыдущей: без этого кадра страница сначала
    // подогнала бы все точки, а следом улетела к одной — два движения вместо
    // одного намеренного.
    check("и карта не показала общий кадр перед перелётом", entry.log.fitBounds.length === 0,
        `fitBounds=${entry.log.fitBounds.length}`);
    check("окно выбранного корпуса открыто", markerOf(entry.log, "lessons", "3").opened === 1,
        `${markerOf(entry.log, "lessons", "3").opened}`);
    check("переход не выбросил второй слой", markersIn(entry.log, "events").length === 2,
        `${markersIn(entry.log, "events").length}`);

    // Неделя берётся из ссылки: человек шёл со своей недели в расписании, и
    // показать ему текущую значило бы соврать про точки — подпись и корпуса
    // отвечали бы про разные даты.
    const LAST_WEEK_FROM = EXPECT_FROM - 7 * 86400;
    const LAST_WEEK_TO = EXPECT_TO - 7 * 86400;
    const otherWeek = boot({ search: `?building=1&from=${LAST_WEEK_FROM}&to=${LAST_WEEK_TO}` });
    await ready(otherWeek);
    const weekCall = find(otherWeek.calls, "GET /api/schedule/map")[0];
    check("слой запрошен за неделю из ссылки, а не за текущую",
        Number(weekCall.params.get("from")) === LAST_WEEK_FROM &&
        Number(weekCall.params.get("to")) === LAST_WEEK_TO, weekCall.search);
    check("неделя над картой подписана тем же отрезком",
        text(otherWeek.doc, "mapWeekLabel") === "26 октября – 1 ноября", text(otherWeek.doc, "mapWeekLabel"));
    check("и к корпусу из ссылки карта приехала на этой неделе",
        focusedMarkers(otherWeek.log).map((m) => m.tooltip.title).join(",") === "1",
        focusedMarkers(otherWeek.log).map((m) => m.tooltip.title).join(", "));

    // Корпус без координаты: перехода не получилось, и страница обязана
    // назвать это словами, а не оставить человека с общим видом кампуса и без
    // объяснения. Плюс — общий кадр ради несостоявшегося перелёта отменять
    // нельзя: без него карта встала бы в стартовую точку 0,0.
    const missed = boot({ search: `?building=5&from=${EXPECT_FROM}&to=${EXPECT_TO}` });
    await ready(missed);
    check("несостоявшийся переход назван в строке состояния",
        /переход: корпус 5 на карте не показан — такой точки сейчас нет/.test(text(missed.doc, "mapStatus")),
        text(missed.doc, "mapStatus"));
    check("несостоявшийся переход не двигает камеру", missed.log.flyTo.length === 0,
        JSON.stringify(missed.log.flyTo));
    check("но общий кадр кампуса остаётся", missed.log.fitBounds.length === 1,
        `${missed.log.fitBounds.length}`);

    // Гость со ссылкой без корпуса входит на обычную карту: ожидание перехода
    // не должно появиться из пустого параметра.
    const plain = boot({ search: "?building=" });
    await ready(plain);
    check("пустой building не притворяется переходом",
        plain.log.flyTo.length === 0 && /переход:/.test(text(plain.doc, "mapStatus")) === false,
        `flyTo=${plain.log.flyTo.length}, ${text(plain.doc, "mapStatus")}`);
    check("обычная карта по-прежнему сама подгоняет кадр", plain.log.fitBounds.length === 1,
        `${plain.log.fitBounds.length}`);

    // ---------- 13. чипы слоёв ----------

    const toggled = boot();
    await ready(toggled);
    toggle(toggled.doc, toggled.window, "layerLessons", false);
    await wait(30);
    check("выключение слоя сняло его маркеры", markersIn(toggled.log, "lessons").length === 0,
        `${markersIn(toggled.log, "lessons").length}`);
    check("снятие слоя — removeLayer, а не пересборка карты",
        toggled.log.removed.includes(lessonsGroup(toggled.log)) && toggled.log.maps.length === 1,
        `removeLayer=${toggled.log.removed.length}, карт=${toggled.log.maps.length}`);
    check("при этом список пар остался на месте", rows(toggled.doc, "mapLessonsList").length === 3,
        `${rows(toggled.doc, "mapLessonsList").length}`);
    check("и второй слой не пострадал", markersIn(toggled.log, "events").length === 2);

    toggle(toggled.doc, toggled.window, "layerLessons", true);
    await wait(30);
    check("включение вернуло маркеры слоя", markersIn(toggled.log, "lessons").length === 2,
        `${markersIn(toggled.log, "lessons").length}`);
    check("группа слоя осталась той же (слоёв не прибавилось)", toggled.log.groups.length === 2,
        `${toggled.log.groups.length}`);

    toggle(toggled.doc, toggled.window, "layerEvents", false);
    toggle(toggled.doc, toggled.window, "layerLessons", false);
    await wait(30);
    check("с выключенными обоими слоями карта честно пуста",
        /На эту неделю рисовать нечего: ни одна позиция периода не имеет координаты\./.test(text(toggled.doc, "mapNotice")),
        text(toggled.doc, "mapNotice"));
    check("выключение слоя не отключило данные", rows(toggled.doc, "mapUnplaced").length === 6,
        `${rows(toggled.doc, "mapUnplaced").length}`);

    // ---------- 14. тема ----------

    const theme = boot();
    await ready(theme);
    const madeBefore = theme.log.markers.length;
    const callsBefore = theme.calls.length;
    theme.doc.documentElement.setAttribute("data-theme", "dark");
    await wait(50);
    check("смена темы перекрасила маркеры", theme.log.markers.length > madeBefore,
        `было ${madeBefore}, стало ${theme.log.markers.length}`);
    check("перекраска не дёрнула сервер", theme.calls.length === callsBefore,
        `${theme.calls.length} против ${callsBefore}`);
    check("перекраска не пересоздала карту и подложку",
        theme.log.maps.length === 1 && theme.log.tiles.length === 1,
        `карт ${theme.log.maps.length}, тайлов ${theme.log.tiles.length}`);

    // ---------- 15. без библиотеки ----------

    const offline = boot({ leaflet: false });
    await ready(offline);
    check("без Leaflet страница не падает", offline.doc.getElementById("mapCanvas") !== null);
    check("без Leaflet сказано, чем заменили карту",
        /Карта недоступна — содержимое показано списками ниже\./.test(text(offline.doc, "mapNotice")),
        text(offline.doc, "mapNotice"));
    check("сообщение пережило первую перерисовку", visible(offline.doc, "mapNotice"), text(offline.doc, "mapNotice"));
    check("списки под картой всё равно собраны",
        rows(offline.doc, "mapLessonsList").length === 3 && rows(offline.doc, "mapEventsList").length === 3,
        `${rows(offline.doc, "mapLessonsList").length}/${rows(offline.doc, "mapEventsList").length}`);
    check("и «чего на карте нет» посчитано", text(offline.doc, "unplacedTotal") === "17 позиций вне кадра",
        text(offline.doc, "unplacedTotal"));
    check("без библиотеки ни один слой Leaflet не создан", offline.log.maps.length === 0);

    // ---------- 16. гарантии в статике ----------

    const mapJs = fs.readFileSync(path.join(REPO, "web/js/map.js"), "utf8");
    const mapHtml = fs.readFileSync(path.join(REPO, "web/map.html"), "utf8");
    const scheduleJs = fs.readFileSync(path.join(REPO, "web/js/schedule.js"), "utf8");

    check("в коде страницы нет ни одного чужого адреса", !/https?:\/\//.test(mapJs),
        (mapJs.match(/https?:\/\/\S*/) || []).join(" "));
    check("в разметке страницы нет ни одного чужого адреса", !/https?:\/\//.test(mapHtml),
        (mapHtml.match(/https?:\/\/\S*/) || []).join(" "));
    check("библиотека подключается раньше скрипта страницы",
        mapHtml.indexOf("js/vendor/leaflet.js") < mapHtml.indexOf("js/map.js"));
    check("неделя карты и расписания считается одним кодом",
        !/function weekRange\(/.test(scheduleJs) && !/function startOfWeek\(/.test(scheduleJs) &&
        !/function weekTitle\(/.test(scheduleJs),
        "в schedule.js осталась своя копия недельного кода");
    // Ключ искажается на `(?<!un)`: подстрока «resolved: "» есть внутри
    // «unresolved: "», и без границы слова проверка считала бы удвоение там,
    // где его нет.
    const stateLabels = (src) => (src.match(/(?<![a-z_])resolved: "/g) || []).length;
    check("словарь состояний на фронте один", stateLabels(mapJs) === 1, `${stateLabels(mapJs)} определения`);

    // ---------- 17. чистота прогона ----------

    check("ни один прогон не оставил необработанного отклонения", rejections.length === 0,
        JSON.stringify(rejections.slice(0, 3)));

    const failed = results.filter((r) => !r.ok);
    console.log(`\nИТОГ: ${results.length - failed.length}/${results.length} проверок пройдено`);
    for (const f of failed) console.log(`  FAIL  ${f.name}${f.detail ? " — " + f.detail : ""}`);
    process.exit(failed.length ? 1 : 0);
})().catch((error) => {
    console.error("СБОЙ ПРОВЕРКИ:", error && error.stack ? error.stack : error);
    process.exit(2);
});
