// Транспортный слой фронтенда: один вход для всех запросов к API.
//
// Подключается ДО common.js: GS.fetchJSON — тонкая обёртка над GS.api.request,
// страницы ничего не знают ни про базовый URL, ни про CSRF-заголовок, ни про
// то, что делать при 401 и 429.
//
// Почему политика 401 живёт здесь: до этого каждая страница сама решала, что
// делать с истёкшей сессией, и решение было не во всех местах — часть вызовов
// (загрузка курсов, запись/отписка на dashboard) показывала текст ошибки в
// блоке, где крутился spinner, и пользователь оставался на странице без
// доступа. Теперь ответ 401 от «сессионного» запроса всегда уводит на вход, а
// там, где 401 — штатный ответ формы (вход с неверным паролем, смена пароля с
// неверным текущим), вызов явно говорит `on401: "throw"`.
(function (global) {
    "use strict";

    const CSRF_COOKIE = "csrf_token";
    const CSRF_HEADER = "X-CSRF-Token";
    const SAFE_METHODS = ["GET", "HEAD", "OPTIONS"];

    function config() {
        return global.SOS_CONFIG || {};
    }

    function baseURL() {
        const raw = config().API_BASE_URL || "";
        return raw.endsWith("/") ? raw.slice(0, -1) : raw;
    }

    // Путь вида "/api/courses" превращается в "/api/courses" (same-origin) или
    // "https://api.example.test/api/courses" (Pages + внешний бэкенд).
    function urlFor(path) {
        if (/^[a-z][a-z0-9+.-]*:\/\//i.test(path)) return path;
        const suffix = path.startsWith("/") ? path : "/" + path;
        const base = baseURL();
        return base ? base + suffix : suffix;
    }

    // Cross-origin запросы обязаны нести cookie явно: same-origin их отдаёт сам.
    function credentialsMode() {
        return baseURL() ? "include" : "same-origin";
    }

    function csrfToken() {
        const match = document.cookie.match(new RegExp("(?:^|;\\s*)" + CSRF_COOKIE + "=([^;]*)"));
        return match ? decodeURIComponent(match[1]) : "";
    }

    function isSafeMethod(method) {
        return SAFE_METHODS.includes(method);
    }

    function loginPath() {
        return config().LOGIN_PATH || "/login";
    }

    // Куда вести после «выйти везде»: тот же вход, но с признаком, что сессии
    // сняты намеренно — страница входа показывает спокойное сообщение, а не
    // «сессия истекла».
    function sessionRevokedPath() {
        return config().SESSION_REVOKED_PATH || loginPath() + "?revoked=1";
    }

    function dashboardPath() {
        return config().DASHBOARD_PATH || "dashboard";
    }

    function homePath() {
        return config().HOME_PATH || "./";
    }

    // Имя страницы из относительного пути: "login?revoked=1" -> "login".
    function pageName(path) {
        const clean = String(path || "").split("?")[0].split("#")[0];
        if (!clean || clean === "./") return "";
        const parts = clean.split("/");
        return parts[parts.length - 1].replace(/\.html?$/i, "");
    }

    // Сравнение по имени последней части, а не endsWith по всему пути: Pages
    // раздаёт страницы под /<репозиторий>/login, и редирект не должен зацикливать
    // там. Хвостовое сравнение ловило бы и "/relogin", поэтому берём сегмент.
    function onLoginPage() {
        const current = pageName((global.location && global.location.pathname) || "");
        return Boolean(current) && current === pageName(loginPath());
    }

    function navigate(path) {
        const hook = config().navigateTo;
        if (typeof hook === "function") {
            hook(path);
            return;
        }
        global.location.href = path;
    }

    function retrySeconds(response) {
        const header = response.headers ? response.headers.get("Retry-After") : null;
        const parsed = Number(header);
        return Number.isFinite(parsed) && parsed > 0 ? Math.ceil(parsed) : 0;
    }

    // Ошибка одна и та же по форме для всех сбоев: status (0 — сети нет),
    // message (уже по-русски, страницы печатают его как есть), data (тело
    // ответа, если там был JSON), retryAfter (секунды из 429), code.
    function apiError(status, message, data, retryAfter) {
        const error = new Error(message);
        error.name = "ApiError";
        error.status = status;
        error.data = data === undefined ? null : data;
        error.retryAfter = retryAfter || 0;
        error.code = data && typeof data === "object" && data.code ? data.code : null;
        return error;
    }

    // Тело ответа — либо JSON (и тогда {"error": "строка"}), либо текст.
    // Формат {"error": {...}} c кодами — docs/02 §4, он не мигрирован: код
    // клиента к нему готов (error.code), но сервер пока отдаёт строку.
    async function readPayload(response) {
        const type = response.headers ? response.headers.get("content-type") || "" : "";
        if (type.includes("application/json")) {
            try {
                return await response.json();
            } catch (broken) {
                return null;
            }
        }
        const text = await response.text();
        return text ? text.slice(0, 300) : null;
    }

    function serverMessage(payload) {
        if (payload && typeof payload === "object" && typeof payload.error === "string") {
            return payload.error;
        }
        if (typeof payload === "string" && payload.trim()) {
            // Ответ без JSON (например, HTML-страница 404 от прокси).
            return payload.trim();
        }
        return "";
    }

    // Сообщение: сначала то, что сказал сервер, и только если он ничего не
    // сказал — наша русская формулировка по статусу. Серверные тексты точнее
    // («title is required»), и страницы уже переводят их по error.message,
    // поэтому подменять их молча нельзя.
    function describe(status, payload, response) {
        const fromServer = serverMessage(payload);

        if (status === 429) {
            // Для 429 серверный текст всегда одинаковый и бесполезный без
            // срока: клиенту важно, сколько ждать.
            const seconds = retrySeconds(response);
            return seconds
                ? `Слишком много попыток. Повторите через ${seconds} с.`
                : "Слишком много попыток, повторите позже";
        }

        if (fromServer) return fromServer;

        if (status === 401) return "Сессия истекла — войдите заново";
        if (status === 403) return "Недостаточно прав для этого действия";
        if (status >= 500) return "Сервер не справился, попробуйте чуть позже";
        return response.statusText || "Запрос не удался";
    }

    async function request(path, options) {
        const settings = options || {};
        const method = (settings.method || "GET").toUpperCase();
        const onUnauthorized = settings.on401 || "redirect";

        const headers = Object.assign({}, settings.headers);
        if (settings.body !== undefined && !isSafeMethod(method)) {
            headers["Content-Type"] = "application/json";
        }
        if (!isSafeMethod(method)) {
            const token = csrfToken();
            if (token) headers[CSRF_HEADER] = token;
        }

        const controller = typeof global.AbortController === "function"
            ? new global.AbortController()
            : null;
        const timeoutMs = config().REQUEST_TIMEOUT_MS || 0;
        let timer = null;
        if (controller && timeoutMs > 0) {
            timer = global.setTimeout(() => controller.abort(), timeoutMs);
        }

        let response;
        try {
            response = await global.fetch(urlFor(path), {
                method,
                headers,
                body: settings.body,
                credentials: settings.credentials || credentialsMode(),
                signal: controller ? controller.signal : undefined,
            });
        } catch (network) {
            const timedOut = network && network.name === "AbortError";
            throw apiError(0, timedOut
                ? "Сервер не отвечает слишком долго — обновите страницу"
                : "Нет связи с сервером");
        } finally {
            if (timer !== null) global.clearTimeout(timer);
        }

        const payload = await readPayload(response);

        if (!response.ok) {
            const error = apiError(
                response.status,
                describe(response.status, payload, response),
                payload,
                retrySeconds(response),
            );

            if (response.status === 401 && onUnauthorized === "redirect" && !onLoginPage()) {
                navigate(loginPath());
            }

            throw error;
        }

        return payload;
    }

    const api = {
        request,
        get: (path, options) => request(path, Object.assign({ method: "GET" }, options)),
        post: (path, body, options) => request(path, Object.assign({
            method: "POST",
            body: body === undefined ? undefined : JSON.stringify(body),
        }, options)),
        urlFor,
        csrfToken,
        // Единственный способ сменить страницу: учитывает SOS_CONFIG.navigateTo,
        // поэтому тесты в jsdom видят редирект, а Pages не ломается на поддомене.
        navigate,
        loginPath,
        sessionRevokedPath,
        dashboardPath,
        homePath,
        // Нужен тестам и страницам, чтобы не дублировать правило редиректа.
        isCrossOrigin: () => Boolean(baseURL()),
    };

    // GS — пространство имён фронтенда (common.js). Пишем в него, а не в window:
    // второй глобалка на уровне window только одна (SOS_CONFIG).
    global.GS = global.GS || {};
    global.GS.api = api;
})(window);
