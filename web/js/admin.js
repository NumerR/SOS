document.addEventListener("DOMContentLoaded", async () => {
    const usersBody = document.getElementById("usersBody");
    const refreshButton = document.getElementById("refreshBtn");

    const roleLabels = {
        student: "студент",
        teacher: "преподаватель",
        admin: "администратор",
    };

    const roleChipClass = {
        student: "chip-muted",
        teacher: "chip-outline",
        admin: "",
    };

    // Двойная защита: страница уже закрыта на сервере, но если кто-то
    // открыл admin.html напрямую без сессии — уводим на вход.
    try {
        const me = await GS.fetchJSON("/api/auth/me");
        if (me.role !== "admin") {
            window.location.href = "/dashboard";
            return;
        }
    } catch (error) {
        if (error.status === 401) {
            window.location.href = "/login";
            return;
        }
        if (usersBody) {
            usersBody.innerHTML = `<tr><td colspan="5"><p class="message message-error">${GS.escapeHtml(error.message || "Ошибка загрузки профиля")}</p></td></tr>`;
        }
        return;
    }

    GS.bindLogout("logoutBtn", "/login");

    function rowMarkup(user) {
        const chipClass = roleChipClass[user.role] !== undefined ? roleChipClass[user.role] : "chip-muted";
        const label = roleLabels[user.role] || user.role;
        return `
            <tr>
                <td class="t-num">#${GS.escapeHtml(user.id)}</td>
                <td class="t-strong">${GS.escapeHtml(user.username)}</td>
                <td class="t-muted">${GS.escapeHtml(user.email)}</td>
                <td>${GS.escapeHtml(user.full_name || "—")}</td>
                <td><span class="chip ${chipClass}">${GS.escapeHtml(label)}</span></td>
            </tr>
        `;
    }

    function renderUsers(users) {
        if (!usersBody) return;

        if (!users.length) {
            usersBody.innerHTML = `
                <tr><td colspan="5">
                    <div class="empty-state" style="border:0; box-shadow:none; background:transparent;">
                        <span class="empty-text">Пользователей пока нет</span>
                    </div>
                </td></tr>
            `;
            return;
        }

        usersBody.innerHTML = users.map(rowMarkup).join("");
    }

    async function loadUsers() {
        if (!usersBody) return;

        usersBody.innerHTML = `
            <tr><td colspan="5">
                <div class="empty-state" style="border:0; box-shadow:none; background:transparent;">
                    <span class="spinner" aria-hidden="true"></span>
                    <span class="empty-hint">Загрузка пользователей…</span>
                </div>
            </td></tr>
        `;

        try {
            const users = await GS.fetchJSON("/api/admin/users");
            renderUsers(users);
        } catch (error) {
            if (error.status === 403) {
                window.location.href = "/dashboard";
                return;
            }
            usersBody.innerHTML = `<tr><td colspan="5"><p class="message message-error">${GS.escapeHtml(error.message || "Не удалось загрузить пользователей")}</p></td></tr>`;
        }
    }

    if (refreshButton) refreshButton.addEventListener("click", loadUsers);

    await loadUsers();
});