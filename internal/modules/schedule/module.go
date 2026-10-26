// Package schedule — модуль расписания: лента дедлайнов и внешний источник.
//
// В модуле два разных набора данных, и путать их не нужно:
//
//   - лента (/api/schedule) собирается из заданий и событий самой платформы —
//     это «что мне сдать на этой неделе», у неё своей таблицы нет;
//   - официальное расписание пар (/api/schedule/groups, /api/schedule/lessons)
//     приходит из АПЭКС-ВУЗ и лежит в таблицах модуля (T4.1).
//
// T4.1 по плану docs/02 §7 должен был стать «пакетом source внутри модуля».
// Буквально это невозможно: модуль импортирует internal/handlers, а
// обработчику импорта нужны клиент и парсер источника — импорт модуля обратно
// был бы циклом, который Go не собирает. Поэтому адаптер источника живёт в
// leaf-пакете internal/apeks, а декларация (миграции и маршруты) осталась
// здесь: ядро по-прежнему не знает ни про АПЭКС, ни про расписание.
package schedule

import (
	"net/http"

	"github.com/NumerR/SOS/internal/authz"
	"github.com/NumerR/SOS/internal/handlers"
	"github.com/NumerR/SOS/internal/platform"
	"github.com/NumerR/SOS/internal/store"
)

// Module — декларация маршрутов расписания.
type Module struct {
	h *handlers.Handlers
}

// New собирает модуль поверх набора обработчиков.
func New(h *handlers.Handlers) *Module { return &Module{h: h} }

func (m *Module) Name() string { return "schedule" }

// Migrations — таблицы внешнего источника.
//
// Версия названа датой, как у аудита: runner сравнивает строки
// (module, version), и следующая миграция расписания не должна выглядеть
// «старше» этой.
func (m *Module) Migrations() []platform.Migration {
	return []platform.Migration{
		{Version: "2026-10-24_schedule_source", SQL: store.ScheduleSourceMigration},
	}
}

// Register заявляет маршруты модуля.
func (m *Module) Register(reg *platform.Registry) {
	h := m.h

	// Лента: право schedule.view есть у всех ролей, но объявлено оно здесь не
	// для формы — маршрутизатор откажет роли, которой права нет, ещё до
	// обращения к базе. Область видимости (свои курсы / всё) — authz.Scope,
	// который спрашивает ScheduleStore.
	reg.Add(platform.Route{
		Path:        "/api/schedule",
		Method:      http.MethodGet,
		Perm:        authz.ScheduleView,
		DenyMessage: "schedule is available to enrolled users",
		Handler:     http.HandlerFunc(h.ScheduleFeed),
	})

	// Чтение официального расписания — публичное, как и страница /schedule:
	// в ответе нет ничего персонального (предмет, время, аудитория,
	// преподаватель), а витрина существуют для гостей.
	//
	// Что здесь важно не забыть при переезде на другой источник: публичным
	// остаётся именно чтение уже импортированных строк. Токен источника
	// держит сервер, и ни один из этих маршрутов к АПЭКС не ходит.
	reg.Add(platform.Route{
		Path:    "/api/schedule/groups",
		Method:  http.MethodGet,
		Public:  true,
		Handler: http.HandlerFunc(h.ScheduleGroupsAPI),
	})
	reg.Add(platform.Route{
		Path:    "/api/schedule/lessons",
		Method:  http.MethodGet,
		Public:  true,
		Handler: http.HandlerFunc(h.ScheduleLessonsAPI),
	})

	// Управление источником — только администратор (authz.ScheduleSourceManage).
	// Каждый из этих маршрутов делает исходящий запрос на сайт вуза, поэтому
	// частота обращений к чужому API — административное решение, а не
	// побочный эффект открытия страницы.
	reg.Add(platform.Route{
		Path:        "/api/schedule/source/status",
		Method:      http.MethodGet,
		Perm:        authz.ScheduleSourceManage,
		DenyMessage: "only administrators can inspect the schedule source",
		Handler:     http.HandlerFunc(h.ScheduleStatusAPI),
	})
	reg.Add(platform.Route{
		Path:        "/api/schedule/source/groups",
		Method:      http.MethodPost,
		Perm:        authz.ScheduleSourceManage,
		DenyMessage: "only administrators can sync the group catalogue",
		CSRF:        true,
		Handler:     http.HandlerFunc(h.ScheduleSyncGroupsAPI),
	})
	reg.Add(platform.Route{
		Path:        "/api/schedule/source/lessons",
		Method:      http.MethodPost,
		Perm:        authz.ScheduleSourceManage,
		DenyMessage: "only administrators can import the schedule",
		CSRF:        true,
		Handler:     http.HandlerFunc(h.ScheduleImportAPI),
	})
}
