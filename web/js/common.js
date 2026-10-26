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

    // Подпись поля по ключу из error.fields (ADR-0003).
    //
    // Сервер называет поле как он его читает в JSON — `title`, `group`,
    // `course_id`. Человек видит в форме «Название», «Группа». Подпись берём у
    // самой формы: ищем input по name и читаем текст его label. Если поля в
    // форме нет (или label нет), возвращаем ключ как есть — лучше незнакомое
    // слово, чем молчание.
    function fieldLabel(form, key) {
        if (!form || !key) return key || "";
        // Даты в формах — поле с суффиксом `_raw`: в API уезжает вычисленный
        // `event_date`/`due_date`, поэтому ключ сервера в elements нет, и без
        // этой пары человек читал бы «— поле: event_date» вместо «Дата и время».
        const control = form.elements ? (form.elements[key] || form.elements[key + "_raw"]) : null;
        if (!control) return key;
        const label = control.labels && control.labels.length ? control.labels[0] : null;
        if (!label) return key;
        // Внутренний <span class="field-label"> несёт подпись, а в label может
        // быть ещё и подсказка-«helper» — берём именно подпись, если она есть.
        const caption = label.querySelector(".field-label");
        return (caption ? caption.textContent : label.textContent).trim() || key;
    }

    // Единый пересказ ошибки API для блока формы: текст сервера плюс подпись
    // поля, если сервер назвал поле. Pages-совместимый и без предположений о
    // формате: у ошибки без конверта (старый сервер, прокси, обрыв сети)
    // остаётся просто message.
    function describeServerError(error, form, fallback) {
        const message = (error && error.message) || fallback || "Запрос не удался";
        const fields = error && error.fields ? error.fields : null;
        if (!fields) return message;

        const keys = Object.keys(fields);
        if (!keys.length) return message;

        const labels = keys.map((key) => fieldLabel(form, key)).join(", ");
        return `${message} — поле: ${labels}`;
    }

    // Общий обработчик формы «что-то создать» (POST + перезагрузка списка).
    //
    // Почему он один на весь фронтенд: до этого шага такие обработчики
    // существовали в трёх копиях — две в schedule.js (ресурс и событие) и одна
    // в dashboard.js (курс), отличались они адресом, текстами и списком полей, а
    // блокировка кнопки, «is-loading», сброс формы и сообщение об ошибке были
    // дословно общие. Класс багов T0.1 (модалки) вырос ровно из этого: правку
    // вносили в одну копию, вторая оставалась старой. Копия в dashboard.js была
    // к тому же старше по поведению — она печатала `error.message` и теряла
    // подпись поля, которую сервер называет в конверте 422 (ADR-0003), то есть
    // на кабинете ошибка «title is required» выглядела голой, а на расписании —
    // с «… — поле: Название».
    //
    // Параметры:
    //   form, message  — узлы формы и строки статуса;
    //   endpoint       — адрес POST;
    //   buildPayload   — FormData -> объект, уезжающий как JSON;
    //   requiredText   — отказ до запроса, если payload.title пуст (по умолчанию
    //                    «Название обязательно», страница курса говорит «Название
    //                    курса обязательно»);
    //   successText, errorText — тексты после ответа и после отказа без message;
    //   reload         — функция перезагрузки списка после успеха.
    // Поведение зафиксировано тестом tests/ui/schedule-forms.cjs, включая ветку
    // 401 («уйти на вход», кнопка не остаётся заблокированной).
    function bindCreateForm(options) {
        const {
            form,
            message,
            endpoint,
            buildPayload,
            successText,
            errorText,
            reload,
            requiredText = "Название обязательно",
        } = options;
        if (!form) return;

        form.addEventListener("submit", async (event) => {
            event.preventDefault();
            hideMessage(message);

            const payload = buildPayload(new FormData(form));
            if (!payload.title) {
                showMessage(message, requiredText, "error");
                return;
            }

            const btn = form.querySelector('button[type="submit"]');
            const original = btn ? btn.innerHTML : "";
            if (btn) { btn.classList.add("is-loading"); btn.disabled = true; }
            try {
                await fetchJSON(endpoint, { method: "POST", body: JSON.stringify(payload) });
                showMessage(message, successText, "success");
                form.reset();
                await reload();
            } catch (error) {
                // Сервер назвал поле — к тексту добавится его подпись из формы
                // («Название обязательно» уходит как текст, поля знает сервер).
                showMessage(message, describeServerError(error, form, errorText), "error");
            } finally {
                if (btn) { btn.classList.remove("is-loading"); btn.disabled = false; btn.innerHTML = original; }
            }
        });
    }

    // Единственная точка, через которую страницы ходят в API. Параметры:
    //   method/body/headers — как в fetch;
    //   on401: "redirect" (по умолчанию) | "throw" — "throw" нужен формам, где
    //   401 является штатным ответом (вход с неверным паролем, смена пароля с
    //   неверным текущим), и страницам с гостевым режимом.
    // Бросает ApiError с status/message/retryAfter/data плюс code и fields из
    // серверного конверта — см. core/api-client.js.
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
        bindCreateForm,
        rateLimitText,
        describeServerError,
        fieldLabel,

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
        auditActionLabel: format.auditActionLabel,
        auditResult: format.auditResult,
        toISODate: format.toISODate,
        dayStartUnix: format.dayStartUnix,
        dayEndUnix: format.dayEndUnix,
    });
})(window);
