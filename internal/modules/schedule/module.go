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
		// Слой карты (ADR-0041): индекс для запроса «дни по всем группам».
		// Отдельной версией, потому что первая на живых базах уже применена и
		// runner её не повторяет.
		{Version: "2026-11-02_schedule_map_index", SQL: store.ScheduleMapIndexMigration},
		// Покрытие месяцев (ADR-0042): третья таблица источника. Миграция
		// заполняет её из уже лежащих пар, иначе база, куда импорт приходил
		// вчера, сегодня выглядела бы незаполненной.
		{Version: "2026-11-03_schedule_coverage", SQL: store.ScheduleCoverageMigration},
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
	// преподаватель), а витрина существует для гостей.
	//
	// С ADR-0042 публичным остаётся показ импортированных строк, и именно он
	// теперь добирает недостающие месяцы из АПЭКС — но делает это сервер:
	// токен источника не уходит ни в один ответ, обращения дедуплицируются по
	// (группа, месяц), ограничены четырьмя одновременными и двенадцатисекундным
	// бюджетом, а при отказе вуза страницу кормит собственная база. Границы
	// описаны в internal/handlers/schedule_readthrough.go.
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

	// Слой карты «пары по корпусам». В отличие от двух путей выше — публичным
	// он не является: карта адресована вошедшему (страница /map под RequireAuth),
	// и право здесь то же, что у ленты, — роль без schedule.view не должна
	// получать и сводку по кампусу.
	reg.Add(platform.Route{
		Path:        "/api/schedule/map",
		Method:      http.MethodGet,
		Perm:        authz.ScheduleView,
		DenyMessage: "the schedule map is available to signed-in users",
		Handler:     http.HandlerFunc(h.ScheduleMapAPI),
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
	// Массовый импорт всего каталога: POST запускает фоновую задачу и отвечает
	// сразу, GET читает её прогресс. Разные методы одного пути — не экономия на
	// строках, а один и тот же объект (задача) в двух операциях: «создать» и
	// «посмотреть».
	reg.Add(platform.Route{
		Path:        "/api/schedule/source/import-all",
		Method:      http.MethodPost,
		Perm:        authz.ScheduleSourceManage,
		DenyMessage: "only administrators can import the whole catalogue",
		CSRF:        true,
		Handler:     http.HandlerFunc(h.ScheduleImportAllAPI),
	})
	reg.Add(platform.Route{
		Path:        "/api/schedule/source/import-all",
		Method:      http.MethodGet,
		Perm:        authz.ScheduleSourceManage,
		DenyMessage: "only administrators can read the import progress",
		Handler:     http.HandlerFunc(h.ScheduleImportAllStatusAPI),
	})
}
