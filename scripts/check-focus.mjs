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
// того же размера, но ссылки в ней другие.
async function settle(cdp) {
  let prev = null;
  for (let i = 0; i < 40; i++) {
    const sig = await cdp.eval(SIGNATURE_JS, FOCUSABLE_SELECTOR);
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

// Обход одной страницы в два прохода. Первый — куда реально приходит Tab
// (свежая загрузка страницы единственная сбрасывает точку продолжения
// клавиатурной навигации, и она же включает клавиатурную модальность, без
// которой :focus-visible на программном фокусе не сработает). Второй — видно ли
// индикатор: два снимка одной области, с фокусом и без. Если страница
// перестроила DOM между проходами, индексы теряют смысл — такой прогон
// помечается находкой, а не тихо продолжается.
async function checkPage(cdp, spec) {
  const who = await ensureLogin(cdp, spec.auth);
  await navigate(cdp, BASE + spec.path);
  const sig = await settle(cdp);
  const items = await cdp.eval(TAG_JS, FOCUSABLE_SELECTOR);
  console.log(`\n── ${spec.path} (${spec.label}) как ${who}: фокусируемых элементов ${items.length}`);
  if (items.length === 0) {
    record(spec.path, "FAIL", "—", "на странице нет ни одного фокусируемого элемента");
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
  for (let i = 0; i < items.length + 4; i++) {
    let fp = null;
    for (let attempt = 0; attempt < 4; attempt++) {
      await tabPress(cdp);
      await wait(20);
      fp = await cdp.eval(ACTIVE_FP_JS);
      if (fp !== order[order.length - 1] || order.length === 0) break;
      dropped++;
    }
    if (fp === null) break;                       // фокус ушёл в chrome браузера
    order.push(fp);
    if (fp === order[0] && order.length > 1) break; // обход замкнулся
  }
  const reached = new Set(order);
  const indexes = Array.from(items.keys());
  const mismatch = indexes.filter((i) => i < order.length && order[i] !== String(i));
  if (mismatch.length) {
    record(spec.path, "FAIL", items[mismatch[0]],
      `порядок Tab не совпадает с порядком DOM: первый сбой на позиции ${mismatch[0]}, ` +
      `фактический порядок ${order.join(' → ')}`);
  }
  const unreachable = indexes.filter((i) => !reached.has(String(i)));
  for (const i of unreachable) {
    record(spec.path, "FAIL", items[i], `до элемента #${i} Tab не доходит за ${order.length} нажатий`);
  }
  console.log(`   проход 1: Tab прошёл ${order.length} из ${items.length}, недоступных ${unreachable.length}, ` +
    `повторов нажатия ${dropped}`);

  // ── проход 2: видимость индикатора ──
  let withFocusVisible = 0;
  let measured = 0;
  for (const i of indexes) {
    if (!reached.has(String(i))) continue;
    if (await cdp.eval(SIGNATURE_JS, FOCUSABLE_SELECTOR) !== sig) {
      record(spec.path, "FAIL", items[i], "страница перестроила DOM во время обхода — индексы больше не значат ничего");
      return;
    }
    const st = await cdp.eval(FOCUS_JS, String(i));
    if (!st) { record(spec.path, "FAIL", items[i], "элемент пропал из DOM"); continue; }
    if (!st.focused) { record(spec.path, "FAIL", items[i], "элемент не принимает фокус"); continue; }
    if (st.visible) withFocusVisible++;
    await wait(SETTLE_MS);
    const clip = clipOf(st);

    const focused = await cdp.send("Page.captureScreenshot", { format: "png", clip, captureBeyondViewport: true });
    await cdp.eval(BLUR_JS);
    await wait(SETTLE_MS);
    const blurred = await cdp.send("Page.captureScreenshot", { format: "png", clip, captureBeyondViewport: true });

    if (SHOTS) {
      mkdirSync(SHOTS, { recursive: true });
      const stem = join(SHOTS, `${spec.path.replace(/[^a-z0-9]+/gi, "_")}_${String(i).padStart(2, "0")}`);
      writeFileSync(`${stem}-focused.png`, Buffer.from(focused.data, "base64"));
      writeFileSync(`${stem}-blurred.png`, Buffer.from(blurred.data, "base64"));
    }

    const diff = await cdp.eval(DIFF_JS, { a: blurred.data, b: focused.data, threshold: MIN_DELTA });
    measured++;
    if (diff.error) { record(spec.path, "FAIL", items[i], diff.error); continue; }

    const detail = `изменилось ${diff.changed} из ${diff.pixels} пикс. на фоне ${diff.bg}; ` +
      `≥${MIN_DELTA}:1 к окружению — ${diff.strong} пикс. (макс ${diff.max}:1), ` +
      `к своему «до» — ${diff.strongChange} (макс ${diff.maxChange}:1)`;
    // Решает первая цифра: индикатор должен отличаться от того, что вокруг
    // (WCAG 1.4.11 + 2.4.7). Вторая — WCAG 2.2 SC 2.4.13, которого проект себе
    // не заявлял, поэтому она замечание, а не провал.
    const verdict = diff.strong < MIN_STRONG_AREA ? "FAIL"
      : diff.strongChange < MIN_STRONG_AREA ? "NOTE" : "PASS";
    record(spec.path, verdict, items[i],
      verdict === "FAIL" ? `индикатор фокуса не виден — ${detail}`
        : verdict === "NOTE" ? `индикатор виден, но изменение слабое — ${detail}` : detail);
    process.stdout.write({ PASS: ".", NOTE: "~", FAIL: "!" }[verdict]);
  }
  process.stdout.write(`\n   проход 2: замерено ${measured}, :focus-visible совпал на ${withFocusVisible}\n`);
}

function usage() {
  console.log(`использование: node scripts/check-focus.mjs [--base=http://127.0.0.1:8162]
                     [--chrome=<путь>] [--page=/register] [--shots=<каталог>] [--pad=18]
                     [--verbose]
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
