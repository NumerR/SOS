// Шапка и футер — один компонент вместо шести копий разметки (остаток T3.1,
// ADR-0013).
//
// Зачем это здесь, а не в серверных шаблонах: страницы раздаёт либо Go-сервер
// (вариант A), либо GitHub Pages (вариант B), и partial-шаблон был бы правкой
// только половины раздачи. CSS при этом остаётся единственный: файл не меняет
// ни одного класса, ни одной вложенности — он воспроизводит разметку, которая
// лежала в HTML, символ в символ.
//
// Размен, о котором надо помнить: шапка появляется только когда исполняется JS.
// Для этого приложения он ничего нового не ломает — без JS на страницах не
// появляется ни один список (данные приходят из API), так что «шапка без JS»
// была бы единственной рабочей частью пустой страницы.
//
// Страница задаёт себе четыре вещи атрибутами на <body>:
//   data-layout       — вариант навигации (ключ в PAGES ниже),
//   data-brand-sub    — подпись рядом с названием,
//   data-footer-note  — левая строка футера (если футер на странице есть),
//   data-footer-stack — правая строка футера (на главной отличается).
//
// Идентификаторы в шапке (#logoutBtn, #adminLink, #loginLink, #userAvatar,
// #userName, #userRole, #themeToggle) те же, что были в HTML: скрипты страниц и
// jsdom-сьюты к ним обращаются, и переименование стало бы поломкой интерфейса,
// а не рефакторингом.
(function (global) {
    "use strict";

    const SVG_ATTRS = 'viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"';

    const ICONS = {
        brand: '<path d="M22 10v6M2 10l10-5 10 5-10 5z"/><path d="M6 12v5c3 3 9 3 12 0v-5"/>',
        calendar: '<rect x="3" y="4" width="18" height="18" rx="2"/><path d="M16 2v4M8 2v4M3 10h18"/>',
        shield: '<path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/>',
        home: '<path d="M3 9l9-7 9 7v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/><path d="M9 22V12h6v10"/>',
        back: '<path d="M19 12H5M12 19l-7-7 7-7"/>',
        logout: '<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><path d="M16 17l5-5-5-5M21 12H9"/>',
    };

    // Значения по умолчанию — те же тексты, что стоят на главной.
    const DEFAULT_SUB = "Student OS · Go · SQLite";
    const DEFAULT_FOOTER_NOTE = "Design · Parchment / Neumorphic / Petrol";
    const DEFAULT_FOOTER_STACK = "Go · SQLite · Vanilla JS";

    // Варианты навигации. items: ссылка (href/label), «user» — блок имени,
    // «logout» — кнопка выхода. primary = синяя кнопка, остальное — вторичная.
    // Поле id — не только атрибут разметки: scripts/check-frontend.mjs читает
    // именно его, чтобы сверить getElementById-мишени страниц с тем, что
    // объявляет компонент (обычный HTML-литерал тут был бы виден лучше, но
    // разметка-то генерируется).
    const PAGES = {
        index: {
            wrapper: "nav",
            id: "authLinks",
            items: [
                { href: "schedule", label: "Расписание" },
                { href: "login", label: "Войти" },
                { href: "register", label: "Регистрация", primary: true },
            ],
        },
        login: {
            wrapper: "nav",
            items: [{ href: "register", label: "Регистрация" }],
        },
        register: {
            wrapper: "nav",
            items: [{ href: "login", label: "Войти" }],
        },
        schedule: {
            wrapper: "actions",
            items: [
                { href: "dashboard", label: "Кабинет", icon: "home" },
                { href: "login", label: "Войти", id: "loginLink", primary: true },
            ],
        },
        dashboard: {
            wrapper: "actions",
            items: [
                { href: "schedule", label: "Расписание", icon: "calendar" },
                { href: "profile", label: "Профиль" },
                { href: "admin", label: "Админ", icon: "shield", id: "adminLink", hidden: true },
                { user: true },
                { action: "logout" },
            ],
        },
        profile: {
            wrapper: "actions",
            items: [
                { href: "dashboard", label: "В кабинет", icon: "back" },
                { href: "schedule", label: "Расписание", icon: "calendar" },
                { user: true },
                { action: "logout" },
            ],
        },
        admin: {
            wrapper: "actions",
            items: [
                { href: "dashboard", label: "В кабинет", icon: "back" },
                { href: "profile", label: "Профиль" },
                { action: "logout" },
            ],
        },
    };

    const SWATCHES = ["parchment", "surface", "line", "teal", "ink"]
        .map((name) => `<span class="swatch ${name}"></span>`)
        .join("");

    // Свой local escape, а не GS.escapeHtml: файл подключается раньше common.js
    // и не имеет права зависеть от него — шапка нужна даже если API-слой ниже
    // упадёт. Значения берутся из атрибутов нашего же HTML, но атрибут —
    // произвольная строка, и экранировать её всё равно надо.
    function escapeText(value) {
        return String(value)
            .replaceAll("&", "&amp;")
            .replaceAll("<", "&lt;")
            .replaceAll(">", "&gt;")
            .replaceAll('"', "&quot;")
            .replaceAll("'", "&#039;");
    }

    function icon(name) {
        return `<svg ${SVG_ATTRS} aria-hidden="true">${ICONS[name]}</svg>`;
    }

    function link(item) {
        const classes = item.primary ? ["btn"] : ["btn", "btn-secondary"];
        if (item.hidden) classes.push("hidden");
        const id = item.id ? ` id="${item.id}"` : "";
        const mark = item.icon ? icon(item.icon) : "";
        // Ссылки ведут на страницы, а не в API, поэтому путь относительный:
        // под каталогом GitHub Pages "/login" указал бы не туда (ADR-0011).
        return `<a${id} href="${escapeText(item.href)}" class="${classes.join(" ")}">${mark}${escapeText(item.label)}</a>`;
    }

    function userBox() {
        return `<div class="user-box">
            <span class="avatar" id="userAvatar" aria-hidden="true">?</span>
            <span class="user-meta">
                <span class="user-name" id="userName">Загрузка…</span>
                <span class="chip chip-muted" id="userRole">—</span>
            </span>
        </div>`;
    }

    function logoutButton() {
        return `<button id="logoutBtn" class="btn btn-secondary">${icon("logout")}Выйти</button>`;
    }

    // Переключатель темы. Два значка в одной кнопке: какой показать, решает CSS
    // по data-theme на <html>, поэтому при загрузке страницы кнопка сразу
    // выглядит верно, не дожидаясь JS. aria-pressed и подпись обновляет
    // core/theme.js — он же ловит нажатие.
    function themeButton() {
        return `<button id="themeToggle" class="btn btn-icon theme-toggle" type="button"
            aria-label="Включить тёмную тему" aria-pressed="false" title="Включить тёмную тему">
            <svg class="icon-sun" ${SVG_ATTRS} aria-hidden="true"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/></svg>
            <svg class="icon-moon" ${SVG_ATTRS} aria-hidden="true"><path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"/></svg>
        </button>`;
    }

    function navItems(items) {
        return items.map((item) => {
            if (item.user) return userBox();
            if (item.action === "logout") return logoutButton();
            return link(item);
        }).join("\n");
    }

    function headerInner(sub, spec) {
        const brand = `<a href="./" class="brand">
            <span class="brand-mark" aria-hidden="true">
                <svg ${SVG_ATTRS}>${ICONS.brand}</svg>
            </span>
            <span class="brand-text">
                <span class="brand-title">SOS</span>
                <span class="brand-sub">${escapeText(sub)}</span>
            </span>
        </a>`;

        const items = navItems(spec.items);
        // У гостевых страниц навигация жила в <nav>, у остальных — в
        // <div class="top-actions">; CSS различает их классами, поэтому обёртка
        // осталась в конфиге страницы. Но div landmark'ом не был: на рабочих
        // страницах («Кабинет», «Профиль», «Админ») у списки ориентиров
        // не появилось ничего, хотя ссылки там те же. Теперь тег один — <nav>,
        // класс сохранён, поэтому вёрстка не сдвинулась ни на пиксель.
        const nav = spec.wrapper === "nav"
            ? `<nav ${spec.id ? `id="${spec.id}" ` : ""}class="nav" aria-label="Навигация страницы">
                ${items}
            </nav>`
            : `<nav class="top-actions" aria-label="Навигация страницы">
                ${items}
            </nav>`;

        return `<div class="container header-row">
            ${brand}

            <div class="header-tools">
                ${nav}
                ${themeButton()}
            </div>
        </div>`;
    }

    function footerInner(note, stack) {
        return `<div class="container footer-row">
            <span class="footer-meta">${escapeText(note)}</span>
            <div class="swatches" aria-hidden="true">
                ${SWATCHES}
            </div>
            <span class="footer-meta">${escapeText(stack)}</span>
        </div>`;
    }

    function render() {
        const body = document.body;
        if (!body) return { layout: "", header: false, footer: false };

        // Пустой <header>/<footer> в HTML — сигнал, что страницу уже отрисовали,
        // или заглушка, которую заполняем мы. Повторный вызов (тесты, hot
        // reload) не должен удваивать разметку.
        const key = body.dataset.layout || "";
        const spec = PAGES[key];
        const header = document.querySelector("header.site-header");
        const footer = document.querySelector("footer.site-footer");

        if (header && !header.dataset.filled) {
            if (!spec) {
                console.error(`core/layout.js: неизвестный data-layout="${key}" — навигация не отрисуется`);
            }
            header.innerHTML = headerInner(
                body.dataset.brandSub || DEFAULT_SUB,
                spec || { wrapper: "nav", items: [] },
            );
            header.dataset.filled = "1";
        }

        if (footer && !footer.dataset.filled) {
            footer.innerHTML = footerInner(
                body.dataset.footerNote || DEFAULT_FOOTER_NOTE,
                body.dataset.footerStack || DEFAULT_FOOTER_STACK,
            );
            footer.dataset.filled = "1";
        }

        return { layout: key, header: Boolean(header), footer: Boolean(footer) };
    }

    // Рисуем сразу: скрипт подключён в конце <body>, разметка страницы выше него
    // уже распарсена, а скрипты страниц — ниже и получают готовую шапку.
    render();

    global.GS = global.GS || {};
    global.GS.layout = {
        render,
        pages: PAGES,
        icons: ICONS,
    };
})(window);
