package handlers

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/NumerR/SOS/internal/authz"
	"github.com/NumerR/SOS/internal/models"
)

// Index отдаёт главную страницу.
//
// Путь «/» — catch-all всего роутера страниц, поэтому всё, что не главная,
// уходит в NotFound: молчаливый http.NotFound отдавал бы браузеру текстовую
// строку «404 page not found», а человек ждёт страницу с навигацией.
func (h *Handlers) Index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		h.NotFound(w, r)
		return
	}

	serveWebFile(w, r, "index.html")
}

// NotFound отдаёт неизвестному пути то, что соответствует клиенту: API-клиент
// получает ошибку в общем формате JSON, браузер — оформленную страницу 404.
//
// Разделение по префиксу /api/, а не по заголовку Accept:Accept можно подделать
// и забыть отправить из curl, а префикс — то, чем маршрутизатор и так
// различает API и страницы (docs/02 §3). Страница при этом отдаётся со статусом
// 404, а не 200: поисковик и мониторинг должны видеть отсутствие страницы, а не
// «есть, красивая».
func (h *Handlers) NotFound(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeError(w, http.StatusNotFound, "route not found")
		return
	}

	serveWebFileWithStatus(w, r, "404.html", http.StatusNotFound)
}

// LoginPage отдаёт страницу входа.
func (h *Handlers) LoginPage(w http.ResponseWriter, r *http.Request) {
	serveWebFile(w, r, "login.html")
}

// RegisterPage отдаёт страницу регистрации.
func (h *Handlers) RegisterPage(w http.ResponseWriter, r *http.Request) {
	serveWebFile(w, r, "register.html")
}

// ProfilePage отдаёт страницу профиля только вошедшим.
//
// Профиль — то же, что и кабинет по модели доступа: страница не содержит
// чужих данных, а её содержимое добирается через GET /api/auth/me. Поэтому
// отказ здесь — redirect на вход, а не 403 в JSON (docs/02 §3: HTML-страницы
// идут мимо Registry).
func (h *Handlers) ProfilePage(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.pageUser(w, r); !ok {
		return
	}

	serveWebFile(w, r, "profile.html")
}

// DashboardPage отдаёт личный кабинет, если пользователь авторизован.
func (h *Handlers) DashboardPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.pageUser(w, r); !ok {
		return
	}

	serveWebFile(w, r, "dashboard.html")
}

// pageUser — пользователь защищённой HTML-страницы, либо ответ уже отправлен.
//
// Отказ бывает двух разных видов, и прежний `!ok` склеивал их в redirect: при
// недоступном хранилище человек попадал на форму входа, где его ждал тот же
// сбой — уже без причины (ADR-0031). Сломанная база теперь отвечает 500 через
// internalError, страницы при этом остаются JSON-ответом: отдельной страницы
// 500 в web/ нет, и это записано в docs/01 §4.
func (h *Handlers) pageUser(w http.ResponseWriter, r *http.Request) (*models.User, bool) {
	user, err := h.authenticatedUser(r)
	if err != nil {
		internalError(w, r, sessionFailureReason, err)
		return nil, false
	}

	if user == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return nil, false
	}

	return user, true
}

// AdminPage отдаёт админ-панель только администраторам.
//
// HTML-страницы не проходят через Registry с обёрткой права: отказ здесь
// выглядит как redirect на /dashboard, а не как 403 в JSON. Поэтому проверка
// остаётся в обработчике, но спрашивает она authz, а не сравнивает строку роли.
func (h *Handlers) AdminPage(w http.ResponseWriter, r *http.Request) {
	user, ok := h.pageUser(w, r)
	if !ok {
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

// serveWebFileWithStatus отдаёт страницу из web/ с кодом, отличным от 200.
// Нужна ровно одна такая страница — 404.
//
// Обычным ServeFile тут пользоваться нельзя: он сам ставит 200 и не даёт
// переопределить код, а страница «не найдено» с кодом 200 — это сообщение
// поисковику и мониторингу, что по адресу что-то есть. Файл читается целиком
// намеренно: Range и Etag этой странице не нужны.
//
// Если файла нет (сборка без статики, неверный рабочий каталог), остаётся
// честный текстовый 404 — лучше так, чем 200 с пустым телом.
func serveWebFileWithStatus(w http.ResponseWriter, r *http.Request, name string, status int) {
	body, err := os.ReadFile(filepath.Join("web", name))
	if err != nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
