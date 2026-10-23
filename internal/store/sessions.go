package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"time"
)

// SessionLifetime — время жизни сессии.
var SessionLifetime = 24 * time.Hour

// Session — сессия пользователя.
type Session struct {
	ID        string
	UserID    int64
	ExpiresAt int64
}

// SessionStore работает с таблицей sessions.
type SessionStore struct {
	db *sql.DB
}

// NewSessionStore создаёт хранилище сессий.
func NewSessionStore(db *sql.DB) *SessionStore {
	return &SessionStore{db: db}
}

// Create создаёт новую сессию для пользователя.
func (s *SessionStore) Create(ctx context.Context, userID int64) (string, error) {
	sessionID, err := newSessionID()
	if err != nil {
		return "", err
	}

	expiresAt := time.Now().Add(SessionLifetime).Unix()

	_, err = s.db.ExecContext(
		ctx,
		`
		INSERT INTO sessions (id, user_id, expires_at)
		VALUES (?, ?, ?)
		`,
		sessionID,
		userID,
		expiresAt,
	)
	if err != nil {
		return "", err
	}

	return sessionID, nil
}

// Get возвращает активную сессию по ID.
func (s *SessionStore) Get(ctx context.Context, id string) (*Session, error) {
	if id == "" {
		return nil, ErrNotFound
	}

	now := time.Now().Unix()

	var userID int64
	var expiresAt int64

	err := s.db.QueryRowContext(
		ctx,
		`
		SELECT user_id, expires_at
		FROM sessions
		WHERE id = ? AND expires_at > ?
		LIMIT 1
		`,
		id,
		now,
	).Scan(&userID, &expiresAt)

	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	return &Session{
		ID:        id,
		UserID:    userID,
		ExpiresAt: expiresAt,
	}, nil
}

// Delete удаляет сессию.
func (s *SessionStore) Delete(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}

	_, err := s.db.ExecContext(
		ctx,
		`DELETE FROM sessions WHERE id = ?`,
		id,
	)

	return err
}

// DeleteAllForUser удаляет все сессии пользователя: отзыв доступа после смены
// роли, блокировки или по «выйти везде».
func (s *SessionStore) DeleteAllForUser(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(
		ctx,
		`DELETE FROM sessions WHERE user_id = ?`,
		userID,
	)

	return err
}

// DeleteAllForUserExcept удаляет все сессии пользователя, кроме указанной.
// Нужна для смены пароля: текущее устройство остаётся рабочим, остальные
// (в том числе украденная кука) — отзываются.
func (s *SessionStore) DeleteAllForUserExcept(ctx context.Context, userID int64, keepSessionID string) (int64, error) {
	result, err := s.db.ExecContext(
		ctx,
		`DELETE FROM sessions WHERE user_id = ? AND id <> ?`,
		userID,
		keepSessionID,
	)
	if err != nil {
		return 0, err
	}

	return result.RowsAffected()
}

// DeleteExpired удаляет сессии, у которых истёк expires_at, и возвращает
// количество удалённых. Без этой чистки таблицa sessions растёт бесконечно:
// Get фильтрует по сроку, но записи не удаляет никто.
func (s *SessionStore) DeleteExpired(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(
		ctx,
		`DELETE FROM sessions WHERE expires_at <= ?`,
		time.Now().Unix(),
	)
	if err != nil {
		return 0, err
	}

	return result.RowsAffected()
}

// CountActive — число сессий, годных прямо сейчас. Нужен чтобы проверить
// отзыв доступа и вывести это в лог очистки.
func (s *SessionStore) CountActive(ctx context.Context) (int64, error) {
	var count int64

	err := s.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM sessions WHERE expires_at > ?`,
		time.Now().Unix(),
	).Scan(&count)

	return count, err
}

func newSessionID() (string, error) {
	bytes := make([]byte, 32)

	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}

	return hex.EncodeToString(bytes), nil
}
