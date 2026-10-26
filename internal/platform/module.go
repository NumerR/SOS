// Package platform — контракт, по которому модуль подключается к ядру.
//
// Идея из docs/02-ARCHITECTURE.md §3: модуль объявляет своё имя, свои миграции и
// свои маршруты, а ядро их собирает. Ядро при этом ничего не знает про
// расписание, курсы или события — оно знает только про Registry, Middleware и
// список модулей в cmd/server/modules.go.
//
// Зачем здесь вообще отдельный слой. Раньше все маршруты объявлялись в main.go
// вручную, и каждый новый модуль правил один и тот же файл: «добавить модуль»
// означало «внесить правку в ядро». Плюс право доступа проверялось в теле
// обработчика, поэтому маршрут мог остаться незащищённым просто потому, что
// автор забыл написать `if user.Role != "teacher"`. Теперь маршрут без права
// виден в одном месте — в декларации Registry, — а проверка ставится
// обёрткой до того, как тело обработчика получит запрос.
package platform

import (
	"net/http"

	"github.com/NumerR/SOS/internal/authz"
)

// Migration — одна версионируемая порция схемы модуля.
//
// Version — строка, а не число, и участвует в уникальном ключе
// (module, version): «0001_notes» и «2026-10-24_add_author» одинаково
// применимы, а переиспользовать уже записанную версию нельзя — runner сочтёт её
// выполненной и молча пропустит новый SQL.
type Migration struct {
	Version string
	SQL     string
}

// Module — то, что обязан предоставить модуль.
//
// Migrations() может вернуть nil: не у каждого модуля своя таблица.
// Register() обязан декларативно перечислить маршруты — без обращений к
// http.ServeMux напрямую.
type Module interface {
	// Name — уникальный идентификатор модуля ("courses", "schedule").
	// Используется как префикс записей миграций и в диагностических ошибках.
	Name() string

	// Migrations — SQL модуля в порядке применения.
	Migrations() []Migration

	// Register — добавляет маршруты модуля в реестр.
	Register(reg *Registry)
}

// Route — один маршрут одного метода.
//
// Разделение по методам (а не «один маршрут на путь, внутри switch») нужно,
// чтобы право доступа было видно в декларации: `POST /api/courses` с
// Perm=course.create читается как предложение, а не тонет в теле обработчика.
type Route struct {
	// Path — точный путь ServeMux ("/api/courses"). Префиксные пути ("/js/")
	// тоже поддерживаются: для них Method обязан быть пустым.
	Path string

	// Method — "GET", "POST" и т. п. Пустая строка означает «любой метод»
	// (для раздачи статики и для handler-ов, которые сами разбирают метод).
	Method string

	// Handler — тело обработчика.
	Handler http.Handler

	// Perm — право из authz, необходимое для доступа. Пустое = «достаточно
	// войти в систему» (для Public=false) или «публичный маршрут» (Public=true).
	// Незнакомое Perm не превращается в «разрешить всем»: Registry сообщит
	// ошибку при сборке маршрутизатора.
	Perm authz.Permission

	// DenyMessage — текст отказа, который получит клиент при 403.
	// Пустой = "forbidden". Текст держится здесь, а не в обработчике, потому
	// что решение об отказе принимает обёртка права, а не тело.
	DenyMessage string

	// Public — не требовать авторизации. Только для login/register: всё, что
	// читает чужие данные, обязано быть защищено сессией.
	Public bool

	// CSRF — требовать токен CSRF (для mutate-маршрутов).
	CSRF bool
}

// Middleware — обёртки, которые ядро подставляет в декларацию маршрута.
//
// Платформа специально не знает, как устроена сессия или токен CSRF: она умеет
// только собрать цепочку. Конкретику даёт пакет internal/handlers, а связывает
// их cmd/server — так platform остаётся тестируемым без базы данных.
type Middleware struct {
	// Auth — требовать активную сессию, иначе 401.
	Auth func(next http.Handler) http.Handler

	// CSRF — требовать токен, иначе 403.
	CSRF func(next http.Handler) http.Handler

	// Permission — требовать право perm, иначе 403 с текстом denyMessage.
	// Реализация обязана сама проверить сессию: право есть только у
	// авторизованного пользователя.
	Permission func(perm authz.Permission, denyMessage string, next http.Handler) http.Handler

	// Error — формат отказа транспорта (405 для неизвестного метода). Поле можно
	// не передавать: подставляется apierror.Write — тот же конверт, что пишут
	// обработчики (ADR-0003), поэтому у клиента одна форма отказа независимо от
	// того, кто отказал.
	Error func(w http.ResponseWriter, status int, message string)
}

// apply собирает цепочку обёрток для одного маршрута.
//
// Порядок важен: снаружи — проверка входа (она дешевле и отсекает анонимов),
// внутри — CSRF, затем тело. RequireAuth сам ничего не пишет в ответ, кроме 401,
// поэтому вложенность можно читать сверху вниз как список условий допуска.
func (mw Middleware) apply(route Route, next http.Handler) http.Handler {
	handler := next

	if route.CSRF {
		handler = mw.CSRF(handler)
	}

	switch {
	case route.Public:
		// Публичный маршрут: ни сессии, ни права.
	case route.Perm != "":
		handler = mw.Permission(route.Perm, route.denyText(), handler)
	default:
		handler = mw.Auth(handler)
	}

	return handler
}

func (route Route) denyText() string {
	if route.DenyMessage != "" {
		return route.DenyMessage
	}
	return "forbidden"
}

// needsAuth/needsCSRF нужны Mount-у, чтобы требовать только те обёртки, которые
// реально используются: тестовый реестр из трёх публичных маршрутов обязан
// собираться без функции Auth.
func (route Route) needsPermissionMiddleware() bool {
	return !route.Public && route.Perm != ""
}

func (route Route) needsAuthMiddleware() bool {
	return !route.Public && route.Perm == ""
}
