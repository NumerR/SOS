package handlers

import (
	"net/http"
	"path/filepath"

	"github.com/NumerR/SOS/internal/authz"
)

// Index отдаёт главную страницу.
func (h *Handlers) Index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	serveWebFile(w, r, "index.html")
}

// LoginPage отдаёт страницу входа.
func (h *Handlers) LoginPage(w http.ResponseWriter, r *http.Request) {
	serveWebFile(w, r, "login.html")
}

// RegisterPage отдаёт страницу регистрации.
func (h *Handlers) RegisterPage(w http.ResponseWriter, r *http.Request) {
	serveWebFile(w, r, "register.html")
}

// DashboardPage отдаёт личный кабинет, если пользователь авторизован.
func (h *Handlers) DashboardPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authenticatedUser(r); !ok {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}

	serveWebFile(w, r, "dashboard.html")
}

// AdminPage отдаёт админ-панель только администраторам.
//
// HTML-страницы не проходят через Registry с обёрткой права: отказ здесь
// выглядит как redirect на /dashboard, а не как 403 в JSON. Поэтому проверка
// остаётся в обработчике, но спрашивает она authz, а не сравнивает строку роли.
func (h *Handlers) AdminPage(w http.ResponseWriter, r *http.Request) {
	user, ok := h.authenticatedUser(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}

	if !canHere(user, authz.AdminPanelView) {
		http.Redirect(w, r, "/dashboard", http.StatusFound)
		return
	}

	serveWebFile(w, r, "admin.html")
}

func serveWebFile(w http.ResponseWriter, r *http.Request, name string) {
	path := filepath.Join("web", name)
	http.ServeFile(w, r, path)
}
