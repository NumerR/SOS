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
//      скрипт (в HTML или в разметке, которую этот скрипт сам и вставляет);
//   4. в HTML нет инлайновых обработчиков (onclick="...") — они и привели к
//      хранящемуся XSS, когда имя задания подставлялось в строку JS.
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

if (problems.length > 0) {
  console.error(`check-frontend: найдено проблем — ${problems.length}`);
  for (const p of problems) console.error("  FAIL " + p);
  process.exit(1);
}

console.log(`check-frontend: ок — страниц ${htmlFiles(WEB).length}, проверены подключения скриптов, стилей, id и инлайновых обработчиков`);
for (const n of notes) console.log("  ok  " + n);
