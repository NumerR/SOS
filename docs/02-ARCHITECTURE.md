# 02. Архитектура MVP

Проект решения. Всё, что помечено «⭐», — изменение относительно текущего кода и требует согласования (см. 03-UNKNOWN-AND-DECISIONS.md). Документ фиксирует контракты, чтобы агенты 2-4 не меняли их в одностороннем порядке (бриф, п.4).

## 1. Общая схема

```
Браузер (GitHub Pages ИЛИ статика Go-сервера)
   │  REST/JSON, cookie-сессия + X-CSRF-Token
   ▼
Go-приложение (модульный монолит, net/http)
   ├─ platform: config · db · authn/authz · csrf · ratelimit · logging · router-registry · migrations runner
   ├─ modules/courses   (курсы, записи)
   ├─ modules/assignments (задания, сдачи, оценки)
   ├─ modules/schedule  (записи расписания + адаптеры источников)
   ├─ modules/events    (события + адаптеры источников, дедупликация)
   └─ modules/resources (витрина материалов)
   │
   ├─ PostgreSQL  (основная ⭐)
   └─ импортёры фоновых задач (tick: schedule source, telegram source)
```

Модульный монолит, микросервисов нет. Ядро отвечает за конфигурацию, аутентификацию, регистрацию модулей, общие компоненты и навигацию (бриф, п.3).

## 2. Структура каталогов ⭐

```
cmd/server/main.go            только сборка зависимостей и запуск
internal/platform/            ядро
  config/                     env → типизированный Config
  database/                  открытие пула, runner миграций (embed SQL)
  httpx/                      writeJSON/writeError/readJSON, единый формат ошибок
  authn/                      сессии, RequireAuth, RequireCSRF, logout-all
  authz/                      пермиссии: Can(user, "course.create")
  ratelimit/                  token bucket per key
  logging/                    slog JSON, request-id
  module.go                   interface Module { Name() string; Register(*Registry) }
internal/modules/<name>/      у модуля: handlers.go, service.go, store.go, migrations/*.sql, module.go, *_test.go
migrations/                   не нужен (миграции живут рядом с модулем)
web/                          index.html, schedule.html, events.html, profile.html, …
  css/tokens.css, base.css, components.css, pages/
  js/core/ (api-client.js, csrf.js, auth.js, dom.js, date.js)
  js/pages/ (dashboard.js, schedule.js, events.js, …)
deploy/                       docker-compose.yml, example env, systemd/Procfile
docs/                         этот набор + ADR + «как добавить модуль»
```

`internal/store/*` и `internal/handlers/*` распадаются по модулям; `models.go` раздается на модульные модели. Миграции модуля применяются тем же общим runner'ом, порядок — по имени файла `<модуль>_<0001>.sql`.

## 3. Контракт модуля ⭐

```go
type Module interface {
    Name() string                       // "schedule", "events"
    Migrations() []Migration            // версия + SQL (embed)
    Register(r *Registry)                // маршруты + middleware-флаги
}

type Route struct {
    Path        string
    Methods     []string
    Perm        authz.Permission         // пустой = публичный
    CSRF        bool                     // true для mutate-маршрутов
    Handler     http.Handler
}
```

Явный список модулей в одном месте (`cmd/server/modules.go`), без `plugin`/динамической загрузки (бриф, п.3). Добавление модуля = новый пакет + одна строка в списке. Права проверяются на сервере через `authz`, а не сравнением строки роли в 15 местах.

## 4. Формат ошибок API ⭐ (согласовать до старта Этапа 3)

Сейчас: `{"error": "строка"}` (`internal/handlers/response.go:17-21`). Предлагаем:

```json
{ "error": { "code": "validation", "message": "title is required", "fields": { "title": "empty" } } }
```

Коды: `invalid_json`, `validation`, `unauthorized`, `forbidden`, `not_found`, `conflict`, `rate_limited`, `csrf_failed`, `internal`. Стабильный `code` нужен фронтенду, чтобы различать истечение сессии и ошибку валидации; `message` остаётся человекочитаемым. Мигрируем бэкенд, фронтенд и тесты в одном изменении (бриф, п.4.9).

## 5. Контракты расписания и событий ⭐

`GET /api/schedule/entries?group_id=&date=|from=&to=`

```json
{
  "updated_at": "2026-10-23T07:00:00Z",
  "source": "tksu",
  "range": { "from": "2026-10-19", "to": "2026-10-25" },
  "entries": [
    { "id": 12, "date": "2026-10-20", "start_at": "2026-10-20T09:00:00Z",
      "end_at": "2026-10-20T10:30:00Z", "discipline": "Математический анализ",
      "teacher": "Иванов И. И.", "group": "БПМИ-21", "room": "к. 305",
      "kind": "lec", "source_url": "https://tksu.ru/..." }
  ]
}
```

`GET /api/events?from=&to=&category=&place=&q=` и `GET /api/events/map?bbox=...`

```json
{
  "events": [
    { "id": 3, "title": "День открытых дверей", "description": "…",
      "starts_at": "2026-11-02T10:00:00Z", "ends_at": "2026-11-02T14:00:00Z",
      "location": { "text": "гл. корпус, пл. Ленина 3", "lat": null, "lon": null,
                    "geo_state": "unresolved" },
      "category": "студенчество", "image_url": null,
      "source": { "kind": "telegram", "channel": "…", "url": "https://t.me/…/123" },
      "published_at": "2026-10-20T08:15:00Z" }
  ]
}
```

Правила: дата-время в API — только RFC 3339 в UTC с offset-информацией; `date` (день занятия) — `YYYY-MM-DD` без времени; часовые пояса разрешаются на бэкенде (`TZ_NAME` в конфиге), а не локалью сервера (сейчас `handlers/schedule.go:60-65` использует локальное время процесса — это баг на DST-сдвигах). Координат нет → `geo_state: "unresolved"`, на карте точка не ставится (бриф, п.3: «отсутствие вымышленных координат»).

Дедупликация импорта: `UNIQUE (source_id, external_id)`; при повторной встрече — upsert с обновлением полей и `source_revision`, а не новая строка.

## 6. Схема БД (целевая, PostgreSQL) ⭐

Сохраняем существующее (`users`, `sessions`, `courses`, `enrollments`, `assignments`, `submissions`, `resources`) и добавляем:

```
groups(id, name UNIQUE, course_of_study, note)
user_group_members(user_id FK, group_id FK, joined_at, PRIMARY KEY(user_id, group_id))
user_preferences(user_id PK FK, timezone, week_start, theme, notifications_json)
event_sources(id, kind, identifier, config_json, enabled, last_success_at, last_error)
events(id, title, description, starts_at timestamptz, ends_at, location_text,
       lat numeric(9,6), lon numeric(10,7), geo_state, category, image_url,
       source_id FK, external_id, source_url, published_at, created_at, updated_at,
       UNIQUE(source_id, external_id))
schedule_sources(id, kind, identifier, config_json, enabled, last_success_at, last_error)
schedule_groups(source_id, source_group_key, name)          -- соответствие группе источника
schedule_entries(id, source_id FK, group_id FK, discipline, teacher, room,
                 activity_kind, starts_at, ends_at, source_revision,
                 source_url, UNIQUE(source_id, source_revision, group_id, starts_at, discipline))
import_runs(id, source_id, started_at, finished_at, status, records_created,
             records_updated, error)
audit_events(id, at, actor_user_id, action, target_type, target_id, ip, user_agent, ok bool)
```

Миграции — файлы SQL + таблица `schema_migrations(version text PK, applied_at)`; откат обязателен только для изменений, ломающих чтение. Правила: колонки времени — `timestamptz`, id — `bigserial`, FK — `ON DELETE` явно, чек-ограничения на `role` и `score`.

## 7. Аутентификация и безопасность (целевое состояние)

Пароли — bcrypt (оставляем, аргон2ид не вносим: лишняя зависимость без выигрыша для MVP). Сессии — серверные, `__Host-gs_session` (HttpOnly, Secure, SameSite=Lax — совместимо с раздачей с одного домена). Права — `authz.Can(user, perm)` с реестром пермиссий по модулям.

Обязательные исправления из отчёта 01.

Уже в коде (T0.2, ADR-0006 и ADR-0007): роль `teacher` не выдаётся при
саморегистрации; rate limit на `login`/`register` и счётчик неудач с
постепенной блокировкой; `DeleteAllForUser` / `DeleteAllForUserExcept` при
logout-all, смене пароля и смене роли; фоновая очистка протухших сессий;
верхняя граница длины пароля; `X-Forwarded-Proto` и `X-Forwarded-For`
доверяются только при `TRUST_PROXY=true`.

Добавлено в T0.3 (ADR-0008): `http.Server` с таймаутами чтения заголовка/тела,
записи, идеала и `MaxHeaderBytes` + graceful shutdown по `SIGINT`/`SIGTERM`;
`WithDBDeadline` — `context.WithTimeout` на каждый запрос; структурированный
лог `slog` с `request_id` и защитой от лог-инъекции; лимиты длины всех
текстовых полей и шкалы оценки (`fields.go`, `422` до обращения к БД);
`__Host-gs_session` при TLS (на локальном HTTP — `gs_session`, иначе префикс
требует Secure и ломает прогон без certificates).

Осталось (T1.2): `audit_events` на вход/выход/оценку/смену роли. Отдельный
долг: `csrf_token` не может быть `__Host-`, потому что токен читает JS для
заголовка `X-CSRF-Token` (double-submit), и `SameSite=Lax` остаётся единственной
защитой cookie вне TLS-сценариев.

## 8. Конфигурация (env)

```
ADDR=:8080                       DATABASE_URL=postgres://…        TZ_NAME=Europe/Moscow
ENV=production|development       TRUST_PROXY=false                SESSION_TTL=24h
LOG_LEVEL=info|debug|warn|error  (уровень slog; debug включает статику в доступ-логе)
CORS_ALLOW_ORIGINS=https://…     (пусто = same-origin, заголовка нет)
ADMIN_USERNAME/ADMIN_EMAIL/ADMIN_FULL_NAME/ADMIN_PASSWORD   (сид, существующий механизм)
TELEGRAM_BOT_TOKEN=<secret>      TELEGRAM_ALLOWED_CHANNELS=…
SCHEDULE_SOURCE_BASE_URL=…       IMPORT_INTERVAL=15m
```

Таймауты HTTP-сервера и дедлайн обращений к БД пока не настраиваются через env —
это константы в `cmd/server/main.go` и `internal/handlers/deadline.go`
(`dbRequestTimeout`). Делать их переменными смысла нет до `T2.1`: значения
подбираются под пул соединений PostgreSQL, а не под SQLite-файл.

Секреты только из окружения управляемого хостинга/GitHub Secrets; `data/`, `.env` уже в `.gitignore`. Логи — `slog` JSON без значений секретов и без паролей.

## 9. Топология развёртывания (два варианта, решает владелец)

**A. Один Go-хост (рекомендую).** Статика и API с одного домена. Работает уже сейчас, cookie-модель остаётся `SameSite=Lax` + same-origin, CSP и CORS не усложняются. GitHub Pages в этой схеме не используется (или используется как зеркало лендинга). Дешевле по времени и надёжнее на мобильных браузерах.

**B. GitHub Pages + внешний backend.** Требует: относительные пути ресурсов под базовым путём репозитория, `API_BASE_URL` во фронтенде, CORS allowlist, `credentials: "include"`, и главное — cookie-авторизация становится кросс-доменной: нужен `SameSite=None; Secure`, а Safari по умолчанию блокирует такие сторонние cookie → вход на iPhone с высокой вероятностью сломается. Надёжная альтернатива в схеме B — bearer-токен в заголовке `Authorization` с хранением вне `localStorage`… что для ванильного фронтенда означает риск XSS вместо риска CSRF. Это реальная цена, а не формальность: бриф требует и Pages, и серверную авторизацию с БД одновременно.

Формулировка для владельца (бриф, п.5): Go-сервер и PostgreSQL на GitHub Pages не запускаются, поэтому либо backend внешний (вариант B с оговорками), либо размещение целиком на одном Go-хосте (вариант A).

## 10. CI (GitHub Actions) ⭐

`ci.yml` на каждый push/PR: `go vet`, `go build`, `go test ./...` с сервис-контейнером PostgreSQL (`services: postgres`), `golang-migrate` dry-run на чистой БД, статическая проверка фронтенда (`node --check` по всем `web/js/**/*.js` + проверка, что все подключаемые скрипты существуют — это поймало бы баг из отчёта 01 §3), `npm run lint` не вводим.
`deploy-frontend.yml` — если выбран вариант B (Pages). `deploy-backend.yml` — по выбранному варианту размещения backend.
Секреты через GitHub Environments, в логи не выводятся.

Фактическое состояние (T0.4, ADR-0009): `.github/workflows/ci.yml` есть и состоит
из трёх работ — `go` (gofmt, vet, build, `go test -race -count=1`), `frontend`
(`node --check` + `scripts/check-frontend.mjs`) и `ui` (jsdom-тесты против живого
сервера, `npm ci` по закоммиченному lockfile). Триггеры — `pull_request`, push в
`main`, ручной запуск; push в feature-ветку не гоняется, чтобы не удваивать прогоны
открытого PR. Чего ещё нет: сервис-контейнер PostgreSQL и dry-run миграций
(перенесено в T2.1 — SQL-пакета миграций пока нет), `deploy-frontend.yml` и
`deploy-backend.yml` (ждут решения о варианте размещения, ADR-0001/D-01).
Пароль сида в работе `ui` — не секрет: он создаёт одноразовую базу внутри раннера.

## 11. Тестирование (минимальный набор для MVP)

- unit: `authz` (таблица «роль × пермиссия»), нормализация времени в импортах, валидация форм, `ratelimit`.
- store-интеграционные: против реальной PostgreSQL в CI; сценарии «повторный импорт не создаёт дубликат», «отзыв сессии`, `upsert` сдачи.
- handler-интеграционные: register→login→me→403 при отсутствии CSRF→logout→401.
- parser fixtures: сохранённые HTML/JSON-ответы источника в `testdata/`, парсер тестируется на них, плюс проверка «структура не совпала → ошибка с алертом, а не пустой результат».
- frontend: смоук-страницы Playwright (маршруты, формы входа, загрузка ленты) — опционально, если выбран вариант B.

## 12. Что делает Агент 1 до Этапа 3

Принять решения из 03, зафиксировать ADR-0001 (тоположение развёртывания), ADR-0002 (PostgreSQL + runner миграций), ADR-0003 (формат ошибок API), ADR-0004 (распределение ролей и `authz`), написать `docs/how-to-add-a-module.md`, обновить README (сейчас он расходится с кодом: CSRF помечен нереализованным, схема БД — 4 таблицы вместо 8).
