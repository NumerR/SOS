document.addEventListener("DOMContentLoaded", async () => {
    const authLinks = document.getElementById("authLinks");

    try {
        const user = await GS.fetchJSON("/api/auth/me");

        if (authLinks && user) {
            authLinks.innerHTML = `
                <a href="/schedule" class="btn btn-secondary">Расписание</a>
                <a href="/dashboard" class="btn">Личный кабинет</a>
                <button id="logoutTop" class="btn btn-secondary">Выйти</button>
            `;

            GS.bindLogout("logoutTop", "/");
        }
    } catch (error) {
        // Пользователь не авторизован — оставляем ссылки на вход и регистрацию.
    }
});