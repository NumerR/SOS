package platform

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// migrationsTable — журнал применённых миграций модулей.
//
// Отдельная таблица (а не колонка в схеме модуля) нужна затем, чтобы модуль не
// мог «забыть» собственную миграцию: факт применения фиксирует ядро, и при
// следующем старте runner видит, что делать нечего.
const migrationsTable = "platform_module_migrations"

const createMigrationsTable = `
CREATE TABLE IF NOT EXISTS platform_module_migrations (
    module     TEXT    NOT NULL,
    version    TEXT    NOT NULL,
    applied_at INTEGER NOT NULL,
    PRIMARY KEY (module, version)
);
`

// ApplyMigrations применяет миграции модулей, которых ещё нет в журнале.
//
// Порядок: модули идут в том же порядке, что лежат в списке cmd/server,
// внутри модуля — в порядке объявления. Порядок важен, потому что миграция
// модуля может ссылаться на таблицу другого модуля (например, внешний ключ на
// users), а перестановка строк в списке не должна менять смысл.
//
// Каждая миграция — отдельная транзакция: SQL не применился — в журнал ничего не
// записано, и следующий старт повторит попытку, а не продолжит с половиной
// схемы.
//
// Ограничение, которое нужно помнить при переезде на PostgreSQL (T2.1):
// плейсхолдеры здесь «?» (диалект SQLite), а текст CREATE TABLE — тоже.
// Таблицы модулей пока одна (platform_module_migrations), поэтому замена
// упирается в этот файл и в SQL модулей, а не в разбросанный по коду синтаксис.
func ApplyMigrations(ctx context.Context, db *sql.DB, modules []Module) error {
	if db == nil {
		return errors.New("platform: nil database")
	}

	if err := validateModules(modules); err != nil {
		return err
	}

	if _, err := db.ExecContext(ctx, createMigrationsTable); err != nil {
		return fmt.Errorf("platform: не удалось создать журнал миграций: %w", err)
	}

	for _, module := range modules {
		for _, migration := range module.Migrations() {
			applied, err := migrationApplied(ctx, db, module.Name(), migration.Version)
			if err != nil {
				return err
			}
			if applied {
				continue
			}

			if err := applyMigration(ctx, db, module.Name(), migration); err != nil {
				return fmt.Errorf("platform: модуль %q, миграция %q: %w", module.Name(), migration.Version, err)
			}
		}
	}

	return nil
}

// validateModules проверяет декларацию модулей до первого обращения к базе.
//
// Имя модуля — часть первичного ключа журнала, поэтому два модуля с одним Name()
// читали бы миграции друг друга. Дубликат версии внутри модуля — второй способ
// потерять SQL: вторая миграция с тем же номером считается уже применённой.
func validateModules(modules []Module) error {
	names := make(map[string]bool, len(modules))

	for _, module := range modules {
		if module == nil {
			return errors.New("platform: в списке модулей есть nil")
		}

		name := strings.TrimSpace(module.Name())
		if name == "" {
			return errors.New("platform: модуль вернул пустое Name()")
		}
		if names[name] {
			return fmt.Errorf("platform: модуль %q объявлен дважды", name)
		}
		names[name] = true

		versions := make(map[string]bool, len(module.Migrations()))
		for _, migration := range module.Migrations() {
			version := strings.TrimSpace(migration.Version)
			if version == "" {
				return fmt.Errorf("platform: модуль %q: у миграции пустой Version", name)
			}
			if versions[version] {
				return fmt.Errorf("platform: модуль %q: версия %q встречается дважды", name, version)
			}
			versions[version] = true

			if strings.TrimSpace(migration.SQL) == "" {
				return fmt.Errorf("platform: модуль %q: миграция %q пустая", name, version)
			}
		}
	}

	return nil
}

func migrationApplied(ctx context.Context, db *sql.DB, module, version string) (bool, error) {
	var count int

	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM platform_module_migrations WHERE module = ? AND version = ?;`,
		module, version,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("platform: не удалось прочитать журнал миграций: %w", err)
	}

	return count > 0, nil
}

func applyMigration(ctx context.Context, db *sql.DB, module string, migration Migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("начать транзакцию: %w", err)
	}
	defer func() {
		// Если до Commit не дошло, транзакцию надо закрыть явно: иначе
		// соединение (а у SQLite их всего одно) останется занятым до GC.
		_ = tx.Rollback()
	}()

	if _, err := tx.ExecContext(ctx, migration.SQL); err != nil {
		return fmt.Errorf("применить SQL: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO platform_module_migrations (module, version, applied_at) VALUES (?, ?, strftime('%s', 'now'));`,
		module, migration.Version,
	); err != nil {
		return fmt.Errorf("записать в журнал: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("зафиксировать: %w", err)
	}

	return nil
}
