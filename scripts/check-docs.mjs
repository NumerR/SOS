#!/usr/bin/env node
// Проверка согласованности документации с деревом.
//
// Зачем она нужна: docs/01–docs/05 и README — самый часто правимый artifact этого
// репозитория, и они же единственный, который никто не проверяет. Слой родился из
// трёх находок одного обхода: README ссылался на `internal/apierror/apierror_test.go`
// — файла никогда не было (ADR-0003 принёс `internal/handlers/errors_test.go`),
// двенадцать координат в docs/01 были записаны голым именем файла, а под таким именем
// в дереве два файла, и пять заявленных объёмов пережили правки, которые их сдвинули.
// `go test` и `check-frontend.mjs` этого видеть не могут по построению: документация
// для них — не код.
//
// Проверки:
//   1. путь файла: каждое имя файла в обратных кавычках существует в дереве. Ссылки
//      вида `/css/style.css` — это путь сайта, они проверяются под web/; URL со
//      схемой (https://…) не проверяются вовсе;
//   2. номера строк: `файл:12-34` или `файл:12,34` обязан сойтись на ОДИН файл
//      (голое `courses.go` с номером — находка: таких файлов в дереве два, и ссылка
//      не говорит, о каком речь) и не выходить за его длину;
//   3. хеши коммитов: семизначный hex в кавычках обязан быть коммитом этого
//      репозитория (`git cat-file -e`). Это не даёт сослаться на коммит, которого
//      нет, или на номер ветки, похожий на хеш;
//   4. заявления об объёме: «`файл.sh` (229 строк)» обязано совпасть с `wc -l` этого
//      файла — цифры в отчётах не должны переживать правки, которые их сдвинули.
//      Считается только в README, docs/01 и docs/02: docs/04 и docs/05 — журналы
//      (задач и решений), их цифры описывают день, а не дерево. Не считаются числа
//      после «было», со стрелкой «→», знаки диффа «−N» и «N строк `ok`» — это вывод
//      проверки, а не файл;
//   5. команды: каждый `npm run <скрипт>` из документации есть в `tests/ui/package.json`.
//
// Ограничение, о котором надо помнить: проверка сверяет координату с длиной файла,
// а не с её содержимым. Ссылка `store.go:40` пройдёт, даже если на 40-й строке
// теперь другой код, — переезд функции внутрь файла layer'у не виден. Исторические
// координаты (как их было до `8fef933`) закрываются освобождением с причиной, а не
// молчаливым согласием.
//
// Освобождения (PATH_EXEMPT, LINE_EXEMPT, SIZE_EXEMPT, HASH_EXEMPT, SCRIPT_EXEMPT)
// устроены как CONTRAST_EXEMPT в ADR-0019: каждое требует причину, а неиспользованное
// освобождение — находка. Так список не обрастает мёртвыми записями.
//
// Запуск локально: node scripts/check-docs.mjs
// Код выхода 0 — всё сошлось, 1 — есть находки (печатаются списком).
// В CI это шаг работы `frontend`; там же стоит `fetch-depth: 0`, потому что в
// shallow-клоне объектов коммитов нет и группа 3 физически нечего не сверяет — вместо
// молчаливого нуля скрипт печатает «ГРУППА ПРОПУЩЕНА».

import { readFileSync, readdirSync, statSync, existsSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { join, relative, sep } from "node:path";

const ROOT = process.argv[2] || ".";

// Файлы, которые не часть дерева в смысле ссылок: история git, установленные
// зависимости, служебный каталог редактора и скомпилированный Python-кэш.
const SKIP_DIRS = new Set([".git", "node_modules", ".qoder", "__pycache__", "dist"]);

// Документация: README и docs/*.md. Files-двойники вида «04-TASK-BREAKDOWN 2.md» —
// копии одной страницы, сделанные самой macOS при скачивании; они не часть набора
// и не правятся, поэтому в обход не берутся (иначе проверка ругалась бы на текст,
// который никто не читает).
const DUP_DOC = / \d+\.md$/;

const problems = [];
const notes = [];

function fail(where, message) {
  problems.push(`${where}: ${message}`);
}

function walk(dir, out) {
  for (const entry of readdirSync(dir).sort()) {
    if (SKIP_DIRS.has(entry)) continue;
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) walk(full, out);
    else out.push(relative(ROOT, full).split(sep).join("/"));
  }
  return out;
}

function docFiles() {
  const docsDir = join(ROOT, "docs");
  const docs = existsSync(docsDir)
    ? readdirSync(docsDir)
        .filter((name) => name.endsWith(".md") && !DUP_DOC.test(name))
        .sort()
        .map((name) => `docs/${name}`)
    : [];
  return ["README.md", ...docs];
}

// ---------- освобождения ----------

// Пути, которых в дереве нет намеренно: целевая схема и план.
const PATH_EXEMPT = new Map([
  ["scraper.py", "файл исходного архива парсера; в tools/schedule-parser лежит только то, что вызывает Go-мост (ADR-0021)"],
  ["csrf.js", "позиция целевой схемы §3 docs/02; отдельным файлом не заведён, CSRF добавляет api-client.js (ADR-0011)"],
  ["dom.js", "позиция целевой схемы §3 docs/02; страницы пишутся на ванильном DOM, обёртка не заведена (ADR-0011)"],
  ["deploy-frontend.yml", "план: workflow выкладки на Pages выбран, только если владелец выберет вариант B (docs/03 B1)"],
  ["deploy-backend.yml", "план: workflow выкладки backend — состав зависит от ответа B1 (docs/03)"],
  ["docker-compose.yml", "план: поднятие PostgreSQL для T2.1; файла нет, потому что Docker на машине разработки отсутствует"],
  ["deploy/docker-compose.yml", "план: расположение compose-файла в ADR-0009; каталога deploy/ в дереве нет"],
  ["0001_init.sql", "план: номер миграции core-схемы из T2.1; на SQLite схему создаёт internal/database/db.go"],
  ["internal/modules/notes/module.go", "пример имени модуля в «как добавить модуль», а не файл репозитория"],
  ["login/index.html", "структура выкладки Pages (docs/03 B1), а не дерево репозитория: там страницы — каталоги с index.html"],
  ["dashboard/index.html", "структура выкладки Pages (docs/03 B1), а не дерево репозитория"],
  ["rasp-stud.html", "страница виджета расписания на стороне АПЭКС-ВУЗ (tksu.ru), а не файл репозитория: docs/01 §5 и docs/05 описывают внешнюю ссылку, которую T4.1 заменил своей страницей"],
]);

// Координаты, описывающие состояние прошлого, а не текущего дерева.
const LINE_EXEMPT = new Map([
  ["dashboard.js:284-309", "копия обработчика submit до ADR-0017: её убрали в `GS.bindCreateForm`, в docs/01 §7 это сказано тем же предложением"],
  ["dashboard.js:394-419", "копия обработчика submit до ADR-0017 (тот же остаток, что и выше)"],
  ["dashboard.js:369", "позиция обработчика 401 до `8fef933`: политика переехала в core/api-client.js"],
]);

// Семизначные hex-строки, которые не хеши коммитов.
const HASH_EXEMPT = new Map([
  ["99999999", "договорённость docs/02 §6 и docs/05: дата или id без точного значения, выдуманный маркер, а не коммит"],
]);

// Документы, описывающие состояние «сейчас»: им поверяется заявление об объёме файла.
const CURRENT_DOCS = new Set(["README.md", "docs/01-STATE-REPORT.md", "docs/02-ARCHITECTURE.md"]);

// Цифры «N строк», которые не обещают размер файла в текущем дереве.
const SIZE_EXEMPT = new Map([
  ["scripts/check-focus.mjs:671", "docs/01 §8 пишет про день ADR-0020: скрипт был 671 строка, ADR-0022 (`86edfe9`) довела его до 891 — предложение это и называет"],
  ["dashboard.html:34", "число удалённой разметки секции, а не размер страницы: docs/01 §7 описывает дифф T3.1"],
]);

// Скрипты npm, упомянутые как план, а не как текущая команда.
const SCRIPT_EXEMPT = new Map([
  ["lint", "упомянут в целевом описании CI (§10 docs/02); в tests/ui/package.json скрипта `lint` нет"],
]);

// ---------- разбор ----------

const EXT = "go|mjs|cjs|js|sh|md|sql|yml|yaml|html|css|json|py|txt|tsv|csv|mod|sum";
// Границы значат не меньше, чем список расширений: без lookbehind ссылкой считалась
// строка `_test.go` из маски `*_test.go`, а без lookahead — `GS.can` из описания прав.
// Placeholder-пути вида `internal/modules/<name>/module.go` отсекаются тем же
// lookbehind: перед именем стоит `<` или `/`.
const FILE_REF = new RegExp(
  String.raw`(?<![A-Za-z0-9_.\-/])([A-Za-z0-9][A-Za-z0-9_.\-/]*\.(?:${EXT}))(?![A-Za-z0-9_])(?::(\d+(?:[-,]\d+)*))?`,
  "g"
);
// Диапазон 7..40: короткая форма в документации — 7 символов, но шестнадцатеричный
// маркер из договорённости про id без точного значения (`99999999`) длиннее, и он
// обязан попасть в HASH_EXEMPT, а не unnoticed.
const HASH_REF = /^[0-9a-f]{7,40}$/;
// Размер файла — это заявление в круглых скобках рядом со ссылкой. Вне скобок та же
// фраза описывает вывод проверки («45 строк `ok`»), а не файл.
const PAREN = /\(([^()]*)\)/g;
// `\b` здесь нельзя: он определён через [A-Za-z0-9_], а после кириллического «строк»
// границы слова нет никогда — с ним проверка не находила ни одного заявления.
const SIZE_IN_PAREN = /(\d{1,5})\s*(?:строк|строки|строка)(?![а-яё])/;
const NPM_REF = /npm run ([a-z0-9][a-z0-9:_-]*)/g;

const tree = walk(ROOT, []);
// Намеренно неполная история (shallow-клон, который делает actions/checkout по
// умолчанию) не даёт проверить хеши: объектов коммитов в таком репозитории нет.
// Проверка честно пропускает группу, а не красит все ссылки подряд;страж ниже не
// позволит этому пропуску выглядеть как зелёный прогон с проверенными хешами.
let shallowRepo = false;
try {
  shallowRepo = execFileSync("git", ["rev-parse", "--is-shallow-repository"], { cwd: ROOT, encoding: "utf8" }).trim() === "true";
} catch {
  shallowRepo = true;
}

const basenames = new Map();
const lineCounts = new Map();
for (const path of tree) {
  const base = path.split("/").pop();
  if (!basenames.has(base)) basenames.set(base, []);
  basenames.get(base).push(path);
  try {
    // Как `wc -l`: считается число переводов строк, а не «последняя строка + 1».
    const body = readFileSync(join(ROOT, path), "utf8");
    lineCounts.set(path, (body.match(/\n/g) || []).length);
  } catch {
    /* бинарный или недоступный файл — в дерево ссылок он не попадёт */
  }
}

// Ссылка «файл» → список путей дерева, которые под неё подходят.
function candidates(ref) {
  const clean = ref.replace(/^\.\//, "");
  if (clean.startsWith("/")) {
    // Путь сайта: /css/style.css раздаётся из web/.
    const asSite = tree.filter((p) => p === `web${clean}`);
    return asSite;
  }
  if (clean.includes("/")) {
    return tree.filter((p) => p === clean || p.endsWith(`/${clean}`));
  }
  return basenames.get(clean) || [];
}

function spans(text) {
  return text.match(/`[^`\n]+`/g) || [];
}

const usedPathExempts = new Set();
const usedLineExempts = new Set();
const usedSizeExempts = new Set();
const usedHashExempts = new Set();
const usedScriptExempts = new Set();

const npmScripts = new Set(Object.keys(JSON.parse(readFileSync(join(ROOT, "tests/ui/package.json"), "utf8")).scripts || {}));

let fileRefs = 0;
let lineRefs = 0;
let hashRefs = 0;
let sizeRefs = 0;
let scriptRefs = 0;

for (const doc of docFiles()) {
  let text;
  try {
    text = readFileSync(join(ROOT, doc), "utf8");
  } catch {
    fail(doc, "файл документации не читается");
    continue;
  }

  for (const span of spans(text)) {
    const body = span.slice(1, -1);
    if (/:\/\//.test(body) || /^www\./.test(body)) continue; // внешний URL

    // 1–2. файлы и координаты строк
    for (const m of body.matchAll(FILE_REF)) {
      const [, ref, spec] = m;
      fileRefs++;
      const where = `${doc} — \`${ref}${spec ? ":" + spec : ""}\``;
      const key = spec ? `${ref}:${spec}` : null;
      const list = candidates(ref);
      if (list.length === 0) {
        if (PATH_EXEMPT.has(ref)) usedPathExempts.add(ref);
        else if (key && LINE_EXEMPT.has(key)) usedLineExempts.add(key);
        else fail(where, "файла нет в дереве (или ссылка опечаталась)");
        continue;
      }
      if (!spec) continue;

      // С номером строки ссылка должна указывать на один файл.
      if (list.length > 1) {
        if (LINE_EXEMPT.has(key)) {
          usedLineExempts.add(key);
          continue;
        }
        fail(where, `голое имя ведёт к ${list.length} файлам (${list.join(", ")}) — с номером строки нужен путь с каталогом`);
        continue;
      }
      lineRefs++;
      const path = list[0];
      const count = lineCounts.get(path) || 0;
      const numbers = spec.split(/[-,]/).map(Number);
      const bad = numbers.filter((n) => n > count);
      if (bad.length) {
        if (LINE_EXEMPT.has(key)) usedLineExempts.add(key);
        else fail(where, `в ${path} ${count} строк, а ссылка на ${bad.join(", ")} — координата устарела или это состояние прошлого (тогда нужна запись в LINE_EXEMPT с причиной)`);
      }
    }

    // 3. хеши коммитов
    if (!shallowRepo && HASH_REF.test(body)) {
      hashRefs++;
      try {
        execFileSync("git", ["cat-file", "-e", `${body}^{commit}`], { cwd: ROOT, stdio: "ignore" });
      } catch {
        if (HASH_EXEMPT.has(body)) usedHashExempts.add(body);
        else fail(`${doc} — \`${body}\``, "в этом репозитории нет коммита с таким хешем");
      }
    }
  }

  // 4. заявления об объёме — только там, где документ описывает состояние «сейчас»:
  // README, docs/01 и целевая часть docs/02. docs/04 — журнал задач, docs/05 — журнал
  // решений; их цифры суть снимки на день работы, и «освежить их до текущих» значило
  // бы переписать историю задним числом. Группы 1–3 и 5 на них действуют по-прежнему.
  if (CURRENT_DOCS.has(doc)) {
  for (const paren of text.matchAll(PAREN)) {
    const inner = paren[1];
    const size = inner.match(SIZE_IN_PAREN);
    if (!size) continue;
    // «было 238», «671 → 891», «−34 строки», «45 строк `ok`» — не размер файла в
    // дереве: первые два описывают состояние до правки, третье — размер куска диффа,
    // четвёртое — число строк вывода проверки.
    const ctxBefore = inner.slice(Math.max(0, size.index - 14), size.index);
    const ctxAfter = inner.slice(size.index + size[0].length, size.index + size[0].length + 14);
    if (/был[оа]|→|->|−|-\s*$/.test(ctxBefore) || /→|−/.test(ctxAfter) || /^\s*(`ok`|ok\b|PASS)/.test(ctxAfter)) continue;
    // Ссылка на файл стоит ПЕРЕД скобками («`x.sh` (229 строк, …)»), поэтому ищем
    // её в хвосте текста перед открывающей скобкой; вариант «внутри скобок» тоже
    // поддерживается — так пишут «(229 строк в `x.sh`)».
    const before = text.slice(Math.max(0, paren.index - 60), paren.index);
    const refs = [...before.matchAll(FILE_REF), ...inner.slice(0, size.index).matchAll(FILE_REF)];
    if (refs.length === 0) continue;
    const ref = refs[refs.length - 1][1];
    const claimed = Number(size[1]);
    const list = candidates(ref);
    if (list.length !== 1) continue; // без однозначного файла цифре верить нечего
    sizeRefs++;
    const count = lineCounts.get(list[0]);
    const where = `${doc} — \`${ref}\``;
    if (count !== claimed) {
      const key = `${ref}:${claimed}`;
      if (SIZE_EXEMPT.has(key)) usedSizeExempts.add(key);
      else fail(where, `заявлено ${claimed} строк, в файле ${count} — цифру надо переснять или освободить с причиной (SIZE_EXEMPT)`);
    }
  }
  }
  for (const m of text.matchAll(NPM_REF)) {
    scriptRefs++;
    if (!npmScripts.has(m[1])) {
      if (SCRIPT_EXEMPT.has(m[1])) usedScriptExempts.add(m[1]);
      else fail(doc, `команды \`npm run ${m[1]}\` в tests/ui/package.json нет`);
    }
  }
}

// Стражи вырождения: пустой прогон выглядит как зелёный CI.
if (docFiles().length < 8) fail("docs/", `распознано только ${docFiles().length} файлов документации — ожидалось минимум 8`);
if (fileRefs < 150) fail("README.md", `всего ссылок на файлы: ${fileRefs} — проверка почти ничего не увидела, разбор сломан`);
if (!shallowRepo && hashRefs < 20) fail("README.md", `ссылок на хеши найдено ${hashRefs} — разбор кавычек сломан`);
if (lineRefs < 50) fail("README.md", `координат строк проверено ${lineRefs} — разбор номеров сломан`);
if (sizeRefs < 5) fail("README.md", `заявлений об объёме сверено ${sizeRefs} — разбор скобок сломан`);
if (scriptRefs < 20) fail("README.md", `упоминаний npm run найдено ${scriptRefs} — разбор команд сломан`);

for (const [ref, reason] of PATH_EXEMPT) {
  if (!usedPathExempts.has(ref)) fail("scripts/check-docs.mjs", `освобождение \`${ref}\` больше не встречается в документации — убери его (${reason})`);
}
for (const [hash, reason] of HASH_EXEMPT) {
  if (!shallowRepo && !usedHashExempts.has(hash)) fail("scripts/check-docs.mjs", `освобождение \`${hash}\` больше не встречается в документации — убери его (${reason})`);
}
for (const [key, reason] of LINE_EXEMPT) {
  if (!usedLineExempts.has(key)) fail("scripts/check-docs.mjs", `освобождение \`${key}\` больше не нужно — убери его (${reason})`);
}
for (const [key, reason] of SIZE_EXEMPT) {
  if (!usedSizeExempts.has(key)) fail("scripts/check-docs.mjs", `освобождение объёма \`${key}\` больше не встречается в документации — убери его (${reason})`);
}
for (const [script, reason] of SCRIPT_EXEMPT) {
  if (!usedScriptExempts.has(script)) fail("scripts/check-docs.mjs", `освобождение \`npm run ${script}\` больше не нужно — убери его (${reason})`);
}

notes.push(`файлы: ссылок ${fileRefs}, уникальных путей в дереве ${tree.length}, освобождений ${PATH_EXEMPT.size}`);
notes.push(`строки: ссылок с номерами ${lineRefs}, освобождённых исторических ${usedLineExempts.size}`);
notes.push(shallowRepo
  ? `хеши: ГРУППА ПРОПУЩЕНА — клон без полной истории (--is-shallow-repository = true); в CI за это отвечает fetch-depth: 0`
  : `хеши: проверено ${hashRefs}, освобождений ${HASH_EXEMPT.size}`);
notes.push(`объём: заявлений «N строк» сверено ${sizeRefs}`);
notes.push(`команды: упоминаний npm run ${scriptRefs}, скриптов в package.json ${npmScripts.size}`);

if (problems.length > 0) {
  console.error(`check-docs: найдено проблем — ${problems.length}`);
  for (const p of problems) console.error("  FAIL " + p);
  process.exit(1);
}

console.log(`check-docs: ок — проверены пути файлов, номера строк, хеши коммитов, заявления об объёме и команды npm в документации`);
for (const n of notes) console.log("  ok  " + n);
for (const [ref, reason] of PATH_EXEMPT) console.log(`  ok  освобождено \`${ref}\` — ${reason}`);
for (const [key, reason] of LINE_EXEMPT) console.log(`  ok  освобождено \`${key}\` — ${reason}`);
for (const [key, reason] of SIZE_EXEMPT) console.log(`  ok  объём освобождён \`${key}\` — ${reason}`);
for (const [hash, reason] of HASH_EXEMPT) console.log(`  ok  освобождён \`${hash}\` — ${reason}`);
for (const [script, reason] of SCRIPT_EXEMPT) console.log(`  ok  освобождено \`npm run ${script}\` — ${reason}`);
