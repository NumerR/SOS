// Проверка панели «Аккаунт и доступ» (dashboard.html + account.js) в jsdom
// против живого сервера: смена пароля и «выйти везде».
//
// Требования: сервер на :8080 и выполненный ../seed.sh (студент s_stud / password123).
// Запуск: cd tests/ui && SOS_BASE_URL=http://localhost:8080 node account-security.cjs
//
// Ограничение, о котором легко споткнуться: вход ограничен 20 попытками на IP за
// 10 минут (internal/ratelimit). Полный `npm run test:all` — это ~16 входов, и
// повторный прогон «поверх» предыдущего упирается в 429 уже на первом логине.
// Поэтому у login() есть lastLoginStatus, а восстановление seed-ного пароля
// вынесено в finally-ветку: иначе сорванный прогон оставляет s_stud с
// временным паролем, и следующий запуск падает с невнятным «s_stud не входит».
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const REPO = path.resolve(__dirname, "..", "..");
const BASE = process.env.SOS_BASE_URL || "http://localhost:8080";

// Временный пароль, потом возвращаем seed-ный: иначе следующий прогон сида
// не сможет войти под s_stud.
const RUN_TAG = Date.now().toString(36).slice(-5);
const TEMP_PASS = "temp-" + RUN_TAG + "-pass1";
const SEED_PASS = "password123";

let lastLoginStatus = 0;
// true с момента, когда пароль реально изменён, и до успешного возврата.
let tempPasswordActive = false;

const results = [];
function check(name, ok, detail = "") {
    results.push({ name, ok, detail });
    console.log(`${ok ? "PASS" : "FAIL"}  ${name}${detail ? " — " + detail : ""}`);
}
function wait(ms) { return new Promise((r) => setTimeout(r, ms)); }

function loginHint(status) {
    if (status === 429) return " — лимит входов 20 на IP за 10 минут, подождите и повторите";
    if (status === 401 || status === 400) return " — неверный логин или пароль";
    return "";
}

async function api(pathname, { method = "GET", body, cookie = "", csrf = "" } = {}) {
    const headers = { "Content-Type": "application/json" };
    if (cookie) headers.Cookie = cookie;
    if (csrf && method !== "GET") headers["X-CSRF-Token"] = csrf;
    const res = await fetch(BASE + pathname, { method, headers, body: body ? JSON.stringify(body) : undefined });
    const text = await res.text();
    let data = null;
    try { data = JSON.parse(text); } catch { data = text; }
    return { status: res.status, data, setCookie: res.headers.getSetCookie?.() || [] };
}

function cookieOf(setCookieHeaders) {
    return setCookieHeaders.map((c) => c.split(";")[0]).join("; ");
}
function csrfOf(cookieHeader) {
    return (cookieHeader.match(/csrf_token=([0-9a-f]+)/) || [])[1] || "";
}

async function login(identifier, password) {
    const res = await api("/api/auth/login", { method: "POST", body: { identifier, password } });
    lastLoginStatus = res.status;
    if (res.status !== 200) return null;
    const cookie = cookieOf(res.setCookie);
    return { cookie, csrf: csrfOf(cookie) };
}

// Возврат seed-ного пароля. Вызывается и при успехе, и в ветке сбоя: сорванный
// на середине прогон иначе «отравляет» следующие.
async function restoreSeedPassword() {
    if (!tempPasswordActive) return true;

    const session = await login("s_stud", TEMP_PASS);
    if (!session) {
        console.error("ВНИМАНИЕ: seed-ный пароль НЕ восстановлен (вход под временным — код "
            + lastLoginStatus + loginHint(lastLoginStatus) + ").");
        console.error("Пароль s_stud сейчас = " + TEMP_PASS + ". Вернуть seed-ный можно через UI");
        console.error("панели «Аккаунт и доступ» или прямо в локальной базе:");
        console.error('  sqlite3 data/app.db "update users set password_hash=(select password_hash');
        console.error("    from users where username='t_teacher') where username='s_stud';\"");
        return false;
    }

    const back = await api("/api/auth/change-password", {
        method: "POST",
        cookie: session.cookie,
        csrf: session.csrf,
        body: { current_password: TEMP_PASS, new_password: SEED_PASS },
    });

    if (back.status !== 200) {
        console.error("ВНИМАНИЕ: возврат seed-ного пароля отклонён — код " + back.status
            + " " + JSON.stringify(back.data));
        return false;
    }

    tempPasswordActive = false;
    return true;
}

(async () => {
    const first = await login("s_stud", SEED_PASS);
    if (!first) {
        throw new Error("s_stud не входит (код " + lastLoginStatus + loginHint(lastLoginStatus)
            + ") — выполните ./seed.sh" + (tempPasswordActive ? "" : ", а если пароль менялся в прошлом прогоне — верните seed-ный (см. WARNING выше)"));
    }

    // «Другое устройство»: вторая сессия того же аккаунта.
    const second = await login("s_stud", SEED_PASS);
    check("вторая сессия того же аккаунта создана", Boolean(second) && second.cookie !== first.cookie);
    if (!second) throw new Error("вторая сессия не создана (код " + lastLoginStatus + loginHint(lastLoginStatus) + ")");

    const html = fs.readFileSync(path.join(REPO, "web/dashboard.html"), "utf8");
    const dom = new JSDOM(html, { url: BASE + "/dashboard", runScripts: "outside-only", pretendToBeVisual: true });
    const { window } = dom;
    const doc = window.document;

    let requests = 0;
    window.fetch = (input, init = {}) => {
        requests += 1;
        const url = String(input).startsWith("http") ? String(input) : BASE + String(input);
        const headers = new window.Headers(init.headers || {});
        headers.set("Cookie", first.cookie);
        return fetch(url, { method: (init.method || "GET").toUpperCase(), headers, body: init.body });
    };
    window.alert = (m) => { window.__alerts = (window.__alerts || []).concat(m); };
    doc.cookie = "csrf_token=" + first.csrf;

    // Только common.js + account.js: dashboard.js тянет курсы и задания,
    // это проверено в assignments-modal.cjs.
    const bundle = ["config.js", "core/api-client.js", "core/format.js", "common.js", "account.js"]
        .map((f) => fs.readFileSync(path.join(REPO, "web/js", f), "utf8"))
        .join("\n;\n");
    window.eval(bundle);

    const form = doc.getElementById("passwordForm");
    const status = doc.getElementById("passwordMessage");
    const logoutAllBtn = doc.getElementById("logoutAllBtn");

    check("панель аккаунта есть в разметке", Boolean(form) && Boolean(logoutAllBtn));

    function fill(name, value) {
        const input = form.querySelector(`[name=${JSON.stringify(name)}]`);
        input.value = value;
        return input;
    }
    async function submit(expectText, label) {
        const before = requests;
        form.dispatchEvent(new window.Event("submit", { bubbles: true, cancelable: true }));
        for (let i = 0; i < 80 && (!status.textContent.trim() || requests === before); i++) await wait(25);
        await wait(60);
        check(label, expectText.test(status.textContent), status.textContent.trim());
    }

    // --- Клиентские проверки: запрос на сервер уходить не должен. ---
    const beforeClientChecks = requests;
    fill("current_password", SEED_PASS);
    fill("new_password", "short");
    fill("repeat_password", "short");
    await submit(/8 символов/, "короткий пароль отклонён на клиенте");
    check("слабый пароль не ушёл на сервер", requests === beforeClientChecks, requests + " запросов");

    fill("new_password", TEMP_PASS);
    fill("repeat_password", "another-pass1");
    await submit(/не совпада/, "несовпадение повтора отклонено на клиенте");

    // --- Неверный текущий пароль. ---
    fill("current_password", "wrong-" + RUN_TAG);
    fill("new_password", TEMP_PASS);
    fill("repeat_password", TEMP_PASS);
    await submit(/неверн/i, "неверный текущий пароль показан пользователю");

    const aliveAfterFail = await api("/api/auth/me", { cookie: first.cookie });
    check("неудачная попытка не разлогинила текущую сессию", aliveAfterFail.status === 200, "код " + aliveAfterFail.status);

    const seedStillWorks = await login("s_stud", SEED_PASS);
    check("пароль не изменился после неверного текущего", Boolean(seedStillWorks));

    // --- Успешная смена. ---
    fill("current_password", SEED_PASS);
    fill("new_password", TEMP_PASS);
    fill("repeat_password", TEMP_PASS);
    // С этого момента пароль изменён: если прогон сорвётся (429, упавший
    // сервер), finally-ветка должна вернуть seed-ный.
    tempPasswordActive = true;
    await submit(/изменён/i, "успешная смена пароля подтверждена в интерфейсе");
    check("сообщение называет число отозванных устройств", /устройств/i.test(status.textContent), status.textContent.trim());

    const oldLogin = await login("s_stud", SEED_PASS);
    check("старый пароль больше не входит", oldLogin === null);

    const newLogin = await login("s_stud", TEMP_PASS);
    check("новый пароль работает", Boolean(newLogin));

    const other = await api("/api/auth/me", { cookie: second.cookie });
    check("вторая сессия отозвана сменой пароля", other.status === 401, "код " + other.status);

    const mine = await api("/api/auth/me", { cookie: first.cookie });
    check("текущая сессия сохранена", mine.status === 200, "код " + mine.status);

    // --- «Выйти везде»: первый клик только подтверждает. ---
    logoutAllBtn.click();
    await wait(80);
    check("первый клик просит подтверждение, а не выходит",
        /точно/i.test(logoutAllBtn.textContent), logoutAllBtn.textContent.trim());

    const stillMine = await api("/api/auth/me", { cookie: first.cookie });
    check("после одного клика сессии живы", stillMine.status === 200, "код " + stillMine.status);

    // Второй клик — реальный выход со всех устройств.
    const freshSecond = await login("s_stud", TEMP_PASS);
    if (!freshSecond) throw new Error("нет сессии «второго устройства» (код " + lastLoginStatus + loginHint(lastLoginStatus) + ")");
    logoutAllBtn.click();
    for (let i = 0; i < 80 && (await api("/api/auth/me", { cookie: first.cookie })).status === 200; i++) await wait(25);

    const revokedSelf = await api("/api/auth/me", { cookie: first.cookie });
    check("второй клик отозвал текущую сессию", revokedSelf.status === 401, "код " + revokedSelf.status);

    const revokedOther = await api("/api/auth/me", { cookie: freshSecond.cookie });
    check("второй клик отозвал и остальные сессии", revokedOther.status === 401, "код " + revokedOther.status);

    check("инлайновых onclick в панели нет", !form.innerHTML.includes("onclick="));
    check("alert не использовался", !window.__alerts, JSON.stringify(window.__alerts || []));

    // --- Возврат seed-ного пароля, чтобы прогон был повторяемым. ---
    // Дополнительный контрольный вход не делаем: ответ 200 на
    // change-password { new_password: SEED_PASS } — уже доказательство, что
    // seed-ный пароль стоит (именно этот endpoint suite выше проверял на
    // «старый пароль больше не входит»). Лишний login здесь дорогой: лимит
    // входов 20/10 мин делится со всеми остальными прогонами.
    const restored = await restoreSeedPassword();
    check("seed-ный пароль восстановлен", restored && !tempPasswordActive);

    const failed = results.filter((r) => !r.ok);
    console.log(`\nИТОГ: ${results.length - failed.length}/${results.length} проверок пройдено`);
    window.close();
    process.exit(failed.length ? 1 : 0);
})().catch(async (error) => {
    console.error("СБОЙ ПРОВЕРКИ:", error.message || error);
    await restoreSeedPassword();
    process.exit(2);
});
