document.addEventListener("DOMContentLoaded", async () => {
    const usersBody = document.getElementById("usersBody");
    const refreshButton = document.getElementById("refreshBtn");
    const statusElement = document.getElementById("adminMsg");

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

    const assignableRoles = ["student", "teacher", "admin"];

    // Двойная защита: страница уже закрыта на сервере, но если кто-то
    // открыл admin.html напрямую без сессии — уводим на вход.
    let me = null;
    try {
        me = await GS.fetchJSON("/api/auth/me");
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
            usersBody.innerHTML = errorRow(GS.escapeHtml(error.message || "Ошибка загрузки профиля"));
        }
        return;
    }

    GS.bindLogout("logoutBtn", "/login");

    function errorRow(message) {
        return `<tr><td colspan="5"><p class="message message-error">${message}</p></td></tr>`;
    }

    function roleControl(user) {
        const chipClass = roleChipClass[user.role] !== undefined ? roleChipClass[user.role] : "chip-muted";
        const label = roleLabels[user.role] || user.role;

        // Собственную роль менять нельзя: сервер ответит 409, а админ лишится
        // доступа к этой странице посреди сеанса.
        if (user.id === me.id) {
            return `<span class="chip ${chipClass}">${GS.escapeHtml(label)}</span>
                    <span class="empty-hint"> — вы</span>`;
        }

        const options = assignableRoles.map((role) => `
            <option value="${role}"${role === user.role ? " selected" : ""}>${GS.escapeHtml(roleLabels[role])}</option>
        `).join("");

        return `
            <div class="role-control">
                <label class="sr-only" for="role-${GS.escapeHtml(user.id)}">Роль пользователя ${GS.escapeHtml(user.username)}</label>
                <select id="role-${GS.escapeHtml(user.id)}" class="control control-small" data-role-select
                        aria-describedby="adminMsg">
                    ${options}
                </select>
                <button type="button" class="btn btn-secondary" data-action="save-role"
                        data-user-id="${GS.escapeHtml(user.id)}" data-current-role="${GS.escapeHtml(user.role)}">
                    Сохранить
                </button>
            </div>`;
    }

    function rowMarkup(user) {
        return `
            <tr>
                <td class="t-num">#${GS.escapeHtml(user.id)}</td>
                <td class="t-strong">${GS.escapeHtml(user.username)}</td>
                <td class="t-muted">${GS.escapeHtml(user.email)}</td>
                <td>${GS.escapeHtml(user.full_name || "—")}</td>
                <td>${roleControl(user)}</td>
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
            usersBody.innerHTML = errorRow(GS.escapeHtml(error.message || "Не удалось загрузить пользователей"));
        }
    }

    // Делегирование: строки перерисовываются, вешать обработчик на каждую — лишний код.
    if (usersBody) {
        usersBody.addEventListener("click", async (event) => {
            const button = event.target.closest("[data-action='save-role']");
            if (!button) return;

            const select = button.closest(".role-control")?.querySelector("[data-role-select]");
            if (!select) return;

            const userId = Number(button.dataset.userId);
            const role = select.value;

            if (role === button.dataset.currentRole) {
                GS.showMessage(statusElement, "Роль не изменилась", "success");
                return;
            }

            button.disabled = true;
            GS.hideMessage(statusElement);

            try {
                await GS.fetchJSON("/api/admin/users/role", {
                    method: "POST",
                    body: JSON.stringify({ user_id: userId, role }),
                });

                // Сессия изменённого пользователя отозвана на сервере —
                // предупреждаем, чтобы это не выглядело как «его выкинуло».
                GS.showMessage(statusElement, "Роль обновлена. У пользователя сброшены входные сессии.", "success");
                await loadUsers();
            } catch (error) {
                GS.showMessage(statusElement, error.message || "Не удалось обновить роль", "error");
                if (error.status === 401) {
                    window.location.href = "/login";
                    return;
                }
            } finally {
                button.disabled = false;
            }
        });
    }

    if (refreshButton) refreshButton.addEventListener("click", loadUsers);

    await loadUsers();
});
