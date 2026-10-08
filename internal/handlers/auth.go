package handlers

import (
	"errors"
	"net/http"
	"regexp"
	"strings"
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

var (
	usernamePattern = regexp.MustCompile(`^[\p{L}\p{N}_.-]{3,32}$`)
	emailPattern    = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
)

// normalizeRole валидирует роль из публичной регистрации.
// admin сюда никогда не проходит — он только через сид.
func normalizeRole(role string) (string, bool) {
	role = strings.ToLower(strings.TrimSpace(role))
	switch role {
	case "", "student":
		return "student", true
	case "teacher":
		return "teacher", true
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
		writeError(w, http.StatusUnprocessableEntity, "role must be student or teacher")
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

	if utf8.RuneCountInString(req.Password) < 8 {
		writeError(w, http.StatusUnprocessableEntity, "password must be at least 8 characters")
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

	userWithSecret, err := h.Users.GetByIdentifier(r.Context(), req.Identifier)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}

		writeError(w, http.StatusInternalServerError, "failed to login")
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(userWithSecret.PasswordHash), []byte(req.Password)); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	if !h.startSession(w, r, &userWithSecret.User) {
		return
	}

	writeJSON(w, http.StatusOK, userWithSecret.User)
}

// Logout удаляет текущую сессию.
func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}

	cookie, err := r.Cookie(sessionCookieName)
	if err == nil && cookie.Value != "" {
		_ = h.Sessions.Delete(r.Context(), cookie.Value)
	}

	clearSessionCookie(w, r)

	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
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
	return true
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, sessionID string) {
	cookie := &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(store.SessionLifetime.Seconds()),
	}

	http.SetCookie(w, cookie)
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	cookie := &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	}

	http.SetCookie(w, cookie)
}

func isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}

	if strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return true
	}

	return false
}
