package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/NumerR/SOS/internal/models"
	"github.com/NumerR/SOS/internal/store"
)

// Тесты журнала действий со стороны обработчиков (T1.2): какое событие пишет
// какой обработчик, что в него попадает и чего в нём быть не должно.
//
// Три инварианта, которые проверяются здесь систематически:
//
//  1. Событие есть на каждой ветке, где меняется доступ: удачный и неудачный
//     вход, блокировка по идентификатору, выход, «выйти везде», смена пароля,
//     регистрация, смена роли.
//  2. Журнал не может испортить запрос: сбой записи оставляет исход
//     обработчика прежним.
//  3. В журнале нет ничего, что можно использовать как вход: паролей,
//     bcrypt-хэшей, токенов сессии и CSRF.

// auditTestLimit — заведомо больше, чем любое событие в одном тесте.
const auditTestLimit = 200

func auditEvents(t *testing.T, h *Handlers) []store.AuditEvent {
	t.Helper()

	events, err := h.Audit.List(context.Background(), store.AuditFilter{Limit: auditTestLimit})
	if err != nil {
		t.Fatalf("прочитать журнал: %v", err)
	}

	return events
}

// lastAudit возвращает свежее событие заданного действия: журнал читается
// свежими записями вперёд, поэтому первая подходящая строка и есть последнее.
func lastAudit(t *testing.T, h *Handlers, action store.AuditAction) store.AuditEvent {
	t.Helper()

	events := auditEvents(t, h)

	for _, event := range events {
		if event.Action == string(action) {
			return event
		}
	}

	t.Fatalf("в журнале нет события %s; есть %+v", action, events)
	return store.AuditEvent{}
}

func countAudit(h *Handlers, action store.AuditAction) int {
	count := 0

	events, err := h.Audit.List(context.Background(), store.AuditFilter{
		Action: string(action),
		Limit:  auditTestLimit,
	})
	if err != nil {
		panic(err)
	}

	for _, event := range events {
		if event.Action == string(action) {
			count++
		}
	}

	return count
}

func TestAuditRecordsSuccessfulLogin(t *testing.T) {
	h := newTestHandlers(t)
	user := createUser(t, h, "auditstud", "auditpassword1", "student")

	const agent = "Mozilla/5.0 (audit-test)"
	rec := callLoginWithAgent(h, `{"identifier":"auditstud","password":"auditpassword1"}`, "203.0.113.9:443", agent)
	if rec.Code != http.StatusOK {
		t.Fatalf("вход не удался: %d %s", rec.Code, rec.Body.String())
	}

	event := lastAudit(t, h, store.AuditLogin)

	if !event.OK {
		t.Error("удачный вход записан как неудачный")
	}
	if event.ActorUserID == nil || *event.ActorUserID != user.ID {
		t.Errorf("actor_user_id = %v, ожидался id %d", event.ActorUserID, user.ID)
	}
	if event.IP != "203.0.113.9" {
		t.Errorf("ip = %q, ожидалось 203.0.113.9 — адрес обязан сохраняться без порта", event.IP)
	}
	// User-Agent пишется как пришёл: по нему админ отличает браузер от
	// переборщика, и терять его «потому что заголовок пустой» нельзя.
	if event.UserAgent != agent {
		t.Errorf("user_agent = %q, ожидалось %q", event.UserAgent, agent)
	}
	if event.TargetType != "user" || event.TargetID != strconv.FormatInt(user.ID, 10) {
		t.Errorf("цель = %s/%s, ожидался user/%d", event.TargetType, event.TargetID, user.ID)
	}

	// Один удачный вход — одна строка: дубль появился бы, если писать и в
	// middleware, и в обработчике.
	if n := countAudit(h, store.AuditLogin); n != 1 {
		t.Errorf("событий auth.login: %d, ожидалось 1", n)
	}
}

// callLoginWithAgent — вход с указанным User-Agent.
//
// Общий request() заголовка не ставит (httptest.NewRequest оставляет его
// пустым), а аудит как раз и проверяет, что агент доезжает до строки журнала.
func callLoginWithAgent(h *Handlers, body, remoteAddr, agent string) *httptest.ResponseRecorder {
	handler := h.RateLimit(h.Limits.Login, "login", ClientIP)(http.HandlerFunc(h.Login))

	req := request(http.MethodPost, "/api/auth/login", body, remoteAddr, nil)
	req.Header.Set("User-Agent", agent)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	return rec
}

func TestAuditRecordsFailedLoginOfKnownUser(t *testing.T) {
	h := newTestHandlers(t)
	user := createUser(t, h, "auditwrong", "auditpassword1", "student")

	rec := callLogin(h, `{"identifier":"auditwrong","password":"auditpassword2"}`, "203.0.113.10:1", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("ожидался 401, получили %d", rec.Code)
	}

	event := lastAudit(t, h, store.AuditLogin)

	if event.OK {
		t.Error("неудачный вход записан как удачный")
	}
	if event.ActorUserID == nil || *event.ActorUserID != user.ID {
		t.Errorf("учётка найдена, но автора в журнале нет: %v", event.ActorUserID)
	}
	if !strings.Contains(event.Detail, "invalid") {
		t.Errorf("detail не объясняет причину: %q", event.Detail)
	}
}

// TestAuditFailedLoginWithoutAccountKeepsActorNull — попытка входа в
// несуществующую учётку запоминается, но автора у неё нет: выдуманное имя не
// ссылается ни на какую строку users.
func TestAuditFailedLoginWithoutAccountKeepsActorNull(t *testing.T) {
	h := newTestHandlers(t)

	rec := callLogin(h, `{"identifier":"nonsuch_user","password":"whatever123"}`, "203.0.113.11:1", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("ожидался 401, получили %d", rec.Code)
	}

	event := lastAudit(t, h, store.AuditLogin)

	if event.ActorUserID != nil {
		t.Errorf("автор = %d, ожидался NULL", *event.ActorUserID)
	}
	if event.TargetType != "identifier" || event.TargetID != "" {
		t.Errorf("цель = %s/%q, ожидался identifier без идентификатора в числовом поле",
			event.TargetType, event.TargetID)
	}
	// Имя учётки в журнале нужно: это ответ на «по кому именно подбирали
	// пароль». Пароля там, разумеется, нет — это проверяет
	// TestAuditStoresNoCredentials.
	if !strings.Contains(event.Detail, "nonsuch_user") {
		t.Errorf("в detail нет идентификатора: %q", event.Detail)
	}
}

// TestAuditRecordsLockout — срабатывание счётчика неудач само по себе событие:
// именно его админ ищет, когда отвечает на «почему меня не пускает».
func TestAuditRecordsLockout(t *testing.T) {
	h := newTestHandlers(t)
	createUser(t, h, "auditlock", "auditpassword1", "student")

	for i := 0; i < 5; i++ {
		callLogin(h, `{"identifier":"auditlock","password":"wrongpassword"}`, "203.0.113.12:1", nil)
	}

	rec := callLogin(h, `{"identifier":"auditlock","password":"auditpassword1"}`, "203.0.113.12:1", nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("шестая попытка: код %d, ожидался 429", rec.Code)
	}

	event := lastAudit(t, h, store.AuditLogin)
	if event.OK {
		t.Error("заблокированная попытка записана как удачный вход")
	}
	if !strings.Contains(event.Detail, "locked") {
		t.Errorf("detail не говорит о блокировке: %q", event.Detail)
	}

	// Бакет считается по идентификатору, а не по адресу: правильный пароль в
	// этот момент не пускает и с другого IP — иначе «locked» было бы словом
	// без подтверждения.
	if again := callLogin(h, `{"identifier":"auditlock","password":"auditpassword1"}`, "203.0.113.13:1", nil); again.Code != http.StatusTooManyRequests {
		t.Errorf("вход с другого адреса после блокировки: код %d, ожидался 429", again.Code)
	}
}

func TestAuditRecordsLogout(t *testing.T) {
	h := newTestHandlers(t)
	user := createUser(t, h, "auditout", "auditpassword1", "student")
	cookies := login(t, h, "auditout", "auditpassword1", "203.0.113.14:1")

	rec := httptest.NewRecorder()
	handler := http.HandlerFunc(h.Logout)
	handler.ServeHTTP(rec, request(http.MethodPost, "/api/auth/logout", "", "203.0.113.14:1", cookies))
	if rec.Code != http.StatusOK {
		t.Fatalf("выход: код %d", rec.Code)
	}

	event := lastAudit(t, h, store.AuditLogout)
	if !event.OK {
		t.Error("выход записан как неуспешный")
	}
	if event.ActorUserID == nil || *event.ActorUserID != user.ID {
		t.Errorf("автор выхода = %v, ожидался id %d", event.ActorUserID, user.ID)
	}
	if event.TargetType != "session" || event.TargetID != "" {
		t.Errorf("цель = %s/%q: идентификатор сессии в журнале быть не должен",
			event.TargetType, event.TargetID)
	}

	// Повторный выход с той же кукой: сессии уже нет, автора установить нечем,
	// и событие обязано остаться честным (ok=false), а не исчезнуть.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, request(http.MethodPost, "/api/auth/logout", "", "203.0.113.14:1", cookies))

	repeated := lastAudit(t, h, store.AuditLogout)
	if repeated.OK || repeated.ActorUserID != nil {
		t.Errorf("повторный выход записан как успешный вход в чужую сессию: %+v", repeated)
	}
	if n := countAudit(h, store.AuditLogout); n != 2 {
		t.Errorf("событий auth.logout: %d, ожидалось 2", n)
	}
}

func TestAuditRecordsLogoutAll(t *testing.T) {
	h := newTestHandlers(t)
	user := createUser(t, h, "auditall", "auditpassword1", "student")
	cookies := login(t, h, "auditall", "auditpassword1", "203.0.113.15:1")

	rec := callAsUser(h, http.HandlerFunc(h.LogoutAll), "/api/auth/logout-all", `{}`, cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("выйти везде: код %d %s", rec.Code, rec.Body.String())
	}

	event := lastAudit(t, h, store.AuditLogoutAll)
	if !event.OK {
		t.Error("«выйти везде» записано как неуспешное")
	}
	if event.ActorUserID == nil || *event.ActorUserID != user.ID {
		t.Errorf("автор = %v, ожидался id %d", event.ActorUserID, user.ID)
	}
}

func TestAuditRecordsPasswordChange(t *testing.T) {
	h := newTestHandlers(t)
	user := createUser(t, h, "auditpass", "auditpassword1", "student")
	cookies := login(t, h, "auditpass", "auditpassword1", "203.0.113.16:1")

	// Неверный текущий пароль — событие безопасности: либо XSS, либо чужая
	// вкладка на общём ноутбуке.
	wrong := callAsUser(h, http.HandlerFunc(h.ChangePassword), "/api/auth/change-password",
		`{"current_password":"notmine12345","new_password":"auditpassword2"}`, cookies)
	if wrong.Code != http.StatusUnauthorized {
		t.Fatalf("ожидался 401, получили %d %s", wrong.Code, wrong.Body.String())
	}

	event := lastAudit(t, h, store.AuditPasswordChange)
	if event.OK {
		t.Error("неверный текущий пароль записан как успешная смена")
	}
	if !strings.Contains(event.Detail, "invalid current") {
		t.Errorf("detail не объясняет причину: %q", event.Detail)
	}
	if event.ActorUserID == nil || *event.ActorUserID != user.ID {
		t.Errorf("автор = %v, ожидался id %d", event.ActorUserID, user.ID)
	}

	good := callAsUser(h, http.HandlerFunc(h.ChangePassword), "/api/auth/change-password",
		`{"current_password":"auditpassword1","new_password":"auditpassword2"}`, cookies)
	if good.Code != http.StatusOK {
		t.Fatalf("смена пароля: код %d %s", good.Code, good.Body.String())
	}

	event = lastAudit(t, h, store.AuditPasswordChange)
	if !event.OK {
		t.Error("успешная смена пароля записана как неудачная")
	}
	// Текущая сессия сохранена, других нет — отозвано нулевое число.
	if !strings.Contains(event.Detail, "revoked_sessions=0") {
		t.Errorf("в detail нет числа отозванных сессий: %q", event.Detail)
	}
}

func TestAuditRecordsRegister(t *testing.T) {
	h := newTestHandlers(t)

	rec := callRegister(h, `{"username":"auditnew","email":"auditnew@example.test","full_name":"Audit New","password":"auditpassword1","role":"student"}`, "203.0.113.17:1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("регистрация: код %d %s", rec.Code, rec.Body.String())
	}

	var user models.User
	if err := json.Unmarshal(rec.Body.Bytes(), &user); err != nil {
		t.Fatalf("разобрать ответ: %v", err)
	}

	event := lastAudit(t, h, store.AuditRegister)
	if !event.OK {
		t.Error("регистрация записана как неудачная")
	}
	if event.ActorUserID == nil || *event.ActorUserID != user.ID {
		t.Errorf("автор регистрации = %v, ожидался id %d", event.ActorUserID, user.ID)
	}
	if event.Detail != "role=student" {
		t.Errorf("detail = %q, ожидалось role=student", event.Detail)
	}
}

// TestRegisterFailureIsNotAudited — осознанное ограничение, а не недосмотр.
//
// Отказы валидации (короткий пароль, занятый логин) не меняют доступ и не
// отличают атаку от опечатки: журнал превратился бы в свалку, в которой
// не видно того, ради чего его ведут.
func TestRegisterFailureIsNotAudited(t *testing.T) {
	h := newTestHandlers(t)

	rec := callRegister(h, `{"username":"a","email":"bad","full_name":"","password":"short","role":"teacher"}`, "203.0.113.21:1")
	if rec.Code == http.StatusCreated {
		t.Fatal("некорректная регистрация прошла")
	}

	events := auditEvents(t, h)
	if len(events) != 0 {
		t.Errorf("журнал пополнен %d записями на отказе регистрации", len(events))
	}
}

func TestAuditRecordsRoleChange(t *testing.T) {
	h := newTestHandlers(t)
	admin := createUser(t, h, "auditadmin", "auditpassword1", "admin")
	target := createUser(t, h, "auditpromoted", "auditpassword1", "student")

	cookies := login(t, h, "auditadmin", "auditpassword1", "203.0.113.18:1")

	rec := callAsUser(h, http.HandlerFunc(h.AdminSetRole), "/api/admin/users/role",
		`{"user_id":`+strconv.FormatInt(target.ID, 10)+`,"role":"teacher"}`, cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("смена роли: код %d %s", rec.Code, rec.Body.String())
	}

	event := lastAudit(t, h, store.AuditRoleChange)
	if !event.OK {
		t.Error("смена роли записана как неудачная")
	}
	if event.ActorUserID == nil || *event.ActorUserID != admin.ID {
		t.Errorf("автор = %v, ожидался id админа %d", event.ActorUserID, admin.ID)
	}
	if event.TargetType != "user" || event.TargetID != strconv.FormatInt(target.ID, 10) {
		t.Errorf("цель = %s/%s, ожидался user/%d", event.TargetType, event.TargetID, target.ID)
	}
	// Значение «до» теряется при записи без журнала: users хранит только
	// текущую роль, поэтому detail — единственный след прежнего доступа.
	if event.Detail != "from=student to=teacher" {
		t.Errorf("detail = %q, ожидался from=student to=teacher", event.Detail)
	}
}

// TestAuditDoesNotBreakRequestWhenWritingFails — журнал не условие доступа.
//
// Таблицы нет (миграция не применилась, база на read-only томе): вход всё
// равно обязан пустить пользователя. Иначе сломанный аудит превращается в
// отказ всего сервиса.
func TestAuditDoesNotBreakRequestWhenWritingFails(t *testing.T) {
	h, db := newTestEnv(t)
	createUser(t, h, "auditbroken", "auditpassword1", "student")

	if _, err := db.Exec(`DROP TABLE audit_events`); err != nil {
		t.Fatalf("удалить таблицу журнала: %v", err)
	}

	rec := callLogin(h, `{"identifier":"auditbroken","password":"auditpassword1"}`, "203.0.113.19:1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("вход при сломанном журнале: код %d %s", rec.Code, rec.Body.String())
	}

	// Тот же путь проверяется на защищённом обработчике: RequireAuth уже
	// положил пользователя в контекст, и ошибка записи не должна мешать входу.
	if _, err := db.Exec(store.AuditEventsMigration); err != nil {
		t.Fatalf("вернуть таблицу: %v", err)
	}

	cookies := login(t, h, "auditbroken", "auditpassword1", "203.0.113.22:1")
	if n := countAudit(h, store.AuditLogin); n != 1 {
		t.Errorf("после восстановления таблицы записано %d событий входа, ожидалось 1", n)
	}
	if len(cookies) == 0 {
		t.Error("вход после восстановления журнала не вернул куку")
	}
}

// TestAuditStoresNoCredentials — в журнале нет ничего, чем можно войти.
//
// Проверяется содержимое строк, а не «правильно ли вызвали функцию»: именно
// это ограничение нельзя обойти случайной правкой в другом месте.
func TestAuditStoresNoCredentials(t *testing.T) {
	h := newTestHandlers(t)
	const (
		password = "auditpassword1"
		newPass  = "auditpassword2"
	)

	createUser(t, h, "auditsecret", password, "student")
	cookies := login(t, h, "auditsecret", password, "203.0.113.20:1")

	callAsUser(h, http.HandlerFunc(h.ChangePassword), "/api/auth/change-password",
		`{"current_password":"`+password+`","new_password":"`+newPass+`"}`, cookies)
	callAsUser(h, http.HandlerFunc(h.LogoutAll), "/api/auth/logout-all", `{}`, cookies)

	token := sessionCookieOf(cookies)
	if token == "" {
		t.Fatal("вход не вернул сессионную куку — тест не о чём")
	}

	secrets := []string{password, newPass, token, "$2a$", "$2b$", csrfFrom(cookies)}

	for _, event := range auditEvents(t, h) {
		blob := strings.Join([]string{
			event.Action, event.TargetType, event.TargetID,
			event.Detail, event.IP, event.UserAgent, event.ActorUsername,
		}, " | ")

		// Пароль не должен попадать в журнал даже текстом: обработчики берут
		// req.Password только ради bcrypt, а случайная строка detail — это
		// утечка учётных данных в таблицу, которую читает админка.
		for _, secret := range secrets {
			if secret == "" {
				continue
			}
			if strings.Contains(blob, secret) {
				t.Errorf("в журнале оказался секрет %q: %s", secret, blob)
			}
		}
	}
}
