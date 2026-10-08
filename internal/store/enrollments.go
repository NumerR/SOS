package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/NumerR/SOS/internal/models"
)

// EnrollmentStore работает с таблицей enrollments и связанными join-запросами.
type EnrollmentStore struct {
	db *sql.DB
}

// NewEnrollmentStore создаёт хранилище записей на курсы.
func NewEnrollmentStore(db *sql.DB) *EnrollmentStore {
	return &EnrollmentStore{db: db}
}

// Enroll записывает пользователя на курс.
// Возвращает true, если запись создана, и false, если пользователь уже был записан.
// Если курса не существует — ErrCourseNotFound.
func (s *EnrollmentStore) Enroll(ctx context.Context, userID, courseID int64) (bool, error) {
	var owner int64
	err := s.db.QueryRowContext(ctx, `SELECT created_by FROM courses WHERE id = ?`, courseID).Scan(&owner)
	if err == sql.ErrNoRows {
		return false, ErrCourseNotFound
	}
	if err != nil {
		return false, err
	}

	res, err := s.db.ExecContext(
		ctx,
		`INSERT OR IGNORE INTO enrollments (user_id, course_id, created_at) VALUES (?, ?, ?)`,
		userID,
		courseID,
		time.Now().Unix(),
	)
	if err != nil {
		return false, err
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}

	return affected > 0, nil
}

// Cancel отписывает пользователя от курса. Идемпотентно: если записи не было — не ошибка.
func (s *EnrollmentStore) Cancel(ctx context.Context, userID, courseID int64) error {
	_, err := s.db.ExecContext(
		ctx,
		`DELETE FROM enrollments WHERE user_id = ? AND course_id = ?`,
		userID,
		courseID,
	)
	return err
}

// ListCoursesByUser возвращает курсы, на которые записан пользователь.
func (s *EnrollmentStore) ListCoursesByUser(ctx context.Context, userID int64) ([]models.Course, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`
		SELECT c.id, c.title, c.description, c.teacher, c.created_by
		FROM enrollments e
		JOIN courses c ON c.id = e.course_id
		WHERE e.user_id = ?
		ORDER BY e.created_at DESC
		`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	courses := make([]models.Course, 0)
	for rows.Next() {
		var c models.Course
		if err := rows.Scan(&c.ID, &c.Title, &c.Description, &c.Teacher, &c.CreatedBy); err != nil {
			return nil, err
		}
		courses = append(courses, c)
	}
	return courses, rows.Err()
}

// ListUsersByCourse возвращает пользователей, записанных на курс (без password_hash).
func (s *EnrollmentStore) ListUsersByCourse(ctx context.Context, courseID int64) ([]models.User, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`
		SELECT u.id, u.username, u.email, u.full_name, u.role
		FROM enrollments e
		JOIN users u ON u.id = e.user_id
		WHERE e.course_id = ?
		ORDER BY u.username ASC
		`,
		courseID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	users := make([]models.User, 0)
	for rows.Next() {
		var u models.User
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.FullName, &u.Role); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// CourseOwner возвращает id создателя курса и флаг существования.
func (s *EnrollmentStore) CourseOwner(ctx context.Context, courseID int64) (int64, bool, error) {
	var owner int64
	err := s.db.QueryRowContext(ctx, `SELECT created_by FROM courses WHERE id = ?`, courseID).Scan(&owner)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return owner, true, nil
}
