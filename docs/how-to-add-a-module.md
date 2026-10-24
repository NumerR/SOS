# Как добавить модуль

Инструкция для тех, кто присоединился к проекту после T1.1. Она описывает, как
подключить новую возможность платформы, не трогая ядро: `internal/platform`,
`internal/authz` и `cmd/server/modules.go` при правильном модуле остаются
нетронутыми.

Рабочий пример, на который ссылается этот документ, исполняется в тестах:
`notesModule` в `cmd/server/modules_test.go` и `notesModule` в
`internal/platform/platform_test.go`. Если инструкция разойдётся с кодом,
тесты упадут — поэтому документ не может тихо устареть.

## Что такое модуль

Модуль — это пакет, который отвечает на три вопроса:

```go
type Module interface {
    Name() string                  // уникальное имя: "schedule", "notes"
    Migrations() []Migration       // своя схема (может быть nil)
    Register(reg *Registry)        // свои маршруты и права на них
}
```

Всё остальное ядро делает само: применяет миграции, строит маршрутизатор,
ставит обёртки входа, CSRF и проверки права, логирует запросы, ограничивает
таймаутами.

Существующие модули (`internal/modules/*`): `accounts`, `courses`,
`assignments`, `resources`, `events`, `schedule`.

## Шаг 1. Пакет модуля

`internal/modules/notes/module.go`:

```go
// Package notes — модуль заметок.
package notes

import (
    "net/http"

    "github.com/NumerR/SOS/internal/authz"
    "github.com/NumerR/SOS/internal/handlers"
    "github.com/NumerR/SOS/internal/platform"
)

type Module struct {
    h *handlers.Handlers
}

func New(h *handlers.Handlers) *Module { return &Module{h: h} }

func (m *Module) Name() string { return "notes" }

func (m *Module) Migrations() []platform.Migration { return nil }

func (m *Module) Register(reg *platform.Registry) {
    h := m.h

    reg.Add(platform.Route{
        Path:   "/api/notes",
        Method: http.MethodGet,
        Handler: http.HandlerFunc(h.ListNotes),
    })
    reg.Add(platform.Route{
        Path:        "/api/notes",
        Method:      http.MethodPost,
        Perm:        authz.NoteCreate,
        DenyMessage: "only teachers can add notes",
        CSRF:        true,
        Handler:     http.HandlerFunc(h.CreateNote),
    })
}
```

Существующие модули держат тела обработчиков в `internal/handlers`, а в пакете
модуля оставляют декларацию. Новый модуль может сделать иначе — положить обработчики
в свой пакет и получать доступ к базе через свой стор; ядру безразлично, кто
отдаёт `http.Handler`. Перенос существующих тел в пакеты модулей — отдельная
задача, она не меняет контракт.

## Шаг 2. Право доступа

Если новому действию нужна роль, а не просто вход, добавьте одну строку в
реестр прав `internal/authz/authz.go`:

```go
const (
    // ...
    NoteCreate Permission = "note.create"
)

var registry = []Spec{
    // ...
    {NoteCreate, "добавление заметки", []Role{RoleTeacher, RoleAdmin}},
}
```

И обновите эталонную матрицу в `internal/authz/authz_test.go` — она написана
руками именно затем, чтобы выдача права была осознанным решением, а
следствием случайной правки.

Правила, которые стоит держать в голове:

- Право, которого нет в реестре, закрыто всем — включая администратора.
  Опечатка в константе не откроет доступ, она его перекроет.
- Маршрутизатор проверяет `authz.ValidPermission` при сборке: несуществующее
  право остановит запуск сервера с внятной ошибкой, а не превратится в вечный
  403.
- Права вида `*.any` («работать с чужими объектами») — это отдельное право, а
  не сравнение `user.Role == "admin"`. Если вашему модулю нужно обходить
  проверку владельца, заведите такое право и спросите его в обработчике.
- Проверка владельца строки («этот курс создан этим человеком») остаётся в
  обработчике: ей нужны данные из базы, а `authz` отвечает только на вопрос
  «можно ли такое действие в принципе».

## Шаг 3. Миграция, если своя таблица

```go
func (m *Module) Migrations() []platform.Migration {
    return []platform.Migration{
        {
            Version: "0001_create_notes",
            SQL: `CREATE TABLE IF NOT EXISTS notes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    author_id INTEGER NOT NULL,
    text TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    FOREIGN KEY(author_id) REFERENCES users(id) ON DELETE CASCADE
);`,
        },
    }
}
```

Что нужно знать про runner:

- Журнал лежит в таблице `platform_module_migrations (module, version,
  applied_at)`. Повторный запуск сервера пропущенные миграции не применяет
  повторно, а уже применённые не трогает.
- Одна миграция = одна транзакция. Не применилась — в журнал ничего не
  попало, следующий старт повторит попытку.
- Версию нельзя переиспользовать: новый SQL обязан получить новый `Version`,
  иначе он будет молча пропущен как «уже применённый».
- `module` — это `Name()`, поэтому два модуля с одинаковым именем не пройдут
  проверку на старте.
- Порядок модулей в `cmd/server/modules.go` = порядок применения. Ссылка
  (внешний ключ) на таблицы другого модуля обязана идти после них.
- Диалект сейчас SQLite (`AUTOINCREMENT`, `strftime`, плейсхолдеры `?`).
  Переезд на PostgreSQL с версионируемыми `.sql`-миграциями — задача T2.1;
  после неё SQL модулей переедет в `migrations/*.sql`, а runner получит
  плейсхолдеры `$n`.

## Шаг 4. Подключить модуль

`cmd/server/modules.go` — единственное место, где модуль появляется:

```go
func moduleList(h *handlers.Handlers) []platform.Module {
    return []platform.Module{
        accounts.New(h),
        courses.New(h),
        assignments.New(h),
        resources.New(h),
        events.New(h),
        schedule.New(h),
        notes.New(h), // ← новая строка
    }
}
```

Больше править не нужно. `/api/notes` появится, миграция применится, право
начнёт проверяться.

## Шаг 5. Проверить

```bash
gofmt -l internal cmd                     # пусто
go vet ./...
go test -race -count=1 ./...              # включая тесты app-уровня
LOG_LEVEL=debug go run ./cmd/server       # в логе — таблица маршрутов и прав
```

`cmd/server/modules_test.go` собирает маршрутизатор из `moduleList(h)`, поэтому
ваш модуль автоматически попадает в интеграционный прогон: права, CSRF, 405 и
404 проверяются на живом `httptest`-сервере. Добавьте своему модулю хотя бы два
случая: «анониму 401» и «роли без права 403».

## Декларация маршрута: поля и ловушки

| Поле | Смысл | Частая ошибка |
|---|---|---|
| `Path` | точный путь (`/api/notes`) или префикс (`/js/`) | путь без `/` — реестр отклонит |
| `Method` | `GET`, `POST`, пустая строка = любой | `get` приведётся к `GET`, но `GET,POST` — ошибка |
| `Perm` | право из `authz` | пустое = «достаточно входа», а не «публично» |
| `Public` | не требовать сессию | вместе с `Perm` — противоречие, старт упадёт |
| `CSRF` | требовать токен | забыть на mutate-маршруте = дыра |
| `DenyMessage` | текст отказа | пустой = `forbidden`, клиенту непонятно |
| `Handler` | тело | `nil` — ошибка сборки |

Порядок обёрток фиксирован: снаружи вход/право, внутри CSRF, затем обработчик.
`RequirePerm` включает в себя проверку сессии, поэтому дублировать
`RequireAuth` в модуле не нужно и нельзя — маршрутизатор это делает сам.

## Что остаётся за кадром

- HTML-страницы и раздача статики не проходят через `Registry`: отказ там
  выглядит как redirect на `/login`, а не как JSON `403`. Они регистрируются в
  `newRouter` напрямую (`internal/handlers/pages.go`), и право для страницы
  проверяется в обработчике через `authz` (`AdminPage` →
  `authz.AdminPanelView`).
- Аудит действий есть с T1.2 (`26066e8`, ADR-0012): таблица `audit_events`
  принадлежит модулю `audit`, а пишет в неё любой обработчик через
  `h.recordAudit(r, auditWrite{...})`. Два свойства этой точки важны при
  добавлении модуля: она nil-safe (`h.Audit == nil` — нет записи, и это не
  ошибка сборки, потому что запись — побочный эффект, а не условие доступа) и
  не меняет ответ запроса (ошибка INSERT уходит в лог `warn`-ом). Имя действия
  добавляется в реестр `internal/store/audit.go`: без него запись всё равно
  ляжет и отрисуется в админке техническим именем, но фильтр чтения её не
  примет — `ValidAuditAction` бережёт путь чтения, а не записи.
  Структурированный лог через `requestLogger(r)` при этом остаётся: это другой
  канал для другого читателя (ADR-0012), а не дубль для исправления.
- Формат ошибок API (`{"error": "текст"}`) менять здесь не нужно: переход на
  `code`/`message` — отдельное согласованное изменение бэкенда, фронтенда и
  тестов (docs/02-ARCHITECTURE.md §4).
