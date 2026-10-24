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
//
// Запуск локально: node scripts/check-frontend.mjs
// Код выхода 0 — всё в порядке, 1 — есть находки (они печатаются списком).

import { readFileSync, readdirSync, existsSync } from "node:fs";
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

// Пары, где цвет действительно несёт текст (или границу контрола). Новый член
// списка добавляется вместе с комментарием в CSS, объясняющим выбор значения.
const CONTRAST_PAIRS = [
  ["--muted", "--parchment", 4.5, "подсказки, меты карточек, «Обновлено»"],
  ["--muted", "--surface", 4.5, "то же внутри панелей"],
  ["--muted", "--parchment-deep", 4.5, "бейдж «без оценки»"],
  ["--ink-soft", "--parchment", 4.5, "подписи секций"],
  ["--ink-soft", "--parchment-deep", 4.5, "chip-muted"],
  ["--ink", "--parchment", 4.5, "основной текст"],
  ["--teal", "--parchment", 4.5, "ссылки и номер секции"],
  ["--ok", "--ok-tint", 4.5, "текст успеха"],
  ["--danger", "--danger-tint", 4.5, "текст отказа"],
  ["#f5f0e6", "--teal", 4.5, "подпись синей кнопки"],
  ["#f5f0e6", "--danger", 4.5, "подпись кнопки удаления"],
  ["--line-strong", "--parchment", 3.0, "граница поля ввода"],
  ["--line-strong", "--surface", 3.0, "граница поля ввода на панели"],
];

const contrastNotes = [];
// Пара может задавать и литеральный цвет (подпись кнопок — #f5f0e6, он нигде
// не объявлен токеном); тогда он участвует в проверке как есть.
function resolveColor(ref) {
  if (ref.startsWith("#")) return ref;
  return tokens[ref] || null;
}

for (const [fg, bg, min, why] of CONTRAST_PAIRS) {
  const [fgValue, bgValue] = [resolveColor(fg), resolveColor(bg)];
  if (!fgValue || !bgValue) {
    fail("css/style.css", "контраст-проверка: токен " + (fgValue ? bg : fg) + " не найден в :root — список пар устарел");
    continue;
  }
  const value = ratio(fgValue, bgValue);
  if (value === null) {
    fail("css/style.css", `контраст ${fg}/${bg}: значение не цвет sRGB hex`);
    continue;
  }
  if (value < min) {
    fail("css/style.css", `контраст ${fg} на ${bg} = ${value.toFixed(2)}:1, нужно ${min}:1 (${why})`);
  } else {
    contrastNotes.push(`контраст ${fg} на ${bg} = ${value.toFixed(2)}:1 (порог ${min}) — ${why}`);
  }
}

if (problems.length > 0) {
  console.error(`check-frontend: найдено проблем — ${problems.length}`);
  for (const p of problems) console.error("  FAIL " + p);
  process.exit(1);
}

console.log(`check-frontend: ок — страниц ${htmlFiles(WEB).length}, проверены подключения скриптов, стилей, id, инлайновые обработчики, структура заголовков, skip-link, scope у таблиц и контраст токенов`);
for (const n of notes) console.log("  ok  " + n);
for (const n of contrastNotes) console.log("  ok  " + n);
