document.addEventListener("DOMContentLoaded", () => {
    const form = document.getElementById("authForm");
    const messageElement = document.getElementById("message");

    if (!form) return;

    // Если пользователь уже вошёл — сразу ведём его в личный кабинет.
    // on401: "throw" — страница входа живёт без сессии, и 401 на probe это
    // штатный ответ. Перенаправление здесь устроило бы цикл из /register в /login.
    GS.fetchJSON("/api/auth/me", { on401: "throw" })
        .then(() => {
            GS.api.navigate(GS.api.dashboardPath());
        })
        .catch(() => {
            // Не авторизован — оставляем форму.
        });

    // Сюда попадают после «выйти везде»: без подписи пользователь не понимает,
    // что произошло — страницы как бы не было.
    if (new URLSearchParams(window.location.search).get("revoked") === "1") {
        GS.showMessage(messageElement, "Все активные сессии завершены. Войдитесь заново.", "success");
    }

    form.addEventListener("submit", async (event) => {
        event.preventDefault();

        GS.hideMessage(messageElement);

        const endpoint = form.dataset.endpoint;
        const formData = new FormData(form);
        const payload = Object.fromEntries(formData.entries());

        const submitButton = form.querySelector('button[type="submit"]');
        const originalButtonText = submitButton ? submitButton.textContent : "";

        if (submitButton) {
            submitButton.disabled = true;
            submitButton.textContent = "Подождите...";
        }

        try {
            await GS.fetchJSON(endpoint, {
                method: "POST",
                body: JSON.stringify(payload),
                // Неверный пароль отвечает 401 — это текст под формой, а не
                // «сессия истекла», поэтому редирект отключён.
                on401: "throw",
            });

            GS.api.navigate(GS.api.dashboardPath());
        } catch (error) {
            GS.showMessage(messageElement, error.message || "Ошибка запроса", "error");

            if (submitButton) {
                submitButton.disabled = false;
                submitButton.textContent = originalButtonText;
            }
        }
    });
});