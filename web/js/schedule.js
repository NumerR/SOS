document.addEventListener("DOMContentLoaded", async () => {
    const feedSection = document.getElementById("feedSection");
    const feedContainer = document.getElementById("feed");
    const resourcesSection = document.getElementById("resourcesSection");
    const resourcesContainer = document.getElementById("resources");
    const createResourceSection = document.getElementById("createResourceSection");
    const resourceForm = document.getElementById("resourceForm");
    const resourceMsg = document.getElementById("resourceMsg");
    const eventsManageSection = document.getElementById("eventsManageSection");
    const eventForm = document.getElementById("eventForm");
    const eventMsg = document.getElementById("eventMsg");
    const guestNotice = document.getElementById("guestNotice");
    const loginLink = document.getElementById("loginLink");
    const refreshButton = document.getElementById("refreshBtn");
    const refreshResources = document.getElementById("refreshResources");
    const applyFilter = document.getElementById("applyFilter");
    const fromDate = document.getElementById("fromDate");
    const toDate = document.getElementById("toDate");

    const roleLabels = { student: "студент", teacher: "преподаватель", admin: "администратор" };
    const canManage = new Set(["teacher", "admin"]);

    const stateMeta = {
        none:      { label: "не сдано",    cls: "feed-state-none" },
        submitted: { label: "сдано",       cls: "feed-state-submitted" },
        graded:    { label: "оценено",     cls: "feed-state-graded" },
        own:       { label: "моё задание", cls: "feed-state-own" },
    };

    let user = null;

    function pad(n) { return String(n).padStart(2, "0"); }
    function toISODate(d) { return d.getFullYear() + "-" + pad(d.getMonth() + 1) + "-" + pad(d.getDate()); }
    function dayStartUnix(iso) {
        const [y, m, d] = iso.split("-").map(Number);
        return Math.floor(new Date(y, m - 1, d, 0, 0, 0, 0).getTime() / 1000);
    }
    function dayEndUnix(iso) {
        const [y, m, d] = iso.split("-").map(Number);
        return Math.floor(new Date(y, m - 1, d, 23, 59, 59, 0).getTime() / 1000);
    }
    function formatDue(ts) {
        const d = new Date(ts * 1000);
        return d.toLocaleDateString("ru-RU", { weekday: "short", day: "numeric", month: "short" }) +
            " · " + d.toLocaleTimeString("ru-RU", { hour: "2-digit", minute: "2-digit" });
    }

    const today = new Date();
    const weekLater = new Date(today.getTime() + 7 * 24 * 3600 * 1000);
    if (fromDate) fromDate.value = toISODate(today);
    if (toDate) toDate.value = toISODate(weekLater);

    // ---------- лента (дедлайны + события) ----------
    function renderFeed(items) {
        if (!feedContainer) return;
        if (!items.length) {
            feedContainer.innerHTML = `
                <div class="empty-state" style="border:0;box-shadow:none;background:transparent;">
                    <span class="empty-text">В этом диапазоне ничего нет</span>
                    <span class="empty-hint">Расширьте период или добавьте задания/события.</span>
                </div>`;
            return;
        }
        feedContainer.innerHTML = items.map((it) => {
            if (it.kind === "event") {
                return `
                    <article class="feed-item is-event">
                        <div class="feed-top">
                            <div class="feed-info">
                                <h4>${GS.escapeHtml(it.title)}</h4>
                                <span class="feed-course">Событие</span>
                            </div>
                            <span class="feed-state feed-state-event">событие</span>
                        </div>
                        ${it.description ? `<p class="feed-desc">${GS.escapeHtml(it.description)}</p>` : ""}
                        <div class="feed-meta">
                            <span class="feed-due">${it.due_date > 0 ? formatDue(it.due_date) : "без точной даты"}</span>
                        </div>
                    </article>`;
            }
            const meta = stateMeta[it.state] || stateMeta.none;
            const scoreTag = it.state === "graded" && it.score >= 0
                ? `<span class="feed-score">${it.score.toFixed(1)}</span>` : "";
            const overdue = it.due_date > 0 && (Date.now() / 1000) > it.due_date && it.state === "none";
            return `
                <article class="feed-item ${overdue ? "is-overdue" : ""}">
                    <div class="feed-top">
                        <div class="feed-info">
                            <h4>${GS.escapeHtml(it.title)}</h4>
                            <span class="feed-course">${GS.escapeHtml(it.course_title || "")}</span>
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
            if (error.status === 401) { showGuestMode(); return; }
            feedContainer.innerHTML = `<p class="message message-error">${GS.escapeHtml(error.message || "Не удалось загрузить ленту")}</p>`;
        }
    }

    // ---------- ресурсы ----------
    function isHttpUrl(u) { return /^https?:\/\//i.test(String(u || "").trim()); }

    function renderResources(resources) {
        if (!resourcesContainer) return;
        if (!resources.length) {
            resourcesContainer.innerHTML = `
                <div class="empty-state">
                    <span class="empty-text">Пока нет ни одного ресурса</span>
                    <span class="empty-hint">${canManage.has(user.role) ? "Добавьте первый через форму ниже." : "Ресурсы появятся, когда их добавят преподаватели."}</span>
                </div>`;
            return;
        }
        resourcesContainer.innerHTML = resources.map((r) => {
            const cat = r.category ? `<span class="chip chip-muted">${GS.escapeHtml(r.category)}</span>` : "";
            const link = isHttpUrl(r.url)
                ? `<a class="btn btn-outline res-link" target="_blank" rel="noopener noreferrer" href="${GS.escapeHtml(r.url)}">Открыть
                     <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/><path d="M15 3h6v6M10 14 21 3"/></svg></a>`
                : "";
            return `
                <article class="card">
                    <div class="card-top">
                        <span class="card-icon" aria-hidden="true">
                            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 19.5A2.5 2.5 0 0 1 6.5 17H20"/><path d="M6.5 2H20v20H6.5A2.5 2.5 0 0 1 4 19.5v-15A2.5 2.5 0 0 1 6.5 2z"/></svg>
                        </span>
                        ${cat}
                    </div>
                    <h3 class="card-title">${GS.escapeHtml(r.title)}</h3>
                    <p class="card-text">${GS.escapeHtml(r.description || "Описание не указано.")}</p>
                    <div class="card-actions">${link}</div>
                </article>`;
        }).join("");
    }

    async function loadResources() {
        if (!resourcesContainer) return;
        resourcesContainer.innerHTML = `<div class="empty-state"><span class="spinner"></span><span class="empty-hint">Загрузка ресурсов…</span></div>`;
        try {
            const resources = await GS.fetchJSON("/api/resources");
            renderResources(resources);
        } catch (error) {
            if (error.status === 401) { showGuestMode(); return; }
            resourcesContainer.innerHTML = `<p class="message message-error">${GS.escapeHtml(error.message || "Не удалось загрузить ресурсы")}</p>`;
        }
    }

    // ---------- режимы ----------
    function showGuestMode() {
        if (feedSection) feedSection.classList.add("hidden");
        if (resourcesSection) resourcesSection.classList.add("hidden");
        if (eventsManageSection) eventsManageSection.classList.add("hidden");
        if (guestNotice) guestNotice.classList.remove("hidden");
        if (loginLink) loginLink.classList.remove("hidden");
    }

    function showAuthedMode() {
        if (feedSection) feedSection.classList.remove("hidden");
        if (resourcesSection) resourcesSection.classList.remove("hidden");
        if (guestNotice) guestNotice.classList.add("hidden");
        if (loginLink) loginLink.classList.add("hidden");

        const manage = canManage.has(user.role);
        if (createResourceSection) createResourceSection.classList.toggle("hidden", !manage);
        if (eventsManageSection) eventsManageSection.classList.toggle("hidden", !manage);
    }

    // ---------- формы создания ----------
    if (resourceForm) {
        resourceForm.addEventListener("submit", async (event) => {
            event.preventDefault();
            GS.hideMessage(resourceMsg);
            const fd = new FormData(resourceForm);
            const payload = {
                title: String(fd.get("title") || "").trim(),
                category: String(fd.get("category") || "").trim(),
                url: String(fd.get("url") || "").trim(),
                description: String(fd.get("description") || "").trim(),
            };
            if (!payload.title) { GS.showMessage(resourceMsg, "Название обязательно", "error"); return; }

            const btn = resourceForm.querySelector('button[type="submit"]');
            const original = btn ? btn.innerHTML : "";
            if (btn) { btn.classList.add("is-loading"); btn.disabled = true; }
            try {
                await GS.fetchJSON("/api/resources", { method: "POST", body: JSON.stringify(payload) });
                GS.showMessage(resourceMsg, "Ресурс добавлен", "success");
                resourceForm.reset();
                await loadResources();
            } catch (error) {
                GS.showMessage(resourceMsg, error.message || "Ошибка при добавлении ресурса", "error");
            } finally {
                if (btn) { btn.classList.remove("is-loading"); btn.disabled = false; btn.innerHTML = original; }
            }
        });
    }

    if (eventForm) {
        eventForm.addEventListener("submit", async (event) => {
            event.preventDefault();
            GS.hideMessage(eventMsg);
            const fd = new FormData(eventForm);
            const dueRaw = String(fd.get("event_date_raw") || "").trim();
            let dueIso = "";
            if (dueRaw) {
                const d = new Date(dueRaw);
                if (!isNaN(d.getTime())) dueIso = d.toISOString();
            }
            const payload = {
                title: String(fd.get("title") || "").trim(),
                description: String(fd.get("description") || "").trim(),
                event_date: dueIso,
            };
            if (!payload.title) { GS.showMessage(eventMsg, "Название обязательно", "error"); return; }

            const btn = eventForm.querySelector('button[type="submit"]');
            const original = btn ? btn.innerHTML : "";
            if (btn) { btn.classList.add("is-loading"); btn.disabled = true; }
            try {
                await GS.fetchJSON("/api/events", { method: "POST", body: JSON.stringify(payload) });
                GS.showMessage(eventMsg, "Событие добавлено", "success");
                eventForm.reset();
                await loadFeed();
            } catch (error) {
                GS.showMessage(eventMsg, error.message || "Ошибка при добавлении события", "error");
            } finally {
                if (btn) { btn.classList.remove("is-loading"); btn.disabled = false; btn.innerHTML = original; }
            }
        });
    }

    // ---------- инициализация ----------
    try {
        user = await GS.fetchJSON("/api/auth/me");
    } catch (error) {
        if (error.status === 401) { showGuestMode(); return; }
        if (feedContainer) feedContainer.innerHTML = `<p class="message message-error">${GS.escapeHtml(error.message || "Ошибка загрузки профиля")}</p>`;
        return;
    }

    showAuthedMode();
    await Promise.all([loadFeed(), loadResources()]);

    if (refreshButton) refreshButton.addEventListener("click", loadFeed);
    if (refreshResources) refreshResources.addEventListener("click", loadResources);
    if (applyFilter) applyFilter.addEventListener("click", loadFeed);
});