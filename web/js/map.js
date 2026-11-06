// Страница карты (ADR-0041): два слоя поверх одного справочника мест.
//
// Зачем карта вообще появилась: и расписание, и события отвечают «к. 3, ауд.
// 301», а где этот корпус — не отвечают. Координата в базе есть (places.lat/lon),
// на экране её не было.
//
// Почему библиотека лежит в репозитории (js/vendor/leaflet.js), а не подключена
// с CDN: бриф п.6 запрещает вшивать в сборку чужой сервис. Страница, которая
// перестала работать, потому что чужой CDN закрылся или сменил адрес, — ровно
// тот случай, и снаружи он неотличим от «у вуза нет карты».
//
// Почему подложка приходит с сервера, а не прописана здесь: адрес тайл-сервера
// называет оператор (MAP_TILES_URL), и в конкретной сборке его может не быть.
// Отсутствие подложки — штатное состояние, а не авария: точки, подписи и
// список текстом работают и на пустом фоне, а причина называется строкой
// состояния, а не молчанием.
//
// Почему под картой есть список того же содержимого: карта — canvas/SVG, она
// недоступна с клавиатуры и со скринридером, а данные на ней не украшение, а
// ответ на вопрос «где занятие». Список остаётся и при живых тайлах.
//
// Почему молчание запрещено: пустая карта и «карты сейчас нет» выглядят
// одинаково и следуют из совершенно разных состояний сборки. Каждый слой обязан
// назвать причину — 503 источника, 422 периода, 500 сбоя базы, — и второй слой
// от этого не гаснет.
document.addEventListener("DOMContentLoaded", async () => {
    const canvas = document.getElementById("mapCanvas");
    const notice = document.getElementById("mapNotice");
    const statusLine = document.getElementById("mapStatus");
    const tilesNote = document.getElementById("tilesNote");
    const weekLabel = document.getElementById("mapWeekLabel");
    const prevWeek = document.getElementById("mapPrevWeek");
    const nextWeek = document.getElementById("mapNextWeek");
    const currentWeekBtn = document.getElementById("mapCurrentWeek");
    const refreshBtn = document.getElementById("mapRefresh");
    const lessonsToggle = document.getElementById("layerLessons");
    const eventsToggle = document.getElementById("layerEvents");
    const lessonsList = document.getElementById("mapLessonsList");
    const eventsList = document.getElementById("mapEventsList");
    const unplacedBox = document.getElementById("mapUnplaced");
    const unplacedTotal = document.getElementById("unplacedTotal");
    const headerName = document.getElementById("userName");
    const headerRole = document.getElementById("userRole");
    const headerAvatar = document.getElementById("userAvatar");

    const plural = GS.plural;
    const weekTitle = GS.weekTitle;
    const weekRange = GS.weekRange;
    const startOfWeek = GS.startOfWeek;

    // Состояние точки — тот же словарь, что на сервере
    // (internal/handlers/map_state.go). Второе имя для того же состояния на
    // фронтенде означало бы, что легенда и ответ сервера могут разойтись, а
    // проверить это было бы нечем.
    const STATE_LABELS = {
        resolved: "точка есть",
        unresolved: "место заведено, координат нет",
        missing: "корпуса нет в справочнике",
        no_building: "аудитория не называет корпус",
        several: "пара названа в двух корпусах",
        no_place: "событие ни к какому месту не привязано",
        no_date: "событию нечего встать в период",
    };

    // Порядок строк «не показано» фиксирован и здесь: список переставляется на
    // каждую перезагрузку, а порядок по частоте заставил бы строки прыгать под
    // глазами человека.
    const STATE_ORDER = ["unresolved", "missing", "no_building", "several", "no_place", "no_date"];

    const LAYERS = [
        { name: "lessons", title: "Пары", path: "/api/schedule/map", countUnit: ["пара", "пары", "пар"] },
        { name: "events", title: "События", path: "/api/events/map", countUnit: ["событие", "события", "событий"] },
    ];

    const state = {
        weekStart: startOfWeek(new Date()),
        tiles: null,
        tilesProblem: "",
        // Причина, по которой конфиг подложки не ответил. Отдельно от
        // tilesProblem: тот говорит «оператор не задал переменную», этот —
        // «сервер не ответил», и путать их нельзя: у этих двух состояний разные
        // адресаты и разные действия.
        configProblem: "",
        lessons: { loading: true, data: null, error: null },
        events: { loading: true, data: null, error: null },
        visible: { lessons: true, events: true },
        // Какая точка сейчас выбрана (ключ слоя: "lessons:1" | "events:7").
        focus: "",
        // Переход, который человек принёс из другой страницы и который ещё не
        // применился: точки появляются только после ответа слоёв, а до этого
        // остаётся храниться именно запросу, а не какому-то виду карты.
        pendingFocus: null,
        // Принесённый переход, который ответила карта, но точки под него не
        // нашлось (корпус без координаты, слой выключен). Отдельно от
        // pendingFocus: тот живёт, пока ожидания нет, а этот — чтобы строка
        // состояния назвала причину молчания, а не оставила человека с пустым
        // экраном и без объяснения.
        missedFocus: "",
    };

    let map = null;
    let tileLayer = null;
    let groups = null;
    let boundsKey = "";
    // Ставится один раз, когда Leaflet не поднялся (библиотеки нет в сборке или
    // контейнер не нашёл её): список ниже живёт, а сообщение о недоступной карте
    // обязана пережить первую же перерисовку.
    let noMap = false;
    const markers = new Map(); // "lessons:3" | "events:7" -> маркер

    const NO_MAP_NOTE = "Карта недоступна — содержимое показано списками ниже.";

    // ---------- мелочи разметки ----------

    function node(tag, className, text) {
        const el = document.createElement(tag);
        if (className) el.className = className;
        if (text) el.textContent = text;
        return el;
    }

    function clear(container) {
        if (container) container.textContent = "";
    }

    // Цвета читаются из токенов темы, а не лежат константами скрипта: маркер
    // обязан перекрашиваться вместе со страницей, иначе в тёмной теме бирюзовые
    // точки на тёмной подложке читаются как «карта не прогрузилась».
    // Fallback равен светлой теме в style.css и нужен там, где каскад ничего не
    // даёт (jsdom без раскладки), а не как второй источник дизайна.
    function token(name, fallback) {
        const style = window.getComputedStyle ? window.getComputedStyle(document.documentElement) : null;
        const value = style && style.getPropertyValue ? style.getPropertyValue(name).trim() : "";
        return value || fallback;
    }

    function palette() {
        return {
            lessons: token("--active-line", "#173ea3"),
            events: token("--teal", "#155e5c"),
            edge: token("--ink", "#232a3b"),
            // Обводка выбранной точки. Отдельный цвет, а не цвет слоя: выбор
            // должен быть отличим и на синем слое пар, и на бирюзовом событий.
            // Тёмный ореол вокруг неё рисует CSS (.map-marker-focus) — у
            // подложки карт свой цвет, которого у нас нет.
            focus: token("--map-focus", "#fbbf24"),
        };
    }

    // Плавность по выбору человека: тот же reduce-motion, что учитывает
    // расписание. Перелёт — движение, и если человек просил его убрать, кадр
    // просто переставляется.
    function motionAllowed() {
        return !(typeof window.matchMedia === "function"
            && window.matchMedia("(prefers-reduced-motion: reduce)").matches);
    }

    // Радиус — по корню из числа, с потолком: площадь кружка растёт квадратом
    // радиуса, и линейный рост превратил бы корпус со сотней пар в пятно на
    // полэкрана.
    function radiusOf(count) {
        return 7 + Math.min(15, Math.round(Math.sqrt(Math.max(0, count)) * 2.6));
    }

    function counted(count, unit) {
        return `${count} ${plural(count, unit[0], unit[1], unit[2])}`;
    }

    function dayKeyToDate(key) {
        const text = String(key || "");
        if (text.length !== 8) return null;
        return new Date(Number(text.slice(0, 4)), Number(text.slice(4, 6)) - 1, Number(text.slice(6, 8)));
    }

    function dayCaption(key) {
        const date = dayKeyToDate(key);
        if (!date) return String(key || "");
        return date.toLocaleDateString("ru-RU", { weekday: "short", day: "numeric", month: "short" });
    }

    function periodQuery() {
        const range = weekRange(state.weekStart);
        return `from=${range.from}&to=${range.to}`;
    }

    function layerConfig(name) {
        return LAYERS.find((layer) => layer.name === name);
    }

    // Переход, принесённый с другой страницы: /map?building=1&from=&to= из
    // расписания, где имя корпуса — ссылка на это место карты.
    //
    // Неделя берётся из `from`: страница расписания передаёт ровно свою, и
    // человеку, переехавшему «посмотреть корпус», показывать текущую неделю
    // нельзя — подпись над таблицей и точки на карте отвечали бы про разные
    // даты. Точка выбирается после ответа слоёв (drawMarkers), потому что до
    // него выбирать нечего.
    function readEntry() {
        if (typeof window.URLSearchParams !== "function") return;

        const params = new window.URLSearchParams(window.location.search || "");
        const from = Number(params.get("from"));
        if (Number.isFinite(from) && from > 0) {
            state.weekStart = startOfWeek(new Date(from * 1000));
        }

        const building = (params.get("building") || "").trim();
        if (building) {
            state.pendingFocus = { key: `lessons:${building}`, label: `корпус ${building}` };
        }
    }

    // ---------- карта ----------

    function initMap() {
        if (!canvas || !window.L || typeof window.L.map !== "function") return false;

        map = window.L.map(canvas, {
            center: [0, 0],
            zoom: 1,
            // Колесо мыши над картой — зум, а не прокрутка страницы: на длинной
            // странице курсор оказывается над картой по пути вниз и человек
            // теряет место, где был. Масштаб остаётся кнопками, двойным кликом
            // и клавиатурой (стрелки, +, −).
            scrollWheelZoom: false,
            // Контрол масштаба создаётся руками, а не берётся стандартный:
            // подписи библиотеки — «Zoom in»/«Zoom out», а кнопка лежит в
            // порядке Tab, и программа чтения экрана выдаёт её имя человеку.
            // Русский язык интерфейса не должен зависеть от настроек библиотеки.
            zoomControl: false,
            // Контрол происхождения рисует ссылку на сайт библиотеки — чужой
            // адрес в интерфейсе, которого бриф (п.6) не хочет даже в виде
            // подписи. Провайдера тайлов страница называет сама, в своём
            // элементе под картой (tilesNote).
            attributionControl: false,
        });
        window.L.control.zoom({
            position: "topleft",
            zoomInTitle: "Приблизить",
            zoomOutTitle: "Отдалить",
        }).addTo(map);
        window.L.control.scale({ imperial: false }).addTo(map);

        groups = {
            lessons: window.L.layerGroup().addTo(map),
            events: window.L.layerGroup().addTo(map),
        };
        return true;
    }

    // Подложка ставится только когда её сменили (старт страницы), а не на
    // каждой перерисовке: пересоздание тайлового слоя на каждый клик чипа
    // слоя заново тянуло бы все тайлы видимой области.
    function applyTiles() {
        if (!map) {
            noteTiles();
            return;
        }
        if (tileLayer) {
            map.removeLayer(tileLayer);
            tileLayer = null;
        }
        if (state.tiles) {
            tileLayer = window.L.tileLayer(state.tiles.url, {
                attribution: state.tiles.attribution || "",
                maxZoom: 19,
            }).addTo(map);
        }
        noteTiles();
    }

    function noteTiles() {
        if (!tilesNote) return;
        if (state.configProblem) {
            // «Не настроена» была бы неправдой: мы не знаем, настроена ли
            // подложка, мы знаем только, что про это не спросили удачно.
            tilesNote.textContent = "подложка: конфиг не ответил";
            return;
        }
        tilesNote.textContent = state.tiles
            ? `подложка: ${state.tiles.attribution || "свой тайл-сервер"}`
            : "подложка: не настроена";
    }

    function showNotice(text) {
        if (!notice) return;
        notice.textContent = text || "";
        notice.classList.toggle("hidden", !text);
    }

    // Точки слоя: только те, у кого есть координата. Остальные строки остаются
    // в списках и в блоке «не показано» — слой пар намеренно не выбрасывает
    // корпус без точки, и страница не имеет права делать это за сервер.
    function layerPoints(name) {
        const layer = state[name];
        const items = layer.data
            ? (name === "lessons" ? layer.data.buildings : layer.data.places)
            : [];
        if (!Array.isArray(items)) return [];

        return items.filter((item) => item && item.place).map((item) => ({
            key: name === "lessons" ? `lessons:${item.building}` : `events:${item.place_id}`,
            kind: name,
            ref: name === "lessons" ? String(item.building) : String(item.place_id),
            lat: item.place.lat,
            lon: item.place.lon,
            title: name === "lessons" ? String(item.building) : String(item.name || ""),
            subtitle: name === "lessons" ? String(item.place.name || "") : "",
            count: name === "lessons" ? item.lesson_count : item.event_count,
            raw: item,
        }));
    }

    // Содержимое всплывающей окна строится узлами, а не строкой HTML: в popup
    // попадают названия корпусов, аудиторий и событий — то, что ввёл человек, и
    // innerHTML с этими строками был бы тем же хранящимся XSS, из-за которого
    // в T0.1 ломались модальные окна.
    function popupLessons(point, marker) {
        const box = node("div", "map-popup");
        box.appendChild(node("strong", null, `Корпус ${point.title}`));
        if (point.subtitle) box.appendChild(node("span", "map-popup-sub", point.subtitle));
        box.appendChild(node("span", "map-popup-meta", `${counted(point.count, ["пара", "пары", "пар"])} за период`));

        const days = Array.isArray(point.raw.days) ? point.raw.days : [];
        for (const day of days.slice(0, 7)) {
            box.appendChild(node("span", "map-popup-day", `${dayCaption(day.day_key)} · ${day.lesson_count}`));
        }

        const holder = node("div", "map-popup-lessons");
        holder.appendChild(node("span", "map-popup-meta", "Список пар этого корпуса…"));
        box.appendChild(holder);

        // Второй запрос — ровно тот, что описан у слоя (ScheduleMapAPI): сводка
        // по корпусам приходит всегда, а строки пар нужны, только когда человек
        // кликнул маркер. Тянуть их для всех корпусов при открытии страницы
        // значило бы гонять по базе то, что никто не собирался читать.
        const query = `/api/schedule/map?${periodQuery()}&building=${encodeURIComponent(point.title)}`;
        GS.fetchJSON(query).then((data) => {
            const item = (data && Array.isArray(data.buildings) && data.buildings[0]) || null;
            clear(holder);
            const lessons = item && Array.isArray(item.lessons) ? item.lessons : [];
            if (!lessons.length) {
                holder.appendChild(node("span", "map-popup-meta", "Строк пар за период нет."));
                return;
            }
            for (const lesson of lessons.slice(0, 12)) {
                const parts = [lesson.time || lesson.date, lesson.discipline || lesson.lesson_type,
                    lesson.group_name, lesson.room].filter(Boolean);
                const text = parts.join(" · ");
                // Строка пары — кнопка, а не текст: нажатие на пару должно
                // перебросить карту на этот корпус (запрос владельца). Другого
                // кликабельного места у строки нет, а молчащая подпись под
                // курсором выглядела бы сломанной.
                const row = node("button", "map-popup-lesson");
                row.type = "button";
                row.textContent = text;
                row.setAttribute("aria-label", `Показать на карте корпус ${point.title}: ${text}`);
                row.addEventListener("click", () => focusMarker(point.key));
                holder.appendChild(row);
            }
            if (lessons.length > 12) {
                const rest = lessons.length - 12;
                holder.appendChild(node("span", "map-popup-meta", `и ещё ${counted(rest, ["пара", "пары", "пар"])}`));
            }
        }).catch((error) => {
            clear(holder);
            holder.appendChild(node("span", "map-popup-meta", `Пары не загрузились: ${errorText(error)}`));
        }).then(() => {
            // Строки пар приезжают уже после того, как Leaflet посчитал
            // автопан: окно вырастает на их высоту, и его заголовок остаётся
            // отрезанным краем карты (живой браузер: 20px сверху при карте
            // 440px). Пересчёт вызывается здесь, а не только на посадке
            // перелёта — список мог доехать и позже.
            repanPopup(marker);
        });

        return box;
    }

    function popupEvents(point) {
        const box = node("div", "map-popup");
        box.appendChild(node("strong", null, point.title));
        box.appendChild(node("span", "map-popup-meta",
            `${counted(point.count, ["событие", "события", "событий"])} за период`));

        for (const event of (Array.isArray(point.raw.events) ? point.raw.events : [])) {
            const parts = [GS.formatDateTime(event.event_date), event.title, event.category].filter(Boolean);
            box.appendChild(node("span", "map-popup-lesson", parts.join(" · ")));
        }
        return box;
    }

    function drawMarkers() {
        if (!map || !groups) {
            // Списки ниже всё равно перерисовываются, и их рисование не имеет
            // права стирать причину, по которой карты нет.
            if (noMap) showNotice(NO_MAP_NOTE);
            return;
        }

        showNotice("");
        markers.clear();
        groups.lessons.clearLayers();
        groups.events.clearLayers();

        const colors = palette();
        const points = [];

        // Пары рисуются первыми, события — поверх них. И те, и другие садятся
        // на одну координату корпуса, а радиус точки считается от числа
        // позиций: окружность корпуса на 86 пар (22px) накрывала точку события
        // (10px) целиком, и клик по ней всегда доставался слою пар — живой
        // Chromium под курсором находил именно его. Пока событие ниже,
        // «нажать на любое событие» невыполнимо ни мышью, ни с клавиатуры.
        for (const name of ["lessons", "events"]) {
            if (!state.visible[name]) continue;

            for (const point of layerPoints(name)) {
                const marker = window.L.circleMarker([point.lat, point.lon], {
                    radius: radiusOf(point.count),
                    color: colors.edge,
                    weight: 2,
                    fillColor: name === "events" ? colors.events : colors.lessons,
                    fillOpacity: 0.85,
                    // Опция лишь не даёт библиотеке подписать фигуру ролью
                    // «кнопка» и вешать свой обработчик Enter; саму остановку
                    // фокуса она не снимает — см. tabindex ниже.
                    keyboard: false,
                });

                marker.bindTooltip(point.title, { direction: "top" });
                // Потолок высоты окна обязателен: список пар растёт до
                // нескольких экранов (живая страница давала 1340px при карте
                // 440px), и Leaflet, пытаясь уместить такое окно, утаскивал
                // камеру на 145м севернее выбранного корпуса — перелёт съедался
                // сразу после посадки. С потолком окно остаётся того размера,
                // которое человек и должен увидеть, а что не влезло — доводил
                // ниже автопан по факту строк.
                marker.bindPopup(name === "lessons" ? () => popupLessons(point, marker) : () => popupEvents(point),
                    POPUP_BOX);
                // Библиотека открывает окно и сама — сразу по нажатию, до
                // посадки перелёта. Её автопан двигает карту посреди полёта,
                // а наступивший от допана moveend съедает наш единственный
                // обработчик посадки (живой Chromium: окно осталось отрезано
                // 69px сверху при маркере ровно в центре кадра). Окно
                // открываем сами, когда долетели. Снимаем ровно тот
                // обработчик, который библиотека вешает bindPopup и снимает
                // bindPopup же: click → _openPopup с контекстом маркера.
                marker.off("click", marker._openPopup, marker);
                marker.addTo(groups[name]);

                // Безымянная остановка в порядке Tab: у SVG-фигуры нет подписи,
                // и человек, обходящий страницу клавиатурой, получал по ней на
                // каждую точку. То же самое делает кнопка «Показать» в списке
                // ниже — она названа словами, поэтому точка остаётся кликабельной
                // мышью, но из таб-порядка убрана. keyboard:false для этого не
                // достаточно: Chromium считает SVG-фигуру фокусируемой сам по
                // себе, и обход в headless-браузере переставал попадать на неё
                // только после явного -1 (cursor, pointer-events и aria-hidden
                // не подействовали ни на что).
                const el = marker.getElement();
                if (el) el.setAttribute("tabindex", "-1");

                markers.set(point.key, marker);
                points.push([point.lat, point.lon]);

                // Нажатие на точку выбранной её и делает: обводка остаётся
                // видимой, пока человек не выберет другую. Окно при этом
                // открывает не библиотека по нажатию (см. off выше), а
                // focusMarker — когда перелёт долетел.
                marker.on("click", () => focusMarker(point.key));
            }
        }

        // Обводка перерисовывается здесь, а не только в focusMarker: этот
        // пересбор создаёт новые объекты, и выбранная точка без этого шага
        // теряла бы подсветку после смены темы, недели или чипа слоя.
        applyFocus(colors);

        if (!points.length) {
            // «Рисовать нечего» во время загрузки — неправда: мы ещё не знаем.
            // Полоса состояния ниже говорит «Загрузка слоёв…», и пояснение на
            // карте не должно противоречить ей первые полсекунды.
            if (state.lessons.loading || state.events.loading) return;
            showNotice(emptyNotice());
            return;
        }

        // Принесённый с другой страницы переход применяется, только когда оба
        // слоя ответили: до их ответа выбирать нечего, а половиной данных
        // кадр подогнанный и потом улетевший к одной точке — это два лишних
        // движения вместо одного намеренного.
        let focused = "";
        if (state.pendingFocus && !state.lessons.loading && !state.events.loading) {
            const entry = state.pendingFocus;
            state.pendingFocus = null;
            if (markers.has(entry.key)) focused = entry.key;
            else state.missedFocus = entry.label;
        }

        // Кадр меняется только когда изменился сам набор точек, а не каждая
        // перерисовка: человек мог отъехать на квартал и нажать «Обновить», и
        // возвращать его к прежнему виду на каждое такое нажатие — значит
        // игнорировать то, что он сам выбрал глазами.
        const key = points.map((p) => `${p[0]},${p[1]}`).sort().join("|");
        if (key !== boundsKey) {
            boundsKey = key;
            // Общий кадр строится, только если человек не просил конкретную
            // точку: два движения подряд выглядели бы как сбой прокрутки.
            // Ждущий переход тоже отменяет кадр — но ровно до своего ответа,
            // а несостоявшийся (ниже) оставляет человеку общий вид кампуса.
            // Проверка идёт и по state.focus: слой пар мог доехать после
            // нажатия, и прежний вид кампуса на лету сбрасывал перелёт назад
            // (живая страница: обводка на корпусе, а камера на масштабе 15 в
            // общем кадре). Выбранная и существующая точка важнее кадра.
            if (!focused && !state.pendingFocus && !(state.focus && markers.has(state.focus))) {
                // 15 — потолок общего кадра, а не 16: на 16 кампус умещается
                // целиком, и последующий перелёт к корпусу не показывал бы
                // приближения. Общий вид и вид выбранной точки обязаны
                // различаться масштабом, иначе «фокус» не чем показать.
                map.fitBounds(window.L.latLngBounds(points), { padding: [24, 24], maxZoom: OVERVIEW_MAX_ZOOM });
            }
        }

        if (focused) focusMarker(focused);
    }

    function emptyNotice() {
        const failed = LAYERS.filter((layer) => state[layer.name].error).length;
        if (failed === LAYERS.length) return "Оба слоя не ответили — причины в строке состояния ниже.";
        if (!state.tiles) return "Подложки нет, и точек на этот период тоже нет.";
        return "На эту неделю рисовать нечего: ни одна позиция периода не имеет координаты.";
    }

    // ---------- подписи ----------

    // Русская формулировка по статусу, а не голый текст сервера: у этих путей
    // он англоязычный («schedule source storage is not connected») и означает
    // состояние сборки, а не ошибку ввода. Английскую строку сервера оставляем
    // только там, где она действительно про данные, — в 422.
    function errorText(error) {
        if (!error) return "сервер не ответил";
        if (error.status === 503) return "слой не подключён в этой сборке";
        if (error.status === 422) return `сервер не принял период (${error.message})`;
        if (error.status === 401) return "сессия истекла — войдите заново";
        if (error.status >= 500) return `сбой сервера (${error.status}), причина в его журнале`;
        return error.message || "запрос не удался";
    }

    function unplacedCount(name) {
        const data = state[name].data;
        if (!data || !data.unplaced) return 0;
        return name === "lessons" ? data.unplaced.lesson_count : data.unplaced.event_count;
    }

    function statusText() {
        if (state.lessons.loading || state.events.loading) return "Загрузка слоёв…";

        const parts = [];
        for (const layer of LAYERS) {
            const current = state[layer.name];
            if (current.error) {
                parts.push(`${layer.title}: ${errorText(current.error)}`);
                continue;
            }
            parts.push(`${layer.title}: ${counted(layerPoints(layer.name).length,
                ["точка", "точки", "точек"])}, вне кадра ${counted(unplacedCount(layer.name),
                ["позиция", "позиции", "позиций"])}`);
        }

        const updated = state.lessons.data && state.lessons.data.updated_at;
        if (updated) parts.push(`расписание источника обновлено ${GS.formatDate(updated)}`);
        // Принесённый с другой страницы корпус, у которого точки нет, обязан
        // быть назван: человек кликнул «Главный корпус» и не получил ничего —
        // без этой строки экран молчал бы про причину, которая у слоя есть.
        // Формулировка не утверждает «координат нет»: точки может не быть и
        // потому, что слой пар сейчас выключен чипом, — а где причина, человек
        // увидит в списке «вне кадра».
        if (state.missedFocus) {
            parts.push(`переход: ${state.missedFocus} на карте не показан — такой точки сейчас нет`);
        }
        if (state.configProblem) {
            parts.push(`конфиг подложки не получен: ${state.configProblem}`);
        } else if (!state.tiles) {
            parts.push(`подложку задаёт ${state.tilesProblem || "MAP_TILES_URL"}`);
        }

        return parts.join(" · ");
    }

    function renderUnplaced() {
        clear(unplacedBox);

        const rows = [];
        for (const layer of LAYERS) {
            const data = state[layer.name].data;
            const reasons = data && data.unplaced && Array.isArray(data.unplaced.reasons)
                ? data.unplaced.reasons : [];
            const field = layer.name === "lessons" ? "lesson_count" : "event_count";
            const examples = layer.name === "lessons" ? "rooms" : "titles";

            for (const reason of reasons) {
                rows.push({
                    key: reason.reason,
                    layer: layer.title,
                    count: reason[field] || 0,
                    unit: layer.countUnit,
                    examples: Array.isArray(reason[examples]) ? reason[examples] : [],
                });
            }
        }

        rows.sort((a, b) => orderOf(a) - orderOf(b) || a.layer.localeCompare(b.layer));

        let total = 0;
        for (const row of rows) {
            total += row.count;
            const line = node("div", "map-unplaced-row");
            line.appendChild(node("span", `map-key is-${row.key}`));
            line.appendChild(node("span", "map-unplaced-what",
                `${STATE_LABELS[row.key] || row.key} — ${row.layer}`));
            line.appendChild(node("span", "map-unplaced-count", counted(row.count, row.unit)));
            if (row.examples.length) {
                line.appendChild(node("span", "map-unplaced-examples", row.examples.join(" · ")));
            }
            unplacedBox.appendChild(line);
        }

        unplacedTotal.textContent = total
            ? `${counted(total, ["позиция", "позиции", "позиций"])} вне кадра`
            : "всё названное — в кадре";

        if (!rows.length) {
            unplacedBox.appendChild(node("p", "map-hint",
                "Ни одна позиция не осталась без точки: у каждого корпуса и каждого места периода есть координата."));
        }
    }

    function orderOf(row) {
        const index = STATE_ORDER.indexOf(row.key);
        return index === -1 ? STATE_ORDER.length : index;
    }

    function renderList(container, layer) {
        clear(container);
        const current = state[layer.name];

        if (current.loading) {
            container.appendChild(node("p", "map-hint", "Загрузка…"));
            return;
        }
        if (current.error) {
            container.appendChild(node("p", "map-hint", `Слой не ответил: ${errorText(current.error)}`));
            return;
        }

        const items = current.data
            ? (layer.name === "lessons" ? current.data.buildings : current.data.places)
            : [];
        const list = Array.isArray(items) ? items : [];

        if (!list.length) {
            container.appendChild(node("p", "map-hint", layer.name === "lessons"
                ? "За этот период ни одна аудитория не назвала корпус."
                : "За этот период событий с привязанным местом нет."));
            return;
        }

        for (const item of list) {
            const title = layer.name === "lessons" ? String(item.building) : String(item.name || "");
            const count = layer.name === "lessons" ? item.lesson_count : item.event_count;
            const row = node("div", "map-row");

            row.appendChild(node("span", `map-key is-${item.place_state}`));
            row.appendChild(node("span", "map-row-title", title));
            row.appendChild(node("span", "map-row-count", counted(count, layer.countUnit)));
            row.appendChild(node("span", `chip ${item.place_state === "resolved" ? "chip-outline" : "chip-muted"}`,
                STATE_LABELS[item.place_state] || item.place_state));

            const key = layer.name === "lessons" ? `lessons:${item.building}` : `events:${item.place_id}`;
            if (markers.has(key)) {
                const button = node("button", "btn btn-secondary", "Показать");
                button.type = "button";
                button.setAttribute("aria-label", `Показать на карте: ${title}`);
                button.addEventListener("click", () => focusMarker(key));
                row.appendChild(button);
            }

            container.appendChild(row);
        }

        // Строки без кнопки — это ровно то, что слой обязан показать текстом и
        // не имеет права показать точкой; случай проверяется, а не прячется.
        if (!layerPoints(layer.name).length) {
            container.appendChild(node("p", "map-hint",
                "Точек на карте у этого слоя нет: ни у одной строки периода нет координаты."));
        }
    }

    // Обводка выбранной точки: ярким цветом поверх обычной границы.
    //
    // Ставится через setStyle, а не отдельным слоем-«звёздочкой»: второй объект
    // на тех же координатах жил бы своей жизнью при перерисовке и смене темы,
    // а здесь выбор — свойство той же точки.
    //
    // Толще линия, но не вдвое: при weight 5+ на малом масштабе соседние
    // корпуса сливаются в одно пятно, и выбрать второй глазами становится
    // нельзя.
    function applyFocus(colors) {
        const tone = colors || palette();
        for (const [key, marker] of markers) {
            const selected = key === state.focus;
            marker.setStyle(selected
                ? { color: tone.focus, weight: 4 }
                : { color: tone.edge, weight: 2 });

            const path = marker.getElement ? marker.getElement() : null;
            if (path && path.classList) path.classList.toggle("map-marker-focus", selected);
        }
    }

    // Zoom, на который страница приезжает по выбранной точке. 17, а не 16:
    // живой Chromium измерил, что общий кадр кампуса в 1.5 раза меньше экрана
    // и так упирается в 16, то есть «фокус» на 16 был бы сдвигом кадра без
    // приближения — ровно тем, от чего человек просил уйти. Верхняя граница —
    // maxZoom карты: у подложки, которая рисует только до 16, перелёт на 17
    // молча остался бы на 16, и обещать больше, чем движок покажет, нельзя.
    const FOCUS_ZOOM = 17;
    const FLY_SECONDS = 0.9;
    const OVERVIEW_MAX_ZOOM = 15;

    // Рамка всплывающего окна. Кадр карты в разметке — 440px, поэтому 300px
    // вместе с носиком и отступом в него помещаются; остальное список пар
    // долистывает сам (leaflet-popup-scrolled даёт overflow:auto).
    const POPUP_BOX = { maxWidth: 300, maxHeight: 300 };

    // Отсчёт перелёта: попап открывается только тем из них, которое ещё не
    // перебито следующим нажатием.
    let focusSeq = 0;

    // Автопан считается по высоте окна в момент вызова. И перелёт, и приезд
    // строк пар меняют кадр или само окно уже после этого вызова, поэтому
    // окно зовётся заново: setLatLng(той же точки) — публичный способ позвать
    // _adjustPan по теперешним числам, не выдумывая смещение самому.
    function repanPopup(marker) {
        const pop = marker && marker.getPopup && marker.getPopup();
        if (pop && pop.isOpen && pop.isOpen()) pop.setLatLng(pop.getLatLng());
    }

    function focusMarker(key) {
        const marker = markers.get(key);
        if (!marker || !map) return false;

        state.focus = key;
        // Удалось показать — прежняя несостоявшаяся просьба больше не о чём:
        // человек уже смотрит на нужную точку, и строка состояния не имеет
        // права напоминать про неудачный вход на страницу.
        state.missedFocus = "";
        applyFocus();

        const where = marker.getLatLng();
        const zoom = Math.min(Math.max(map.getZoom(), FOCUS_ZOOM), map.getMaxZoom());
        // Плавный перелёт вместо мгновенного setView: человеку важно увидеть, в
        // какую сторону уехал кадр между двумя корпусами, иначе «корпус 3» для
        // него просто другая картинка вместо ответа, куда идти.
        const seq = ++focusSeq;
        if (motionAllowed()) {
            map.flyTo(where, zoom, { duration: FLY_SECONDS, easeLinearity: 0.25 });
            // Всплывающее окно само разворачивает карту, чтобы уместиться
            // (autoPan). Открытое до посадки, оно тянет кадр обратно к прежнему
            // центру — живой Chromium показал, что перелёт к корпусу 1
            // заканчивался ровно там, где начался. Поэтому окно дожидается
            // moveend, а перебитый перелёт его не получает.
            map.once("moveend", () => {
                if (seq !== focusSeq) return;
                marker.openPopup();
                // Автопан считается по высоте окна в момент вызова, а строки
                // пар приезжают позже посадки. Поэтому пересчёт зовётся ещё
                // раз здесь: кадр здесь последний, и смещение считается по
                // нему, а не по окну, которое было на момент открытия.
                repanPopup(marker);
            });
        } else {
            map.setView(where, zoom);
            marker.openPopup();
            repanPopup(marker);
        }
        return true;
    }

    function render() {
        if (weekLabel) weekLabel.textContent = weekTitle(state.weekStart);
        drawMarkers();
        renderList(lessonsList, layerConfig("lessons"));
        renderList(eventsList, layerConfig("events"));
        renderUnplaced();
        if (statusLine) statusLine.textContent = statusText();
    }

    // ---------- загрузка ----------

    async function loadConfig() {
        try {
            const config = await GS.fetchJSON("/api/map/config");
            state.tiles = config && config.tiles ? config.tiles : null;
            state.tilesProblem = config && config.problem ? String(config.problem) : "";
            state.configProblem = "";
        } catch (error) {
            // Конфиг не получен — это не повод думать, что подложки нет:
            // причина называется отдельно от «подложки нет в сборке». Строку
            // состояния прямо здесь не писать: следом идёт reload() → render(),
            // которая перезапишет её своим текстом, — поэтому причина живёт в
            // состоянии и попадает в строку через statusText().
            state.tiles = null;
            state.tilesProblem = "";
            state.configProblem = errorText(error);
        }
        applyTiles();
    }

    async function loadLayer(layer) {
        state[layer.name].loading = true;
        render();
        try {
            const data = await GS.fetchJSON(`${layer.path}?${periodQuery()}`);
            state[layer.name] = { loading: false, data, error: null };
        } catch (error) {
            state[layer.name] = { loading: false, data: null, error };
        }
    }

    async function reload() {
        // Оба слоя независимо: отказ одного (источник расписания не настроен —
        // 503) не имеет права гасить второй, события-то в базе лежат.
        await Promise.all(LAYERS.map((layer) => loadLayer(layer)));
        render();
    }

    // ---------- управление ----------

    function shiftWeek(delta) {
        const start = state.weekStart;
        state.weekStart = new Date(start.getFullYear(), start.getMonth(), start.getDate() + 7 * delta);
        // Кадр обязана пересчитать новый набор точек, иначе после переезда на
        // другую неделю взгляд остаётся там, где рисовался прошлый период.
        boundsKey = "";
        return reload();
    }

    function bindControls() {
        if (prevWeek) prevWeek.addEventListener("click", () => shiftWeek(-1));
        if (nextWeek) nextWeek.addEventListener("click", () => shiftWeek(1));
        if (currentWeekBtn) currentWeekBtn.addEventListener("click", () => {
            state.weekStart = startOfWeek(new Date());
            return reload();
        });
        if (refreshBtn) refreshBtn.addEventListener("click", reload);

        for (const [input, name] of [[lessonsToggle, "lessons"], [eventsToggle, "events"]]) {
            if (!input) continue;
            input.addEventListener("change", () => {
                state.visible[name] = input.checked;
                if (map && groups) {
                    if (input.checked) groups[name].addTo(map);
                    else map.removeLayer(groups[name]);
                }
                boundsKey = "";
                render();
            });
        }
    }

    // Перекраска при смене темы: цвета читаются из токенов, а тема переключается
    // без перезагрузки страницы (core/theme.js ставит data-theme на <html>).
    // Наблюдатель за этим атрибутом — единственный способ узнать о смене, не
    // переписывая theme.js и не залезая в его localStorage.
    function watchTheme() {
        if (typeof window.MutationObserver !== "function") return;
        new window.MutationObserver(() => drawMarkers()).observe(document.documentElement, {
            attributes: true,
            attributeFilter: ["data-theme"],
        });
    }

    // ---------- страница ----------

    if (!initMap()) {
        noMap = true;
        showNotice(NO_MAP_NOTE);
    }
    if (statusLine) statusLine.textContent = "Загрузка слоёв…";
    readEntry();
    bindControls();
    watchTheme();

    // Шапку заполняет этот файл, а не core/layout.js: имя и роль приходят из
    // API, а компонент обязан остаться от API независимым.
    try {
        const user = await GS.fetchJSON("/api/auth/me");
        if (headerName) headerName.textContent = user.full_name || user.username;
        if (headerRole) headerRole.textContent = GS.roleLabel(user.role);
        if (headerAvatar) headerAvatar.textContent = GS.initialsFrom(user);
    } catch (error) {
        // 401 сюда доезжает как редирект api-client'а: страница под защитой
        // сессии, и гость на ней оказаться не может.
        if (error.status !== 401 && statusLine) statusLine.textContent = errorText(error);
    }

    await loadConfig();
    await reload();
});
