// Панель «Аккаунт и доступ»: смена пароля и «выйти везде».
// Обработчики вешаются на форму и кнопки, inline-обработчиков в HTML нет.
(function () {
    const form = document.getElementById("passwordForm");
    const message = document.getElementById("passwordMessage");
    const logoutAllBtn = document.getElementById("logoutAllBtn");

    if (!form || !logoutAllBtn) return;

    // GS — свойство window, которое собирают core/api-client.js, core/format.js и
    // common.js (все три дописывают один объект). Имя GS доступно здесь просто
    // потому, что свойства window видны как глобальные.
    const { fetchJSON, showMessage, hideMessage } = GS;

    function errorText(error) {
        // Сообщения сервера на английском — переводим то, что реально может
        // случиться, остальное показываем как есть.
        const map = {
            "current password is incorrect": "Текущий пароль указан неверно.",
            "password must be at least 8 characters": "Новый пароль короче 8 символов.",
            "password is too long": "Новый пароль слишком длинный.",
            "new password must differ from the current one": "Новый пароль совпадает с текущим.",
            "csrf token missing": "Сессия истекла — обновите страницу и войдите снова.",
            unauthorized: "Сессия истекла — войдите снова.",
        };

        return map[error.message] || error.message || "Не удалось изменить пароль.";
    }

    form.addEventListener("submit", async (event) => {
        event.preventDefault();
        hideMessage(message);

        const fields = new FormData(form);
        const current = String(fields.get("current_password") || "");
        const next = String(fields.get("new_password") || "");
        const repeat = String(fields.get("repeat_password") || "");

        // Клиентская проверка — только удобство: настоящее ограничение на сервере.
        if (next.length < 8) {
            showMessage(message, "Новый пароль короче 8 символов.", "error");
            return;
        }
        if (next !== repeat) {
            showMessage(message, "Новый пароль и его повтор не совпадают.", "error");
            return;
        }

        const submit = form.querySelector("button[type=submit]");
        submit.disabled = true;

        try {
            const answer = await fetchJSON("/api/auth/change-password", {
                method: "POST",
                body: JSON.stringify({ current_password: current, new_password: next }),
                // 401 здесь означает «текущий пароль не тот», а не «сессия
                // истекла»: уводить на вход нельзя, текст показывает errorText.
                on401: "throw",
            });

            const revoked = Number(answer && answer.revoked_sessions) || 0;
            showMessage(
                message,
                revoked > 0
                    ? `Пароль изменён. Выход выполнен на ${revoked} других устройствах.`
                    : "Пароль изменён. Других активных сессий не было.",
                "success",
            );
            form.reset();
        } catch (error) {
            showMessage(message, errorText(error), "error");
        } finally {
            submit.disabled = false;
        }
    });

    // «Выйти везде» снимает и текущую сессию, поэтому после ответа сервера
    // оставаться на странице смысла нет — уводим на вход.
    async function confirmLogoutAll() {
        logoutAllBtn.disabled = true;
        logoutAllBtn.textContent = "Выходим…";

        try {
            await fetchJSON("/api/auth/logout-all", { method: "POST" });
            GS.api.navigate(GS.api.sessionRevokedPath());
        } catch (error) {
            // alert блокирует поток и не показывается в headless-проверках —
            // пишем в то же статусное сообщение, что и форма пароля.
            showMessage(message, error.message || "Не удалось завершить сессии.", "error");
            logoutAllBtn.disabled = false;
            logoutAllBtn.textContent = "Выйти везде";
            logoutAllBtn.classList.remove("btn-danger");
            armed = false;
        }
    }

    // Подтверждение делаем кликом, а не window.confirm: последний блокирует
    // поток и плохо работает на телефоне.
    let armed = false;
    logoutAllBtn.addEventListener("click", () => {
        if (!armed) {
            armed = true;
            logoutAllBtn.textContent = "Точно выйти везде?";
            logoutAllBtn.classList.add("btn-danger");

            setTimeout(() => {
                armed = false;
                logoutAllBtn.textContent = "Выйти везде";
                logoutAllBtn.classList.remove("btn-danger");
            }, 5000);

            return;
        }

        confirmLogoutAll();
    });
})();
