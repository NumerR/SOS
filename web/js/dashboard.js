document.addEventListener("DOMContentLoaded", async () => {
    const coursesContainer = document.getElementById("courses");
    const myCoursesSection = document.getElementById("myCoursesSection");
    const myCoursesContainer = document.getElementById("myCourses");
    const courseStudentsPanel = document.getElementById("courseStudentsPanel");
    const courseStudentsTitle = document.getElementById("courseStudentsTitle");
    const courseStudentsList = document.getElementById("courseStudentsList");
    const closeStudentsBtn = document.getElementById("closeStudents");

    const courseForm = document.getElementById("courseForm");
    const createSection = document.getElementById("createSection");
    const studentNotice = document.getElementById("studentNotice");
    const adminLink = document.getElementById("adminLink");

    const globalMessage = document.getElementById("globalMessage");
    const messageElement = document.getElementById("message");
    const userNameElement = document.getElementById("userName");
    const userRoleElement = document.getElementById("userRole");
    const userAvatarElement = document.getElementById("userAvatar");
    const refreshButton = document.getElementById("refreshBtn");

    const roleLabels = { student: "студент", teacher: "преподаватель", admin: "администратор" };
    const canCreateRoles = new Set(["teacher", "admin"]);
    const canEnrollRoles = new Set(["student", "admin"]);
    const canViewStudentsRoles = new Set(["teacher", "admin"]);

    let user = null;
    let mySet = new Set(); 

    function initialsFrom(u) {
        const source = (u.full_name || u.username || "?").trim();
        const parts = source.split(/\s+/).filter(Boolean);
        if (parts.length >= 2) return (parts[0][0] + parts[1][0]).toUpperCase();
        return source.slice(0, 2).toUpperCase();
    }

    function globalMsg(text, type = "success") {
        GS.showMessage(globalMessage, text, type);
        clearTimeout(globalMsg._t);
        globalMsg._t = setTimeout(() => GS.hideMessage(globalMessage), 4000);
    }

    // Expose refresh function globally for modals to trigger reload
    window.refreshDashboardData = loadAll;

    // ---------- рендер каталога ----------
    function catalogCardActions(course) {
        const actions = [];
        const enrolled = mySet.has(course.id);

        if (canEnrollRoles.has(user.role)) {
            if (enrolled) {
                actions.push(`<button class="btn btn-secondary" data-action="unenroll" data-course-id="${course.id}">Отписаться</button>`);
            } else {
                actions.push(`<button class="btn" data-action="enroll" data-course-id="${course.id}">Записаться</button>`);
            }
        }

        // NEW: Assignments Button Logic
        if (user.role === "student" && enrolled) {
             actions.push(`<button class="btn btn-outline" data-action="view-assignments-student" data-course-id="${course.id}" data-course-title="${GS.escapeHtml(course.title)}">Задания</button>`);
        }
        
        if (canViewStudentsRoles.has(user.role) && (user.role === "admin" || course.created_by === user.id)) {
            actions.push(`<button class="btn btn-secondary" data-action="students" data-course-id="${course.id}" data-course-title="${GS.escapeHtml(course.title)}">Студенты</button>`);
            // NEW: Manage Assignments for Owner/Admin
            actions.push(`<button class="btn btn-outline" data-action="manage-assignments" data-course-id="${course.id}" data-course-title="${GS.escapeHtml(course.title)}">Управление заданиями</button>`);
        }

        if (!actions.length) return "";
        return `<div class="card-actions">${actions.join("")}</div>`;
    }

    function courseCard(course, withActions) {
        return `
            <article class="card">
                <div class="card-top">
                    <span class="card-icon" aria-hidden="true">
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 19.5A2.5 2.5 0 0 1 6.5 17H20"/><path d="M6.5 2H20v20H6.5A2.5 2.5 0 0 1 4 19.5v-15A2.5 2.5 0 0 1 6.5 2z"/></svg>
                    </span>
                    <span class="chip chip-outline">#${GS.escapeHtml(course.id)}</span>
                </div>
                <h3 class="card-title">${GS.escapeHtml(course.title)}</h3>
                <p class="card-text">${GS.escapeHtml(course.description || "Описание не указано.")}</p>
                <p class="card-meta">
                    <span>${GS.escapeHtml(course.teacher || "—")}</span>
                    <span class="dot-sep"></span>
                    <span>создатель #${GS.escapeHtml(course.created_by)}</span>
                </p>
                ${withActions ? catalogCardActions(course) : ""}
            </article>
        `;
    }

    function renderCatalog(courses) {
        if (!coursesContainer) return;
        if (!courses.length) {
            coursesContainer.innerHTML = `
                <div class="empty-state">
                    <span class="empty-icon" aria-hidden="true">
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="11" cy="11" r="7"/><path d="M21 21l-4.3-4.3"/></svg>
                    </span>
                    <span class="empty-text">Пока нет ни одного курса</span>
                    <span class="empty-hint">${canCreateRoles.has(user.role) ? "Добавьте первый курс через форму ниже." : "Курсы появятся, когда их добавят преподаватели."}</span>
                </div>`;
            return;
        }
        coursesContainer.innerHTML = courses.map((c) => courseCard(c, true)).join("");
    }

    function renderMyCourses(courses) {
        if (!myCoursesContainer) return;
        if (!courses.length) {
            myCoursesContainer.innerHTML = `
                <div class="empty-state">
                    <span class="empty-text">Вы пока не записаны ни на один курс</span>
                    <span class="empty-hint">Выберите курс в каталоге ниже и нажмите «Записаться».</span>
                </div>`;
            return;
        }
        myCoursesContainer.innerHTML = courses.map((c) => `
            <article class="card">
                <div class="card-top">
                    <span class="card-icon" aria-hidden="true">
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 19.5A2.5 2.5 0 0 1 6.5 17H20"/><path d="M6.5 2H20v20H6.5A2.5 2.5 0 0 1 4 19.5v-15A2.5 2.5 0 0 1 6.5 2z"/></svg>
                    </span>
                    <span class="chip chip-outline">#${GS.escapeHtml(c.id)}</span>
                </div>
                <h3 class="card-title">${GS.escapeHtml(c.title)}</h3>
                <p class="card-text">${GS.escapeHtml(c.teacher || "—")}</p>
                <div class="card-actions">
                     <button class="btn btn-outline" data-action="view-assignments-student" data-course-id="${c.id}" data-course-title="${GS.escapeHtml(c.title)}">Мои задания</button>
                     <button class="btn btn-secondary" data-action="unenroll" data-course-id="${c.id}">Отписаться</button>
                </div>
            </article>
        `).join("");
    }

    // ---------- загрузка данных ----------
    async function loadAll() {
        if (coursesContainer) {
            coursesContainer.innerHTML = `<div class="empty-state"><span class="spinner"></span><span class="empty-hint">Загрузка курсов…</span></div>`;
        }

        try {
            const requests = [GS.fetchJSON("/api/courses")];
            if (canEnrollRoles.has(user.role)) requests.push(GS.fetchJSON("/api/enrollments/mine"));

            const results = await Promise.all(requests);
            const courses = results[0];
            const mine = canEnrollRoles.has(user.role) ? results[1] : [];

            mySet = new Set(mine.map((c) => c.id));
            renderCatalog(courses);
            if (canEnrollRoles.has(user.role)) renderMyCourses(mine);
        } catch (error) {
            if (coursesContainer) {
                coursesContainer.innerHTML = `<p class="message message-error">${GS.escapeHtml(error.message || "Не удалось загрузить данные")}</p>`;
            }
        }
    }

    // ---------- действия записи ----------
    async function doEnroll(courseId, btn) {
        btn.disabled = true;
        try {
            await GS.fetchJSON("/api/enrollments", {
                method: "POST",
                body: JSON.stringify({ course_id: Number(courseId) }),
            });
            globalMsg("Вы записаны на курс", "success");
            await loadAll();
        } catch (error) {
            globalMsg(error.message || "Не удалось записаться", "error");
            btn.disabled = false;
        }
    }

    async function doUnenroll(courseId, btn) {
        btn.disabled = true;
        try {
            await GS.fetchJSON("/api/enrollments/cancel", {
                method: "POST",
                body: JSON.stringify({ course_id: Number(courseId) }),
            });
            globalMsg("Вы отписались от курса", "success");
            await loadAll();
        } catch (error) {
            globalMsg(error.message || "Не удалось отписаться", "error");
            btn.disabled = false;
        }
    }

    async function doStudents(courseId, title) {
        if (!courseStudentsPanel) return;
        courseStudentsPanel.classList.remove("hidden");
        if (courseStudentsTitle) courseStudentsTitle.textContent = "Студенты курса: " + title;
        if (courseStudentsList) courseStudentsList.innerHTML = `<div class="empty-state" style="border:0;box-shadow:none;background:transparent;"><span class="spinner"></span><span class="empty-hint">Загрузка…</span></div>`;
        courseStudentsPanel.scrollIntoView({ behavior: "smooth", block: "nearest" });

        try {
            const users = await GS.fetchJSON("/api/enrollments/course?course_id=" + encodeURIComponent(courseId));
            if (!users.length) {
                if (courseStudentsList) courseStudentsList.innerHTML = `<p class="empty-hint">На этот курс пока никто не записан.</p>`;
                return;
            }
            if (courseStudentsList) {
                courseStudentsList.innerHTML = users.map((u) => `
                    <div class="person-row">
                        <span class="avatar" aria-hidden="true">${GS.escapeHtml(initialsFrom(u))}</span>
                        <span class="person-info">
                            <span class="person-name">${GS.escapeHtml(u.full_name || u.username)}</span>
                            <span class="person-login">@${GS.escapeHtml(u.username)}</span>
                        </span>
                        <span class="spacer"></span>
                        <span class="chip chip-muted">${GS.escapeHtml(roleLabels[u.role] || u.role)}</span>
                    </div>
                `).join("");
            }
        } catch (error) {
            if (courseStudentsList) courseStudentsList.innerHTML = `<p class="message message-error">${GS.escapeHtml(error.message || "Не удалось загрузить список")}</p>`;
        }
    }

    // NEW: Handle Assignment Actions
    async function handleAssignmentAction(actionType, courseId, courseTitle) {
        if (actionType === "view-assignments-student") {
            const content = await window.renderStudentAssignments(courseId, courseTitle);
            ModalManager.open(`Задания: ${courseTitle}`, content);
        } else if (actionType === "manage-assignments") {
            // For teachers/admins: Show list of assignments with ability to check submissions
            const content = await renderTeacherAssignmentsList(courseId, courseTitle);
            ModalManager.open(`Управление заданиями: ${courseTitle}`, content);
        }
    }

    // Helper for Teacher View inside Modal
    async function renderTeacherAssignmentsList(courseId, courseTitle) {
        try {
            const res = await GS.fetchJSON(`/api/assignments?course_id=${courseId}`);
            
            if (!res.length) {
                return `<div class="empty-state" style="border:0;box-shadow:none;background:transparent;">
                            <span class="empty-text">Нет заданий</span>
                            <button class="btn mt-4" onclick="openCreateAssignmentModal(${courseId})">Создать первое задание</button>
                        </div>`;
            }

            const items = res.map(a => `
                <div class="assignment-item">
                    <div class="assignment-top">
                        <div class="assignment-info">
                            <h4>${GS.escapeHtml(a.title)}</h4>
                            <span class="assignment-meta">${formatDate(a.due_date)}</span>
                        </div>
                        <span class="chip chip-outline">#${a.id}</span>
                    </div>
                    <div class="assignment-actions">
                        <button class="btn btn-secondary" onclick="openCheckSubmissionsModal(${a.id}, '${GS.escapeHtml(a.title)}')">
                            Проверить сдачи
                        </button>
                    </div>
                </div>
            `).join("");

            return `
                <div class="stack mb-4">
                    <button class="btn" onclick="openCreateAssignmentModal(${courseId})">+ Новое задание</button>
                </div>
                ${items}
            `;
        } catch (err) {
            return `<p class="message message-error">${GS.escapeHtml(err.message)}</p>`;
        }
    }

    // Placeholder for Create Assignment Modal (to be implemented fully next)
    window.openCreateAssignmentModal = function(courseId) {
        const html = `
            <form id="createAssForm" class="form stack">
                <input type="hidden" name="course_id" value="${courseId}">
                <label class="field">
                    <span class="field-label">Название</span>
                    <input class="control" type="text" name="title" required placeholder="HW 1">
                </label>
                <label class="field">
                    <span class="field-label">Описание</span>
                    <textarea class="control" name="description" rows="3" placeholder="Что нужно сделать..."></textarea>
                </label>
                <label class="field">
                    <span class="field-label">Дедлайн (ISO Date)</span>
                    <input class="control" type="datetime-local" name="due_date_raw">
                    <span class="helper">Оставьте пустым, если дедлайна нет.</span>
                </label>
                <div class="form-actions">
                    <button type="submit" class="btn">Создать</button>
                </div>
                <p id="createAssMsg" class="message hidden"></p>
            </form>
        `;
        ModalManager.open("Новое задание", html);

        const form = document.getElementById("createAssForm");
        const msg = document.getElementById("createAssMsg");

        form.addEventListener("submit", async (e) => {
            e.preventDefault();
            const fd = new FormData(form);
            const dueRaw = fd.get("due_date_raw");
            let dueIso = "";
            if(dueRaw) {
                // Convert local datetime string to ISO UTC roughly for backend parsing
                const d = new Date(dueRaw);
                if(!isNaN(d.getTime())) dueIso = d.toISOString();
            }

            const payload = {
                course_id: Number(fd.get("course_id")),
                title: String(fd.get("title")).trim(),
                description: String(fd.get("description")).trim(),
                due_date: dueIso
            };

            try {
                await GS.fetchJSON("/api/assignments", {
                    method: "POST",
                    body: JSON.stringify(payload)
                });
                GS.showMessage(msg, "Задание создано", "success");
                setTimeout(() => {
                    ModalManager.close();
                    // Re-open parent modal to show updated list
                    handleAssignmentAction("manage-assignments", payload.course_id, ""); 
                }, 800);
            } catch (err) {
                GS.showMessage(msg, err.message, "error");
            }
        });
    };

    window.openCheckSubmissionsModal = async function(assignmentId, assignmentTitle) {
        const content = await window.renderTeacherSubmissions(assignmentId, assignmentTitle);
        ModalManager.open(`Сдачи: ${assignmentTitle}`, content);
    };

    function attachDelegation(el) {
        if (!el) return;
        el.addEventListener("click", (event) => {
            const btn = event.target.closest("[data-action]");
            if (!btn) return;
            
            const action = btn.dataset.action;
            const courseId = btn.dataset.courseId;
            const courseTitle = btn.dataset.courseTitle || "";

            if (action === "enroll") doEnroll(courseId, btn);
            else if (action === "unenroll") doUnenroll(courseId, btn);
            else if (action === "students") doStudents(courseId, courseTitle);
            else if (action === "view-assignments-student" || action === "manage-assignments") {
                handleAssignmentAction(action, courseId, courseTitle);
            }
        });
    }

    // ---------- инициализация ----------
    try {
        user = await GS.fetchJSON("/api/auth/me");
    } catch (error) {
        if (error.status === 401) { window.location.href = "/login"; return; }
        if (coursesContainer) coursesContainer.innerHTML = `<p class="message message-error">${GS.escapeHtml(error.message || "Ошибка загрузки профиля")}</p>`;
        return;
    }

    if (userNameElement) userNameElement.textContent = user.full_name || user.username;
    if (userRoleElement) userRoleElement.textContent = roleLabels[user.role] || user.role;
    if (userAvatarElement) userAvatarElement.textContent = initialsFrom(user);

    const canCreate = canCreateRoles.has(user.role);
    const canEnroll = canEnrollRoles.has(user.role);
    if (createSection) createSection.classList.toggle("hidden", !canCreate);
    if (studentNotice) studentNotice.classList.toggle("hidden", canCreate);
    if (adminLink) adminLink.classList.toggle("hidden", user.role !== "admin");
    if (myCoursesSection) myCoursesSection.classList.toggle("hidden", !canEnroll);

    GS.bindLogout("logoutBtn", "/login");

    attachDelegation(coursesContainer);
    attachDelegation(myCoursesContainer);

    if (closeStudentsBtn) closeStudentsBtn.addEventListener("click", () => courseStudentsPanel && courseStudentsPanel.classList.add("hidden"));
    if (refreshButton) refreshButton.addEventListener("click", loadAll);

    if (courseForm) {
        courseForm.addEventListener("submit", async (event) => {
            event.preventDefault();
            GS.hideMessage(messageElement);
            const formData = new FormData(courseForm);
            const payload = {
                title: String(formData.get("title") || "").trim(),
                teacher: String(formData.get("teacher") || "").trim(),
                description: String(formData.get("description") || "").trim(),
            };
            if (!payload.title) { GS.showMessage(messageElement, "Название курса обязательно", "error"); return; }

            const submitButton = courseForm.querySelector('button[type="submit"]');
            const original = submitButton ? submitButton.innerHTML : "";
            if (submitButton) { submitButton.classList.add("is-loading"); submitButton.disabled = true; }

            try {
                await GS.fetchJSON("/api/courses", { method: "POST", body: JSON.stringify(payload) });
                GS.showMessage(messageElement, "Курс успешно добавлен", "success");
                courseForm.reset();
                await loadAll();
            } catch (error) {
                GS.showMessage(messageElement, error.message || "Ошибка при создании курса", "error");
            } finally {
                if (submitButton) { submitButton.classList.remove("is-loading"); submitButton.disabled = false; submitButton.innerHTML = original; }
            }
        });
    }

    await loadAll();
});