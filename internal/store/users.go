package store

import (
	"context"
	"database/sql"
	"strings"

	"github.com/NumerR/SOS/internal/models"
)

// UserStore работает с таблицей users.
type UserStore struct {
	db *sql.DB
}

// NewUserStore создаёт хранилище пользователей.
func NewUserStore(db *sql.DB) *UserStore {
	return &UserStore{db: db}
}

// Create создаёт нового пользователя.
func (s *UserStore) Create(
	ctx context.Context,
	username string,
	email string,
	fullName string,
	passwordHash string,
	role string,
) (*models.User, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	email = strings.ToLower(strings.TrimSpace(email))
	fullName = strings.TrimSpace(fullName)

	if role == "" {
		role = "student"
	}

	result, err := s.db.ExecContext(
		ctx,
		`
		INSERT INTO users (
			username,
			email,
			full_name,
			password_hash,
			role
		) VALUES (?, ?, ?, ?, ?)
		`,
		username,
		email,
		fullName,
		passwordHash,
		role,
	)
	if err != nil {
		if isUniqueConstraintError(err) {
			return nil, ErrDuplicate
		}
		return nil, err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}

	return &models.User{
		ID:       id,
		Username: username,
		Email:    email,
		FullName: fullName,
		Role:     role,
	}, nil
}

// Ensure идемпотентно создаёт пользователя, если его ещё нет
// (по username или email). Возвращает пользователя и флаг created.
// Используется для сида администратора при старте.
func (s *UserStore) Ensure(
	ctx context.Context,
	username string,
	email string,
	fullName string,
	passwordHash string,
	role string,
) (*models.User, bool, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	email = strings.ToLower(strings.TrimSpace(email))

	existing, err := s.getByUsernameOrEmail(ctx, username, email)
	if err == nil {
		return existing, false, nil
	}
	if !strings.Contains(err.Error(), ErrNotFound.Error()) {
		return nil, false, err
	}

	created, err := s.Create(ctx, username, email, fullName, passwordHash, role)
	if err != nil {
		return nil, false, err
	}

	return created, true, nil
}

// GetByID возвращает пользователя по ID.
func (s *UserStore) GetByID(ctx context.Context, id int64) (*models.User, error) {
	var user models.User

	err := s.db.QueryRowContext(
		ctx,
		`
		SELECT id, username, email, full_name, role
		FROM users
		WHERE id = ?
		`,
		id,
	).Scan(
		&user.ID,
		&user.Username,
		&user.Email,
		&user.FullName,
		&user.Role,
	)

	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	return &user, nil
}

// List возвращает всех пользователей (для админ-панели).
func (s *UserStore) List(ctx context.Context) ([]models.User, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`
		SELECT id, username, email, full_name, role
		FROM users
		ORDER BY id ASC
		`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	users := make([]models.User, 0)

	for rows.Next() {
		var user models.User

		err := rows.Scan(
			&user.ID,
			&user.Username,
			&user.Email,
			&user.FullName,
			&user.Role,
		)
		if err != nil {
			return nil, err
		}

		users = append(users, user)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return users, nil
}

// GetByIdentifier ищет пользователя по username или email.
func (s *UserStore) GetByIdentifier(ctx context.Context, identifier string) (*models.UserWithSecret, error) {
	identifier = strings.ToLower(strings.TrimSpace(identifier))
	if identifier == "" {
		return nil, ErrNotFound
	}

	var user models.User
	var passwordHash string

	err := s.db.QueryRowContext(
		ctx,
		`
		SELECT id, username, email, full_name, role, password_hash
		FROM users
		WHERE username = ? OR email = ?
		LIMIT 1
		`,
		identifier,
		identifier,
	).Scan(
		&user.ID,
		&user.Username,
		&user.Email,
		&user.FullName,
		&user.Role,
		&passwordHash,
	)

	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	return &models.UserWithSecret{
		User:         user,
		PasswordHash: passwordHash,
	}, nil
}

// getByUsernameOrEmail — внутренний помощник без password_hash.
func (s *UserStore) getByUsernameOrEmail(ctx context.Context, username, email string) (*models.User, error) {
	var user models.User

	err := s.db.QueryRowContext(
		ctx,
		`
		SELECT id, username, email, full_name, role
		FROM users
		WHERE username = ? OR email = ?
		LIMIT 1
		`,
		username,
		email,
	).Scan(
		&user.ID,
		&user.Username,
		&user.Email,
		&user.FullName,
		&user.Role,
	)

	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	return &user, nil
}

func isUniqueConstraintError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
