package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/NumerR/SOS/internal/models"
)

type SubmissionStore struct {
	db *sql.DB
}

func NewSubmissionStore(db *sql.DB) *SubmissionStore {
	return &SubmissionStore{db: db}
}

// Upsert creates or updates a submission for a user on an assignment.
func (s *SubmissionStore) Upsert(ctx context.Context, userID, assignmentID int64, content string) (*models.Submission, error) {
	now := time.Now().Unix()

	// Try to update existing first
	res, err := s.db.ExecContext(
		ctx,
		`UPDATE submissions SET content = ?, submitted_at = ?, score = -1, comment = '', graded_at = 0 WHERE user_id = ? AND assignment_id = ?`,
		content, now, userID, assignmentID,
	)
	if err != nil {
		return nil, err
	}

	affected, _ := res.RowsAffected()
	if affected > 0 {
		return s.GetByUserAndAssignment(ctx, userID, assignmentID)
	}

	// Insert new
	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO submissions (assignment_id, user_id, content, submitted_at, score) VALUES (?, ?, ?, ?, -1)`,
		assignmentID, userID, content, now,
	)
	if err != nil {
		return nil, err
	}

	return s.GetByUserAndAssignment(ctx, userID, assignmentID)
}

// GetByUserAndAssignment fetches a specific submission.
func (s *SubmissionStore) GetByUserAndAssignment(ctx context.Context, userID, assignmentID int64) (*models.Submission, error) {
	var sub models.Submission
	err := s.db.QueryRowContext(
		ctx,
		`SELECT id, assignment_id, user_id, content, score, comment, submitted_at, graded_at FROM submissions WHERE user_id = ? AND assignment_id = ?`,
		userID, assignmentID,
	).Scan(&sub.ID, &sub.AssignmentID, &sub.UserID, &sub.Content, &sub.Score, &sub.Comment, &sub.SubmittedAt, &sub.GradedAt)

	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &sub, nil
}

// ListByAssignment returns all submissions for an assignment, joined with user info.
func (s *SubmissionStore) ListByAssignment(ctx context.Context, assignmentID int64) ([]models.Submission, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`
		SELECT 
			sub.id, sub.assignment_id, sub.user_id, sub.content, sub.score, sub.comment, sub.submitted_at, sub.graded_at,
			u.full_name, u.username
		FROM submissions sub
		JOIN users u ON u.id = sub.user_id
		WHERE sub.assignment_id = ?
		ORDER BY sub.submitted_at ASC
		`,
		assignmentID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	subs := make([]models.Submission, 0)
	for rows.Next() {
		var sub models.Submission
		if err := rows.Scan(
			&sub.ID, &sub.AssignmentID, &sub.UserID, &sub.Content, &sub.Score, &sub.Comment, &sub.SubmittedAt, &sub.GradedAt,
			&sub.UserName, &sub.UserUsername,
		); err != nil {
			return nil, err
		}
		subs = append(subs, sub)
	}
	return subs, rows.Err()
}

// ListMine returns all submissions by a specific user.
func (s *SubmissionStore) ListMine(ctx context.Context, userID int64) ([]models.Submission, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`
		SELECT 
			sub.id, sub.assignment_id, sub.user_id, sub.content, sub.score, sub.comment, sub.submitted_at, sub.graded_at,
			a.title
		FROM submissions sub
		JOIN assignments a ON a.id = sub.assignment_id
		WHERE sub.user_id = ?
		ORDER BY sub.submitted_at DESC
		`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	subs := make([]models.Submission, 0)
	for rows.Next() {
		var sub models.Submission
		if err := rows.Scan(
			&sub.ID, &sub.AssignmentID, &sub.UserID, &sub.Content, &sub.Score, &sub.Comment, &sub.SubmittedAt, &sub.GradedAt,
			&sub.AssignTitle,
		); err != nil {
			return nil, err
		}
		subs = append(subs, sub)
	}
	return subs, rows.Err()
}

// Grade updates the score and comment of a submission.
func (s *SubmissionStore) Grade(ctx context.Context, submissionID int64, score float64, comment string) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(
		ctx,
		`UPDATE submissions SET score = ?, comment = ?, graded_at = ? WHERE id = ?`,
		score, comment, now, submissionID,
	)
	return err
}
