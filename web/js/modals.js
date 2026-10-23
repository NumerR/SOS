/* Модальные окна модуля заданий (курсы → задания → сдачи → оценки).

   Подключается там, где есть разметка #modalContainer / #modalDialog /
   #modalTitle / #modalBody / #modalCloseBtn — сейчас это dashboard.html.

   Все обработчики навешаны через делегирование на [data-modal-action] и
   form[data-modal-form]. Инлайновых onclick нет: значение, подставленное в
   JS-строку внутри атрибута, не экранируется надёжно (escapeHtml даёт
   &#039;, который HTML-парсер возвращает как ' до разбора JS) — это был
   хранящийся XSS через название задания, см. docs/01-STATE-REPORT.md §4.5. */

const ModalManager = (() => {
    const FOCUSABLE = [
        "a[href]",
        "button:not([disabled])",
        "input:not([disabled])",
        "textarea:not([disabled])",
        "select:not([disabled])",
        '[tabindex]:not([tabindex="-1"])',
    ].join(",");

    let isOpen = false;
    let lastFocused = null;
    let closeTimer = null;

    function refs() {
        return {
            overlay: document.getElementById("modalContainer"),
            dialog: document.getElementById("modalDialog"),
            title: document.getElementById("modalTitle"),
            body: document.getElementById("modalBody"),
            closeBtn: document.getElementById("modalCloseBtn"),
        };
    }

    // open возвращает false, если на странице нет контейнера — вызывающий код
    // не должен молча «терять» действие (именно так и выглядела исходная поломка).
    function open(title, contentHtml) {
        const { overlay, dialog, title: titleEl, body, closeBtn } = refs();
        if (!overlay || !dialog || !titleEl || !body) {
            console.error("ModalManager: на странице нет #modalContainer/#modalDialog/#modalBody");
            return false;
        }

        window.clearTimeout(closeTimer);
        lastFocused = document.activeElement;

        titleEl.textContent = title;
        body.innerHTML = contentHtml;

        overlay.classList.remove("hidden");
        void overlay.offsetWidth; // reflow, иначе transition не проиграется
        overlay.classList.add("active");
        isOpen = true;

        const target = body.querySelector(FOCUSABLE) || closeBtn || dialog;
        if (target && typeof target.focus === "function") target.focus();
        return true;
    }

    function close() {
        if (!isOpen) return;
        const { overlay, body } = refs();
        if (!overlay) return;

        isOpen = false;
        overlay.classList.remove("active");
        closeTimer = window.setTimeout(() => {
            overlay.classList.add("hidden");
            body.innerHTML = "";
        }, 200);

        if (lastFocused && document.contains(lastFocused)) {
            lastFocused.focus();
        }
        lastFocused = null;
    }

    function onKeydown(event) {
        if (!isOpen) return;

        if (event.key === "Escape") {
            event.preventDefault();
            close();
            return;
        }

        if (event.key !== "Tab") return;

        const { dialog, closeBtn } = refs();
        if (!dialog) return;

        // Кнопка закрытия идёт первой в порядке обхода, затем содержимое.
        const nodes = [closeBtn, ...Array.from(dialog.querySelectorAll(FOCUSABLE))]
            .filter((node) => node && !node.disabled);
        if (!nodes.length) return;

        const first = nodes[0];
        const last = nodes[nodes.length - 1];

        if (event.shiftKey && document.activeElement === first) {
            event.preventDefault();
            last.focus();
        } else if (!event.shiftKey && document.activeElement === last) {
            event.preventDefault();
            first.focus();
        }
    }

    document.addEventListener("keydown", onKeydown);

    // Закрытие по клику вне диалога и по крестику — тоже делегированием.
    document.addEventListener("click", (event) => {
        if (!isOpen) return;
        const { overlay, closeBtn } = refs();
        if (!overlay) return;

        if (event.target === overlay) {
            close();
            return;
        }
        if (closeBtn && closeBtn.contains(event.target)) close();
    });

    return { open, close, isOpen: () => isOpen };
})();

const AssignmentsUI = (() => {
    const emptyState = (text, hint = "") => `
        <div class="empty-state" style="border:0;box-shadow:none;background:transparent;">
            <span class="empty-text">${GS.escapeHtml(text)}</span>
            ${hint ? `<span class="empty-hint">${GS.escapeHtml(hint)}</span>` : ""}
        </div>`;

    const statusLine = (id) => `<p id="${id}" class="message hidden" role="status"></p>`;

    // ---------- разметка ----------

    function studentList(assignments) {
        if (!assignments.length) {
            return emptyState("Нет активных заданий", "Преподаватель ещё не добавил задания по этому курсу.");
        }

        return assignments.map((a) => {
            const overdue = a.due_date > 0 && Date.now() / 1000 > a.due_date;
            return `
                <div class="assignment-item">
                    <div class="assignment-top">
                        <div class="assignment-info">
                            <h4>${GS.escapeHtml(a.title)}</h4>
                            <span class="assignment-meta">${GS.formatDate(a.due_date)}${overdue ? " · просрочено" : ""}</span>
                        </div>
                        <span class="chip chip-outline">#${GS.escapeHtml(a.id)}</span>
                    </div>
                    <p class="assignment-desc">${GS.escapeHtml(a.description || "Описание не указано.")}</p>
                    <div class="assignment-actions">
                        <button class="btn btn-secondary"
                                data-modal-action="open-submit"
                                data-assignment-id="${GS.escapeHtml(a.id)}"
                                data-assignment-title="${GS.escapeHtml(a.title)}">Сдать работу</button>
                    </div>
                </div>`;
        }).join("");
    }

    function teacherList(assignments, courseId, courseTitle) {
        const createButton = `
            <div class="stack mb-4">
                <button class="btn" data-modal-action="open-create" data-course-id="${GS.escapeHtml(courseId)}"
                        data-course-title="${GS.escapeHtml(courseTitle || "")}">+ Новое задание</button>
            </div>`;

        if (!assignments.length) {
            return emptyState("Нет заданий") + createButton;
        }

        const items = assignments.map((a) => `
            <div class="assignment-item">
                <div class="assignment-top">
                    <div class="assignment-info">
                        <h4>${GS.escapeHtml(a.title)}</h4>
                        <span class="assignment-meta">${GS.formatDate(a.due_date)}</span>
                    </div>
                    <span class="chip chip-outline">#${GS.escapeHtml(a.id)}</span>
                </div>
                <p class="assignment-desc">${GS.escapeHtml(a.description || "Описание не указано.")}</p>
                <div class="assignment-actions">
                    <button class="btn btn-secondary"
                            data-modal-action="open-submissions"
                            data-assignment-id="${GS.escapeHtml(a.id)}"
                            data-assignment-title="${GS.escapeHtml(a.title)}">Проверить сдачи</button>
                </div>
            </div>`).join("");

        return createButton + items;
    }

    function submissionsTable(submissions) {
        if (!submissions.length) {
            return emptyState("Ещё никто не сдал работу", "Список обновится, как только появится первая сдача.");
        }

        const rows = submissions.map((s) => {
            const name = s.user_name || s.user_username || "";
            const scoreDisplay = s.score >= 0 ? s.score.toFixed(1) : "—";
            const gradeClass = s.score >= 0 ? (s.score >= 3 ? "grade-success" : "grade-danger") : "grade-pending";

            return `
                <tr>
                    <td>
                        <div class="student-cell">
                            <span class="student-avatar-small" aria-hidden="true">${GS.escapeHtml(GS.initialsFrom({ full_name: name, username: s.user_username }))}</span>
                            <div>
                                <strong>${GS.escapeHtml(name)}</strong><br>
                                <small>@${GS.escapeHtml(s.user_username || "")}</small>
                            </div>
                        </div>
                    </td>
                    <td>
                        <details>
                            <summary>Показать ответ</summary>
                            <pre class="submission-content">${GS.escapeHtml(s.content)}</pre>
                        </details>
                    </td>
                    <td><span class="grade-badge ${gradeClass}">${GS.escapeHtml(scoreDisplay)}</span></td>
                    <td>
                        <div class="stack" style="gap:4px;">
                            <label class="sr-only" for="score-${GS.escapeHtml(s.id)}">Оценка</label>
                            <input id="score-${GS.escapeHtml(s.id)}" class="control grading-input" type="number"
                                   step="0.1" min="0" max="10" inputmode="decimal" placeholder="Оценка"
                                   value="${s.score >= 0 ? GS.escapeHtml(s.score) : ""}"
                                   data-score-for="${GS.escapeHtml(s.id)}">
                            <label class="sr-only" for="comment-${GS.escapeHtml(s.id)}">Комментарий</label>
                            <textarea id="comment-${GS.escapeHtml(s.id)}" class="control" style="min-height:60px;"
                                      placeholder="Комментарий" data-comment-for="${GS.escapeHtml(s.id)}">${GS.escapeHtml(s.comment || "")}</textarea>
                            <button class="btn btn-secondary" data-modal-action="save-grade"
                                    data-submission-id="${GS.escapeHtml(s.id)}">Сохранить</button>
                        </div>
                    </td>
                </tr>`;
        }).join("");

        return `
            ${statusLine("modalStatus")}
            <table class="submissions-table">
                <thead>
                    <tr><th scope="col">Студент</th><th scope="col">Работа</th><th scope="col">Оценка</th><th scope="col">Действия</th></tr>
                </thead>
                <tbody>${rows}</tbody>
            </table>`;
    }

    const submitForm = (assignmentId, assignmentTitle) => `
        <form class="submit-form stack" data-modal-form="submit-work">
            <input type="hidden" name="assignment_id" value="${GS.escapeHtml(assignmentId)}">
            <p class="empty-hint">${GS.escapeHtml(assignmentTitle)}</p>
            <label class="field">
                <span class="field-label">Ваша работа</span>
                <textarea class="control" name="content" required
                          placeholder="Вставьте ссылку на репозиторий или текст решения..."></textarea>
                <span class="helper">Сдачу можно обновить до дедлайна — повторная отправка заменяет предыдущую.</span>
            </label>
            <div class="form-actions"><button type="submit" class="btn">Отправить</button></div>
            ${statusLine("modalStatus")}
        </form>`;

    const createForm = (courseId, courseTitle) => `
        <form class="form stack" data-modal-form="create-assignment">
            <input type="hidden" name="course_id" value="${GS.escapeHtml(courseId)}">
            <input type="hidden" name="course_title" value="${GS.escapeHtml(courseTitle || "")}">
            <label class="field">
                <span class="field-label">Название</span>
                <input class="control" type="text" name="title" maxlength="200" required placeholder="Домашнее задание 1">
            </label>
            <label class="field">
                <span class="field-label">Описание</span>
                <textarea class="control" name="description" maxlength="2000" rows="3" placeholder="Что нужно сделать..."></textarea>
            </label>
            <label class="field">
                <span class="field-label">Дедлайн</span>
                <input class="control" type="datetime-local" name="due_date_raw">
                <span class="helper">Оставьте пустым, если дедлайна нет.</span>
            </label>
            <div class="form-actions"><button type="submit" class="btn">Создать</button></div>
            ${statusLine("modalStatus")}
        </form>`;

    // ---------- загрузка и открытие ----------

    async function load(url) {
        try {
            return await GS.fetchJSON(url);
        } catch (error) {
            return { error: GS.escapeHtml(error.message || "Не удалось загрузить данные") };
        }
    }

    function errorBlock(message) {
        return `<p class="message message-error">${GS.escapeHtml(message)}</p>`;
    }

    async function openStudent(courseId, courseTitle) {
        const data = await load(`/api/assignments?course_id=${encodeURIComponent(courseId)}`);
        if (data.error) return ModalManager.open(`Задания: ${courseTitle}`, errorBlock(data.error));
        ModalManager.open(`Задания: ${courseTitle}`, studentList(data));
    }

    async function openTeacher(courseId, courseTitle) {
        const data = await load(`/api/assignments?course_id=${encodeURIComponent(courseId)}`);
        if (data.error) return ModalManager.open(`Управление заданиями: ${courseTitle}`, errorBlock(data.error));
        ModalManager.open(`Управление заданиями: ${courseTitle}`, teacherList(data, courseId, courseTitle));
    }

    async function openSubmissions(assignmentId, assignmentTitle) {
        const data = await load(`/api/submissions?assignment_id=${encodeURIComponent(assignmentId)}`);
        if (data.error) return ModalManager.open(`Сдачи: ${assignmentTitle}`, errorBlock(data.error));
        ModalManager.open(`Сдачи: ${assignmentTitle}`, submissionsTable(data));
    }

    function openCreate(courseId, courseTitle) {
        ModalManager.open("Новое задание", createForm(courseId, courseTitle));
    }

    function openSubmit(assignmentId, assignmentTitle) {
        ModalManager.open("Сдача работы", submitForm(assignmentId, assignmentTitle));
    }

    // после изменений просим страницу перезагрузить данные (без прямой ссылки на неё)
    function notifyChanged() {
        document.dispatchEvent(new CustomEvent("sos:data-changed"));
    }

    // ---------- действия ----------

    document.addEventListener("click", async (event) => {
        const button = event.target.closest("[data-modal-action]");
        if (!button) return;

        const { modalAction, courseId, courseTitle, assignmentId, assignmentTitle, submissionId } = button.dataset;

        if (modalAction === "open-submit") { openSubmit(assignmentId, assignmentTitle); return; }
        if (modalAction === "open-create") { openCreate(courseId, courseTitle); return; }
        if (modalAction === "open-submissions") { openSubmissions(assignmentId, assignmentTitle); return; }

        if (modalAction === "save-grade") {
            const row = button.closest("tr");
            if (!row) return;

            const scoreInput = row.querySelector(`[data-score-for="${submissionId}"]`);
            const commentInput = row.querySelector(`[data-comment-for="${submissionId}"]`);
            const statusEl = document.getElementById("modalStatus");
            if (!scoreInput || !commentInput) return;

            const score = Number.parseFloat(scoreInput.value);
            if (!Number.isFinite(score) || score < 0) {
                GS.showMessage(statusEl, "Введите числовую оценку от 0 и выше", "error");
                scoreInput.focus();
                return;
            }

            button.disabled = true;
            try {
                await GS.fetchJSON("/api/submissions/grade", {
                    method: "POST",
                    body: JSON.stringify({
                        submission_id: Number(submissionId),
                        score,
                        comment: commentInput.value.trim(),
                    }),
                });
                const badge = row.querySelector(".grade-badge");
                if (badge) {
                    badge.textContent = score.toFixed(1);
                    badge.className = `grade-badge ${score >= 3 ? "grade-success" : "grade-danger"}`;
                }
                GS.showMessage(statusEl, `Оценка ${score.toFixed(1)} сохранена`, "success");
                notifyChanged();
            } catch (error) {
                GS.showMessage(statusEl, error.message || "Не удалось сохранить оценку", "error");
            } finally {
                button.disabled = false;
            }
        }
    });

    document.addEventListener("submit", async (event) => {
        const form = event.target.closest("form[data-modal-form]");
        if (!form) return;

        event.preventDefault();

        const kind = form.dataset.modalForm;
        const statusEl = form.querySelector('[id="modalStatus"]');
        const submitButton = form.querySelector('button[type="submit"]');

        let payload;
        let url;
        let reopen = null;

        if (kind === "submit-work") {
            const data = new FormData(form);
            const content = String(data.get("content") || "").trim();
            if (!content) {
                GS.showMessage(statusEl, "Текст работы обязателен", "error");
                return;
            }
            url = "/api/submissions";
            payload = { assignment_id: Number(data.get("assignment_id")), content };
        } else if (kind === "create-assignment") {
            const data = new FormData(form);
            const title = String(data.get("title") || "").trim();
            if (!title) {
                GS.showMessage(statusEl, "Название обязательно", "error");
                return;
            }
            let dueIso = "";
            const dueRaw = String(data.get("due_date_raw") || "").trim();
            if (dueRaw) {
                const parsed = new Date(dueRaw);
                if (!Number.isNaN(parsed.getTime())) dueIso = parsed.toISOString();
            }
            url = "/api/assignments";
            payload = {
                course_id: Number(data.get("course_id")),
                title,
                description: String(data.get("description") || "").trim(),
                due_date: dueIso,
            };
            // course_title — служебное поле интерфейса, в API не уходит:
            // readJSON на сервере отклоняет неизвестные поля запроса.
            reopen = {
                courseId: payload.course_id,
                courseTitle: String(data.get("course_title") || ""),
            };
        } else {
            return;
        }

        if (submitButton) {
            submitButton.classList.add("is-loading");
            submitButton.disabled = true;
        }

        try {
            await GS.fetchJSON(url, { method: "POST", body: JSON.stringify(payload) });

            if (kind === "submit-work") {
                GS.showMessage(statusEl, "Работа отправлена", "success");
            } else {
                GS.showMessage(statusEl, "Задание создано", "success");
            }

            notifyChanged();
            window.setTimeout(() => {
                if (reopen) {
                    // пересобираем список заданий того же курса, не закрывая окно
                    openTeacher(reopen.courseId, reopen.courseTitle);
                    return;
                }
                ModalManager.close();
            }, 700);
        } catch (error) {
            GS.showMessage(statusEl, error.message || "Не удалось сохранить", "error");
        } finally {
            if (submitButton) {
                submitButton.classList.remove("is-loading");
                submitButton.disabled = false;
            }
        }
    });

    return { openStudent, openTeacher };
})();
