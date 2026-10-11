package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/NumerR/SOS/internal/models"
)

// ResourceStore работает с таблицей resources.
type ResourceStore struct {
	db *sql.DB
}

// NewResourceStore создаёт хранилище ресурсов.
func NewResourceStore(db *sql.DB) *ResourceStore {
	return &ResourceStore{db: db}
}

// List возвращает все ресурсы (глобальная витрина, видят все авторизованные).
func (s *ResourceStore) List(ctx context.Context) ([]models.Resource, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`
		SELECT id, title, category, url, description, created_by, created_at
		FROM resources
		ORDER BY id DESC
		`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	resources := make([]models.Resource, 0)
	for rows.Next() {
		var r models.Resource
		if err := rows.Scan(
			&r.ID, &r.Title, &r.Category, &r.URL, &r.Description, &r.CreatedBy, &r.CreatedAt,
		); err != nil {
			return nil, err
		}
		resources = append(resources, r)
	}
	return resources, rows.Err()
}

// Create добавляет ресурс.
func (s *ResourceStore) Create(ctx context.Context, input models.CreateResourceInput, createdBy int64) (*models.Resource, error) {
	title := strings.TrimSpace(input.Title)
	category := strings.TrimSpace(input.Category)
	url := strings.TrimSpace(input.URL)
	description := strings.TrimSpace(input.Description)
	now := time.Now().Unix()

	res, err := s.db.ExecContext(
		ctx,
		`INSERT INTO resources (title, category, url, description, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		title, category, url, description, createdBy, now,
	)
	if err != nil {
		return nil, err
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}

	return &models.Resource{
		ID:          id,
		Title:       title,
		Category:    category,
		URL:         url,
		Description: description,
		CreatedBy:   createdBy,
		CreatedAt:   now,
	}, nil
}
