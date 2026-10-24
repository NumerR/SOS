// Package audit — модуль журнала действий (audit_events).
//
// Это первый модуль, который подключился к платформе целиком через контракт
// T1.1: у него своя миграция, свой маршрут и своё право, а ядро не правилось.
// Раньше аудит жил только в логе процесса (ADR-0008) — там, где его не
// читает администратор, и docs/02-ARCHITECTURE.md §7 относил это в «осталось
// к T1.2».
//
// Модуль не пишет события сам: запись делают обработчики тех модулей, где
// произошло действие (вход, смена роли, оценка). Здесь лежат два права
// прохода — схема таблицы и путь чтения. Так остаётся возможной запись из
// любого модуля, и аудит не становится обязательной зависимостью каждого
// обработчика.
package audit

import (
	"net/http"

	"github.com/NumerR/SOS/internal/authz"
	"github.com/NumerR/SOS/internal/handlers"
	"github.com/NumerR/SOS/internal/platform"
	"github.com/NumerR/SOS/internal/store"
)

// Module — декларация журнала действий.
type Module struct {
	h *handlers.Handlers
}

// New собирает модуль поверх набора обработчиков.
func New(h *handlers.Handlers) *Module { return &Module{h: h} }

// Name — имя модуля; оно же префикс записей в журнале миграций.
func (m *Module) Name() string { return "audit" }

// Migrations — таблица audit_events.
//
// Версия названа датой, а не «0001», потому что порядок в списке важнее
// нумерации: runner сравнивает строки (module, version), и следующая миграция
// журнала не должна выглядеть «старше» этой.
func (m *Module) Migrations() []platform.Migration {
	return []platform.Migration{
		{Version: "2026-10-24_audit_events", SQL: store.AuditEventsMigration},
	}
}

// Register заявляет маршруты модуля.
func (m *Module) Register(reg *platform.Registry) {
	h := m.h

	// Чтение журнала — единственная операция модуля над этими данными. Право
	// audit.view уже включает проверку входа, поэтому RequireAuth в цепочке
	// ставит маршрутизатор, а не обработчик.
	//
	// Строго GET: список должен оставаться доступным без CSRF-токена —
	// администратор открывает его сразу после входа, когда токен ещё не
	// прочитан страницей.
	reg.Add(platform.Route{
		Path:        "/api/admin/audit",
		Method:      http.MethodGet,
		Perm:        authz.AuditView,
		DenyMessage: "only administrators can read the audit log",
		Handler:     http.HandlerFunc(h.AdminAudit),
	})
}
