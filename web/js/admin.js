document.addEventListener("DOMContentLoaded", async () => {
    const usersBody = document.getElementById("usersBody");
    const refreshButton = document.getElementById("refreshBtn");
    const statusElement = document.getElementById("adminMsg");
    const auditBody = document.getElementById("auditBody");
    const refreshAuditButton = document.getElementById("refreshAuditBtn");

    // Подписи, классы чипов и список ролей для селекта — в core/format.js:
    // раньше этот словарь был скопирован трижды (админка, дашборд, лента).
    const assignableRoles = GS.assignableRoles();

    // Двойная защита: страница уже закрыта на сервере, но если кто-то
    // открыл admin.html напрямую без сессии — уводим на вход (401 обработает
    // api-client), а не-администратору — в кабинет.
    let me = null;
    try {
        me = await GS.fetchJSON("/api/auth/me");
        if (me.role !== "admin") {
            GS.api.navigate(GS.api.dashboardPath());
            return;
        }
    } catch (error) {
        if (error.status === 401) return;
        if (usersBody) {
            usersBody.innerHTML = errorRow(GS.escapeHtml(error.message || "Ошибка загрузки профиля"));
        }
        return;
    }

    GS.bindLogout("logoutBtn");

    function errorRow(message) {
        return `<tr><td colspan="5"><p class="message message-error">${message}</p></td></tr>`;
    }

    function roleControl(user) {
        const chipClass = GS.roleChipClass(user.role);
        const label = GS.roleLabel(user.role);

        // Собственную роль менять нельзя: сервер ответит 409, а админ лишится
        // доступа к этой странице посреди сеанса.
        if (user.id === me.id) {
            return `<span class="chip ${chipClass}">${GS.escapeHtml(label)}</span>
                    <span class="empty-hint"> — вы</span>`;
        }

        const options = assignableRoles.map((role) => `
            <option value="${role}"${role === user.role ? " selected" : ""}>${GS.escapeHtml(GS.roleLabel(role))}</option>
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
                GS.api.navigate(GS.api.dashboardPath());
                return;
            }
            usersBody.innerHTML = errorRow(GS.escapeHtml(error.message || "Не удалось загрузить пользователей"));
        }
    }

    // ---------- журнал действий (T1.2) ----------
    //
    // Сервер отдаёт последние события списком без общего количества: «страница
    // не пустая, но есть ещё» выводится из длины списка, а не из отдельного
    // запроса. Здесь только отрисовка — право audit.view проверяет сервер.

    const AUDIT_COLUMNS = 6;

    // Столько же записей просит страница, сколько показывает. Сервер не отдаёт
    // общего количества, поэтому «ровно limit» — единственный честный признак
    // того, что внизу могли остаться более старые события.
    const AUDIT_LIMIT = 50;

    const auditMore = document.getElementById("auditMore");

    function showAuditMore(visible) {
        if (!auditMore) return;
        auditMore.classList.toggle("hidden", !visible);
    }

    function auditPlaceholder(kind, text) {
        const markup = kind === "spinner"
            ? `<span class="spinner" aria-hidden="true"></span><span class="empty-hint">${text}</span>`
            : `<span class="empty-text">${text}</span>`;

        return `
            <tr><td colspan="${AUDIT_COLUMNS}">
                <div class="empty-state" style="border:0; box-shadow:none; background:transparent;">
                    ${markup}
                </div>
            </td></tr>
        `;
    }

    // Объект действия: «user #12», «session», «identifier». Тип пишет сервер,
    // идентификатор может быть числом строкой (id записи) или пустым.
    function auditTarget(event) {
        if (!event.target_type) return `<span class="t-muted">—</span>`;

        const id = event.target_id ? ` <span class="t-muted">#${GS.escapeHtml(event.target_id)}</span>` : "";
        return GS.escapeHtml(event.target_type) + id;
    }

    function auditRow(event) {
        const result = GS.auditResult(event.ok);
        // detail — технические подробности решения (from=…, score=…), они нужны
        // администратору, но не должны спорить с названием действия.
        const detail = event.detail
            ? `<div class="empty-hint">${GS.escapeHtml(event.detail)}</div>`
            : "";

        return `
            <tr>
                <td class="t-muted">${GS.escapeHtml(GS.formatDateTime(event.at))}</td>
                <td>${GS.escapeHtml(event.actor_username || "не установлен")}</td>
                <td class="t-strong">${GS.escapeHtml(GS.auditActionLabel(event.action))}${detail}</td>
                <td>${auditTarget(event)}</td>
                <td><span class="chip ${result.chip}">${GS.escapeHtml(result.label)}</span></td>
                <td class="t-muted">${GS.escapeHtml(event.ip || "—")}</td>
            </tr>
        `;
    }

    function renderAudit(events) {
        if (!auditBody) return;

        showAuditMore(events.length === AUDIT_LIMIT);

        if (!events.length) {
            auditBody.innerHTML = auditPlaceholder("empty", "Событий пока нет");
            return;
        }

        auditBody.innerHTML = events.map(auditRow).join("");
    }

    async function loadAudit() {
        if (!auditBody) return;

        showAuditMore(false);
        auditBody.innerHTML = auditPlaceholder("spinner", "Загрузка журнала…");

        try {
            const events = await GS.fetchJSON("/api/admin/audit?limit=" + AUDIT_LIMIT);
            renderAudit(events);
        } catch (error) {
            if (error.status === 403) {
                GS.api.navigate(GS.api.dashboardPath());
                return;
            }
            auditBody.innerHTML = `<tr><td colspan="${AUDIT_COLUMNS}"><p class="message message-error">${GS.escapeHtml(error.message || "Не удалось загрузить журнал")}</p></td></tr>`;
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
                // Смена роли — событие журнала: администратор должен видеть его
                // сразу, иначе последняя строка выглядит потерянной.
                await loadAudit();
            } catch (error) {
                GS.showMessage(statusElement, error.message || "Не удалось обновить роль", "error");
                // 401 = собственная сессия истекла: на вход уводит api-client,
                // здесь только не пытаемся перерисовать список.
                if (error.status === 401) return;
            } finally {
                button.disabled = false;
            }
        });
    }

    if (refreshButton) refreshButton.addEventListener("click", loadUsers);
    if (refreshAuditButton) refreshAuditButton.addEventListener("click", loadAudit);

    // Списки независимы, поэтому ждём их вместе: страница не должна
    // выглядеть как админка без журнала.
    await Promise.all([loadUsers(), loadAudit()]);
});
