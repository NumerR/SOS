document.addEventListener("DOMContentLoaded", async () => {
    const feedSection = document.getElementById("feedSection");
    const feedContainer = document.getElementById("feed");
    const guestNotice = document.getElementById("guestNotice");
    const loginLink = document.getElementById("loginLink");
    const refreshButton = document.getElementById("refreshBtn");
    const applyFilter = document.getElementById("applyFilter");
    const fromDate = document.getElementById("fromDate");
    const toDate = document.getElementById("toDate");

    const stateMeta = {
        none:      { label: "не сдано",  cls: "feed-state-none" },
        submitted: { label: "сдано",     cls: "feed-state-submitted" },
        graded:    { label: "оценено",   cls: "feed-state-graded" },
        own:       { label: "моё задание", cls: "feed-state-own" },
    };

    function pad(n) { return String(n).padStart(2, "0"); }
    function toISODate(d) { return d.getFullYear() + "-" + pad(d.getMonth() + 1) + "-" + pad(d.getDate()); }
    function dayStartUnix(iso) {
        const [y, m, d] = iso.split("-").map(Number);
        return new Date(y, m - 1, d, 0, 0, 0, 0).getTime() / 1000 | 0;
    }
    function dayEndUnix(iso) {
        const [y, m, d] = iso.split("-").map(Number);
        return new Date(y, m - 1, d, 23, 59, 59, 0).getTime() / 1000 | 0;
    }
    function formatDue(ts) {
        const d = new Date(ts * 1000);
        return d.toLocaleDateString("ru-RU", { weekday: "short", day: "numeric", month: "short" }) +
            " · " + d.toLocaleTimeString("ru-RU", { hour: "2-digit", minute: "2-digit" });
    }

    // Дефолтный диапазон: сегодня .. +7 дней.
    const today = new Date();
    const weekLater = new Date(today.getTime() + 7 * 24 * 3600 * 1000);
    if (fromDate) fromDate.value = toISODate(today);
    if (toDate) toDate.value = toISODate(weekLater);

    function renderFeed(items) {
        if (!feedContainer) return;
        if (!items.length) {
            feedContainer.innerHTML = `
                <div class="empty-state" style="border:0;box-shadow:none;background:transparent;">
                    <span class="empty-text">В этом диапазоне дедлайнов нет</span>
                    <span class="empty-hint">Расширьте период или добавьте задания в кабинете.</span>
                </div>`;
            return;
        }
        feedContainer.innerHTML = items.map((it) => {
            const meta = stateMeta[it.state] || stateMeta.none;
            const scoreTag = it.state === "graded" && it.score >= 0
                ? `<span class="feed-score">${it.score.toFixed(1)}</span>` : "";
            const overdue = it.due_date > 0 && (Date.now() / 1000) > it.due_date && (it.state === "none");
            return `
                <article class="feed-item ${overdue ? "is-overdue" : ""}">
                    <div class="feed-top">
                        <div class="feed-info">
                            <h4>${GS.escapeHtml(it.title)}</h4>
                            <span class="feed-course">${GS.escapeHtml(it.course_title)}</span>
                        </div>
                        <span class="feed-state ${meta.cls}">${GS.escapeHtml(meta.label)}${scoreTag}</span>
                    </div>
                    ${it.description ? `<p class="feed-desc">${GS.escapeHtml(it.description)}</p>` : ""}
                    <div class="feed-meta">
                        <span class="feed-due">${formatDue(it.due_date)}${overdue ? " · просрочено" : ""}</span>
                        <span class="dot-sep"></span>
                        <span>задание #${GS.escapeHtml(it.assignment_id)}</span>
                    </div>
                </article>`;
        }).join("");
    }

    async function loadFeed() {
        if (!feedContainer) return;
        feedContainer.innerHTML = `<div class="empty-state" style="border:0;box-shadow:none;background:transparent;"><span class="spinner"></span><span class="empty-hint">Загрузка ленты…</span></div>`;

        const from = dayStartUnix(fromDate.value || toISODate(today));
        const to = dayEndUnix(toDate.value || toISODate(weekLater));

        try {
            const items = await GS.fetchJSON(`/api/schedule?from=${from}&to=${to}`);
            renderFeed(items);
        } catch (error) {
            if (error.status === 401) {
                showGuestMode();
                return;
            }
            feedContainer.innerHTML = `<p class="message message-error">${GS.escapeHtml(error.message || "Не удалось загрузить ленту")}</p>`;
        }
    }

    function showGuestMode() {
        if (feedSection) feedSection.classList.add("hidden");
        if (guestNotice) guestNotice.classList.remove("hidden");
        if (loginLink) loginLink.classList.remove("hidden");
    }

    function showAuthedMode() {
        if (feedSection) feedSection.classList.remove("hidden");
        if (guestNotice) guestNotice.classList.add("hidden");
        if (loginLink) loginLink.classList.add("hidden");
    }

    // Определяем режим по профилю.
    let authed = false;
    try {
        await GS.fetchJSON("/api/auth/me");
        authed = true;
    } catch (error) {
        authed = false;
    }

    if (authed) {
        showAuthedMode();
        await loadFeed();
    } else {
        showGuestMode();
    }

    if (refreshButton) refreshButton.addEventListener("click", () => { if (authed) loadFeed(); });
    if (applyFilter) applyFilter.addEventListener("click", () => { if (authed) loadFeed(); });
});