package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/NumerR/SOS/internal/apeks"
	"github.com/NumerR/SOS/internal/authz"
	"github.com/NumerR/SOS/internal/models"
	"github.com/NumerR/SOS/internal/store"
)

const sessionCookieName = "gs_session"

// secureSessionCookieName — то же имя с префиксом __Host-.
//
// Браузер принимает __Host-* только по HTTPS, только с Path=/ и только без
// Domain. Это закрывает два обхода, которые остаются у обычной куки: её
// может подставить любой поддомен того же хоста и её может сузить/расширить
// Path. На plain HTTP такой префикс браузер выбросил бы, поэтому локальная
// разработка продолжает ходить под обычным именем.
const secureSessionCookieName = "__Host-" + sessionCookieName

// sessionCookieNames — порядок чтения: сначала защищённое имя, потом обычное.
// Если в браузере остались обе (переезд на HTTPS), победит та, которую
// подделать дешевле нельзя.
func sessionCookieNames() []string {
	return []string{secureSessionCookieName, sessionCookieName}
}

// sessionCookieNameFor — имя, под которым кука выпускается прямо сейчас.
func sessionCookieNameFor(r *http.Request) string {
	if isSecureRequest(r) {
		return secureSessionCookieName
	}
	return sessionCookieName
}

type contextKey string

const userContextKey contextKey = "user"

// sessionFailureReason — текст отказа, когда хранилище сессий не ответило.
// Одно имя на middleware и страницы, потому что по нему ищут в логе: reason
// совпадает с тем, что видит клиент (ADR-0030), и две формулировки одного и
// того же сбоя превратили бы разбор жалобы в перебор вариантов.
const sessionFailureReason = "failed to verify session"

// Deps — зависимости обработчиков. Собираются в main.go инициализатором
// с именами полей: девять позиционных аргументов подряд легко перепутать
// местами, и компилятор это не заметит.
type Deps struct {
	Users       *store.UserStore
	Sessions    *store.SessionStore
	Courses     *store.CourseStore
	Enrollments *store.EnrollmentStore
	Assignments *store.AssignmentStore
	Submissions *store.SubmissionStore
	Schedule    *store.ScheduleStore
	Resources   *store.ResourceStore
	Events      *store.EventStore

	// Audit — журнал действий (T1.2). Отличается от остальных полей одним:
	// отсутствие хранилища не отменяет запрос, а только лишает его строки в
	// журнале (см. handlers.recordAudit). Поэтому nil здесь не ошибка сборки,
	// а состояние, на которое опираются unit-тесты пакетов без журнала.
	Audit  *store.AuditStore
	Limits Limiters

	// ScheduleSource — таблицы внешнего расписания (T4.1): каталог групп и
	// импортированные пары.
	ScheduleSource *store.ScheduleSourceStore

	// Apeks — доступ к источнику расписания. nil означает «источник не
	// настроен», и это рабочее состояние, а не ошибка сборки: чтение уже
	// импортированных данных при nil работает, а запросы управления отвечают
	// 503 с внятной подсказкой.
	//
	// Тип — интерфейс, а не конкретный клиент: реализаций стало две (Go-разбор
	// и приложенный Python-парсер), и выбирает их оператор переменной
	// SCHEDULE_SOURCE. Повод и границы решения — docs/05-DECISIONS.md, ADR-0021.
	// Обработчик от этого не умнее: он по-прежнему спрашивает только группы и
	// пары, а права, CSRF и частоту обращений к чужому API держит здесь, а не
	// внутри движка.
	//
	// nil с ADR-0021 перестаёт значить одно-единственное: токена может и не
	// быть, а может быть токен и недоступный парсер (нет python3, нет fetch.py,
	// неразобравшийся адрес, неизвестное значение SCHEDULE_SOURCE). Поэтому у
	// nil есть два сопровождающих поля ниже.
	Apeks apeks.Backend

	// ScheduleSourceMissing — какая переменная окружения виновата в том, что
	// Apeks равен nil ("APEKS_TOKEN", "PYTHON_BIN", "APEKS_API_URL",
	// "SCHEDULE_SOURCE"). Пустое значение — «неизвестно»: панель тогда
	// подставляет прежний общий текст про APEKS_TOKEN, и ни один тест,
	// собирающий Deps вручную, не обязан это поле знать.
	//
	// Значение — имя переменной, а не сообщение ошибки: в тексте ошибки моста
	// есть абсолютные пути процесса, и показывать их браузеру незачем
	// (тот же принцип, что с токеном в ADR-0015).
	ScheduleSourceMissing string

	// ScheduleSourceHint — готовая подсказка админской панели и текста 503 для
	// той же причины. Пустая — значит панель формулирует сама (случай
	// ненастроенного токена: её прежний текст и так верный).
	ScheduleSourceHint string
}

// Handlers — HTTP-обработчики. Поля Deps доступны напрямую: h.Users, h.Sessions…
type Handlers struct {
	Deps
}

// New создаёт набор обработчиков.
func New(deps Deps) *Handlers {
	return &Handlers{Deps: deps}
}

// UserFromContext — авторизованный пользователь запроса.
//
// Экспортировано для обработчиков модулей: middleware входа кладёт
// пользователя в контекст, и модулю незачем знать, как устроена кука сессии —
// ему нужен только автор. nil означает «запрос не прошёл RequireAuth или
// RequirePerm», то есть маршрутизатор собран неправильно.
func UserFromContext(r *http.Request) *models.User {
	if r == nil {
		return nil
	}
	user, ok := r.Context().Value(userContextKey).(*models.User)
	if !ok {
		return nil
	}
	return user
}

// currentUser возвращает пользователя из контекста запроса.
func (h *Handlers) currentUser(r *http.Request) *models.User {
	return UserFromContext(r)
}

// sessionIDFromRequest берёт идентификатор сессии из куки любого из двух
// имён. Единая точка нужна затем, чтобы чтение и отзыв сессий не разошлись:
// «Logout» под обычным именем оставил бы __Host-* жить в браузере.
func sessionIDFromRequest(r *http.Request) string {
	for _, name := range sessionCookieNames() {
		cookie, err := r.Cookie(name)
		if err == nil && cookie.Value != "" {
			return cookie.Value
		}
	}
	return ""
}

// authenticatedUser проверяет cookie-сессию и возвращает пользователя.
//
// Исхода три, а не два, как давал прежний bool: «входа нет» (nil, nil),
// «хранилище не ответило» (nil, err) и вход состоялся (user, nil). Склейка
// первых двух отвечала 401 там, где база лежала: человек с жалобой «не
// пускает» получал страницу входа, мониторинг — всплеск 401 вместо 500, а в
// логе не появлялось ничего, кроме строки доступа. Это ровно та подмена,
// которую ADR-0029 убрал из проверки прав, только на пути входа, и ADR-0030
// требует, чтобы у настоящего сбоя была причина в логе.
func (h *Handlers) authenticatedUser(r *http.Request) (*models.User, error) {
	sessionID := sessionIDFromRequest(r)
	if sessionID == "" {
		return nil, nil
	}

	session, err := h.Sessions.Get(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("чтение сессии: %w", err)
	}

	user, err := h.Users.GetByID(r.Context(), session.UserID)
	if err != nil {
		// Сессия живая, а пользователя нет: аккаунт удалили или связь сгорела.
		// Это «входа нет», а не отказ сервера — иначе выход удалённого
		// пользователя превратился бы в 500.
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("чтение пользователя сессии: %w", err)
	}

	return user, nil
}

// RequireAuth — middleware: доступ только авторизованным.
// Попутно выпускает csrf-cookie, если её ещё нет (совместимость со старыми сессиями).
func (h *Handlers) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := h.authenticatedUser(r)
		if err != nil {
			internalError(w, r, sessionFailureReason, err)
			return
		}
		if user == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		ensureCSRFCookie(w, r)

		ctx := context.WithValue(r.Context(), userContextKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequirePerm — middleware: вход в систему плюс право из authz.
//
// Это замена RequireRole(...): раньше маршрут декларировал список ролей, и
// решение о доступе жило в двух местах — в маршруте и внутри обработчика
// (`hasRole`). Теперь маршрут декларирует *право*, а соответствие
// «роль → право» стоит в одной таблице (internal/authz).
//
// denyMessage — текст отказа: клиенту нужен внятный «only teachers can create
// courses», а не голое «forbidden». Текст передаёт маршрутизатор, потому что
// именно он знает, какое действие запрашивали.
//
// Требование к сборщику маршрутов: каждый защищённый путь обязан пройти через
// этот middleware. Проверка «принадлежит ли объект пользователю» отсюда не
// видна — она остаётся в обработчике, потому что требует данных из базы.
func (h *Handlers) RequirePerm(perm authz.Permission, denyMessage string, next http.Handler) http.Handler {
	if denyMessage == "" {
		denyMessage = "forbidden"
	}

	return h.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := h.currentUser(r)
		if user == nil {
			// RequireAuth положил пользователя в контекст. nil здесь означает,
			// что middleware собрали в неправильном порядке, — отвечаем 401 и
			// не пытаемся угадать права.
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		if !authz.Can(authz.Role(user.Role), perm) {
			writeError(w, http.StatusForbidden, denyMessage)
			return
		}

		next.ServeHTTP(w, r)
	}))
}

// canHere — право текущего пользователя без обращения к строке роли.
//
// Обработчики больше не сравнивают user.Role с литералом: им нужно понять,
// распространяется ли их действие на чужие объекты (права вида *.any).
func canHere(user *models.User, perm authz.Permission) bool {
	if user == nil {
		return false
	}
	return authz.Can(authz.Role(user.Role), perm)
}
