package handlers

import (
	"testing"

	"database/sql"

	"github.com/NumerR/SOS/internal/store"
)

// applyMapSchema доводит схему теста до боевого состояния обоих слоёв карты:
// таблица мест плюс правка событий под второй слой.
//
// Почему руками, а не модулями: places и events импортируют этот пакет, и
// тесты обработчиков не могут звать модули — замкнулся бы цикл (то же правило,
// из-за которого текст DDL живёт в store, а заявляет его модуль).
//
// Порядок важен: ALTER добавляет в events ссылку на places, поэтому таблица
// мест к его моменту обязана существовать — как и в живом сервере, где модуль
// places стоит в списке раньше events (cmd/server/modules.go).
func applyMapSchema(t *testing.T, db *sql.DB) {
	t.Helper()

	schema := []struct {
		name string
		ddl  string
	}{
		{"places", store.PlacesMigration},
		{"events для карты", store.EventsMapMigration},
	}

	for _, part := range schema {
		if _, err := db.Exec(part.ddl); err != nil {
			t.Fatalf("схема %s: %v", part.name, err)
		}
	}
}
