// Страница профиля: Identity-блок из GET /api/auth/me.
//
// Панель смены пароля и «выйти везде» на этой странице обслуживает account.js
// (он же раньше висел на dashboard). Здесь только чтение профиля — и ровно одна
// вещь, за которой следит приёмка T3.1: «Загрузка…» не должна оставаться
// навсегда, при сбое на месте spinner появляется текст ошибки.
(function () {
    const nameEl = document.getElementById("profileName");
    const usernameEl = document.getElementById("profileUsername");
    const emailEl = document.getElementById("profileEmail");
    const roleEl = document.getElementById("profileRole");
    const statusEl = document.getElementById("profileStatus");

    // Значения пишем через textContent, а не innerHTML: имя и почта — строки,
    // которые администратор мог изменить через API, и экранировать их в шаблоне
    // легче забыть.
    function setText(element, value) {
        if (element) element.textContent = value;
    }

    function setPending() {
        [nameEl, usernameEl, emailEl].forEach((element) => {
            if (element) element.textContent = "Загрузка…";
        });
        if (roleEl) roleEl.textContent = "—";
    }

    // «Загрузка…» в строке таблицы — тот же вечный spinner, что и в блоке ленты:
    // при отказе меняем её на прочитываемый текст, а шапку и кнопку выхода
    // оставляем working.
    function setError(text) {
        setPending();
        [nameEl, usernameEl, emailEl].forEach((element) => {
            if (element) element.textContent = "—";
        });
        GS.showMessage(statusEl, text, "error");
    }

    async function loadProfile() {
        setPending();

        let user;
        try {
            user = await GS.fetchJSON("/api/auth/me");
        } catch (error) {
            // 401 здесь штатный случай «сессии нет»: api-client уже увёл на
            // вход, текст показываем для случая «сервер ответил что-то другое».
            setError(error.message || "Не удалось прочитать профиль.");
            return;
        }

        if (!user || !user.username) {
            setError("Ответ сервера не похож на профиль — обновите страницу.");
            return;
        }

        GS.hideMessage(statusEl);
        setText(nameEl, user.full_name || user.username);
        setText(usernameEl, user.username);
        setText(emailEl, user.email || "—");

        if (roleEl) {
            roleEl.textContent = GS.roleLabel(user.role);
            roleEl.className = "chip " + GS.roleChipClass(user.role);
        }

        // Шапку заполняет тот же набор id, что и на dashboard: layout.js их
        // рисует, а данные знает только этот запрос.
        const headerName = document.getElementById("userName");
        const headerRole = document.getElementById("userRole");
        const headerAvatar = document.getElementById("userAvatar");
        setText(headerName, user.full_name || user.username);
        setText(headerAvatar, GS.initialsFrom(user));
        if (headerRole) headerRole.textContent = GS.roleLabel(user.role);
    }

    loadProfile();
})();
