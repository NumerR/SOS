const ModalManager = (() => {
    const container = document.getElementById("modalContainer");
    const titleEl = document.getElementById("modalTitle");
    const bodyEl = document.getElementById("modalBody");
    const closeBtn = document.getElementById("modalCloseBtn");

    let isOpen = false;

    function open(title, contentHtml) {
        if (!container || !titleEl || !bodyEl) return;
        
        titleEl.textContent = title;
        bodyEl.innerHTML = contentHtml;
        container.classList.remove("hidden");
        
        // Force reflow for animation
        void container.offsetWidth; 
        container.classList.add("active");
        isOpen = true;
    }

    function close() {
        if (!isOpen) return;
        container.classList.remove("active");
        setTimeout(() => {
            container.classList.add("hidden");
            bodyEl.innerHTML = ""; // Clear DOM
        }, 200); // Wait for transition
        isOpen = false;
    }

    // Global click outside to close
    container?.addEventListener("click", (e) => {
        if (e.target === container) close();
    });

    closeBtn?.addEventListener("click", close);

    // Escape key
    document.addEventListener("keydown", (e) => {
        if (e.key === "Escape" && isOpen) close();
    });

    return { open, close };
})();

// Helper to format date
function formatDate(timestamp) {
    if (!timestamp || timestamp === 0) return "Без дедлайна";
    const d = new Date(timestamp * 1000);
    return d.toLocaleDateString("ru-RU", { day: "numeric", month: "short", year: "numeric" });
}

// --- RENDERERS ---

/**
 * Renders the list of assignments for a course (Student View)
 */
window.renderStudentAssignments = async function(courseId, courseTitle) {
    try {
        const res = await GS.fetchJSON(`/api/assignments?course_id=${courseId}`);
        
        if (!res.length) {
            return `<div class="empty-state" style="border:0;box-shadow:none;background:transparent;">
                        <span class="empty-text">Нет активных заданий</span>
                    </div>`;
        }

        const items = res.map(a => {
            const isOverdue = a.due_date > 0 && Date.now()/1000 > a.due_date;
            const statusClass = isOverdue ? 'grade-danger' : '';
            
            return `
                <div class="assignment-item">
                    <div class="assignment-top">
                        <div class="assignment-info">
                            <h4>${GS.escapeHtml(a.title)}</h4>
                            <span class="assignment-meta">${formatDate(a.due_date)} ${isOverdue ? '(просрочено)' : ''}</span>
                        </div>
                        <span class="chip ${statusClass}">#${a.id}</span>
                    </div>
                    <p class="assignment-desc">${GS.escapeHtml(a.description || "Описание не указано.")}</p>
                    <div class="assignment-actions">
                        <button class="btn btn-secondary" onclick="openSubmitModal(${a.id}, '${GS.escapeHtml(a.title)}')">
                            Сдать работу
                        </button>
                    </div>
                </div>
            `;
        }).join("");

        return items;
    } catch (err) {
        return `<p class="message message-error">${GS.escapeHtml(err.message)}</p>`;
    }
};

/**
 * Opens modal with submission form
 */
window.openSubmitModal = async function(assignmentId, assignmentTitle) {
    const html = `
        <form id="submitForm" class="submit-form stack">
            <input type="hidden" name="assignment_id" value="${assignmentId}">
            <label class="field">
                <span class="field-label">Ваша работа</span>
                <textarea class="control" name="content" required placeholder="Вставьте ссылку на репозиторий или текст решения..."></textarea>
                <span class="helper">Вы можете отредактировать отправку до дедлайна.</span>
            </label>
            <div class="form-actions">
                <button type="submit" class="btn">Отправить</button>
            </div>
            <p id="submitMsg" class="message hidden"></p>
        </form>
    `;
    
    ModalManager.open(`Сдача: ${assignmentTitle}`, html);

    const form = document.getElementById("submitForm");
    const msgEl = document.getElementById("submitMsg");

    form.addEventListener("submit", async (e) => {
        e.preventDefault();
        const fd = new FormData(form);
        const payload = {
            assignment_id: Number(fd.get("assignment_id")),
            content: String(fd.get("content")).trim()
        };

        if (!payload.content) {
            GS.showMessage(msgEl, "Текст работы обязателен", "error");
            return;
        }

        try {
            await GS.fetchJSON("/api/submissions", {
                method: "POST",
                body: JSON.stringify(payload)
            });
            GS.showMessage(msgEl, "Работа успешно отправлена!", "success");
            setTimeout(() => {
                ModalManager.close();
                // Refresh dashboard data if needed
                if(window.refreshDashboardData) window.refreshDashboardData();
            }, 1000);
        } catch (err) {
            GS.showMessage(msgEl, err.message || "Ошибка отправки", "error");
        }
    });
};

/**
 * Renders teacher view: List of submissions for an assignment
 */
window.renderTeacherSubmissions = async function(assignmentId, assignmentTitle) {
    try {
        const res = await GS.fetchJSON(`/api/submissions?assignment_id=${assignmentId}`);
        
        if (!res.length) {
            return `<div class="empty-state" style="border:0;box-shadow:none;background:transparent;">
                        <span class="empty-text">Еще никто не сдал работу</span>
                    </div>`;
        }

        const rows = res.map(s => {
            const scoreDisplay = s.score >= 0 ? `${s.score.toFixed(1)}` : "—";
            const gradeClass = s.score >= 0 ? (s.score >= 3 ? 'grade-success' : 'grade-danger') : 'grade-pending';
            
            return `
                <tr>
                    <td>
                        <div class="student-cell">
                            <span class="student-avatar-small">${initialsFromName(s.user_name || s.user_username)}</span>
                            <div>
                                <strong>${GS.escapeHtml(s.user_name || s.user_username)}</strong><br>
                                <small>@${GS.escapeHtml(s.user_username)}</small>
                            </div>
                        </div>
                    </td>
                    <td>
                        <details>
                            <summary style="cursor:pointer;color:var(--primary)">Показать ответ</summary>
                            <pre style="white-space:pre-wrap;font-family:inherit;margin-top:8px;padding:8px;background:var(--parchment);border-radius:4px;">${GS.escapeHtml(s.content)}</pre>
                        </details>
                    </td>
                    <td>
                        <span class="grade-badge ${gradeClass}">${scoreDisplay}</span>
                    </td>
                    <td>
                        <div class="stack" style="gap:4px;">
                            <input type="number" step="0.1" min="0" max="5" class="grading-input control" 
                                   placeholder="Оценка" value="${s.score >= 0 ? s.score : ''}" 
                                   data-sub-id="${s.id}">
                            <textarea class="control" style="min-height:60px;" 
                                      placeholder="Комментарий" 
                                      data-comment-id="${s.id}">${GS.escapeHtml(s.comment)}</textarea>
                            <button class="btn btn-secondary" style="font-size:11px;padding:6px 10px;width:100%;"
                                    onclick="saveGrade(${s.id})">Сохранить</button>
                        </div>
                    </td>
                </tr>
            `;
        }).join("");

        return `
            <table class="submissions-table">
                <thead>
                    <tr>
                        <th>Студент</th>
                        <th>Работа</th>
                        <th>Оценка</th>
                        <th>Действия</th>
                    </tr>
                </thead>
                <tbody>
                    ${rows}
                </tbody>
            </table>
        `;
    } catch (err) {
        return `<p class="message message-error">${GS.escapeHtml(err.message)}</p>`;
    }
};

/**
 * Saves grade via API
 */
window.saveGrade = async function(submissionId) {
    const scoreInput = document.querySelector(`input[data-sub-id='${submissionId}']`);
    const commentInput = document.querySelector(`textarea[data-comment-id='${submissionId}']`);
    
    if(!scoreInput || !commentInput) return;

    const scoreVal = parseFloat(scoreInput.value);
    const isNaNScore = isNaN(scoreVal);
    
    // Allow clearing grade by sending empty string or special logic? 
    // Backend expects float. If input is empty, maybe send -1? 
    // Let's assume user must enter number to save, or we handle null on backend later.
    // For now, require number.
    if (isNaNScore) {
        alert("Введите числовую оценку");
        return;
    }

    try {
        await GS.fetchJSON("/api/submissions/grade", {
            method: "POST",
            body: JSON.stringify({
                submission_id: submissionId,
                score: scoreVal,
                comment: commentInput.value.trim()
            })
        });
        
        // Visual feedback
        const badge = scoreInput.closest("tr").querySelector(".grade-badge");
        if(badge) {
            badge.textContent = scoreVal.toFixed(1);
            badge.className = `grade-badge ${scoreVal >= 3 ? 'grade-success' : 'grade-danger'}`;
        }
        
        // Optional: Show toast/message
        console.log("Grade saved");
    } catch (err) {
        alert("Ошибка сохранения: " + err.message);
    }
};

// Helper reused from dashboard scope if possible, otherwise define locally
function initialsFromName(name) {
    if (!name) return "?";
    const parts = name.trim().split(/\s+/);
    if (parts.length >= 2) return (parts[0][0] + parts[1][0]).toUpperCase();
    return name.slice(0, 2).toUpperCase();
}