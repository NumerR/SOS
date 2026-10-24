// Package accounts — модуль аккаунтов: регистрация, вход/выход, смена пароля и
// админ-контур пользователей.
//
// Пакет не содержит тел обработчиков — они живут в internal/handlers. Здесь
// лежит декларация: какие пути принадлежат модулю, какое право нужно на каждый
// метод и нужна ли проверка CSRF. Именно эта декларация и есть «контракт
// модуля» из docs/02-ARCHITECTURE.md §3.
package accounts

import (
	"net/http"

	"github.com/NumerR/SOS/internal/authz"
	"github.com/NumerR/SOS/internal/handlers"
	"github.com/NumerR/SOS/internal/platform"
)

// Module — декларация маршрутов аккаунтов.
type Module struct {
	h *handlers.Handlers
}

// New собирает модуль поверх готового набора обработчиков.
func New(h *handlers.Handlers) *Module { return &Module{h: h} }

// Name — имя модуля; оно же префикс записей миграций в журнале.
func (m *Module) Name() string { return "accounts" }

// Migrations — у аккаунтов своих миграций пока нет: таблицы users и sessions
// создаются базовой схемой (internal/database), а переезд всей схемы на
// версионируемые миграции — задача T2.1.
func (m *Module) Migrations() []platform.Migration { return nil }

// Register заявляет маршруты модуля.
func (m *Module) Register(reg *platform.Registry) {
	h := m.h

	// Публичные эндпоинты. Ограничены по IP ещё здесь, а не в main.go: лимитер
	// — часть контракта этого пути, и забыть его теперь так же сложно, как
	// забыть middleware в цепочке.
	//
	// CSRF на них не проверяется осознанно: межсайтовый POST с SameSite=Lax
	// сюда не доходит, а куки с токеном на момент входа ещё нет
	// (known limitation, docs/01-STATE-REPORT.md).
	reg.Add(platform.Route{
		Path:    "/api/auth/register",
		Method:  http.MethodPost,
		Public:  true,
		Handler: h.RateLimit(h.Limits.Register, "register", handlers.ClientIP)(http.HandlerFunc(h.Register)),
	})
	reg.Add(platform.Route{
		Path:    "/api/auth/login",
		Method:  http.MethodPost,
		Public:  true,
		Handler: h.RateLimit(h.Limits.Login, "login", handlers.ClientIP)(http.HandlerFunc(h.Login)),
	})

	// Выход не требует сессии: кука может быть уже протухшей, а снять её всё
	// равно надо. Метод строго POST — GET-выход позволял разорвать чужую
	// сессию картинкой на чужой странице.
	reg.Add(platform.Route{
		Path:    "/api/auth/logout",
		Method:  http.MethodPost,
		Public:  true,
		Handler: http.HandlerFunc(h.Logout),
	})

	// «Выйти везде» меняет состояние — RequireAuth + CSRF.
	reg.Add(platform.Route{
		Path:    "/api/auth/logout-all",
		Method:  http.MethodPost,
		CSRF:    true,
		Handler: http.HandlerFunc(h.LogoutAll),
	})

	// Смена пароля: тот же CSRF плюс лимитер — текущий пароль внутри открытой
	// сессии тоже можно перебирать.
	reg.Add(platform.Route{
		Path:    "/api/auth/change-password",
		Method:  http.MethodPost,
		CSRF:    true,
		Handler: h.RateLimit(h.Limits.Password, "password", handlers.ClientIP)(http.HandlerFunc(h.ChangePassword)),
	})

	// Только чтение — без CSRF.
	reg.Add(platform.Route{
		Path:    "/api/auth/me",
		Method:  http.MethodGet,
		Handler: http.HandlerFunc(h.Me),
	})

	// Админ-контур: единственный способ получить роль teacher или admin.
	reg.Add(platform.Route{
		Path:        "/api/admin/users",
		Method:      http.MethodGet,
		Perm:        authz.UserList,
		DenyMessage: "only administrators can list users",
		Handler:     http.HandlerFunc(h.AdminUsers),
	})
	reg.Add(platform.Route{
		Path:        "/api/admin/users/role",
		Method:      http.MethodPost,
		Perm:        authz.UserRoleManage,
		DenyMessage: "only administrators can assign roles",
		CSRF:        true,
		Handler:     http.HandlerFunc(h.AdminSetRole),
	})
}
