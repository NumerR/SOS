#!/usr/bin/env node
// Проверка целостности фронтенда без сборки.
//
// Зачем он нужен: класс багов из T0.1. `web/js/modals.js` существовал на диске,
// был синтаксически валидным, но не был подключён ни к одной странице, а
// `dashboard.js` вызывал `ModalManager` -> `ReferenceError` на открытии модального
// окна. `go test` этого не видит, `node --check` тоже: файл-то целый. Виден он
// ровно по одному признаку — файл есть, страницы на него не ссылаются.
//
// Проверки:
//   1. каждый <script src> и <link href> из HTML указывает на существующий файл;
//   2. каждый JS-файл в web/js подключён хотя бы одной страницей;
//   3. все getElementById-мишени объявлены на той странице, где запускается
//      скрипт (в HTML, в разметке, которую этот скрипт сам и вставляет, или в
//      конфиге core/layout.js — см. LAYOUT_ID_FIELD);
//   4. в HTML нет инлайновых обработчиков (onclick="...") — они и привели к
//      хранящемуся XSS, когда имя задания подставлялось в строку JS;
//   5. доступность структуры (T0.5): `lang`, `<title>`, один `<h1>`, отсутствие
//      разрыва уровня заголовков, skip-link первым делом и в существующий
//      фокусируемый id, `scope` у каждого `<th>`;
//   6. контраст текстовых пар токенов — пороги WCAG 2.1 (4.5:1 для текста,
//      3:1 для границы поля). Проверка считает отношения по формуле sRGB, а не
//      сверяет с журналом: иначе правка цвета в :root прошла бы незаметно.
//      Дополнительно считается покрытие (остаток ADR-0016): каждая пара
//      «текст + подложка», которую задаёт правило style.css или инлайновый
//      style="" в HTML, обязана быть в списке измеренных или в именованном
//      исключении с причиной (ADR-0019);
//   7. общий submit-механизм форм создания: определение GS.bindCreateForm одно,
//      страницы его вызывают, и копий этого обработчика в страничных скриптах
//      не появилось (класс багов T0.1);
//   8. связь «поле → подсказка / строка сообщения» через aria-describedby:
//      ссылки ведут на существующие id, каждое поле формы со статус-строкой на
//      неё ссылается, подсказка с id подключена, и сгенерированные в JS поля не
//      исключение (остаток T0.5, ADR-0018);
//   9. индикатор фокуса (остаток доступности, ADR-0020): каждое правило с
//      :focus/:focus-visible/:focus-within, которое снимает системный контур или
//      рисует свой, обязано показать цвет с контрастом >= 3:1 против каждой
//      подложки проекта — «фокус есть, но не виден» теперь красное;
//  10. сьюты не догружают страницы руками (ADR-0026): ручной диспатч события
//      загрузки в `tests/ui/*.cjs` красный — jsdom стреляет им сам, а второй
//      запуск обработчиков отсоединяет узлы, уже снятые сьютом.
//  11. у локальных CSS/JS в адресе есть ?v=дата, и она не старше самих файлов
//      (ADR-0037): иначе правка статики остаётся невидимой за старым кэшем.
//  12. правило, которое анимирует масштабом деталь внутри <svg>, обязано задать
//      transform-box и transform-origin (ADR-0038): без них Chromium отсчитывает
//      scale от угла viewBox, и значок «идёт» уезжает по диагонали за такт.
//
// Запуск локально: node scripts/check-frontend.mjs
// Код выхода 0 — всё в порядке, 1 — есть находки (они печатаются списком).

import { readFileSync, readdirSync, existsSync, statSync } from "node:fs";
import { join, posix } from "node:path";

const ROOT = process.argv[2] || ".";
const WEB = join(ROOT, "web");
const JS_DIR = join(WEB, "js");

const problems = [];
const notes = [];

function fail(page, message) {
  problems.push(`${page}: ${message}`);
}

function htmlFiles(dir) {
  return readdirSync(dir)
    .filter((name) => name.endsWith(".html"))
    .sort();
}

// JS-файлы ищем рекурсивно: с T3.1 у фронтенда есть web/js/core/, и проверка
// «каждый скрипт подключён хотя бы одной страницей» обязана видеть файлы в
// подпапках — иначе ровно тот баг, ради которого скрипт написан (файл есть,
// страницы о нём не знают), снова станет невидимым.
// Возвращаем пути относительно web/js: на них ссылается href вида
// "/js/core/api-client.js".
function jsFiles(dir, prefix = "") {
  const out = [];

  for (const entry of readdirSync(dir, { withFileTypes: true }).sort((a, b) => a.name.localeCompare(b.name))) {
    if (entry.isDirectory()) {
      out.push(...jsFiles(join(dir, entry.name), `${prefix}${entry.name}/`));
    } else if (entry.name.endsWith(".js")) {
      out.push(`${prefix}${entry.name}`);
    }
  }

  return out;
}

// href из HTML может быть абсолютным ("/js/common.js") или относительным
// ("css/style.css"): и то и другое резолвится от web/. Внешние ссылки
// (http(s)://, //cdn) не проверяем — их доступность не вопрос репозитория.
function resolveLocalHref(href) {
  const clean = href.split("?")[0].split("#")[0];
  if (!clean || /^(https?:)?\/\//.test(clean) || clean.startsWith("mailto:")) return null;
  if (/^(data|blob):/.test(clean)) return null;
  return join(WEB, clean.replace(/^\//, ""));
}

// Версия статики в адресе (ADR-0037). no-cache действует на ответы, которые
// браузер уже запросил; копия, скачанная до его появления, может считаться
// свежей и не запрашиваться вовсе — тогда страница собирается из смеси нового
// JS и старого CSS, и человек видит «правки не применились». Дата в ?v=
// меняет сам адрес, поэтому хватает обычной перезагрузки. Дата не должна быть
// старше самих файлов: это ловит правку CSS/JS без поднятия версии.
function assetDateFloor() {
  const files = [
    ...readdirSync(join(WEB, "css")).filter((n) => n.endsWith(".css")).map((n) => join(WEB, "css", n)),
    ...jsFiles(JS_DIR).map((n) => join(JS_DIR, n)),
  ];
  let newest = 0;
  for (const file of files) newest = Math.max(newest, statSync(file).mtimeMs);
  return Number(new Date(newest).toISOString().slice(0, 10).replaceAll("-", ""));
}

const assetFloor = assetDateFloor();

function checkAssetVersion(page, href) {
  const file = resolveLocalHref(href);
  if (file === null || !/\.(css|js)$/.test(file)) return;
  const match = href.match(/[?&]v=(\d{8})\b/);
  if (!match) {
    fail(page, `у ${href} нет ?v=даты — браузер может показать копию из старого кэша`);
  } else if (Number(match[1]) < assetFloor) {
    fail(page, `версия ${match[1]} у ${href} старше самих CSS/JS (${assetFloor}) — поднимите дату в адресах всех страниц`);
  }
}

// Атрибуты вида id="modalTitle" — и в HTML, и в JS-шаблонных строках.
const ID_DECLARATION = /\bid=["']([A-Za-z0-9_:.#$%&+*~=@-]+)["']/g;
const GET_ELEMENT = /getElementById\(\s*["']([^"']+)["']\s*\)/g;

// Исключение для core/layout.js: этот файл объявляет идентификаторы шапки не
// разметкой, а данными (`id: "adminLink"` в конфиге страницы), и литеральной
// формы id="..." у них нет. Разрешаем эту форму только для этого файла: если
// открыть её для всех скриптов, любое поле `id` в любом объекте данных
// (строка курса, запись журнала) стало бы «объявлением» — и проверка «мишени
// getElementById нет на странице» молча перестала бы что-то ловить.
const LAYOUT_ID_FIELD = /\bid:\s*["']([A-Za-z0-9_-]+)["']/g;

function matches(re, text) {
  const out = new Set();
  for (const m of text.matchAll(re)) out.add(m[1]);
  return out;
}

const pages = htmlFiles(WEB);
if (pages.length === 0) {
  console.error("check-frontend: в web/ нет ни одного .html — скрипт сломан, а не проект");
  process.exit(1);
}

const referenced = new Map(); // имя js-файла -> список страниц
const declaredIdsByPage = new Map();

for (const page of pages) {
  const htmlPath = join(WEB, page);
  const html = readFileSync(htmlPath, "utf8");

  // 1. подключённые файлы существуют
  const scriptHrefs = [...html.matchAll(/<script\b[^>]*\bsrc=["']([^"']+)["'][^>]*>/gi)].map((m) => m[1]);
  const pageScripts = [];

  for (const href of scriptHrefs) {
    checkAssetVersion(page, href);
    const file = resolveLocalHref(href);
    if (file === null) {
      notes.push(`${page}: внешняя ссылка на скрипт ${href} — не проверяется`);
      continue;
    }
    if (!existsSync(file)) {
      fail(page, `подключён несуществующий скрипт ${href}`);
      continue;
    }
    pageScripts.push(file);

    const name = file.slice(JS_DIR.length + 1);
    if (!referenced.has(name)) referenced.set(name, []);
    referenced.get(name).push(page);
  }

  for (const href of [...html.matchAll(/<link\b[^>]*\bhref=["']([^"']+)["'][^>]*>/gi)].map((m) => m[1])) {
    checkAssetVersion(page, href);
    const file = resolveLocalHref(href);
    if (file !== null && !existsSync(file)) {
      fail(page, `подключён несуществующий файл стилей ${href}`);
    }
  }

  // 4. инлайновые обработчики
  const inline = [...html.matchAll(/\bon(click|load|error|mouse\w+|key\w+|submit|input|change)=/gi)].map((m) => m[0]);
  if (inline.length > 0) {
    fail(page, `инлайновые обработчики (${inline.length}): ${inline.slice(0, 3).join(", ")} — только addEventListener`);
  }

  // 3. id объявлены на странице
  const declared = matches(ID_DECLARATION, html);
  for (const scriptPath of pageScripts) {
    const source = readFileSync(scriptPath, "utf8");
    // Скрипт может сам создавать разметку с id — они тоже считаются объявленными.
    for (const id of matches(ID_DECLARATION, source)) declared.add(id);
    if (scriptPath.endsWith(posix.join("core", "layout.js"))) {
      for (const id of matches(LAYOUT_ID_FIELD, source)) declared.add(id);
    }
  }
  declaredIdsByPage.set(page, { declared, scripts: pageScripts });
}

// 2. каждый js-файл подключён
for (const file of jsFiles(JS_DIR)) {
  const pages = referenced.get(file) || [];
  if (pages.length === 0) {
    fail("web/js", `${file} не подключён ни к одной странице — ровно так сломались модальные окна в T0.1`);
  } else {
    notes.push(`web/js/${file} → ${pages.join(", ")}`);
  }
}
// 3 (вторая половина): на что ссылается getElementById
for (const [page, { declared, scripts }] of declaredIdsByPage) {
  for (const scriptPath of scripts) {
    const source = readFileSync(scriptPath, "utf8");
    for (const id of matches(GET_ELEMENT, source)) {
      if (!declared.has(id)) {
        fail(page, `${posix.basename(scriptPath)} ищет getElementById("${id}"), но такой id на этой странице никто не объявляет`);
      }
    }
  }
}

// ---------- 5. доступность структуры страницы (T0.5) ----------
//
// Проверка статическая и сознательно не «умная»: она ловит не «недоступный
// интерфейс вообще», а четыре конкретных пропуска, которые были в этих страницах
// и которые возвращаются молча — их не видно ни в скриншоте, ни в go test.
for (const page of pages) {
  const html = readFileSync(join(WEB, page), "utf8");

  if (!/<html\b[^>]*\blang=["']/i.test(html)) fail(page, "у <html> нет lang — скринридер читает страницу с неверным произношением");
  if (!/<title>\s*\S/i.test(html)) fail(page, "у страницы нет <title>");

  const h1 = [...html.matchAll(/<h1\b[^>]*>/gi)];
  if (h1.length !== 1) {
    fail(page, `<h1> найдено ${h1.length} — должен быть ровно один: без него у страницы нет имени для ассистивных технологий`);
  }

  // Уровень заголовков не должен перескакивать (h1 -> h3): обход Tab по
  // структуре опирается на соседние уровни.
  const levels = [...html.matchAll(/<h([1-6])\b[^>]*>/gi)].map((m) => Number(m[1]));
  for (let i = 1; i < levels.length; i += 1) {
    if (levels[i] - levels[i - 1] > 1) {
      fail(page, `заголовок h${levels[i - 1]} сменяется h${levels[i]} — разрыв уровня в позиции ${i}`);
      break;
    }
  }

  const skip = html.match(/<a\b[^>]*class="skip-link"[^>]*>/i);
  if (!skip) {
    fail(page, "нет skip-link: с клавиатуры навигация (шапка) идёт перед содержимым на каждой странице");
  } else {
    const firstAnchor = html.search(/<a[\s>]/i);
    if (firstAnchor !== skip.index) {
      fail(page, "skip-link не первая ссылка на странице — с Tab он не спасает");
    }
    const target = (skip[0].match(/href="#([^"]+)"/) || [])[1];
    if (!target) {
      fail(page, "у skip-link нет фрагмента href=\"#...\"");
    } else if (!new RegExp(`id=["']${target}["']`).test(html)) {
      fail(page, `skip-link ведёт на #${target}, но элемента с таким id на странице нет`);
    } else if (!new RegExp(`<(?:main|section|div|span)[^>]*id=["']${target}["'][^>]*tabindex=["']-1["']`).test(html)
      && !new RegExp(`id=["']${target}["'][^>]*tabindex=["']-1`).test(html)) {
      fail(page, `цель skip-link #${target} без tabindex="-1": браузер прокрутит страницу, но фокус останется в шапке`);
    }
  }

  for (const th of [...html.matchAll(/<th\b([^>]*)>/gi)]) {
    if (!/\bscope=["']/i.test(th[1])) {
      fail(page, `<th> без scope — при чтении таблицы вслух подпись столбца теряется: ${th[0]}`);
      break;
    }
  }
}

// ---------- 6. контраст токенов ----------
//
// Значения берутся из :root, поэтому правка цвета в CSS проходит проверку, а не
// просто меняет картинку. Пороги: 4.5:1 для текста (AA, обычный размер) и 3:1
// для границы компонента (1.4.11).
function channel(value) {
  const v = value / 255;
  return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
}

function luminance(hex) {
  const clean = hex.replace("#", "");
  if (!/^[0-9a-f]{6}$/i.test(clean)) return null;
  const [r, g, b] = [0, 2, 4].map((i) => parseInt(clean.slice(i, i + 2), 16));
  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b);
}

function ratio(a, b) {
  const [l1, l2] = [luminance(a), luminance(b)];
  if (l1 === null || l2 === null) return null;
  const [max, min] = [Math.max(l1, l2), Math.min(l1, l2)];
  return (max + 0.05) / (min + 0.05);
}

const css = readFileSync(join(WEB, "css", "style.css"), "utf8");
const tokens = {};
for (const m of css.matchAll(/(--[a-z-]+):\s*(#[0-9a-fA-F]{6})\s*;/g)) {
  if (!(m[1] in tokens)) tokens[m[1]] = m[2];
}

// Тёмная тема объявляет те же токены вторым блоком. Проверять контраст только
// по первому вхождению нельзя: в тёмной теме значения другие, и зелёный CI по
// светлым парам ничего не говорит о тёмных. Блок начинается с :root, поэтому
// проверка покрытия ниже его пропускает — здесь он читается явно.
const darkTokens = { ...tokens };
const darkBlock = /:root\[data-theme="dark"\]\s*\{([\s\S]*?)\}/.exec(css);
if (!darkBlock) {
  fail("css/style.css", 'не найден блок :root[data-theme="dark"] — тёмная тема не объявлена');
} else {
  for (const m of darkBlock[1].matchAll(/(--[a-z-]+):\s*(#[0-9a-fA-F]{6})\s*;/g)) {
    darkTokens[m[1]] = m[2];
  }
}
const THEMES = [
  { name: "светлая", values: tokens },
  { name: "тёмная", values: darkTokens },
];

// Пары, где цвет действительно несёт текст (или границу контрола). Новый член
// списка добавляется вместе с комментарием в CSS, объясняющим выбор значения.
// Ниже есть проверка покрытия: пара, которую задаёт правило style.css, обязана
// оказаться здесь или в CONTRAST_EXEMPT — список нельзя «забыть дополнить».
const CONTRAST_PAIRS = [
  ["--muted", "--parchment", 4.5, "подсказки, меты карточек, «Обновлено»"],
  ["--muted", "--surface", 4.5, "то же внутри панелей"],
  ["--muted", "--parchment-deep", 4.5, "бейдж «без оценки»"],
  ["--ink-soft", "--parchment-deep", 4.5, "chip-muted"],
  ["--ink-soft", "--parchment", 4.5, "подписи секций"],
  ["--ink-soft", "--surface", 3.0, "пиктограмма `.btn-icon` (1.4.11 — как граница компонента)"],
  ["--ink", "--parchment", 4.5, "основной текст"],
  ["--ink", "--surface", 4.5, "текст на панелях и светлых кнопках `.btn-secondary`"],
  ["--accent", "--parchment", 4.5, "ссылки, подписи секций, день недели в расписании, `.btn-icon:hover`"],
  ["--accent", "--surface", 4.5, "ссылка и подпись на панели"],
  ["--teal-ink", "--teal-tint", 4.5, "аватары, `.card-icon`, `.lesson-num`, статус «сдано», `.btn-outline:hover`"],
  ["--teal-ink", "--parchment-deep", 4.5, "моноширинный `code`"],
  ["--teal-ink", "--surface", 4.5, "текст `.btn-outline`"],
  ["--ok", "--ok-tint", 4.5, "текст успеха"],
  ["--danger", "--danger-tint", 4.5, "текст отказа"],
  ["--danger", "--parchment", 4.5, "красный текст на фоне страницы (`.modal-close:hover` — на tint)"],
  ["#f5f0e6", "--teal", 4.5, "подпись синей кнопки"],
  ["#f5f0e6", "--danger-btn", 4.5, "подпись кнопки удаления (заливка `--danger-btn`)"],
  ["#f5f0e6", "--danger-btn-hover", 4.5, "подпись кнопки удаления в `:hover`"],
  ["--pending-ink", "--pending-bg", 4.5, "плашка «не начата»"],
  ["--active-ink", "--active-bg", 4.5, "плашка «идёт»"],
  ["--done-ink", "--done-bg", 4.5, "плашка «закончена»"],
  ["#f5f0e6", "--teal-hover", 4.5, "подпись синей кнопки в `:hover`"],
  ["#f5f0e6", "--teal-press", 4.5, "подпись синей кнопки в `:active`"],
  ["#f4efe4", "--teal", 4.5, "логотип в шапке и skip-link в фокусе"],
  ["#f3eee3", "--teal", 4.5, "подпись чипа `.chip`"],
  ["#f1ece1", "--teal", 4.5, "текст баннера `.alert`"],
  ["--line-strong", "--parchment", 3.0, "граница поля ввода"],
  ["--line-strong", "--surface", 3.0, "граница поля ввода на панели"],
];

// Пары, которые сознательно не держат порог. Причина обязана быть проверяемой:
// WCAG освобождает неактивные компоненты (1.4.3), а не «не понравилось значение».
const CONTRAST_EXEMPT = [
  ["#b3aa97", "--parchment-deep", "текст отключённой кнопки: 1,58:1, но `disabled` в этом проекте — " +
    "только состояние отправки, где `.btn.is-loading { color: transparent }` и спиннер вместо подписи"],
];

const contrastNotes = [];
// Пара может задавать и литеральный цвет (подпись кнопок — #f5f0e6, он нигде
// не объявлен токеном); тогда он участвует в проверке как есть. Литералы равны
// в обеих темах, токены берутся из набора своей темы.
function resolveColor(ref, values = tokens) {
  if (ref.startsWith("#")) return ref;
  return values[ref] || null;
}

for (const [fg, bg, min, why] of CONTRAST_PAIRS) {
  for (const theme of THEMES) {
    const [fgValue, bgValue] = [resolveColor(fg, theme.values), resolveColor(bg, theme.values)];
    if (!fgValue || !bgValue) {
      fail("css/style.css", `контраст-проверка [${theme.name} тема]: токен ` +
        (fgValue ? bg : fg) + " не найден — список пар устарел");
      continue;
    }
    const value = ratio(fgValue, bgValue);
    if (value === null) {
      fail("css/style.css", `контраст ${fg}/${bg} [${theme.name} тема]: значение не цвет sRGB hex`);
      continue;
    }
    if (value < min) {
      fail("css/style.css", `контраст ${fg} на ${bg} [${theme.name} тема] = ${value.toFixed(2)}:1, нужно ${min}:1 (${why})`);
    } else {
      contrastNotes.push(`контраст ${fg} на ${bg} [${theme.name}] = ${value.toFixed(2)}:1 (порог ${min}) — ${why}`);
    }
  }
}

// Покрытие списка пар самим CSS. Пока список пополняют «по мере надобности»,
// новая подложка появляется в правиле, никто не считает её с текстом, и проверка
// остаётся зелёной — тот же молчаливый дрейф, что с копиями обработчиков.
// Здесь три замыкающих правила:
//   а) правило, задающее в одном блоке и текст, и подложку (в том числе через
//      --btn-fg/--btn-bg), обязано попасть в CONTRAST_PAIRS или CONTRAST_EXEMPT;
//   б) цвет подложки в состоянии (:hover, :active, :focus, [disabled]) обязан
//      встречаться в измеренных парах — иначе состояние никто не считал;
//   в) то же требование к парам, собранным инлайновым style="" в HTML.
const INDIRECT = new Set(["--btn-fg", "--btn-bg"]);
function normalizeColor(raw) {
  const value = raw.trim().split(/\s+/)[0];
  const via = /^var\((--[a-zA-Z-]+)\)$/.exec(value);
  // var(--btn-fg) — это ссылка на вариант кнопки, а не цвет: конкретное значение
  // стоит в том же или в родственном блоке, и мы считаем пару именно по нему.
  if (via) return INDIRECT.has(via[1].toLowerCase()) ? null : via[1].toLowerCase();
  if (/^#[0-9a-fA-F]{6}$/.test(value)) return value.toLowerCase();
  return null; // transparent, rgba(), inherit, градиенты — не пара «текст → подложка»
}

function pairOf(chunk) {
  const grab = (names) => {
    const m = new RegExp(`(?:^|[;\\s])(?:${names})\\s*:\\s*([^;]+)`).exec(chunk);
    return m ? normalizeColor(m[1]) : null;
  };
  const fg = grab("color|--btn-fg");
  const bg = grab("background|background-color|--btn-bg");
  return fg && bg ? [fg, bg] : null;
}

const measuredPairs = new Set();
const measuredBackgrounds = new Set();
for (const [fg, bg] of [...CONTRAST_PAIRS, ...CONTRAST_EXEMPT]) {
  measuredPairs.add(`${fg.toLowerCase()}|${bg.toLowerCase()}`);
  measuredBackgrounds.add(bg.toLowerCase());
}

const usedPairs = new Map();
let inlinePairs = 0;

function requirePair(fg, bg, where, label) {
  const key = `${fg}|${bg}`;
  if (!usedPairs.has(key)) usedPairs.set(key, label);
  if (!measuredPairs.has(key)) {
    fail(where, `пара ${fg} на ${bg} задана правилом «${label}», но её нет в CONTRAST_PAIRS — ` +
      "посчитай контраст и добавь пару (или исключение в CONTRAST_EXEMPT с причиной)");
  }
}

const bareCss = css.replace(/\/\*[\s\S]*?\*\//g, "");
for (const block of bareCss.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
  const selector = block[1].trim().replace(/\s+/g, " ");
  if (selector.startsWith(":root") || selector.startsWith("@")) continue;
  const pair = pairOf(block[2]);
  if (pair) requirePair(pair[0], pair[1], "css/style.css", selector);
  const bg = pair ? pair[1] : (() => {
    const m = /(?:^|[;\s])(?:background|background-color)\s*:\s*([^;]+)/.exec(block[2]);
    return m ? normalizeColor(m[1]) : null;
  })();
  if (bg && /:hover|:active|:focus|\[disabled\]/.test(selector) && !measuredBackgrounds.has(bg)) {
    fail("css/style.css", `состояние «${selector}» красит подложку ${bg}, которой нет ни в одной измеренной паре`);
  }
}

// Постраничные inline-стили: три исключения в HTML (transparent-подложки у
// .empty-state) ничего не красят, но правило то же — пара из style="" обязана быть
// измерена. Иначе «one-off блок на одной странице» остаётся зоной без проверки.
for (const file of htmlFiles(WEB)) {
  const html = readFileSync(join(WEB, file), "utf8");
  for (const attr of html.matchAll(/style="([^"]*)"/g)) {
    const pair = pairOf(attr[1]);
    if (!pair) continue;
    inlinePairs++;
    requirePair(pair[0], pair[1], file, `style="${attr[1].trim()}"`);
  }
}

// Стражи вырождения (тот же класс риска, что в группах 7 и 8): пустой список —
// это не «зелёный CI», а пропавшая проверка.
if (CONTRAST_PAIRS.length === 0) {
  fail("css/style.css", "список контрастных пар пуст — проверке контраста нечего считать");
}
if (usedPairs.size === 0) {
  fail("css/style.css", "в style.css не найдено ни одного правила с парой «текст + подложка» — проверка покрытия стала пустой");
}
// Исключение без пары в CSS — устаревшая договорённость, а не освобождённый цвет.
for (const [fg, bg] of CONTRAST_EXEMPT) {
  if (!usedPairs.has(`${fg.toLowerCase()}|${bg.toLowerCase()}`)) {
    fail("css/style.css", `исключение ${fg} на ${bg} больше не встречается в правилах style.css — убери его из CONTRAST_EXEMPT`);
  }
}

contrastNotes.push(`покрытие: уникальных пар «текст + подложка» в CSS и inline-стилях ${usedPairs.size}, ` +
  `из них инлайновых в HTML ${inlinePairs}, измеренных пар ${CONTRAST_PAIRS.length}, ` +
  `исключений ${CONTRAST_EXEMPT.length} — все покрыты`);

// ---------- 7. общий submit-механизм форм создания ----------
//
// Проверка про класс багов, из-за которого T0.1 и сломался: один и тот же
// обработчик, продублированный в двух файлах, один из которых потом правят.
// Формы «что-то создать» (POST + перезагрузка списка) до этого шага существовали
// тремя копиями — две в schedule.js и одна в dashboard.js, отличавшиеся адресом,
// полями и текстами. Теперь они идут через GS.bindCreateForm (common.js), и вот
// что закреплено:
//   · определение bindCreateForm ровно одно и лежит оно в common.js;
//   · ни одна страница не перестала его вызывать (мёртвый механизм — тот же род
//     бага, что неподключённый modals.js до T0.1);
//   · копия механизма не завелась снова: submit-слушатель с блокировкой кнопки
//     («is-loading») разрешён общему определению и modals.js, где делегирование
//     устроено иначе — одна точка на весь документ с разбором по data-атрибутам,
//     а не привязка к конкретной форме.
const SUBMIT_DELEGATION_EXCEPTION = "modals.js";
const definitions = [];
const users = [];
const copies = [];

for (const file of jsFiles(JS_DIR)) {
  const source = readFileSync(join(JS_DIR, file), "utf8");
  const isDefinition = /^\s*function bindCreateForm\s*\(/m.test(source);
  if (isDefinition) definitions.push(file);
  if (source.includes("GS.bindCreateForm(")) users.push(file);

  const handRolled = /addEventListener\(\s*["']submit["']/.test(source)
    && source.includes('classList.add("is-loading")');
  if (handRolled && !isDefinition && posix.basename(file) !== SUBMIT_DELEGATION_EXCEPTION) {
    copies.push(file);
  }
}

if (definitions.length !== 1) {
  fail("web/js", `определений bindCreateForm найдено ${definitions.length} (${definitions.join(", ") || "нет"}) — нужно ровно одно, в common.js`);
} else if (definitions[0] !== "common.js") {
  fail("web/js", `bindCreateForm определён в ${definitions[0]}, а общий механизм должен жить в common.js`);
}
if (definitions.length === 1 && users.length === 0) {
  fail("web/js", "GS.bindCreateForm не вызывает ни одна страница — механизм мёртвый, как modals.js до T0.1");
}
for (const file of users) notes.push(`${file} → GS.bindCreateForm (общих форм: ${users.length})`);
for (const file of copies) {
  fail("web/js", `${file}: своя копия submit-механизма с блокировкой кнопки — используй GS.bindCreateForm или добавь исключение с причиной`);
}

// ---------- 8. aria-describedby: поле ссылается на свою подсказку и строку сообщения ----------
//
// ADR-0016 (T0.5) закрыл структуру страницы и контраст, но оставил ровно одну
// вещь, записанную в «что не закрыло»: строки сообщений подписаны role="status",
// а поле никуда на них не ссылается. Живой регион читается только в момент
// изменения текста, и если фокус уже стоит в поле, отказ вслух проходит мимо —
// пользователь либо ловит его на слух, либо уходит с поля и возвращается.
// Связь «поле → сообщение» (1.3.1 / 3.3.1) — это aria-describedby, и она
// статическая: её нельзя забыть «на одной ветке кода», её можно только не
// написать совсем, или написать с опечаткой в id.
//
// Четыре правила, все по HTML страниц (плюс четвертое — по шаблонам в JS):
//   1) каждая ссылка aria-describedby ведёт на id, который в этом же файле есть;
//   2) в форме, где есть роль-статус строка, каждое поле с class="control"
//      на неё ссылается (radio/checkbox-группа — через fieldset, как и должно);
//   3) если у поля есть подсказка span.helper с id, поле на неё ссылается;
//   4) поле, которое генерирует скрипт (modals.js, admin.js), тоже с ссылкой —
//      в HTML его не найти, поэтому смотрим прямо теги в шаблонах.
function attrsOf(tag) {
  const out = {};
  for (const m of tag.matchAll(/([a-zA-Z_:][-a-zA-Z0-9_:.]*)="([^"]*)"/g)) {
    out[m[1].toLowerCase()] = m[2];
  }
  return out;
}

function describedBy(tag) {
  return (attrsOf(tag)["aria-describedby"] || "").trim().split(/\s+/).filter(Boolean);
}

const CONTROL_TAG = /<(?:input|textarea|select)\b[\s\S]*?>/gi;

function isControlTag(tag) {
  return /class="[^"]*control/.test(tag) && attrsOf(tag).type !== "hidden";
}

let describedLinks = 0;
let statusForms = 0;

for (const page of pages) {
  const html = readFileSync(join(WEB, page), "utf8");
  const ids = new Set([...html.matchAll(/\bid="([^"]+)"/g)].map((m) => m[1]));

  for (const m of html.matchAll(/aria-describedby="([^"]*)"/g)) {
    for (const ref of m[1].trim().split(/\s+/)) {
      if (!ref) continue;
      describedLinks += 1;
      if (!ids.has(ref)) {
        fail(page, `aria-describedby ссылается на «${ref}», а элемента с таким id на странице нет — для ассистивных технологий ссылка пустая`);
      }
    }
  }

  for (const form of html.match(/<form\b[\s\S]*?<\/form>/gi) || []) {
    const statusIds = [...form.matchAll(/<(?:p|div|span)\b[^>]*role="status"[^>]*>/gi)]
      .map((m) => attrsOf(m[0]).id)
      .filter(Boolean);
    if (!statusIds.length) continue;
    statusForms += 1;

    for (const m of form.matchAll(CONTROL_TAG)) {
      if (!isControlTag(m[0])) continue;
      const missing = statusIds.filter((sid) => !describedBy(m[0]).includes(sid));
      if (missing.length) {
        const name = attrsOf(m[0]).name || attrsOf(m[0]).id || "(без имени)";
        fail(page, `поле ${name} не ссылается на строку сообщения своей формы (${missing.join(", ")}) — отказ, пришедший в это поле, вслух не слышно`);
      }
    }

    for (const fs of form.match(/<fieldset\b[\s\S]*?<\/fieldset>/gi) || []) {
      if (!/type="radio"|type="checkbox"/.test(fs)) continue;
      const open = (fs.match(/<fieldset\b[\s\S]*?>/i) || [""])[0];
      const missing = statusIds.filter((sid) => !describedBy(open).includes(sid));
      if (missing.length) {
        fail(page, `fieldset с группой переключателей не ссылается на строку сообщения (${missing.join(", ")})`);
      }
    }
  }

  for (const block of html.match(/<label\b[^>]*class="[^"]*\bfield\b[^"]*"[\s\S]*?<\/label>/gi) || []) {
    const helper = (block.match(/<span\b[^>]*class="helper"[^>]*\bid="([^"]+)"/i) || [])[1];
    if (!helper) continue;
    for (const m of block.matchAll(CONTROL_TAG)) {
      if (!isControlTag(m[0])) continue;
      if (!describedBy(m[0]).includes(helper)) {
        fail(page, `подсказка #${helper} не подключена через aria-describedby — вслух её не прочитать`);
      }
    }
  }
}

let generatedControls = 0;
for (const file of jsFiles(JS_DIR)) {
  const source = readFileSync(join(JS_DIR, file), "utf8");
  if (!source.includes('class="control')) continue;
  for (const m of source.matchAll(CONTROL_TAG)) {
    if (!isControlTag(m[0])) continue;
    generatedControls += 1;
    if (!describedBy(m[0]).length) {
      fail(`web/js/${file}`, `генерированное поле без aria-describedby (${m[0].replace(/\s+/g, " ").slice(0, 60)}…) — сообщение модалки остаётся не связанным с полем`);
    }
  }
}

notes.push(`aria-describedby: ссылок ${describedLinks}, форм со статус-строкой ${statusForms}, полей в JS-шаблонах ${generatedControls}`);

// Агрегат не должен молча выродиться: если ссылок нет вообще, правила 2 и 3
// ничего не проверили (формы потеряли role="status" или страницы переписали).
if (describedLinks === 0) {
  fail("web/", "ни одного aria-describedby во всём фронтенде — проверка связи «поле → сообщение» стала пустой");
}
if (statusForms === 0) {
  fail("web/", "ни одна форма не содержит строку с role=\"status\" — правилу про ссылку на сообщение нечего проверять");
}

// ---------- 9. индикатор фокуса обязан быть чем-то показан ----------
//
// WCAG 2.4.7 требует, чтобы элемент, получивший фокус, был виден, а 1.4.11 —
// чтобы сам индикатор держал 3:1 с тем, что вокруг него. До ADR-0020 этот пункт
// был единственной закрытой договорённостью доступности без замка: кольца в
// style.css существовали, но их цвет никто не считал. Живой замер
// (scripts/check-focus.mjs, ADR-0020) нашёл реальную дыру: у радиокнопки кольцо
// рисовал только --teal-tint (#d6e2df) — 1.10:1 к подложке панели #efe9dc и
// 1.03:1 к фону страницы, а в кадре элемента 211 изменившихся пикселей из 8464,
// из них ни одного с контрастом ≥3:1 (максимум 1.33:1). Фокус был, но не виделся
// — глазами это выглядит как «Tab провалился в никуда».
//
// Скриншоты — это живая приёмка: нужен браузер и запущенный сервер, поэтому в CI
// check-focus.mjs не стоит (обоснование — ADR-0020). Здесь статический замок на
// тот же дефект, чтобы новое «мягкое» кольцо не вернулось незаметно:
//   · правило с :focus / :focus-visible / :focus-within, которое снимает
//     системный контур или рисует свой (outline, box-shadow, border), обязано
//     задать хотя бы один цвет с контрастом >= 3:1 против каждой подложки;
//   · правило, снявшее контур и не показавшее ничего вместо него, красное —
//     кроме именованного исключения с проверяемой причиной;
//   · стражи: ни одного фокус-правила в CSS или ни одного посчитанного кольца —
//     это не «зелёный CI», а пропавшая проверка (тот же класс риска, что в
//     группах 6-8); неиспользуемое исключение тоже красное.
//

// Кольцо фокуса должно быть видно на любой подложке, а не на «ту, что была
// под рукой при замере»: компонент стоит то на фоне страницы, то на панели, то
// на утопленном поле. Статически мы не знаем, какая из трёх, поэтому держим
// худшую — цвет обязан пройти 3:1 (WCAG 1.4.11) против каждой.
const FOCUS_SURFACES = ["--parchment", "--parchment-deep", "--surface"];
const MIN_FOCUS_RATIO = 3.0;

// Единственное освобождённое правило. Причина проверяемая: `.main` — цель
// skip-link (у контейнера tabindex="-1"), а не компонент управления; WCAG 2.4.7
// требует индикатор у того, чем можно управлять. Без этой строки браузер рисовал
// бы рамку вокруг половины страницы, а переход и так виден по смещению содержимого.
const FOCUS_EXEMPT = new Map([
  [".main:focus", "у контейнера нет собственной рамки, и снимает он только системный контур; " +
    "сам переход виден по сместившемуся содержимому и по focus на skip-link"],
]);

// Свойства, которыми в этом проекте рисуют индикатор. `border-radius` в список
// не входит намеренно: скругление цвета не задаёт, и считать по нему нечего.
const INDICATOR_DECL = /(?:^|[;\s])(outline|outline-color|box-shadow|border|border-color|border-(?:top|right|bottom|left)(?:-(?:color|width))?)\s*:\s*([^;]+)/g;
const COLOR_REF = /var\((--[a-zA-Z-]+)\)|(#[0-9a-fA-F]{6})/g;

// Токенизированные тени (--e1, --inset1) разворачиваются в rgba-список, не в
// цвет: они в кадре есть, но контрастом не считаются — несущий цвет кольца всегда
// стоит отдельным var() или hex.
function colorRefs(value, values) {
  const out = [];
  for (const m of value.matchAll(COLOR_REF)) {
    const ref = (m[1] || m[2]).toLowerCase();
    const hex = resolveColor(ref, values);
    if (hex) out.push({ ref, hex });
  }
  return out;
}

let focusRules = 0;
let focusRings = 0;
let focusWorst = null;
const seenExempts = new Set();
const focusBlocks = [];

for (const block of bareCss.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
  const selector = block[1].trim().replace(/\s+/g, " ");
  if (selector.startsWith(":root") || selector.startsWith("@")) continue;
  if (!/:focus(-visible|-within)?\b/.test(selector)) continue;
  focusRules += 1;
  if (FOCUS_EXEMPT.has(selector)) {
    seenExempts.add(selector);
    continue;
  }
  focusBlocks.push({ selector, body: block[2] });
}

// Кольцо проверяется в каждой теме отдельно: --ring в тёмной теме светлый, и
// контраст, посчитанный только по светлым подложкам, о нём ничего не говорит.
const ringCounts = [];
for (const theme of THEMES) {
  const focusSurfaces = [];
  for (const name of FOCUS_SURFACES) {
    const hex = theme.values[name];
    if (!hex) {
      fail("css/style.css", `контраст-проверка [${theme.name} тема]: подложка ${name} не найдена — список FOCUS_SURFACES устарел`);
      continue;
    }
    focusSurfaces.push({ name, hex });
  }

  // Для каждого нарисованного цвета считаем худшую из подложек: кольцо
  // пригодно, только если проходит и на светлой панели, и на утопленном поле.
  function worstSurface(color) {
    let worst = Infinity;
    let where = null;
    for (const surface of focusSurfaces) {
      const value = ratio(color.hex, surface.hex);
      if (value === null) return null;
      if (value < worst) {
        worst = value;
        where = surface.name;
      }
    }
    return { worst, where };
  }

  let rings = 0;
  let themeWorst = null;
  for (const { selector, body } of focusBlocks) {
    const drawn = [];
    let indicatorDecls = 0;
    let removesOutline = false;
    for (const m of body.matchAll(INDICATOR_DECL)) {
      indicatorDecls += 1;
      if (m[1] === "outline" && /^\s*(?:none|0)\b/.test(m[2])) removesOutline = true;
      drawn.push(...colorRefs(m[2], theme.values));
    }

    // Правило не трогало ни контур, ни тень, ни рамку — системный индикатор
    // остался на месте, рисовать нечего.
    if (!indicatorDecls) continue;

    if (!drawn.length) {
      fail("css/style.css", `правило «${selector}» [${theme.name} тема] ${removesOutline ? "снимает системный контур и не задаёт ни одного посчитаемого цвета" : "рисует индикатор фокуса без цвета из токенов или hex"} — чем показан фокус, непонятно`);
      continue;
    }
    rings += 1;

    let best = null;
    for (const color of drawn) {
      const candidate = worstSurface(color);
      if (!candidate) continue;
      if (!best || candidate.worst > best.worst) best = { ...candidate, ref: color.ref };
    }
    if (!best) {
      fail("css/style.css", `правило «${selector}» [${theme.name} тема]: цвета индикатора (${drawn.map((c) => c.ref).join(", ")}) не sRGB hex — контраст кольца посчитать нечем`);
      continue;
    }
    if (best.worst < MIN_FOCUS_RATIO) {
      fail("css/style.css", `правило «${selector}» [${theme.name} тема]: самый контрастный цвет индикатора ${best.ref} даёт ${best.worst.toFixed(2)}:1 против ${best.where} — нужно ${MIN_FOCUS_RATIO}:1, кольцо фокуса на этой подложке не видно (WCAG 1.4.11/2.4.7)`);
      continue;
    }
    if (!themeWorst || best.worst < themeWorst.worst) {
      themeWorst = { ...best, selector };
    }
    if (!focusWorst || best.worst < focusWorst.worst) {
      focusWorst = { ...best, selector, theme: theme.name };
    }
  }

  ringCounts.push(rings);
  focusRings += rings;
  contrastNotes.push(`фокус [${theme.name}]: колец ${rings}, порог ${MIN_FOCUS_RATIO}:1 против каждой из подложек (${FOCUS_SURFACES.join(", ")})` +
    (themeWorst ? ` — самое слабое: ${themeWorst.ref} в «${themeWorst.selector}», ${themeWorst.worst.toFixed(2)}:1 против ${themeWorst.where}` : ""));
}

// Стражи вырождения: пустая проверка выглядит как зелёный CI.
if (focusRules === 0) {
  fail("css/style.css", "в style.css не найдено ни одного правила с :focus — проверке индикатора фокуса нечего считать");
}
if (focusRings === 0 || ringCounts.some((n) => n === 0)) {
  fail("css/style.css", "ни одно правило фокуса не рисует индикатора с посчитанным цветом хотя бы в одной теме — кольцам проверять нечего");
}
for (const [selector, reason] of FOCUS_EXEMPT) {
  if (!seenExempts.has(selector)) {
    fail("css/style.css", `исключение «${selector}» больше не встречается в правилах style.css — убери его из FOCUS_EXEMPT (${reason})`);
  }
}

contrastNotes.push(`фокус: правил ${focusRules}, колец с посчитанным контрастом ${focusRings}, ` +
  `порог ${MIN_FOCUS_RATIO}:1 против каждой из подложек (${FOCUS_SURFACES.join(", ")}), ` +
  `исключений ${FOCUS_EXEMPT.size}` +
  (focusWorst ? ` — самое слабое проходящее кольцо: ${focusWorst.ref} в «${focusWorst.selector}» [${focusWorst.theme} тема], ${focusWorst.worst.toFixed(2)}:1 против ${focusWorst.where}` : ""));
for (const [selector, reason] of FOCUS_EXEMPT) {
  if (seenExempts.has(selector)) contrastNotes.push(`фокус: освобождено «${selector}» — ${reason}`);
}

// ---------- 10. сьюты не догружают страницы руками ----------
//
// Класс найден ADR-0026: `admin-roles.cjs` на холодном стеке давал 18/24 там, где
// полный прогон давал 24/24. Сьют после `window.eval(bundle)` сам стрелял
// `dispatchEvent(new Event("DOMContentLoaded"))`, а jsdom стреляет этим событием
// сам на следующем тике после разбора. Обработчик страницы выполнялся дважды,
// вторая перерисовка отсоединяла узлы, которые сьют уже снял, и клик по
// отсоединённой кнопке не порождал запроса — в логе сервера не было ни одного
// POST, а сообщение `#adminMsg` оставалось пустым. Прогрев полного прогона
// маскировал гонку: обе перерисовки успевали до съёма строк.
//
// Статически это выглядит как безупречный код сьюта, поэтому замок здесь: ни один
// `tests/ui/*.cjs` не должен диспатчить событие загрузки страницы. Остальные
// dispatchEvent (submit, keydown, change) легальны — они изображают действие
// пользователя, а не перезагрузку страницы. Ищем событие в одной строке с
// dispatchEvent: собранное в переменную проверка не увидит.
const SUITE_DIR = join(ROOT, "tests", "ui");
const PAGE_LOAD_EVENTS = new Set(["DOMContentLoaded", "readystatechange", "load"]);
const DISPATCH_EVENT = /dispatchEvent\(\s*(?:new\s+)?((?:\w+\.)*Event)\(\s*["']([^"']+)["']/g;

if (!existsSync(SUITE_DIR)) {
  fail("tests/ui", "каталога с jsdom-сьютами нет — проверке нечего читать, это не «сьюты чисты»");
} else {
  const suites = readdirSync(SUITE_DIR).filter((f) => f.endsWith(".cjs")).sort();
  let dispatchedLoad = 0;
  for (const file of suites) {
    const text = readFileSync(join(SUITE_DIR, file), "utf8");
    for (const m of text.matchAll(DISPATCH_EVENT)) {
      if (!PAGE_LOAD_EVENTS.has(m[2])) continue;
      dispatchedLoad += 1;
      fail(`tests/ui/${file}`, `вручную диспатчится «${m[2]}» (${m[1]}): jsdom стреляет событием загрузки сам на следующем тике после разбора, поэтому обработчик страницы выполняется дважды — вторая перерисовка отсоединяет узлы, уже снятые сьютом (ADR-0026)`);
    }
  }
  if (!suites.length) {
    fail("tests/ui", "в каталоге нет ни одного .cjs-сьюта — проверка вырождается в зелёный CI");
  }
  notes.push(`сьюты: файлов ${suites.length}, ручных диспатчей события загрузки — ${dispatchedLoad} (приём ADR-0026)`);
}

// ---------- 12. масштабируемая деталь внутри SVG обязана задавать transform-box ----------
//
// Значок «идёт» — кружок внутри <svg> (STATUS_ICONS в schedule.js), и pulse-dot
// масштабировал его без transform-box. Началом отсчёта в Chromium служит левый
// верхний угол viewBox (вычисленный transform-origin — 0 0), поэтому кружок за
// такт уезжал по диагонали на 2.3px: глаз читает это как «иконка дрожит»
// (ADR-0038). Живые слои такую правку не ловят: check-focus замораживает
// анимации, а jsdom их не исполняет вовсе — значит проверять надо текст CSS.

// Ключевые кадры, которые двигают геометрию: transform со scale() или свойство
// scale. rotate/translate внутри <svg> центром не обязаны, а scale — обязан.
const scalingKeyframes = new Set();
for (const m of bareCss.matchAll(/@keyframes\s+([\w-]+)\s*\{([\s\S]*?\n\})/g)) {
  if (/transform\s*:[^;}]*scale\(/.test(m[2]) || /(?:^|[;\s])scale\s*:/.test(m[2])) {
    scalingKeyframes.add(m[1]);
  }
}

// Классы, объявленные внутри <svg> ... </svg>: и в HTML-разметке, и в строковых
// шаблонах JS — значки собирается именно там, и статический HTML страницы их не
// содержит.
const svgClasses = new Set();
const markupFiles = [
  ...htmlFiles(WEB).map((page) => join(WEB, page)),
  ...jsFiles(JS_DIR).map((file) => join(JS_DIR, file)),
];
for (const file of markupFiles) {
  const text = readFileSync(file, "utf8");
  for (const frag of text.matchAll(/<svg[\s\S]*?<\/svg>/g)) {
    for (const cls of frag[0].matchAll(/class="([^"]*)"/g)) {
      for (const name of cls[1].trim().split(/\s+/)) svgClasses.add(name);
    }
  }
}

let scaledInsideSvg = 0;
for (const block of bareCss.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
  const selector = block[1].trim().replace(/\s+/g, " ");
  if (selector.startsWith(":root") || selector.startsWith("@")) continue;
  const anim = /(?:^|[;\s])animation(?:-name)?\s*:\s*([^;]+)/.exec(block[2]);
  if (!anim) continue;
  const names = [...anim[1].matchAll(/[\w-]+/g)].map((m) => m[0]).filter((n) => scalingKeyframes.has(n));
  if (!names.length) continue;
  // Деталь ищется в последней части селектора: правило
  // `.lesson-status.is-active .status-dot` анимирует `.status-dot`, а не плашку.
  const tail = selector.split(/[\s>+~]+/).pop();
  const classes = [...tail.matchAll(/\.([\w-]+)/g)].map((m) => m[1]);
  if (!classes.some((c) => svgClasses.has(c))) continue;
  scaledInsideSvg += 1;
  const missing = ["transform-box", "transform-origin"].filter((p) => !new RegExp(`(?:^|[;\\s])${p}\\s*:`).test(block[2]));
  if (missing.length) {
    fail("css/style.css", `правило «${selector}» крутит кадры «${names.join(", ")}» на детали внутри <svg> ` +
      `и не задаёт ${missing.join(" и ")} — началом отсчёта станет левый верхний угол viewBox, и деталь ` +
      `будет уезжать за такт вместо того, чтобы дышать на месте (ADR-0038)`);
  }
}

if (scalingKeyframes.size && !svgClasses.size) {
  fail("css/style.css", "в фронтенде нет ни одного класса внутри <svg>, но есть масштабирующие @keyframes — " +
    "проверка ADR-0038 съехала на новую разметку и ничего не считает");
}
if (scalingKeyframes.size) {
  notes.push(`масштаб в SVG: кадров ${scalingKeyframes.size}, классов внутри <svg> ${svgClasses.size}, ` +
    `правил, анимирующих деталь в <svg> — ${scaledInsideSvg} (приём ADR-0038)`);
}

if (problems.length > 0) {
  console.error(`check-frontend: найдено проблем — ${problems.length}`);
  for (const p of problems) console.error("  FAIL " + p);
  process.exit(1);
}

console.log(`check-frontend: ок — страниц ${htmlFiles(WEB).length}, проверены подключения скриптов, стилей, id, инлайновые обработчики, структура заголовков, skip-link, scope у таблиц, контраст токенов и его покрытие, общность submit-механизма, ссылки aria-describedby, различимость колец фокуса и отсутствие ручных догрузок страниц в сьютах`);
for (const n of notes) console.log("  ok  " + n);
for (const n of contrastNotes) console.log("  ok  " + n);
