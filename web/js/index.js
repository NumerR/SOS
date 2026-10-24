document.addEventListener("DOMContentLoaded", async () => {
    const authLinks = document.getElementById("authLinks");

    try {
        // Гостю на лендинге 401 нужен «тихий»: редирект на вход сделал бы
        // первую страницу посещением в никуда.
        const user = await GS.fetchJSON("/api/auth/me", { on401: "throw" });

        if (authLinks && user) {
            // Ссылки относительные — как src у скриптов: под каталогом GitHub
            // Pages абсолютный "/schedule" уходит в корень домена, где страниц нет.
            authLinks.innerHTML = `
                <a href="schedule" class="btn btn-secondary">Расписание</a>
                <a href="dashboard" class="btn">Личный кабинет</a>
                <button id="logoutTop" class="btn btn-secondary">Выйти</button>
            `;

            GS.bindLogout("logoutTop", GS.api.homePath());
        }
    } catch (error) {
        // Пользователь не авторизован — оставляем ссылки на вход и регистрацию.
    }
});