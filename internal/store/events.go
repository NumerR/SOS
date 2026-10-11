package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/NumerR/SOS/internal/models"
)

// EventStore работает с таблицей events.
type EventStore struct {
	db *sql.DB
}

// NewEventStore создаёт хранилище событий.
func NewEventStore(db *sql.DB) *EventStore {
	return &EventStore{db: db}
}

// List возвращает все события (глобальный инфоповод).
func (s *EventStore) List(ctx context.Context) ([]models.Event, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`
		SELECT id, title, description, event_date, created_by, created_at
		FROM events
		ORDER BY event_date ASC, id DESC
		`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]models.Event, 0)
	for rows.Next() {
		var e models.Event
		if err := rows.Scan(
			&e.ID, &e.Title, &e.Description, &e.EventDate, &e.CreatedBy, &e.CreatedAt,
		); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// ListInRange возвращает события в диапазоне [from, to] (unix, включительно).
// Используется лентой расписания.
func (s *EventStore) ListInRange(ctx context.Context, from, to int64) ([]models.Event, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`
		SELECT id, title, description, event_date, created_by, created_at
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

	events := make([]models.Event, 0)
	for rows.Next() {
		var e models.Event
		if err := rows.Scan(
			&e.ID, &e.Title, &e.Description, &e.EventDate, &e.CreatedBy, &e.CreatedAt,
		); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// Create добавляет событие.
func (s *EventStore) Create(ctx context.Context, input models.CreateEventInput, createdBy int64) (*models.Event, error) {
	title := strings.TrimSpace(input.Title)
	description := strings.TrimSpace(input.Description)
	now := time.Now().Unix()

	res, err := s.db.ExecContext(
		ctx,
		`INSERT INTO events (title, description, event_date, created_by, created_at) VALUES (?, ?, ?, ?, ?)`,
		title, description, input.EventDate, createdBy, now,
	)
	if err != nil {
		return nil, err
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}

	return &models.Event{
		ID:          id,
		Title:       title,
		Description: description,
		EventDate:   input.EventDate,
		CreatedBy:   createdBy,
		CreatedAt:   now,
	}, nil
}
