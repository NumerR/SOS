// Общие мелочи интерфейса: подписи ролей, видимость кнопок по роли, работа с
// датами. Всё это — про то, что показать, а не про то, что разрешено: право
// проверяет сервер (`internal/authz`), а здесь только наборы ролей для
// отрисовки. Если на фронте и в серверном реестре прав разойдётся мнение о том,
// кому что можно, победит сервер (403), а интерфейс потеряет кнопку — это
// известная и намеренная асимметрия.
//
// Файл подключается до common.js и дополняет тот же объект GS, что и
// api-client.js: второй глобалки на уровне window у фронтенда нет.
(function (global) {
    "use strict";

    const ROLE_LABELS = {
        student: "студент",
        teacher: "преподаватель",
        admin: "администратор",
    };

    // Порядок и состав списка, который админ видит в селекте роли. Совпадает с
    // assignableRoles на сервере (internal/handlers/admin.go) — если роль
    // перестанет назначаться вручную, менять надо в двух местах, и это единственное
    // место на фронте, где она перечислена.
    const ASSIGNABLE_ROLES = ["student", "teacher", "admin"];

    const ROLE_CHIP = {
        student: "chip-muted",
        teacher: "chip-outline",
        admin: "",
    };

    const TEACHER_AND_ADMIN = ["teacher", "admin"];
    const STUDENT_AND_ADMIN = ["student", "admin"];

    // Подписи действий журнала (audit_events.action, T1.2).
    //
    // Ключи — строки реестра из internal/store/audit.go: сервер пишет имена,
    // человек видит объяснение. Жёсткого контракта между этими двумя файлами
    // нет, и это осознанный размен: подпись неизвестного действия отдаётся
    // как есть, поэтому новое действие заведомо видно в админке техническим
    // именем, а не исчезает молча.
    const AUDIT_ACTIONS = {
        "auth.login": "вход",
        "auth.logout": "выход",
        "auth.logout_all": "выход на всех устройствах",
        "auth.password_change": "смена пароля",
        "auth.register": "регистрация",
        "admin.role_change": "смена роли",
        "submission.grade": "оценка работы",
        "schedule.groups_sync": "синхронизация каталога групп",
        "schedule.import": "импорт расписания из источника",
    };

    function auditActionLabel(action) {
        return AUDIT_ACTIONS[action] || action || "—";
    }

    // Результат события: «выполнено» / «не выполнено». ok=false — это не
    // ошибка интерфейса, а зафиксированный отказ (неверный текущий пароль,
    // перебор по идентификатору), и выделять его надо как предупреждение.
    function auditResult(ok) {
        return ok
            ? { label: "выполнено", chip: "chip-outline" }
            : { label: "не выполнено", chip: "chip-muted" };
    }

    function roleLabel(role) {
        return ROLE_LABELS[role] || role || "—";
    }

    function roleChipClass(role) {
        return ROLE_CHIP[role] || "chip-muted";
    }

    // Три вопроса «показывать ли элемент этой роли». Названы по действиям, а не
    // по ролям, чтобы вызывающий код не сравнивал строку роли.
    const can = {
        createCourse: (role) => TEACHER_AND_ADMIN.includes(role),
        viewStudents: (role) => TEACHER_AND_ADMIN.includes(role),
        manageBoard: (role) => TEACHER_AND_ADMIN.includes(role),
        enroll: (role) => STUDENT_AND_ADMIN.includes(role),
        // Зеркало серверного права course.students.view.any: показывать ли
        // кнопку «студенты курса» у чужого курса. Владелец строки проверяется
        // на сервере, здесь только отрисовка.
        viewStudentsOfAnyCourse: (role) => role === "admin",
        // Зеркало authz.ScheduleSourceManage: панели импорта расписания нет ни
        // у студента, ни у преподавателя. Синхронизация и импорт делают исходящие
        // запросы к сайту вуза, поэтому кнопка должна быть у одного человека, а
        // отказ сервера (403) не должен выглядеть как сломанная кнопка.
        manageScheduleSource: (role) => role === "admin",
    };

    // ---------- даты ----------
    // Формат запросов к API: `from`/`to` у ленты — unix-секунды начала и конца
    // дня, значения в полях <input type=date> — "ГГГГ-ММ-ДД".

    function pad(value) {
        return String(value).padStart(2, "0");
    }

    function toISODate(date) {
        return date.getFullYear() + "-" + pad(date.getMonth() + 1) + "-" + pad(date.getDate());
    }

    function isoParts(iso) {
        const parts = String(iso || "").split("-").map(Number);
        return parts.length === 3 && parts.every(Number.isFinite) ? parts : null;
    }

    // День начинается с 00:00 локального времени: лента должна показать то, что
    // студент видит «сегодня», а не сдвиг на часовой пояс сервера.
    function dayStartUnix(iso) {
        const parts = isoParts(iso);
        if (!parts) return 0;
        return Math.floor(new Date(parts[0], parts[1] - 1, parts[2], 0, 0, 0, 0).getTime() / 1000);
    }

    // Верхняя граница — 23:59:59 того же дня, иначе «один день» отсекал бы
    // вечерние дедлайны.
    function dayEndUnix(iso) {
        const parts = isoParts(iso);
        if (!parts) return 0;
        return Math.floor(new Date(parts[0], parts[1] - 1, parts[2], 23, 59, 59, 0).getTime() / 1000);
    }

    // ---------- неделя ----------
    //
    // Словарь недели переехал из schedule.js сюда, а не скопирован во вторую
    // страницу: у расписания и у карты «текущая неделя» обязана быть одним и
    // тем же отрезком, иначе человек перешёл бы с «2–8 ноября» на «3–9 ноября»
    // и решал бы, что на одной из страниц данные старше. Границы считаются
    // через те же dayStartUnix/dayEndUnix, что и вся остальная переписка с
    // сервером в unix-секундах.

    // Понедельник недели, в которую попадает дата. Локальное время, не UTC:
    // «эта неделя» считается по календарю человека, который смотрит страницу, и
    // в 00:30 воскресенья по Гринвичу этот календарь ещё суббота.
    function startOfWeek(date) {
        const shift = (date.getDay() + 6) % 7;
        return new Date(date.getFullYear(), date.getMonth(), date.getDate() - shift);
    }

    // Прибавка дней через календарь, а не через миллисекунды: сутки бывают 23
    // и 25 часа, и «+6*24 ч» через переход в летнее время дал бы воскресенье
    // вместо субботы — конец недели уехал бы на день.
    function addDays(date, days) {
        return new Date(date.getFullYear(), date.getMonth(), date.getDate() + days);
    }

    // Родительный падеж месяца: «8 ноября» -> «ноября».
    //
    // Считается датой, а не словарём названий: {month: "long"} сам по себе
    // даёт именительный («ноябрь»), и подписать «2–8 ноябрь» было бы грубой
    // ошибкой в языке, которую видно на каждой второй неделе.
    function monthGenitive(date) {
        return date.toLocaleDateString("ru-RU", { day: "numeric", month: "long" }).replace(/^\d+\s+/, "");
    }

    // Подпись недели: «2–8 ноября», «26 октября – 1 ноября»,
    // «29 декабря 2026 – 4 января 2027».
    //
    // Три формы, а не одна: достаточно месяца, когда неделя внутри него, два
    // — когда пересекает границу, и год — когда пересекает новогоднюю. «29
    // декабря – 4 января» без года читается наугад («это же прошлая неделя?»),
    // и именно на такой неделе человек планирует поездку.
    function weekTitle(start) {
        const end = addDays(start, 6);

        if (start.getMonth() === end.getMonth()) {
            return `${start.getDate()}–${end.getDate()} ${monthGenitive(end)}`;
        }

        const day = (date) => `${date.getDate()} ${monthGenitive(date)}`;
        if (start.getFullYear() === end.getFullYear()) {
            return `${day(start)} – ${day(end)}`;
        }
        return `${day(start)} ${start.getFullYear()} – ${day(end)} ${end.getFullYear()}`;
    }

    // Период показа в unix-секундах: от полуночи понедельника до конца
    // воскресенья.
    function weekRange(start) {
        return {
            from: dayStartUnix(toISODate(start)),
            to: dayEndUnix(toISODate(addDays(start, 6))),
        };
    }

    // 0 и пустота = «сроки нет». Фразу возвращаем отсюда, а не из страниц:
    // раньше «Без дедлайна» и «без срока» были в двух местах по-разному.
    function formatDate(timestamp) {
        if (!timestamp) return "Без дедлайна";
        return new Date(timestamp * 1000).toLocaleDateString("ru-RU", {
            day: "numeric", month: "short", year: "numeric",
        });
    }

    // Для ленты: день недели + дата + время одним куском, чтобы строка
    // «вт, 4 ноя · 18:30» собиралась в одном месте.
    function formatDateTime(timestamp) {
        if (!timestamp) return "Без времени";
        const date = new Date(timestamp * 1000);
        return date.toLocaleDateString("ru-RU", { weekday: "short", day: "numeric", month: "short" }) +
            " · " + date.toLocaleTimeString("ru-RU", { hour: "2-digit", minute: "2-digit" });
    }

    // Инициалы для аватара: первая буква имени + первая буква фамилии, а если
    // фамилии нет — первые две буквы одного слова.
    function initialsFrom(person) {
        const source = String((person && (person.full_name || person.username)) || "?").trim();
        if (!source) return "?";
        const parts = source.split(/\s+/).filter(Boolean);
        if (parts.length >= 2) return (parts[0][0] + parts[1][0]).toUpperCase();
        return source.slice(0, 2).toUpperCase();
    }

    // Три формы русской счётной формы: «1 пара», «2 пары», «5 пар». Живёт здесь
    // потому, что подписей с числом на фронтенде два места — расписание и карта,
    // — и «4 пар» на одной из страниц читалось бы как баг страницы, а не как
    // расхождение двух копий одного правила.
    function plural(count, one, few, many) {
        const mod10 = count % 10;
        const mod100 = count % 100;
        if (mod10 === 1 && mod100 !== 11) return one;
        if (mod10 >= 2 && mod10 <= 4 && (mod100 < 10 || mod100 >= 20)) return few;
        return many;
    }

    // startOfWeek, addDays, monthGenitive, weekTitle, weekRange — см. блок
    // «неделя» выше; plural — счётная форма для подписей с числом.
    global.GS = global.GS || {};
    global.GS.format = {
        roleLabel,
        roleChipClass,
        assignableRoles: () => ASSIGNABLE_ROLES.slice(),
        can,
        auditActionLabel,
        auditResult,
        pad,
        toISODate,
        dayStartUnix,
        dayEndUnix,
        startOfWeek,
        addDays,
        monthGenitive,
        weekTitle,
        weekRange,
        plural,
        formatDate,
        formatDateTime,
        initialsFrom,
    };
})(window);
