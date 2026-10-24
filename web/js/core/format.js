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
        formatDate,
        formatDateTime,
        initialsFrom,
    };
})(window);
