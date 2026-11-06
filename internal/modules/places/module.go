// Package places — модуль справочника мест для карты.
//
// Это фундамент обоих слоёв карты (события и пары расписания), и он специально
// вынесен в отдельный модуль, а не живёт внутри events: координаты у здания
// одни, а показывают их два разных слоя. Если бы геометрия осталась колонкой
// события, второй слой скопировал бы её себе, и расхождение «корпус на карте в
// двухстах метрах от самого себя» стало бы вопросом времени.
//
// Второе отличие от остальных модулей: данные здесь вводятся руками, а не
// импортируются из источника. Поэтому права на запись — у тех же, кто ведёт
// витрину (event.create), а не у администратора: преподаватель, который
// добавляет событие в актовый зал, обязан иметь возможность этот зал завести.
package places

import (
	"net/http"

	"github.com/NumerR/SOS/internal/authz"
	"github.com/NumerR/SOS/internal/handlers"
	"github.com/NumerR/SOS/internal/platform"
	"github.com/NumerR/SOS/internal/store"
)

// Module — декларация справочника мест.
type Module struct {
	h *handlers.Handlers
}

// New собирает модуль поверх набора обработчиков.
func New(h *handlers.Handlers) *Module { return &Module{h: h} }

// Name — имя модуля; оно же префикс записей в журнале миграций.
func (m *Module) Name() string { return "places" }

// Migrations — таблица places и пять корпусов, названных владельцем.
//
// Версия названа датой, как в audit: runner сравнивает строки (module,
// version), и следующая правка справочника не должна выглядеть «старше» этой.
//
// Модуль ставится в списке раньше events: правка схемы событий (place_id)
// ссылается на таблицу мест, а миграции применяются в порядке списка.
//
// Сид корпусов идёт второй миграцией, а не добавлен в текст первой: первая уже
// применена на живых базах, и править её значит заставить runner накатить
// другое содержимое под той же версией — или, при другом порядке сравнения,
// вообще не накатить. Отдельная версия применяется один раз и ровно там, где
// справочник ещё не знает корпусов.
func (m *Module) Migrations() []platform.Migration {
	return []platform.Migration{
		{Version: "2026-11-02_places", SQL: store.PlacesMigration},
		{Version: "2026-11-06_places_campus", SQL: store.CampusBuildingsSeed},
	}
}

// Register заявляет маршруты модуля.
//
// Чтение открыто всякому вошедшему (RequireAuth без права): список мест —
// география кампуса, а не чужие данные, и карта без него не рисуется.
//
// Запись — под правом place.manage и с CSRF: справочник общий на всех, и
// подставленный запрос, добавивший «корпус» с чужими координатами, испортил бы
// вид всем пользователям сразу.
func (m *Module) Register(reg *platform.Registry) {
	h := m.h

	reg.Add(platform.Route{
		Path:    "/api/places",
		Method:  http.MethodGet,
		Handler: http.HandlerFunc(h.PlacesAPI),
	})
	reg.Add(platform.Route{
		Path:        "/api/places",
		Method:      http.MethodPost,
		Perm:        authz.PlaceManage,
		DenyMessage: "only teachers can maintain the places directory",
		CSRF:        true,
		Handler:     http.HandlerFunc(h.PlacesAPI),
	})
	reg.Add(platform.Route{
		Path:        "/api/places/update",
		Method:      http.MethodPost,
		Perm:        authz.PlaceManage,
		DenyMessage: "only teachers can maintain the places directory",
		CSRF:        true,
		Handler:     http.HandlerFunc(h.PlaceUpdate),
	})

	// Конфигурация подложки. Маршрут живёт здесь, а не в отдельном модуле, по
	// одной причине: у конфига нет ни таблицы, ни прав, ни своего источника
	// данных — он две строки из окружения, которые страница спрашивает рядом со
	// справочником. Модуль без миграций ради одного GET удвоил бы список
	// moduleList, ничего в него не добавив.
	reg.Add(platform.Route{
		Path:    "/api/map/config",
		Method:  http.MethodGet,
		Handler: http.HandlerFunc(h.MapConfigAPI),
	})
}
