#!/usr/bin/env node
// =============================================================
// check-focus.mjs — приёмка состояний фокуса живым браузером
// =============================================================
//
// Зачем: доступность страниц проверяется двумя слоями, и оба смотрят на
// разметку и стили, а не на картинку. `scripts/check-frontend.mjs` считает
// контраст цветов и то, что правило фокуса вообще существует;
// `tests/ui/accessibility.cjs` проверяет связку поля с его описанием в jsdom.
// Ни один из них не отвечает на вопрос человека с клавиатуры: «когда я нажал
// Tab, я увидел, куда попал?». Это WCAG 2.4.7 (Focus Visible), и он зависит от
// каскада, от клиппинга контейнером, от `transition` и от того, рисует ли
// браузер тень там, где её ждал автор CSS.
//
// Что делает: поднимает headless Chromium (свой профиль, свой порт; к браузеру
// пользователя не подключается и его вкладки не трогает), открывает каждую
// страницу, входит туда, куда нужно, и обходит элементы реальными нажатиями
// Tab. Дальше два замера. Первый — порядок: список фокусируемых элементов
// сверяется с тем, куда клавиатура действительно приходит (порядок Tab и
// порядок DOM расходятся из-за tabindex, и молча «доехать до i-го» нельзя).
// Второй — видимость: для каждого элемента сравниваются два снимка одной
// области, с фокусом и без, и считается, сколько пикселей изменилось и с какой
// контрастностью.
//
// Порог: индикатор считается видимым, если среди изменившихся пикселей
// нашлось не меньше MIN_STRONG_AREA таких, что цвет «после» отличается от
// окружающего фона (медиана рамки кадра) хотя бы на MIN_DELTA = 3:1 — столько
// WCAG 1.4.11 требует от нетекстового элемента. Меньше — фокус есть, а
// человека к нему не пускают: он не видит, куда попал.
//
// Порог по площади, а не по «самому сильному пикселю», нужен потому, что
// единичная яркая точка в области ничего не доказывает: у текстового поля её
// даёт мигающая каретка (17 пикселей на 10:1 против настоящего кольца в
// сотни пикселей). Каретку, переходы и анимации замеры всё равно заморожены
// отдельно — но площадь остаётся второй страховкой.
//
// Второй замер — контрастность самого изменения (пиксель «до» против пикселя
// «после») — это дух WCAG 2.2 SC 2.4.13. Проект заявляет 2.1 AA, поэтому
// слабое изменение печатается замечанием, а не провалом: у поля, которое
// просто темнеет границей, индикатор виден, но изменение составляет 1,9:1.
//
// С ADR-0022 к страницам добавились модальные окна: их содержимое появляется
// только по клику, и ни одна статическая проверка про него не знает. Пять
// состояний диалога на /dashboard открываются настоящими кликами по настоящим
// кнопкам, и внутри них идёт тот же двухпроходный обход — только по элементам
// `#modalDialog` и в координатах вьюпорта (оверлей прибит к окну). Для диалога
// проверяется ещё и ловушка: Tab обязан остаться внутри. Ничего не
// отправляется: кнопка «сохранить» в форме только фокусируется и замеряется.
//
// Запуск (нужен живой сервер и сид):
//   ADDR=127.0.0.1:8162 DB_PATH=/tmp/sos-focus.db \
//     ADMIN_USERNAME=admin ADMIN_PASSWORD='focus-pass-123' go run ./cmd/server
//   SOS_BASE_URL=http://127.0.0.1:8162 ADMIN_USERNAME=admin \
//     ADMIN_PASSWORD='focus-pass-123' bash tests/ui/seed.sh
//   node scripts/check-focus.mjs --base=http://127.0.0.1:8162
//
// Браузер: $CHROME_PATH, --chrome=<путь>, иначе поиск в кэше Playwright и в
// /Applications. Если браузера нет — код 2 и явное сообщение: «не проверено»
// не должно выглядеть как «проверено и зелёное».
//
// В CI этого шага нет: раннер GitHub Actions без браузера давал бы красный CI
// на ровном месте, а смысл проверки — замер отрисовки, которого на статичном
// раннере не сделать. См. docs/05-DECISIONS.md, ADR-0020.

import { spawn } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { join } from "node:path";

const args = process.argv.slice(2);
const arg = (name) => {
  const hit = args.find((a) => a.startsWith(`--${name}=`));
  return hit ? hit.slice(name.length + 3) : null;
};
const flag = (name) => args.includes(`--${name}`);

const BASE = (arg("base") || process.env.SOS_BASE_URL || "http://localhost:8080").replace(/\/$/, "");
const PAD = Number(arg("pad") || 18); // насколько расширить область элемента
const MIN_REGION = 56; // нижняя граница кадра: цель фокуса может быть 0x0
const MIN_DELTA = 3.0; // контрастность «до → после»
const MIN_STRONG_AREA = 24; // сколько пикселей должны так измениться
const SETTLE_MS = 160; // переходы заморожены — нужен только следующий кадр
const SHOTS = arg("shots"); // каталог для PNG (для ручной проверки находок)
const ONLY = arg("page"); // проверить одну страницу (отладка)
const VIEWPORT = { width: 1280, height: 900 };

const FOCUSABLE_SELECTOR = 'a[href], button, input:not([type="hidden"]), select, textarea, [tabindex]:not([tabindex="-1"]), summary, [contenteditable="true"]';

// Те же признаки, но только внутри открытого диалога. Отдельный селектор нужен
// потому, что за модалкой остаётся живая страница: без ограничения обход
// считал бы её элементы недоступными и падал бы с ложной находкой.
const DIALOG_SELECTOR = [
  'a[href]', 'button', 'input:not([type="hidden"])', 'select', 'textarea',
  '[tabindex]:not([tabindex="-1"])', 'summary', '[contenteditable="true"]',
].map((part) => `#modalDialog ${part}`).join(", ");

// Модальные окна (ADR-0022). Разметка диалога одна — #modalContainer на
// /dashboard, — но содержимое у него разное, и по-разному выглядит кольцо у
// поля внутри него: список заданий, форма создания, сдача работы, таблица
// сдач с полями оценки. steps — цепочка кликов от свежей страницы до нужного
// содержимого; ничего не отправляется, кнопка «сохранить» только фокусируется.
const MODALS = [
  {
    id: "assignments-student", page: "/dashboard", auth: "student",
    label: "список заданий студента",
    steps: ['[data-action="view-assignments-student"]'],
  },
  {
    id: "submit-work", page: "/dashboard", auth: "student",
    label: "форма сдачи работы",
    steps: ['[data-action="view-assignments-student"]', '[data-modal-action="open-submit"]'],
  },
  {
    id: "assignments-teacher", page: "/dashboard", auth: "teacher",
    label: "управление заданиями (преподаватель)",
    steps: ['[data-action="manage-assignments"]'],
  },
  {
    id: "create-assignment", page: "/dashboard", auth: "teacher",
    label: "форма нового задания",
    steps: ['[data-action="manage-assignments"]', '[data-modal-action="open-create"]'],
  },
  {
    id: "submissions", page: "/dashboard", auth: "teacher",
    label: "таблица сдач с оценками",
    steps: ['[data-action="manage-assignments"]', '[data-modal-action="open-submissions"]'],
  },
];

const CREDS = {
  student: { identifier: "s_stud", password: "password123" },
  teacher: { identifier: "t_teacher", password: "password123" },
  admin: { identifier: process.env.ADMIN_USERNAME || "admin", password: process.env.ADMIN_PASSWORD || "" },
};

const PAGES = [
  { path: "/", auth: null, label: "витрина" },
  { path: "/login", auth: null, label: "вход" },
  { path: "/register", auth: null, label: "регистрация" },
  { path: "/net-404-eto-tot-zhe-404", auth: null, label: "страница 404" },
  { path: "/dashboard", auth: "student", label: "кабинет студента" },
  { path: "/schedule", auth: "student", label: "расписание" },
  { path: "/profile", auth: "student", label: "профиль" },
  { path: "/admin", auth: "admin", label: "админ-панель" },
];

// ---------- браузер ----------

const PLAYWRIGHT_ROOTS = [
  join(homedir(), "Library/Caches/ms-playwright"),
  join(process.env.HOME || "", ".cache/ms-playwright"),
];

const PLAYWRIGHT_GLOBS = [
  "chromium_headless_shell-*/chrome-headless-shell-mac-*/chrome-headless-shell",
  "chromium_headless_shell-*/chrome-headless-shell-linux*/chrome-headless-shell",
  "chromium-*/chrome-mac-x64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing",
  "chromium-*/chrome-mac-*/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing",
  "chromium-*/chrome-linux*/chrome",
];

const SYSTEM_CHROMES = [
  "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
  "/Applications/Chromium.app/Contents/MacOS/Chromium",
  "/usr/bin/google-chrome",
  "/usr/bin/chromium",
  "/usr/bin/chromium-browser",
];

// Разворачивает «каталог/подкаталог/*/файл» без зависимостей и без shell.
function expand(root, pattern) {
  if (!existsSync(root)) return [];
  let current = [root];
  for (const part of pattern.split("/")) {
    const next = [];
    for (const dir of current) {
      if (!part.includes("*")) {
        const candidate = join(dir, part);
        if (existsSync(candidate)) next.push(candidate);
        continue;
      }
      const prefix = part.slice(0, part.indexOf("*"));
      const suffix = part.slice(part.indexOf("*") + 1);
      let names = [];
      try { names = readdirSync(dir); } catch { names = []; }
      for (const name of names) {
        if (!name.startsWith(prefix) || !name.endsWith(suffix)) continue;
        const candidate = join(dir, name);
        if (existsSync(candidate)) next.push(candidate);
      }
    }
    current = next;
  }
  return current;
}

function findChrome() {
  const explicit = arg("chrome") || process.env.CHROME_PATH;
  if (explicit) {
    if (!existsSync(explicit)) die(`браузер не найден по пути: ${explicit}`);
    return explicit;
  }
  for (const root of PLAYWRIGHT_ROOTS) {
    for (const pattern of PLAYWRIGHT_GLOBS) {
      const hit = expand(root, pattern)[0];
      if (hit) return hit;
    }
  }
  for (const candidate of SYSTEM_CHROMES) if (existsSync(candidate)) return candidate;
  die(
    "браузер не найден: задайте CHROME_PATH или --chrome=<путь к chrome-headless-shell>.\n" +
      "  Приёмка фокуса не заменяется статическим разбором: ей нужен настоящий движок\n" +
      "  отрисовки. Код 2 означает «не проверено», а не «ок»."
  );
}

function die(message, code = 2) {
  console.error(message);
  process.exit(code);
}

// ---------- CDP-клиент ----------

class Session {
  constructor(wsUrl) {
    this.ws = new WebSocket(wsUrl);
    this.nextId = 1;
    this.pending = new Map();
    this.ws.addEventListener("message", (ev) => {
      const msg = JSON.parse(ev.data);
      if (!msg.id || !this.pending.has(msg.id)) return;
      const { resolve, reject, method } = this.pending.get(msg.id);
      this.pending.delete(msg.id);
      if (msg.error) reject(new Error(`${method}: ${JSON.stringify(msg.error)}`));
      else resolve(msg.result);
    });
  }

  open() {
    return new Promise((resolve, reject) => {
      this.ws.addEventListener("open", resolve, { once: true });
      this.ws.addEventListener("error", () => reject(new Error("CDP: веб-сокет не открылся")), { once: true });
    });
  }

  send(method, params = {}) {
    const id = this.nextId++;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject, method });
      this.ws.send(JSON.stringify({ id, method, params }));
    });
  }

  // Тело выполняется в странице как async-функция с одним параметром `p`:
  // наружу не уезжают массивы пикселей, только итоги.
  async eval(body, p) {
    const res = await this.send("Runtime.evaluate", {
      expression: `(async (p) => {\n${body}\n})(${p === undefined ? "undefined" : JSON.stringify(p)})`,
      awaitPromise: true,
      returnByValue: true,
    });
    if (res.exceptionDetails) {
      const e = res.exceptionDetails.exception;
      throw new Error("ошибка в странице: " + (e ? e.description || e.message : res.exceptionDetails.text));
    }
    return res.result.value;
  }

  close() {
    try { this.ws.close(); } catch { /* уже закрыт */ }
  }
}

async function launchChrome(bin) {
  const profile = mkdtempSync(join(tmpdir(), "sos-focus-profile-"));
  const proc = spawn(bin, [
    "--headless",
    "--disable-gpu",
    "--disable-dev-shm-usage",
    "--no-sandbox",
    "--hide-scrollbars",
    `--user-data-dir=${profile}`,
    "--remote-debugging-port=0",
    `--window-size=${VIEWPORT.width},${VIEWPORT.height}`,
    "about:blank",
  ], { stdio: ["ignore", "ignore", "pipe"] });
  let stderr = "";
  proc.stderr.on("data", (d) => { stderr += d; });

  const kill = () => { try { proc.kill("SIGKILL"); } catch { /* уже мёртв */ } };
  const portFile = join(profile, "DevToolsActivePort");
  const deadline = Date.now() + 20000;
  let port = 0;
  while (Date.now() < deadline) {
    if (existsSync(portFile)) {
      const text = readFileSync(portFile, "utf8").trim();
      if (text) { port = Number(text.split("\n")[0].trim()); break; }
    }
    await new Promise((r) => setTimeout(r, 100));
  }
  if (!port) { kill(); die("Chromium не поднял DevTools за 20 секунд:\n" + stderr); }

  const info = await (await fetch(`http://127.0.0.1:${port}/json/version`)).json().catch(() => ({}));
  const list = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
  const page = list.find((t) => t.type === "page" && t.webSocketDebuggerUrl);
  if (!page) { kill(); die("у Chromium нет page-таргета:\n" + stderr); }
  const cdp = new Session(page.webSocketDebuggerUrl);
  await cdp.open();
  const version = info.Browser || info.product || "?";
  return { cdp, proc, kill, profile, version };
}

// ---------- JS, который выполняется в странице ----------

// describe() и фильтр видимости нужны и для подписи DOM, и для разметки
// кандидатов — один раз, чтобы «что мы увидели» и «что мы помечали» совпадали.
const PAGE_PRELUDE = `
  function focusables() {
    const out = [];
    for (const el of document.querySelectorAll(p)) {
      if (el.disabled || el.getAttribute('tabindex') === '-1') continue;
      const cs = getComputedStyle(el);
      if (cs.display === 'none' || cs.visibility === 'hidden') continue;
      let hidden = false;
      for (let q = el.parentElement; q; q = q.parentElement) {
        const s = getComputedStyle(q);
        if (s.display === 'none' || s.visibility === 'hidden') { hidden = true; break; }
      }
      if (hidden) continue;
      out.push(el);
    }
    return out;
  }
  function describe(el) {
    const name = el.getAttribute('aria-label')
      || (el.textContent || '').trim().replace(/\\s+/g, ' ').slice(0, 46)
      || el.getAttribute('placeholder')
      || el.getAttribute('name')
      || el.getAttribute('href')
      || '';
    const cls = (el.className || '').toString().trim().split(/\\s+/).filter(Boolean).slice(0, 2).join('.');
    const type = el.getAttribute('type') ? '[' + el.getAttribute('type') + ']' : '';
    return el.tagName.toLowerCase() + type
      + (el.id ? '#' + el.id : '')
      + (cls ? '.' + cls : '')
      + (name ? ' «' + name + '»' : '');
  }
`;

const SIGNATURE_JS = PAGE_PRELUDE + `
  return focusables().map(describe).join('|');
`;

const TAG_JS = PAGE_PRELUDE + `
  const items = [];
  for (const el of focusables()) {
    el.setAttribute('data-fp', String(items.length));
    items.push(describe(el));
  }
  return items;
`;

const ACTIVE_FP_JS = `
  const el = document.activeElement;
  if (!el || el === document.body || el === document.documentElement) return null;
  return el.getAttribute('data-fp');
`;

const BLUR_JS = `const a = document.activeElement; if (a && a.blur) a.blur(); return true;`;

// Метки прошлого обхода. Нужны, потому что обход модалки перенумеровывает
// элементы внутри диалога, а старый `data-fp` остаётся на элементах страницы за
// ним: без сброса замер по индексу #0 взял бы элемент со страницы, а не из окна.
const UNTAG_JS = `
  for (const el of document.querySelectorAll('[data-fp]')) el.removeAttribute('data-fp');
  return true;
`;

// Клик по первому совпадению — так открываются модалки: настоящим событием по
// настоящей кнопке, а не вызовом внутренней функции скрипта.
const CLICK_JS = `
  const el = document.querySelector(p);
  if (!el) return false;
  el.scrollIntoView({ block: 'center', inline: 'nearest' });
  el.click();
  return true;
`;

// Состояние диалога. Отдельно проверяется, что фокус после открытия уехал
// внутрь: это не про кольцо, а про то, куда человека вообще видно.
const DIALOG_STATE_JS = `
  const overlay = document.getElementById('modalContainer');
  if (!overlay) return { open: false, reason: 'на странице нет #modalContainer — модалки живут только там, где подключён modals.js' };
  if (overlay.classList.contains('hidden')) return { open: false, reason: 'диалог скрыт классом .hidden' };
  const dialog = document.getElementById('modalDialog');
  if (!dialog) return { open: false, reason: 'нет #modalDialog' };
  const cs = getComputedStyle(dialog);
  if (cs.display === 'none' || cs.visibility === 'hidden') return { open: false, reason: 'диалог не отрисован' };
  const box = dialog.getBoundingClientRect();
  if (box.width < 40 || box.height < 40) return { open: false, reason: 'у диалога нет размеров: ' + box.width + 'x' + box.height };
  const a = document.activeElement;
  return { open: true, inside: !!a && dialog.contains(a), focus: a ? a.tagName.toLowerCase() : '—' };
`;

// На время замеров страница лишается переходов, анимаций и мигающей каретки.
// Без этого снимок «после» ловил бы середину transition, а каретка добавляла
// бы ~17 пикселей сильных изменений там, где индикатора нет вообще.
const FREEZE_JS = `
  if (!document.getElementById('focus-probe-style')) {
    const s = document.createElement('style');
    s.id = 'focus-probe-style';
    s.textContent = '*, *::before, *::after { transition: none !important; animation: none !important; caret-color: transparent !important; }';
    document.head.appendChild(s);
  }
  return true;
`;

// Фокус по индексу для замера видимости. Здесь именно el.focus(), а не
// подсчёт нажатий Tab: blur() не откатывает «точку продолжения клавиатурной
// навигации», и досчёт каждый раз приземлялся бы куда попало. Клавиатурная
// модальность к этому моменту уже включена первым проходом, поэтому
// :focus-visible на программном фокусе срабатывает — это проверяется ниже и
// печатается в отчёте, а не предполагается.
const FOCUS_JS = `
  const el = document.querySelector('[data-fp="' + p + '"]');
  if (!el) return null;
  el.focus();
  const r = el.getBoundingClientRect();
  return {
    focused: document.activeElement === el,
    visible: el.matches(':focus-visible'),
    x: r.x + window.scrollX, y: r.y + window.scrollY, w: r.width, h: r.height,
  };
`;

// То же для элемента внутри открытого диалога. Разница в системе координат:
// оверлей прибит к вьюпорту (`position: fixed`), а снимок берётся по
// координатам документа. Прокрутка сбрасывается до и после focus() — иначе
// кадр уехал бы по фону, когда диалог сам прокрутил своё содержимое к полю.
const FOCUS_DIALOG_JS = `
  window.scrollTo(0, 0);
  const el = document.querySelector('[data-fp="' + p + '"]');
  if (!el) return null;
  el.focus();
  window.scrollTo(0, 0);
  const r = el.getBoundingClientRect();
  return {
    focused: document.activeElement === el,
    visible: el.matches(':focus-visible'),
    x: r.x, y: r.y, w: r.width, h: r.height,
  };
`;

// Диалог закрывается штатной кнопкой: следующий случай всё равно начинается с
// новой загрузки страницы, но открытого оверлея на ней быть не должно.
const CLOSE_DIALOG_JS = `
  const btn = document.getElementById('modalCloseBtn');
  if (!btn) return false;
  btn.click();
  return true;
`;

// Diff двух снимков считает сама страница: браузер декодирует собственный PNG.
//
// Что считается «видимым»: пиксели, которые фокус изменил (|до − после| > 8,
// шум растеризации), сравниваются с цветом окружающего фона — медианой внешней
// рамки области на снимке «без фокуса». Это вопрос, который задаёт человек:
// «индикатор отличается от того, что вокруг?». Порог 3:1 — нетекстовый элемент
// по WCAG 1.4.11.
//
// Заодно печатается и вторая цифра: контрастность самого изменения (пиксель до
// против пикселя после). Это дух WCAG 2.2 SC 2.4.13, и она честнее для случая,
// когда граница поля просто меняет оттенок: поле видно (первая цифра), но
// изменение слабое (вторая). Решает первая, вторая — находка без провала.
const DIFF_JS = `
  const img = async (b64) => {
    const bin = atob(b64);
    const bytes = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
    const bmp = await createImageBitmap(new Blob([bytes], { type: 'image/png' }));
    const cv = new OffscreenCanvas(bmp.width, bmp.height);
    const ctx = cv.getContext('2d', { willReadFrequently: true });
    ctx.drawImage(bmp, 0, 0);
    return { w: bmp.width, h: bmp.height, data: ctx.getImageData(0, 0, bmp.width, bmp.height).data };
  };
  const lin = (v) => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); };
  const lum = (c) => 0.2126 * lin(c[0]) + 0.7152 * lin(c[1]) + 0.0722 * lin(c[2]);
  const ratio = (x, y) => {
    const a = lum(x) + 0.05;
    const b = lum(y) + 0.05;
    return a > b ? a / b : b / a;
  };
  const hex = (c) => '#' + c.slice(0, 3).map((v) => v.toString(16).padStart(2, '0')).join('');
  const A = await img(p.a);
  const B = await img(p.b);
  if (A.w !== B.w || A.h !== B.h) {
    return { error: 'размеры кадров разошлись: ' + A.w + 'x' + A.h + ' и ' + B.w + 'x' + B.h };
  }
  // Окружающий фон — медиана двухпиксельной рамки кадра «без фокуса».
  const frame = [];
  const at = (x, y) => { const i = (y * A.w + x) * 4; return [A.data[i], A.data[i + 1], A.data[i + 2]]; };
  for (let x = 0; x < A.w; x++) { frame.push(at(x, 0), at(x, 1), at(x, A.h - 1), at(x, A.h - 2)); }
  for (let y = 2; y < A.h - 2; y++) { frame.push(at(0, y), at(1, y), at(A.w - 1, y), at(A.w - 2, y)); }
  const med = (ch) => frame.map((c) => c[ch]).sort((x, y) => x - y)[frame.length >> 1];
  const bg = [med(0), med(1), med(2)];

  let changed = 0, strong = 0, strongChange = 0, max = 1, maxChange = 1;
  for (let i = 0; i < A.data.length; i += 4) {
    const x = [A.data[i], A.data[i + 1], A.data[i + 2]];
    const y = [B.data[i], B.data[i + 1], B.data[i + 2]];
    const d = Math.max(Math.abs(x[0] - y[0]), Math.abs(x[1] - y[1]), Math.abs(x[2] - y[2]));
    if (d <= 8) continue;                     // шум растеризации и антиалиасинга
    changed++;
    const c = ratio(y, bg);                   // индикатор против того, что вокруг
    const k = ratio(x, y);                    // насколько вообще изменился пиксель
    if (c > max) max = c;
    if (k > maxChange) maxChange = k;
    if (c >= p.threshold) strong++;
    if (k >= p.threshold) strongChange++;
  }
  return {
    pixels: A.w * A.h, changed, strong, strongChange, bg: hex(bg),
    max: Math.round(max * 100) / 100, maxChange: Math.round(maxChange * 100) / 100,
  };
`;

// ---------- работа со страницей ----------

const wait = (ms) => new Promise((r) => setTimeout(r, ms));

async function navigate(cdp, url) {
  await cdp.send("Page.navigate", { url });
  for (let i = 0; i < 60; i++) {
    if (await cdp.eval("return document.readyState;") === "complete") return;
    await wait(100);
  }
  throw new Error("страница не догрузилась: " + url);
}

// Страницы дорисовывают содержимое через fetch: ждём, пока перестанет меняться
// не число элементов, а их идентичность — шапка после /api/auth/me остаётся
// того же размера, но ссылки в ней другие. Селектор приходит параметром: тем же
// приёмом ждём и наполнение открытого диалога.
async function settle(cdp, selector = FOCUSABLE_SELECTOR) {
  let prev = null;
  for (let i = 0; i < 40; i++) {
    const sig = await cdp.eval(SIGNATURE_JS, selector);
    if (sig && sig === prev) return sig;
    prev = sig;
    await wait(150);
  }
  return prev;
}

let loggedInAs = "гость";

async function ensureLogin(cdp, who) {
  const wanted = who || "гость";
  if (loggedInAs === wanted) return wanted;
  const { identifier, password } = CREDS[wanted] || {};
  if (wanted === "admin" && !password) {
    // Нехватка проверяется до запуска браузера (в основном блоке); сюда
    // попадаем только если список страниц и переменные разошлись в процессе.
    die("для /admin нужен ADMIN_PASSWORD (тот же, что у сервера): без неё страница уходит в redirect, и проверка молча пропустила бы её");
  }
  await navigate(cdp, BASE + "/login");
  await settle(cdp);
  const code = await cdp.eval(`
    const res = await fetch('/api/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(p),
    });
    return res.status;
  `, { identifier, password });
  if (code !== 200) throw new Error(`вход ${identifier} не удался: HTTP ${code}`);
  loggedInAs = wanted;
  return wanted;
}

async function tabPress(cdp) {
  const key = { key: "Tab", code: "Tab", windowsVirtualKeyCode: 9, nativeVirtualKeyCode: 9 };
  await cdp.send("Input.dispatchKeyEvent", { type: "keyDown", ...key });
  await cdp.send("Input.dispatchKeyEvent", { type: "keyUp", ...key });
}

// Область замера — координаты документа (не вьюпорта): снимок берётся с
// captureBeyondViewport, и на прокрученной странице вьюпортные координаты
// показывали бы чужой кусок макета вместо элемента. Элемент при этом всё
// равно виден: el.focus() прокручивает страницу к нему сам.
//
// Минимальный размер области нужен для невидимых целей фокуса: у radio-input
// из регистрации бокс 0x0, и без нижней границы кольцо в 28px просто не
// попало бы в кадр.
function clipOf(rect) {
  const w = Math.max(rect.w, MIN_REGION) + PAD * 2;
  const h = Math.max(rect.h, MIN_REGION) + PAD * 2;
  const cx = rect.x + rect.w / 2;
  const cy = rect.y + rect.h / 2;
  return {
    x: Math.max(0, Math.floor(cx - w / 2)),
    y: Math.max(0, Math.floor(cy - h / 2)),
    width: Math.ceil(w),
    height: Math.ceil(h),
    scale: 1,
  };
}

const rows = [];

function record(page, verdict, element, detail) {
  rows.push({ page, verdict, element, detail });
}

// Обход фокусируемых элементов в два прохода. Первый — куда реально приходит Tab
// (свежая загрузка страницы единственная сбрасывает точку продолжения
// клавиатурной навигации, и она же включает клавиатурную модальность, без
// которой :focus-visible на программном фокусе не сработает). Второй — видно ли
// индикатор: два снимка одной области, с фокусом и без. Если страница
// перестроила DOM между проходами, индексы теряют смысл — такой прогон
// помечается находкой, а не тихо продолжается.
//
// Один и тот же обход нужен и для страницы целиком, и для открытой модалки
// (ADR-0022), поэтому селектор и способ снять координаты приходят аргументами,
// а не берутся из модуля: диалог лежит на `position: fixed` подложке, и его
// геометрию надо мерить в координатах вьюпорта, а не документа.
async function runPasses(cdp, label, selector, opts) {
  // Метки прошлого обхода сбрасываются всегда: модалка нумерует элементы заново,
  // и иначе замер нашёл бы по индексу элемент, который уже не тот.
  await cdp.eval(UNTAG_JS);
  const sig = await settle(cdp, selector);
  const items = await cdp.eval(TAG_JS, selector);
  console.log(`   фокусируемых элементов: ${items.length}`);
  if (items.length === 0) {
    record(label, "FAIL", "—", opts.emptyMessage);
    return;
  }
  await cdp.eval(FREEZE_JS);

  // ── проход 1: порядок Tab ──
  // Последовательные dispatchKeyEvent иногда не доходят до рендерера, и тогда
  // кажется, что фокус «застрял» на элементе. Это артефакт инструмента:
  // нажатие повторяется, пока фокус не сдвинулся, а число повторов печатается
  // в строке прохода. Помогает не всегда — настоящая задержка остаётся
  // находкой, потому что повтор её не убирает.
  const order = [];
  let dropped = 0;
  let escaped = false;
  for (let i = 0; i < items.length + 4; i++) {
    let fp = null;
    for (let attempt = 0; attempt < 4; attempt++) {
      await tabPress(cdp);
      await wait(20);
      fp = await cdp.eval(ACTIVE_FP_JS);
      if (fp !== order[order.length - 1] || order.length === 0) break;
      dropped++;
    }
    if (fp === null) { escaped = true; break; }  // фокус ушёл в chrome браузера или за пределы диалога
    order.push(fp);
    if (fp === order[0] && order.length > 1) break; // обход замкнулся
  }
  const reached = new Set(order);
  const indexes = Array.from(items.keys());
  // Для страницы порядок DOM и порядок Tab обязаны совпасть: расхождение значит
  // tabindex или перестроенный между проходами содержимое. У открытого диалога
  // точка входа другая — ловушка начинает обход с кнопки закрытия, а не с
  // первого элемента разметки, — поэтому там проверяется только замкнутость.
  if (opts.assertOrder) {
    const mismatch = indexes.filter((i) => i < order.length && order[i] !== String(i));
    if (mismatch.length) {
      record(label, "FAIL", items[mismatch[0]],
        `порядок Tab не совпадает с порядком DOM: первый сбой на позиции ${mismatch[0]}, ` +
        `фактический порядок ${order.join(' → ')}`);
    }
  }
  if (opts.trap && escaped) {
    record(label, "FAIL", "—",
      `Tab ушёл за пределы диалога: ловушка фокуса не держит (пройдено ${order.length} из ${items.length})`);
  }
  const unreachable = indexes.filter((i) => !reached.has(String(i)));
  for (const i of unreachable) {
    record(label, "FAIL", items[i], `до элемента #${i} Tab не доходит за ${order.length} нажатий`);
  }
  console.log(`   проход 1: Tab прошёл ${order.length} из ${items.length}, недоступных ${unreachable.length}, ` +
    `повторов нажатия ${dropped}` + (opts.trap ? `, ловушка ${escaped ? "НЕ держит" : "держит"}` : ""));

  // ── проход 2: видимость индикатора ──
  let withFocusVisible = 0;
  let measured = 0;
  for (const i of indexes) {
    if (!reached.has(String(i))) continue;
    if (await cdp.eval(SIGNATURE_JS, selector) !== sig) {
      record(label, "FAIL", items[i], "страница перестроила DOM во время обхода — индексы больше не значат ничего");
      return;
    }
    const st = await cdp.eval(opts.focusJs, String(i));
    if (!st) { record(label, "FAIL", items[i], "элемент пропал из DOM"); continue; }
    if (!st.focused) { record(label, "FAIL", items[i], "элемент не принимает фокус"); continue; }
    if (st.visible) withFocusVisible++;
    await wait(SETTLE_MS);
    const clip = clipOf(st);

    const focused = await cdp.send("Page.captureScreenshot", { format: "png", clip, captureBeyondViewport: true });
    await cdp.eval(BLUR_JS);
    await wait(SETTLE_MS);
    const blurred = await cdp.send("Page.captureScreenshot", { format: "png", clip, captureBeyondViewport: true });

    if (SHOTS) {
      mkdirSync(SHOTS, { recursive: true });
      const stem = join(SHOTS, `${opts.slug}_${String(i).padStart(2, "0")}`);
      writeFileSync(`${stem}-focused.png`, Buffer.from(focused.data, "base64"));
      writeFileSync(`${stem}-blurred.png`, Buffer.from(blurred.data, "base64"));
    }

    const diff = await cdp.eval(DIFF_JS, { a: blurred.data, b: focused.data, threshold: MIN_DELTA });
    measured++;
    if (diff.error) { record(label, "FAIL", items[i], diff.error); continue; }

    const detail = `изменилось ${diff.changed} из ${diff.pixels} пикс. на фоне ${diff.bg}; ` +
      `≥${MIN_DELTA}:1 к окружению — ${diff.strong} пикс. (макс ${diff.max}:1), ` +
      `к своему «до» — ${diff.strongChange} (макс ${diff.maxChange}:1)`;
    // Решает первая цифра: индикатор должен отличаться от того, что вокруг
    // (WCAG 1.4.11 + 2.4.7). Вторая — WCAG 2.2 SC 2.4.13, которого проект себе
    // не заявлял, поэтому она замечание, а не провал.
    const verdict = diff.strong < MIN_STRONG_AREA ? "FAIL"
      : diff.strongChange < MIN_STRONG_AREA ? "NOTE" : "PASS";
    record(label, verdict, items[i],
      verdict === "FAIL" ? `индикатор фокуса не виден — ${detail}`
        : verdict === "NOTE" ? `индикатор виден, но изменение слабое — ${detail}` : detail);
    process.stdout.write({ PASS: ".", NOTE: "~", FAIL: "!" }[verdict]);
  }
  process.stdout.write(`\n   проход 2: замерено ${measured}, :focus-visible совпал на ${withFocusVisible}\n`);
}

// Обход страницы: вход, загрузка, два прохода по всем фокусируемым элементам.
async function checkPage(cdp, spec) {
  const who = await ensureLogin(cdp, spec.auth);
  await navigate(cdp, BASE + spec.path);
  console.log(`\n── ${spec.path} (${spec.label}) как ${who}`);
  await runPasses(cdp, spec.path, FOCUSABLE_SELECTOR, {
    assertOrder: true,
    focusJs: FOCUS_JS,
    slug: spec.path.replace(/[^a-z0-9]+/gi, "_"),
    emptyMessage: "на странице нет ни одного фокусируемого элемента",
  });
}

// Обход открытой модалки (ADR-0022): страница грузится заново, по цепочке
// кликов открывается диалог, и замеряется только его содержимое.
//
// Почему это отдельный проход, а не часть страницы: `check-frontend.mjs`
// (группа 9) видит CSS-правило и может сказать, что цвет кольца посчитан, но
// не знает, откроется ли диалог и не перекроет ли его подложка, тень или
// `overflow: auto` у `.modal-content` то, что правило обещает. Живой замер —
// единственный способ увидеть кольцо внутри окна.
async function checkModal(cdp, spec) {
  const who = await ensureLogin(cdp, spec.auth);
  const label = `${spec.page}+${spec.id}`;
  await navigate(cdp, BASE + spec.page);
  console.log(`\n── ${spec.page} → ${spec.label} как ${who}`);
  await settle(cdp);

  let state = null;
  for (const step of spec.steps) {
    const clicked = await cdp.eval(CLICK_JS, step);
    if (!clicked) {
      record(label, "FAIL", step, "модалка не открыта: на странице нет элемента по селектору");
      return;
    }
    state = await waitDialog(cdp);
    if (!state.open) {
      record(label, "FAIL", step, `модалка не открылась после клика по «${step}»: ${state.reason || "диалог остался скрыт"}`);
      return;
    }
  }
  if (!state.inside) {
    record(label, "NOTE", "—", `диалог открыт, но фокус после открытия не уехал в него: activeElement ${state.focus}`);
  }

  await runPasses(cdp, label, DIALOG_SELECTOR, {
    trap: true,
    focusJs: FOCUS_DIALOG_JS,
    slug: label.replace(/[^a-z0-9]+/gi, "_"),
    emptyMessage: "в открытом диалоге нет ни одного фокусируемого элемента",
  });

  // Диалог закрывается, чтобы следующий случай начался с чистого документа:
  // открытый оверлей перекрывает страницу и меняет порядок Tab у соседа.
  await cdp.eval(CLOSE_DIALOG_JS);
}

// Диалог наполняется асинхронно (открыть → fetch → innerHTML), поэтому ждём не
// класс, а устойчивую подпись его содержимого.
async function waitDialog(cdp) {
  let prev = null;
  for (let i = 0; i < 40; i++) {
    const state = await cdp.eval(DIALOG_STATE_JS);
    if (state.open) {
      const sig = await cdp.eval(SIGNATURE_JS, DIALOG_SELECTOR);
      if (sig !== undefined && sig === prev) return state;
      prev = sig;
    }
    await wait(150);
  }
  return await cdp.eval(DIALOG_STATE_JS);
}

function usage() {
  console.log(`использование: node scripts/check-focus.mjs [--base=http://127.0.0.1:8162]
                     [--chrome=<путь>] [--page=/register] [--shots=<каталог>] [--pad=18]
                     [--no-modals] [--verbose]
проверяются восемь страниц и пять состояний модального окна на /dashboard (список
заданий студента, форма сдачи, список преподавателя, форма задания, таблица сдач);
--no-modals оставляет только страницы, --page=/dashboard — только страницу с модалками
переменные: SOS_BASE_URL, CHROME_PATH, ADMIN_USERNAME, ADMIN_PASSWORD (для /admin)
код выхода: 0 — видно, 1 — есть невидимые фокусы, 2 — проверка не выполнялась`);
}

(async () => {
  if (flag("help")) { usage(); return; }
  const pages = ONLY ? PAGES.filter((item) => item.path === ONLY) : PAGES;
  if (ONLY && pages.length === 0) die(`страница ${ONLY} не описана в PAGES`);
  // Не хватает учётки — видно до запуска браузера, а не в середине прогона,
  // когда половина страниц уже замерена и результаты жалко выбрасывать.
  if (pages.some((s) => s.auth === "admin") && !CREDS.admin.password) {
    die("для /admin нужен ADMIN_PASSWORD (тот же, что у сервера): без неё страница уходит в redirect, и проверка молча пропустила бы её");
  }
  const bin = findChrome();
  const { cdp, kill, profile, version } = await launchChrome(bin);
  console.log(`браузер: ${bin}`);
  console.log(`версия:  ${version}`);
  console.log(`база:    ${BASE}`);
  console.log(`порог:   ≥${MIN_STRONG_AREA} пикселей с контрастом ≥${MIN_DELTA}:1, область = элемент ±${PAD}px`);

  try {
    await cdp.send("Page.enable");
    await cdp.send("Runtime.enable");
    await cdp.send("Emulation.setDeviceMetricsOverride", { ...VIEWPORT, deviceScaleFactor: 1, mobile: false });
    for (const spec of pages) {
      try {
        await checkPage(cdp, spec);
      } catch (err) {
        record(spec.path, "FAIL", "—", "страница не проверена: " + (err && err.message || err));
      }
    }
    // Модальные окна (ADR-0022): их содержимое появляется только по клику, и
    // статическая проверка CSS про него ничего не знает.
    const modals = flag("no-modals") ? []
      : ONLY ? (ONLY === "/dashboard" ? MODALS : [])
      : MODALS;
    if (flag("no-modals")) console.log("\nмодалки пропущены (--no-modals)");
    if (ONLY && modals.length === 0 && !flag("no-modals")) {
      console.log(`\nмодалки пропущены: они живут только на /dashboard, а запрошена ${ONLY}`);
    }
    for (const spec of modals) {
      try {
        await checkModal(cdp, spec);
      } catch (err) {
        record(`${spec.page}+${spec.id}`, "FAIL", "—", "модалка не проверена: " + (err && err.message || err));
      }
    }
  } finally {
    cdp.close();
    kill();
    await new Promise((r) => setTimeout(r, 200));
    rmSync(profile, { recursive: true, force: true });
  }

  const pass = rows.filter((r) => r.verdict === "PASS");
  const notes = rows.filter((r) => r.verdict === "NOTE");
  const fail = rows.filter((r) => r.verdict === "FAIL");
  if (flag("verbose")) {
    console.log("\n────── все замеры (--verbose) ──────");
    for (const r of rows) console.log(`${r.verdict.padEnd(5)} ${r.page}  ${r.element}\n       ${r.detail}`);
  }
  console.log("\n══════ находки ══════");
  for (const r of fail) console.log(`FAIL  ${r.page}\n      ${r.element}\n      ${r.detail}`);
  if (fail.length === 0) console.log("нет");
  if (notes.length) {
    console.log("\n────── замечания (WCAG 2.2 SC 2.4.13, проектом не заявлялся) ──────");
    for (const r of notes) console.log(`NOTE  ${r.page}\n      ${r.element}\n      ${r.detail}`);
  }
  console.log(`\nзамеров: ${rows.length}, видимый индикатор ${pass.length}, замечаний ${notes.length}, невидимый ${fail.length}`);
  console.log(fail.length === 0
    ? `ок: до каждого элемента доходит Tab, и каждый меняет картинку минимум на ${MIN_STRONG_AREA} пикселей с контрастом ≥${MIN_DELTA}:1`
    : "есть элементы, фокус которых не виден — см. находки выше");
  process.exit(fail.length === 0 ? 0 : 1);
})().catch((err) => {
  console.error("прогон оборвался: " + (err && err.stack || err));
  process.exit(2);
});
