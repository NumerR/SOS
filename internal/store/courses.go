package store

import (
	"context"
	"database/sql"
	"strings"

	"github.com/NumerR/SOS/internal/models"
)

// CourseStore работает с таблицей courses.
type CourseStore struct {
	db *sql.DB
}

// NewCourseStore создаёт хранилище курсов.
func NewCourseStore(db *sql.DB) *CourseStore {
	return &CourseStore{db: db}
}

// List возвращает все курсы.
func (s *CourseStore) List(ctx context.Context) ([]models.Course, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`
		SELECT id, title, description, teacher, created_by
		FROM courses
		ORDER BY id DESC
		`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	courses := make([]models.Course, 0)

	for rows.Next() {
		var course models.Course

		err := rows.Scan(
			&course.ID,
			&course.Title,
			&course.Description,
			&course.Teacher,
			&course.CreatedBy,
		)
		if err != nil {
			return nil, err
		}

		courses = append(courses, course)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return courses, nil
}

// Create создаёт новый курс.
func (s *CourseStore) Create(
	ctx context.Context,
	input models.CreateCourseInput,
	createdBy int64,
) (*models.Course, error) {
	title := strings.TrimSpace(input.Title)
	description := strings.TrimSpace(input.Description)
	teacher := strings.TrimSpace(input.Teacher)

	result, err := s.db.ExecContext(
		ctx,
		`
		INSERT INTO courses (
			title,
			description,
			teacher,
			created_by
		) VALUES (?, ?, ?, ?)
		`,
		title,
		description,
		teacher,
		createdBy,
	)
	if err != nil {
		return nil, err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}

	return &models.Course{
		ID:          id,
		Title:       title,
		Description: description,
		Teacher:     teacher,
		CreatedBy:   createdBy,
	}, nil
}
