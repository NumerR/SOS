// Тема оформления: светлая по умолчанию, тёмная по выбору человека.
//
// Файл подключён в <head>, до <body>: если применять тему из скрипта внизу
// страницы, браузер успевает нарисовать светлый фон и только потом перекрашивает
// его — на медленном соединении это видимое мигание. Здесь атрибут ставится до
// первой отрисовки.
//
// Выбор хранится в localStorage, а без него берётся системная настройка
// (prefers-color-scheme). Кнопка живёт в шапке и рисуется core/layout.js позже,
// поэтому нажатие ловится делегированием на документе: к моменту клика кнопка
// уже существует, а сам файл не зависит от порядка подключения скриптов.
(function (global) {
    "use strict";

    const KEY = "sos.theme";
    const root = document.documentElement;
    const media = typeof global.matchMedia === "function"
        ? global.matchMedia("(prefers-color-scheme: dark)")
        : null;

    function stored() {
        try {
            const value = global.localStorage.getItem(KEY);
            return value === "light" || value === "dark" ? value : "";
        } catch (error) {
            // Приватный режим: выбор не переживёт перезагрузку, но страница
            // обязана работать.
            return "";
        }
    }

    function system() {
        return media && media.matches ? "dark" : "light";
    }

    function current() {
        return root.dataset.theme === "dark" ? "dark" : "light";
    }

    function button() {
        return document.getElementById("themeToggle");
    }

    // Подпись называет действие, а не состояние: человеку с клавиатуры нужно
    // знать, что произойдёт после нажатия, а не что включено сейчас.
    function syncButton() {
        const el = button();
        if (!el) return;

        const dark = current() === "dark";
        const label = dark ? "Включить светлую тему" : "Включить тёмную тему";
        el.setAttribute("aria-label", label);
        el.setAttribute("title", label);
        el.setAttribute("aria-pressed", dark ? "true" : "false");
    }

    function apply(theme, persist) {
        const next = theme === "dark" ? "dark" : "light";
        root.dataset.theme = next;
        if (persist) {
            try {
                global.localStorage.setItem(KEY, next);
            } catch (error) {
                // см. stored()
            }
        }
        syncButton();
        return next;
    }

    function toggle() {
        // Переход на время смены: без него картинка переключается рывком. Класс
        // снимаем после самой долгой из объявленных transition (.25s).
        root.classList.add("theme-switching");
        const next = apply(current() === "dark" ? "light" : "dark", true);
        global.setTimeout(() => root.classList.remove("theme-switching"), 300);
        return next;
    }

    // Кнопки ещё нет на момент выполнения этого файла — шапку рисует layout.js.
    document.addEventListener("click", (event) => {
        const target = event.target && event.target.closest
            ? event.target.closest("#themeToggle")
            : null;
        if (!target) return;

        event.preventDefault();
        toggle();
    });

    // Без сохранённого выбора тема следует за системой и меняется вместе с ней;
    // после явного выбора система уже не решает.
    if (media) {
        const onChange = () => {
            if (!stored()) apply(system(), false);
        };
        if (typeof media.addEventListener === "function") media.addEventListener("change", onChange);
        else if (typeof media.addListener === "function") media.addListener(onChange);
    }

    const initial = stored() || system();
    apply(initial, false);
    document.addEventListener("DOMContentLoaded", syncButton);

    global.GS = global.GS || {};
    global.GS.theme = {
        current,
        apply: (theme) => apply(theme, true),
        toggle,
        key: KEY,
    };
})(window);
