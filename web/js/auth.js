document.addEventListener("DOMContentLoaded", () => {
    const form = document.getElementById("authForm");
    const messageElement = document.getElementById("message");

    if (!form) return;

    // Если пользователь уже вошёл — сразу ведём его в личный кабинет.
    GS.fetchJSON("/api/auth/me")
        .then(() => {
            window.location.href = "/dashboard";
        })
        .catch(() => {
            // Не авторизован — оставляем форму.
        });

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
            });

            window.location.href = "/dashboard";
        } catch (error) {
            GS.showMessage(messageElement, error.message || "Ошибка запроса", "error");

            if (submitButton) {
                submitButton.disabled = false;
                submitButton.textContent = originalButtonText;
            }
        }
    });
});