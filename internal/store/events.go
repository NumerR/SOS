package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/NumerR/SOS/internal/models"
)

// EventStore работает с таблицей events.
type EventStore struct {
	db *sql.DB
}

// EventsMapMigration — два столбца, без которых слой событий на карте не
// существует: ссылка на справочник мест и категория инфоповода.
//
// Отдельной версией, заявленной модулем events, а не правкой базовой схемы:
// runner сравнивает (module, version) и уже применённую миграцию не повторяет,
// поэтому новая строка внутри CREATE TABLE не появилась бы ни на одной живой
// базе (то же правило, что у 2026-11-02_schedule_map_index).
//
// place_id ссылается на places, а не хранит координаты: у места они одни, и
// вторая копия разъехалась бы с первой — «корпус на карте в двухстах метрах от
// самого себя» (см. комментарий к PlacesMigration). NULL означает «событие ни к
// какому месту не привязано»: это рабочее состояние, а не ошибка, и карта
// обязана показать такое событие числом «не показаны», а не выбросить молча.
//
// Ссылка живёт без действия при удалении, и это не недосмотр: ON DELETE CASCADE
// стёр бы инфоповод вместе со случившейся опечаткой в справочнике, а справочник
// правят руками. Без действия FK просто не даст удалить место, на которое
// событие ссылается, — человек увидит причину в отказе, а не потеряет данные.
//
// Категория — свободная строка, а не закрытый список: рубрик у вуза столько,
// сколько он придумает, и выдумывать их за него означало бы нарушить бриф (п.3
// — никаких вымышленных данных). Пустая строка — «без категории», а не «другая
// категория».
//
// Индекса по place_id нет намеренно: слой карты фильтрует по event_date (для
// него idx_events_event_date уже есть), а место добирает JOIN по первичному
// ключу. Индекс, который никто не спрашивает, только замедляет запись.
const EventsMapMigration = `
ALTER TABLE events ADD COLUMN place_id INTEGER REFERENCES places(id);
ALTER TABLE events ADD COLUMN category TEXT NOT NULL DEFAULT '';
`

// NewEventStore создаёт хранилище событий.
func NewEventStore(db *sql.DB) *EventStore {
	return &EventStore{db: db}
}

// eventColumns — единый набор колонок чтения события. Нужен одной строкой,
// чтобы List, ListInRange и Create не разошлись в составе ответа: у этой таблицы
// columns добавляются миграцией, и опечатка в списке утонула бы в рантайме.
const eventColumns = `id, title, description, event_date, place_id, category, created_by, created_at`

// List возвращает все события (глобальный инфоповод).
func (s *EventStore) List(ctx context.Context) ([]models.Event, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`
		SELECT `+eventColumns+`
		FROM events
		ORDER BY event_date ASC, id DESC
		`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanEvents(rows)
}

// ListInRange возвращает события в диапазоне [from, to] (unix, включительно).
// Используется лентой расписания.
func (s *EventStore) ListInRange(ctx context.Context, from, to int64) ([]models.Event, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`
		SELECT `+eventColumns+`
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

	return scanEvents(rows)
}

func scanEvents(rows *sql.Rows) ([]models.Event, error) {
	events := make([]models.Event, 0)
	for rows.Next() {
		var (
			e       models.Event
			placeID sql.NullInt64
		)
		if err := rows.Scan(
			&e.ID, &e.Title, &e.Description, &e.EventDate,
			&placeID, &e.Category, &e.CreatedBy, &e.CreatedAt,
		); err != nil {
			return nil, err
		}
		if placeID.Valid {
			id := placeID.Int64
			e.PlaceID = &id
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// Create добавляет событие.
func (s *EventStore) Create(ctx context.Context, input models.CreateEventInput, createdBy int64) (*models.Event, error) {
	title := strings.TrimSpace(input.Title)
	description := strings.TrimSpace(input.Description)
	category := strings.TrimSpace(input.Category)
	now := time.Now().Unix()

	placeID := nullableID(input.PlaceID)

	res, err := s.db.ExecContext(
		ctx,
		`INSERT INTO events (title, description, event_date, place_id, category, created_by, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		title, description, input.EventDate, placeID, category, createdBy, now,
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
		PlaceID:     input.PlaceID,
		Category:    category,
		CreatedBy:   createdBy,
		CreatedAt:   now,
	}, nil
}

// nullableID переводит *int64 в параметр: nil обязан стать NULL, иначе
// «место не выбрано» уехало бы в базу как ссылка на несуществующее место 0 —
// ровно та подмена, из-за которой карта не отличает «не выбрали» от «выбрали
// ерунду».
func nullableID(id *int64) any {
	if id == nil {
		return nil
	}
	return *id
}

// MapEvent — событие периода вместе с id выбранного для него места.
//
// Координат здесь нет нарочно: они живут в справочнике, и второй слой,
// скопировавший их себе в строку события, расшёлся бы с первым («корпус на
// карте в двухстах метрах от самого себя»). Обработчик добирает место одним
// чтением отсортированного справочника — см. EventsMapAPI.
type MapEvent struct {
	ID        int64
	Title     string
	Category  string
	EventDate int64
	PlaceID   *int64
}

// MapEvents читает события периода.
//
// Отдельный запрос, а не ListInRange: у слоя карты есть своё требование к
// датам, и в списке оно не должно появляться.
//
// События без даты (event_date = 0) попадают в выборку нарочно: карта не имеет
// права решать, что недатированный инфоповод не существует. В период он встать
// не может, и обработчик честно кладёт его в «не показано» с причиной, — иначе
// пустая карта значила бы «событий нет», чего источник не говорил (ADR-0029).
func (s *EventStore) MapEvents(ctx context.Context, from, to int64) ([]MapEvent, error) {
	if to < from {
		return nil, fmt.Errorf("store: пустой диапазон событий %d..%d", from, to)
	}

	rows, err := s.db.QueryContext(
		ctx,
		`
		SELECT id, title, category, event_date, place_id
		FROM events
		WHERE event_date = 0 OR event_date BETWEEN ? AND ?
		ORDER BY event_date ASC, id ASC
		`,
		from, to,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]MapEvent, 0, 8)

	for rows.Next() {
		var (
			event   MapEvent
			placeID sql.NullInt64
		)
		if err := rows.Scan(
			&event.ID, &event.Title, &event.Category, &event.EventDate, &placeID,
		); err != nil {
			return nil, err
		}
		if placeID.Valid {
			id := placeID.Int64
			event.PlaceID = &id
		}

		events = append(events, event)
	}

	return events, rows.Err()
}
