package store

import (
	"context"
	"database/sql"
	"sort"

	"github.com/NumerR/SOS/internal/models"
)

// ScheduleStore собирает ленту расписания из дедлайнов заданий и событий.
type ScheduleStore struct {
	db *sql.DB
}

// NewScheduleStore создаёт агрегатор ленты.
func NewScheduleStore(db *sql.DB) *ScheduleStore {
	return &ScheduleStore{db: db}
}

// Feed возвращает элементы ленты в диапазоне [from, to] (unix, включительно),
// отсортированные по дате. Дедлайны зависят от роли, события глобальны.
func (s *ScheduleStore) Feed(ctx context.Context, user *models.User, from, to int64) ([]models.ScheduleFeedItem, error) {
	deadlines, err := s.deadlinesFor(ctx, user, from, to)
	if err != nil {
		return nil, err
	}

	events, err := s.eventsFor(ctx, from, to)
	if err != nil {
		return nil, err
	}

	items := append(deadlines, events...)
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].DueDate < items[j].DueDate
	})
	return items, nil
}

func (s *ScheduleStore) eventsFor(ctx context.Context, from, to int64) ([]models.ScheduleFeedItem, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`
		SELECT id, title, description, event_date
		FROM events
		WHERE event_date > 0 AND event_date BETWEEN ? AND ?
		ORDER BY event_date ASC
		`,
		from, to,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]models.ScheduleFeedItem, 0)
	for rows.Next() {
		var it models.ScheduleFeedItem
		if err := rows.Scan(&it.EventID, &it.Title, &it.Description, &it.DueDate); err != nil {
			return nil, err
		}
		it.Kind = "event"
		it.Score = -1
		items = append(items, it)
	}
	return items, rows.Err()
}

func (s *ScheduleStore) deadlinesFor(ctx context.Context, user *models.User, from, to int64) ([]models.ScheduleFeedItem, error) {
	switch user.Role {
	case "student":
		return s.feedForStudent(ctx, user.ID, from, to)
	case "teacher":
		return s.feedForOwner(ctx, user.ID, from, to)
	case "admin":
		return s.feedForAll(ctx, from, to)
	default:
		return []models.ScheduleFeedItem{}, nil
	}
}

func (s *ScheduleStore) feedForStudent(ctx context.Context, userID, from, to int64) ([]models.ScheduleFeedItem, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`
		SELECT
			a.id, a.title, a.description, a.due_date, a.course_id, c.title,
			CASE
				WHEN sub.id IS NULL THEN 'none'
				WHEN sub.score >= 0 THEN 'graded'
				ELSE 'submitted'
			END AS state,
			COALESCE(sub.score, -1) AS score
		FROM assignments a
		JOIN courses c ON c.id = a.course_id
		JOIN enrollments e ON e.course_id = a.course_id AND e.user_id = ?
		LEFT JOIN submissions sub ON sub.assignment_id = a.id AND sub.user_id = ?
		WHERE a.due_date > 0 AND a.due_date BETWEEN ? AND ?
		ORDER BY a.due_date ASC
		`,
		userID, userID, from, to,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFeed(rows)
}

func (s *ScheduleStore) feedForOwner(ctx context.Context, ownerID, from, to int64) ([]models.ScheduleFeedItem, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`
		SELECT
			a.id, a.title, a.description, a.due_date, a.course_id, c.title,
			'own' AS state, -1 AS score
		FROM assignments a
		JOIN courses c ON c.id = a.course_id
		WHERE a.created_by = ? AND a.due_date > 0 AND a.due_date BETWEEN ? AND ?
		ORDER BY a.due_date ASC
		`,
		ownerID, from, to,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFeed(rows)
}

func (s *ScheduleStore) feedForAll(ctx context.Context, from, to int64) ([]models.ScheduleFeedItem, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`
		SELECT
			a.id, a.title, a.description, a.due_date, a.course_id, c.title,
			'own' AS state, -1 AS score
		FROM assignments a
		JOIN courses c ON c.id = a.course_id
		WHERE a.due_date > 0 AND a.due_date BETWEEN ? AND ?
		ORDER BY a.due_date ASC
		`,
		from, to,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFeed(rows)
}

func scanFeed(rows *sql.Rows) ([]models.ScheduleFeedItem, error) {
	items := make([]models.ScheduleFeedItem, 0)
	for rows.Next() {
		var it models.ScheduleFeedItem
		if err := rows.Scan(
			&it.AssignmentID, &it.Title, &it.Description, &it.DueDate,
			&it.CourseID, &it.CourseTitle, &it.State, &it.Score,
		); err != nil {
			return nil, err
		}
		it.Kind = "deadline"
		items = append(items, it)
	}
	return items, rows.Err()
}
