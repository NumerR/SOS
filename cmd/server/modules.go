package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/NumerR/SOS/internal/handlers"
	"github.com/NumerR/SOS/internal/modules/accounts"
	"github.com/NumerR/SOS/internal/modules/assignments"
	"github.com/NumerR/SOS/internal/modules/audit"
	"github.com/NumerR/SOS/internal/modules/courses"
	"github.com/NumerR/SOS/internal/modules/events"
	"github.com/NumerR/SOS/internal/modules/resources"
	"github.com/NumerR/SOS/internal/modules/schedule"
	"github.com/NumerR/SOS/internal/platform"
)

// moduleList — единственный список модулей платформы.
//
// Это то место, ради которого существует whole файл: подключить модуль значит
// добавить строку сюда и больше нигде. Порядок чтения = порядок применения
// миграций, поэтому зависимости (внешние ключи на таблицы других модулей)
// должны идти после тех таблиц, на которые ссылаются.
func moduleList(h *handlers.Handlers) []platform.Module {
	return []platform.Module{
		accounts.New(h),
		courses.New(h),
		assignments.New(h),
		resources.New(h),
		events.New(h),
		schedule.New(h),
		// Аудит — последним: его таблица ссылается на users из базовой схемы,
		// а обработчики аккаунтов пишут в неё уже после применения миграций.
		audit.New(h),
	}
}

// newRouter собирает http.Handler из модулей, страниц и статики.
//
// Разделено на три части, а не «всё через Registry», потому что HTML-страницы и
// раздача файлов отказывают по-другому: страница вместо 403 в JSON делает
// redirect на /login, а статика вообще не имеет метода-права. Пихать их в
// реестр маршрутов означало бы добавить в контракт два поля «иначе», которых
// никто не использует.
//
// Ошибкой возвращается неоправданная конфигурация маршрутов (дубликат пути,
// право не из реестра) — это не рантайм-сбой, а ошибка сборки, поэтому main.go
// обязан на ней остановить запуск, а не продолжать с половинным роутером.
func newRouter(h *handlers.Handlers, db *sql.DB, modules []platform.Module) (http.Handler, error) {
	if h == nil {
		return nil, errors.New("newRouter: nil handlers")
	}
	if db == nil {
		return nil, errors.New("newRouter: nil database")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", healthHandler(db))

	registry := platform.NewRegistry()
	for _, module := range modules {
		module.Register(registry)
	}

	if problems := registry.Problems(); len(problems) > 0 {
		return nil, fmt.Errorf("newRouter: реестр маршрутов некорректен (%d шт):\n  - %s",
			len(problems), strings.Join(problems, "\n  - "))
	}

	if err := platform.Mount(mux, registry, platform.Middleware{
		Auth:       h.RequireAuth,
		CSRF:       h.RequireCSRF,
		Permission: h.RequirePerm,
	}); err != nil {
		return nil, err
	}

	// HTML-страницы. Отказ здесь — redirect, а не JSON, поэтому они вне
	// реестра маршрутов (см. комментарий выше).
	mux.HandleFunc("/", h.Index)
	mux.HandleFunc("/login", h.LoginPage)
	mux.HandleFunc("/register", h.RegisterPage)
	mux.HandleFunc("/dashboard", h.DashboardPage)
	mux.HandleFunc("/profile", h.ProfilePage)
	mux.HandleFunc("/admin", h.AdminPage)
	mux.HandleFunc("/schedule", h.SchedulePage)

	// Статика.
	mux.Handle("/css/", http.StripPrefix("/css/", http.FileServer(http.Dir(filepath.Join("web", "css")))))
	mux.Handle("/js/", http.StripPrefix("/js/", http.FileServer(http.Dir(filepath.Join("web", "js")))))

	return mux, nil
}

// applyModuleMigrations применяет миграции модулей поверх базовой схемы.
//
// Вынесено в отдельную функцию, чтобы main.go и тесты получали одинаковый
// результат: расхождение здесь означает «тесты проверяют одну схему, прод —
// другую».
func applyModuleMigrations(ctx context.Context, db *sql.DB, modules []platform.Module) error {
	return platform.ApplyMigrations(ctx, db, modules)
}

// healthHandler — живость сервиса.
//
// Отвечает не «да», а «да, и база отвечает»: иначе мониторинг молча считал бы
// мёртвый сервер рабочим. Таймаут проверки маленький, чтобы дешёвый GET не
// превращался в зависание.
func healthHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")

		pingCtx, cancel := context.WithTimeout(r.Context(), healthPingTimeout)
		defer cancel()

		if err := db.PingContext(pingCtx); err != nil {
			slog.Error("health: база не ответила", slog.Any("error", err))
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"error","db":"error"}`))
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","db":"ok"}`))
	}
}

// routeTable логирует, какие маршруты заявила платформа.
//
// Делается на уровне DEBUG, потому что на INFO это превратило бы каждый запуск
// в простыню; при LOG_LEVEL=debug список прав и путей виден без чтения кода.
func routeTable(modules []platform.Module) {
	registry := platform.NewRegistry()
	for _, module := range modules {
		module.Register(registry)
	}

	for _, route := range registry.Routes() {
		method := route.Method
		if method == "" {
			method = "ANY"
		}

		permission := string(route.Perm)
		if permission == "" {
			permission = "-"
		}

		slog.Debug("маршрут",
			slog.String("method", method),
			slog.String("path", route.Path),
			slog.String("permission", permission),
			slog.Bool("csrf", route.CSRF),
			slog.Bool("public", route.Public),
		)
	}
}
