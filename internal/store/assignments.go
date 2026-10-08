package store

import (
	"context"
	"database/sql"
	"strings"

	"github.com/NumerR/SOS/internal/models"
)

type AssignmentStore struct {
	db *sql.DB
}

func NewAssignmentStore(db *sql.DB) *AssignmentStore {
	return &AssignmentStore{db: db}
}

// ListByCourse returns all assignments for a specific course.
func (s *AssignmentStore) ListByCourse(ctx context.Context, courseID int64) ([]models.Assignment, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, course_id, title, description, due_date, created_by FROM assignments WHERE course_id = ? ORDER BY id DESC`,
		courseID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	assignments := make([]models.Assignment, 0)
	for rows.Next() {
		var a models.Assignment
		if err := rows.Scan(&a.ID, &a.CourseID, &a.Title, &a.Description, &a.DueDate, &a.CreatedBy); err != nil {
			return nil, err
		}
		assignments = append(assignments, a)
	}
	return assignments, rows.Err()
}

// GetByID returns a single assignment.
func (s *AssignmentStore) GetByID(ctx context.Context, id int64) (*models.Assignment, error) {
	var a models.Assignment
	err := s.db.QueryRowContext(
		ctx,
		`SELECT id, course_id, title, description, due_date, created_by FROM assignments WHERE id = ?`,
		id,
	).Scan(&a.ID, &a.CourseID, &a.Title, &a.Description, &a.DueDate, &a.CreatedBy)

	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// Create adds a new assignment.
func (s *AssignmentStore) Create(ctx context.Context, input models.CreateAssignmentInput, createdBy int64) (*models.Assignment, error) {
	title := strings.TrimSpace(input.Title)
	desc := strings.TrimSpace(input.Description)

	res, err := s.db.ExecContext(
		ctx,
		`INSERT INTO assignments (course_id, title, description, due_date, created_by) VALUES (?, ?, ?, ?, ?)`,
		input.CourseID, title, desc, input.DueDate, createdBy,
	)
	if err != nil {
		return nil, err
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}

	return &models.Assignment{
		ID:          id,
		CourseID:    input.CourseID,
		Title:       title,
		Description: desc,
		DueDate:     input.DueDate,
		CreatedBy:   createdBy,
	}, nil
}
