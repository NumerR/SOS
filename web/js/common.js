// GS — пространство имён интерфейса: то, что нужно каждой странице (экран
// HTML, статусные сообщения, подписи, вызов API). Транспортом занимается
// core/api-client.js, общими подписями и датами — core/format.js; они подключаются
// раньше этого файла и дописывают тот же объект.
//
// Раньше GS объявлялся через `const` на верхнем уровне: он жил в глобальной
// lexical-области и не был свойством window, поэтому core-файлы не могли его
// дополнить. Теперь единственный объект висит на window, и `GS` остаётся тем же
// именем, к которому привык код страниц.
(function (global) {
    "use strict";

    function escapeHtml(value) {
        return String(value)
            .replaceAll("&", "&amp;")
            .replaceAll("<", "&lt;")
            .replaceAll(">", "&gt;")
            .replaceAll('"', "&quot;")
            .replaceAll("'", "&#039;");
    }

    function showMessage(element, text, type = "success") {
        if (!element) return;

        element.textContent = text;
        element.className = `message ${type}`;
    }

    function hideMessage(element) {
        if (!element) return;

        element.textContent = "";
        element.className = "message hidden";
    }

    // Текст для 429: страницы печатают его как есть, если не хотят показывать
    // свою формулировку.
    function rateLimitText(error) {
        const seconds = error && error.retryAfter ? Math.ceil(error.retryAfter) : 0;
        return seconds
            ? `Слишком много попыток. Повторите через ${seconds} с.`
            : "Слишком много попыток, повторите позже";
    }

    // Единственная точка, через которую страницы ходят в API. Параметры:
    //   method/body/headers — как в fetch;
    //   on401: "redirect" (по умолчанию) | "throw" — "throw" нужен формам, где
    //   401 является штатным ответом (вход с неверным паролем, смена пароля с
    //   неверным текущим), и страницам с гостевым режимом.
    // Бросает ApiError с status/message/retryAfter/data — см. core/api-client.js.
    function fetchJSON(url, options = {}) {
        if (!global.GS.api) {
            throw new Error("Не подключён core/api-client.js — запросы к API недоступны");
        }

        return global.GS.api.request(url, options);
    }

    // Выход из текущей сессии. Путь указываем относительно страницы
    // ("login", а не "/login"): так же, как и src у скриптов, это даёт работе
    // под каталогом GitHub Pages без отдельной настройки base.
    function bindLogout(buttonId, redirectTo = "login") {
        const button = document.getElementById(buttonId);
        if (!button) return;

        button.addEventListener("click", async () => {
            button.disabled = true;
            button.textContent = "Выходим...";

            try {
                await fetchJSON("/api/auth/logout", { method: "POST" });

                if (global.GS.api) global.GS.api.navigate(redirectTo);
                else global.location.href = redirectTo;
            } catch (error) {
                global.alert(error.message || "Не удалось выйти");
                button.disabled = false;
                button.textContent = "Выйти";
            }
        });
    }

    let format = global.GS.format;
    if (!format) {
        // Не падаем сразу: странице ещё может хватить fetchJSON. Но и молча
        // undefined вместо roleLabel оставлять нельзя — это ровно тот класс
        // поломки (неподключённый скрипт), который ловит check-frontend.mjs.
        console.error("common.js: не подключён core/format.js — подписи ролей и даты будут пустыми");
        format = {};
    }

    Object.assign(global.GS, {
        escapeHtml,
        showMessage,
        hideMessage,
        fetchJSON,
        bindLogout,
        rateLimitText,

        // Подписи и данные-помощники живут в core/format.js; здесь они
        // переэкспортируются под короткими именами, чтобы страницам не
        // приходилось знать, в каком файле что лежит.
        formatDate: format.formatDate,
        formatDateTime: format.formatDateTime,
        initialsFrom: format.initialsFrom,
        roleLabel: format.roleLabel,
        roleChipClass: format.roleChipClass,
        assignableRoles: format.assignableRoles,
        can: format.can,
        toISODate: format.toISODate,
        dayStartUnix: format.dayStartUnix,
        dayEndUnix: format.dayEndUnix,
    });
})(window);
