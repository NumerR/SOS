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

func newSessionID() (string, error) {
	bytes := make([]byte, 32)

	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}

	return hex.EncodeToString(bytes), nil
}
