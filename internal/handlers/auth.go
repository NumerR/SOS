package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/NumerR/SOS/internal/models"
	"github.com/NumerR/SOS/internal/store"
	"golang.org/x/crypto/bcrypt"
)

type RegisterRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	FullName string `json:"full_name"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

type LoginRequest struct {
	Identifier string `json:"identifier"`
	Password   string `json:"password"`
}

// ChangePasswordRequest — смена пароля внутри авторизованной сессии.
// Подтверждение нового пароля намеренно не запрашивается: это делает фронтенд,
// а серверу достаточно одного поля, иначе API нельзя было бы описать честно.
type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

const (
	// minPasswordLength — то же требование, что у регистрации.
	minPasswordLength = 8
	// maxPasswordLength ограничен сверху, чтобы bcrypt не тра CPU на
	// многомегабайтное тело: bcrypt обрезает вход после 72 байт, а длинные
	// строки — удобный способ замедить обработчик.
	maxPasswordLength = 200
)

var (
	usernamePattern = regexp.MustCompile(`^[\p{L}\p{N}_.-]{3,32}$`)
	emailPattern    = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

	// dummyPasswordHash — заведомо чужой bcrypt-хэш фиктивной стоимости 10.
	// Нужен только для того, чтобы ветка «пользователь не найден» тратила на
	// сравнение столько же времени, сколько ветка «пользователь найден».
	// Пароль к нему не подбирается: он никогда не признаётся успешным входом
	// (см. проверку userWithSecret == nil в Login).
	dummyPasswordHash = []byte("$2a$10$CwTycUXWue0Thq9StjUM0uJ8Z1MZ0D3vLqKZ2h8uJgW6cHmB7lTIW")
)

// normalizeRole валидирует роль из публичной регистрации.
//
// Публично разрешена только роль студента. Раньше здесь же проходил и
// «teacher», то есть любой посетитель получал право создавать курсы и
// оценивать чужие работы (учётка t_teacher в тестовой базе создана именно
// так). Роль теперь назначает администратор — см. AdminSetRole.
func normalizeRole(role string) (string, bool) {
	role = strings.ToLower(strings.TrimSpace(role))
	switch role {
	case "", "student":
		return "student", true
	default:
		return "", false
	}
}

// Register создаёт нового пользователя и сразу входит в систему.
func (h *Handlers) Register(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}

	var req RegisterRequest
	if !readJSON(w, r, &req) {
		return
	}

	req.Username = strings.ToLower(strings.TrimSpace(req.Username))
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	req.FullName = strings.TrimSpace(req.FullName)

	role, ok := normalizeRole(req.Role)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "role is assigned by an administrator; register as a student")
		return
	}

	if !usernamePattern.MatchString(req.Username) {
		writeError(w, http.StatusUnprocessableEntity, "username must contain 3-32 letters, digits, dots, dashes or underscores")
		return
	}

	if !emailPattern.MatchString(req.Email) {
		writeError(w, http.StatusUnprocessableEntity, "invalid email")
		return
	}

	if n := utf8.RuneCountInString(req.Password); n < minPasswordLength || n > maxPasswordLength {
		writeError(w, http.StatusUnprocessableEntity, "password must be between 8 and 200 characters")
		return
	}

	if utf8.RuneCountInString(req.FullName) > 100 {
		writeError(w, http.StatusUnprocessableEntity, "full name is too long")
		return
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to hash password")
		return
	}

	user, err := h.Users.Create(
		r.Context(),
		req.Username,
		req.Email,
		req.FullName,
		string(passwordHash),
		role,
	)
	if err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			writeError(w, http.StatusConflict, "username or email already exists")
			return
		}

		writeError(w, http.StatusInternalServerError, "failed to create user")
		return
	}

	if !h.startSession(w, r, user) {
		return
	}

	writeJSON(w, http.StatusCreated, user)
}

// Login проверяет учётные данные и создаёт сессию.
//
// Ограничения здесь двухуровневые: middleware по IP (main.go) отсекает флуд,
// а бакет по идентификатору считает именно неудачные проверки пароля — чтобы
// перебор одного аккаунта с разных адресов тоже упирался в лимит.
func (h *Handlers) Login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}

	var req LoginRequest
	if !readJSON(w, r, &req) {
		return
	}

	req.Identifier = strings.ToLower(strings.TrimSpace(req.Identifier))

	if req.Identifier == "" || req.Password == "" {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	accountKey := "login:" + req.Identifier
	if remaining := h.Limits.LoginAccount.Tokens(accountKey); remaining < 1 {
		h.tooManyAttempts(w, r, accountKey)
		return
	}

	userWithSecret, err := h.Users.GetByIdentifier(r.Context(), req.Identifier)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "failed to login")
		return
	}

	// Сравниваем с bcrypt всегда: если пользователя нет, сравниваем с фиктивным
	// хэшем. Иначе ответ «нет такого логина» приходит быстрее, и по таймингу
	// можно перебрать существующие учётные записи.
	hash := dummyPasswordHash
	if userWithSecret != nil {
		hash = []byte(userWithSecret.PasswordHash)
	}

	// Неудача всегда отвечает 401 «invalid credentials»: сообщать «вы только что
	// заблокировали аккаунт» смысла нет, а вот следующая попытка упрётся в
	// проверку бакета выше и получит 429.
	if err := bcrypt.CompareHashAndPassword(hash, []byte(req.Password)); err != nil || userWithSecret == nil {
		h.Limits.LoginAccount.Consume(accountKey, 1)
		requestLogger(r).Warn("auth: неудачный вход",
			slog.String("identifier", redactKey(req.Identifier)),
			slog.String("ip", ClientIP(r)),
		)
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	h.Limits.LoginAccount.Reset(accountKey)

	if !h.startSession(w, r, &userWithSecret.User) {
		return
	}

	requestLogger(r).Info("auth: вход выполнен",
		slog.Int64("user_id", userWithSecret.User.ID),
		slog.String("ip", ClientIP(r)),
	)
	writeJSON(w, http.StatusOK, userWithSecret.User)
}

// tooManyAttempts — ответ «слишком много неудачных попыток» с Retry-After.
func (h *Handlers) tooManyAttempts(w http.ResponseWriter, r *http.Request, accountKey string) {
	retry := h.Limits.LoginAccount.RetryAfter(accountKey)
	if retry < time.Second {
		retry = time.Second
	}
	w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())))
	writeError(w, http.StatusTooManyRequests, "too many failed attempts, try again later")
	requestLogger(r).Warn("auth: перебор по идентификатору заблокирован",
		slog.String("identifier", redactKey(accountKey)),
		slog.String("retry_after", retry.String()),
	)
}

// Logout удаляет текущую сессию.
func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}

	if sessionID := sessionIDFromRequest(r); sessionID != "" {
		_ = h.Sessions.Delete(r.Context(), sessionID)
	}

	clearSessionCookie(w, r)

	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
	})
}

// LogoutAll отзывает все сессии пользователя, включая текущую.
//
// Смысл в компрометации: «выйти везде» чистит чужие устройства, когда пароль
// уже мог утечь. Кука снимается и у этого запроса, поэтому клиент после
// ответа обязан уйти на страницу входа.
func (h *Handlers) LogoutAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}

	user := h.currentUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if err := h.Sessions.DeleteAllForUser(r.Context(), user.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke sessions")
		return
	}

	// Отдельно удалять текущую сессию не нужно: DeleteAllForUser забирает и её.
	clearSessionCookie(w, r)
	clearCSRFCookie(w, r)

	requestLogger(r).Info("auth: вышли все сессии",
		slog.Int64("user_id", user.ID),
		slog.String("ip", ClientIP(r)),
	)

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ChangePassword меняет пароль по текущему паролю и отзывает остальные сессии.
//
// Текущая сессия сохраняется: пользователя не выбрасывает из вкладки, где он
// только что ввёл новый пароль. Остальные устройства выходят — именно так
// выглядит «смена пароля спасла аккаунт».
func (h *Handlers) ChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}

	user := h.currentUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req ChangePasswordRequest
	if !readJSON(w, r, &req) {
		return
	}

	if utf8.RuneCountInString(req.NewPassword) < minPasswordLength {
		writeError(w, http.StatusUnprocessableEntity, "password must be at least 8 characters")
		return
	}
	if utf8.RuneCountInString(req.NewPassword) > maxPasswordLength {
		writeError(w, http.StatusUnprocessableEntity, "password is too long")
		return
	}

	withSecret, err := h.Users.GetWithSecretByID(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load user")
		return
	}

	// Старый пароль проверяется обязательно: без этого любой XSS или оставленная
	// на чужом ноутбуке вкладка получила бы право менять пароль произвольно.
	if err := bcrypt.CompareHashAndPassword([]byte(withSecret.PasswordHash), []byte(req.CurrentPassword)); err != nil {
		requestLogger(r).Warn("auth: неверный текущий пароль при смене",
			slog.Int64("user_id", user.ID),
			slog.String("ip", ClientIP(r)),
		)
		writeError(w, http.StatusUnauthorized, "current password is incorrect")
		return
	}

	if req.CurrentPassword == req.NewPassword {
		writeError(w, http.StatusUnprocessableEntity, "new password must differ from the current one")
		return
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to hash password")
		return
	}

	if err := h.Users.SetPassword(r.Context(), user.ID, string(passwordHash)); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update password")
		return
	}

	revoked := int64(0)
	current := sessionIDFromRequest(r)

	if current != "" {
		revoked, err = h.Sessions.DeleteAllForUserExcept(r.Context(), user.ID, current)
	} else {
		// Сессии в куке нет (например, протухла) — отзываем всё, что есть.
		err = h.Sessions.DeleteAllForUser(r.Context(), user.ID)
	}

	if err != nil {
		// Пароль уже новый — молчать нельзя: это ровно тот случай, когда
		// пользователь ждёт, что чужие устройства вышли.
		writeError(w, http.StatusInternalServerError, "password changed but other sessions could not be revoked")
		return
	}

	h.Limits.Password.Reset(ClientIP(r))
	requestLogger(r).Info("auth: пароль изменён",
		slog.Int64("user_id", user.ID),
		slog.Int64("revoked_sessions", revoked),
		slog.String("ip", ClientIP(r)),
	)

	writeJSON(w, http.StatusOK, map[string]any{
		"status":           "ok",
		"revoked_sessions": revoked,
	})
}

// Me возвращает текущего пользователя.
func (h *Handlers) Me(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}

	user := h.currentUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	writeJSON(w, http.StatusOK, user)
}

func (h *Handlers) startSession(w http.ResponseWriter, r *http.Request, user *models.User) bool {
	sessionID, err := h.Sessions.Create(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create session")
		return false
	}

	setSessionCookie(w, r, sessionID)

	// CSRF-токен выдаём сразу вместе с сессией. Раньше это делал только
	// RequireAuth, поэтому после register/login первый же POST возвращал
	// 403 «csrf token missing», пока клиент не сходит на GET /api/auth/me.
	ensureCSRFCookie(w, r)
	return true
}

// setSessionCookie выпускает сессионную куку под именем, которое соответствует
// протоколу: на HTTPS это __Host-gs_session, на локальном HTTP — обычное имя,
// потому что браузер префикс __Host- без Secure просто выбросил бы.
func setSessionCookie(w http.ResponseWriter, r *http.Request, sessionID string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieNameFor(r),
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(store.SessionLifetime.Seconds()),
	})
}

// clearSessionCookie снимает оба имени. Одно из них заведомо лишнее, но
// «угадать», какое именно лежит в браузере, мы не можем: пользователь мог
// застать переход с http на https, и тогда обе куки живут рядом.
func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	for _, name := range sessionCookieNames() {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			Secure:   isSecureRequest(r),
			SameSite: http.SameSiteLaxMode,
			MaxAge:   -1,
		})
	}
}

func isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}

	// X-Forwarded-Proto — заголовок, который пишет прокси. Без явного доверия
	// ему любой клиент может подставить «https» и получить куку с Secure,
	// или наоборот: приложение за plain HTTP начнёт выдавать незащищённую
	// gs_session. Решение — тот же переключатель TRUST_PROXY, что и для IP.
	if trustProxy && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return true
	}

	return false
}
