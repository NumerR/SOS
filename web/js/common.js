const GS = (() => {
    function escapeHtml(value) {
        return String(value)
            .replaceAll("&", "&amp;")
            .replaceAll("<", "&lt;")
            .replaceAll(">", "&gt;")
            .replaceAll('"', "&quot;")
            .replaceAll("'", "&#039;");
    }

    function showMessage(element, text, type = "success") {
        if (!element) return;

        element.textContent = text;
        element.className = `message ${type}`;
    }

    function hideMessage(element) {
        if (!element) return;

        element.textContent = "";
        element.className = "message hidden";
    }

    //unix-секунды -> человекочитаемая дата; 0/пусто = «без срока».
    function formatDate(timestamp) {
        if (!timestamp || timestamp === 0) return "Без дедлайна";
        const date = new Date(timestamp * 1000);
        return date.toLocaleDateString("ru-RU", { day: "numeric", month: "short", year: "numeric" });
    }

    // Инициалы для аватара: первая буква имени + первая буквы фамилии.
    function initialsFrom(person) {
        const source = String((person && (person.full_name || person.username)) || "?").trim();
        if (!source) return "?";
        const parts = source.split(/\s+/).filter(Boolean);
        if (parts.length >= 2) return (parts[0][0] + parts[1][0]).toUpperCase();
        return source.slice(0, 2).toUpperCase();
    }

    // Читает csrf-токен из cookie (выставляется сервером при входе/регистрации
    // и пополняется в RequireAuth).
    function getCSRFToken() {
        const match = document.cookie.match(/(?:^|;\s*)csrf_token=([^;]*)/);
        return match ? decodeURIComponent(match[1]) : "";
    }

    async function fetchJSON(url, options = {}) {
        const method = (options.method || "GET").toUpperCase();

        const headers = {
            "Content-Type": "application/json",
            ...(options.headers || {}),
        };

        // Для mutate-запросов прикладываем двойной токен (заголовок == cookie).
        if (method !== "GET" && method !== "HEAD") {
            const token = getCSRFToken();
            if (token) {
                headers["X-CSRF-Token"] = token;
            }
        }

        const response = await fetch(url, {
            credentials: "same-origin",
            ...options,
            headers,
        });

        const contentType = response.headers.get("content-type");

        let data = null;

        if (contentType && contentType.includes("application/json")) {
            data = await response.json();
        } else {
            data = await response.text();
        }

        if (!response.ok) {
            const message =
                (data && typeof data === "object" && data.error) ||
                response.statusText ||
                "Request failed";

            const error = new Error(message);
            error.status = response.status;
            error.data = data;

            throw error;
        }

        return data;
    }

    function bindLogout(buttonId, redirectTo = "/login") {
        const button = document.getElementById(buttonId);
        if (!button) return;

        button.addEventListener("click", async () => {
            button.disabled = true;
            button.textContent = "Выходим...";

            try {
                await fetchJSON("/api/auth/logout", {
                    method: "POST",
                });

                window.location.href = redirectTo;
            } catch (error) {
                alert(error.message || "Не удалось выйти");
                button.disabled = false;
                button.textContent = "Выйти";
            }
        });
    }

    return {
        escapeHtml,
        showMessage,
        hideMessage,
        formatDate,
        initialsFrom,
        fetchJSON,
        bindLogout,
    };
})();