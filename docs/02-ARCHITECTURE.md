# 02. Архитектура MVP

Проект решения. Всё, что помечено «⭐», — изменение относительно текущего кода и требует согласования (см. 03-UNKNOWN-AND-DECISIONS.md). Документ фиксирует контракты, чтобы агенты 2-4 не меняли их в одностороннем порядке (бриф, п.4).

## 1. Общая схема

```
Браузер (GitHub Pages ИЛИ статика Go-сервера)
   │  REST/JSON, cookie-сессия + X-CSRF-Token
   ▼
Go-приложение (модульный монолит, net/http)
   ├─ platform: config · db · authn/authz · csrf · ratelimit · logging · router-registry · migrations runner
   ├─ modules/accounts  (регистрация, вход, роли, «выйти везде»)
   ├─ modules/courses   (курсы, записи)
   ├─ modules/assignments (задания, сдачи, оценки)
   ├─ modules/schedule  (записи расписания + адаптеры источников)
   ├─ modules/events    (события + адаптеры источников, дедупликация)
   ├─ modules/resources (витрина материалов)
   └─ modules/audit     (журнал действий: таблица + GET /api/admin/audit, T1.2)
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

Фактическое состояние после T1.1 и T1.2 (`439a08b`, `26066e8`) — каркас собран,
распад по модулям
впереди: созданы `internal/platform/{module,routes,migrations}.go` (ядро, 4 файла
всего), `internal/authz/authz.go` (права),
`internal/modules/<name>/module.go` для семи модулей (accounts, courses,
assignments, resources, events, schedule и добавленный в T1.2 `audit` —
26066e8) и `cmd/server/modules.go` со списком модулей. Обработчики и хранилище
пока общие — `internal/handlers/*` и `internal/store/*` лежат единым пакетом, у
модулей в них только объявления маршрутов; `internal/platform/{config,database,httpx,authn}` из
схемы не заведены: `config` появится вместе с env-настройками T2.1, `database` и
`httpx` — это текущие `internal/database` и `internal/handlers/response.go`.
`Migrations()` возвращает пусто у шести модулей из семи: их core-таблицы
(users, sessions, courses …) по-прежнему создаёт `internal/database/db.go`,
runner ведёт только таблицы самих модулей (T2.1). У `audit` миграция реальная
(`2026-10-24_audit_events`) — это первый случай, когда модуль сам приносит
свою схему, и он же проверка того, что ядро под новый модуль править не нужно.

Фактическое состояние фронтенда после T3.1 (`8fef933`, ADR-0011) — схема выше
реализована не целиком и иначе в деталях: `web/js/config.js` (единственный
источник базового URL и путей страниц), `web/js/core/api-client.js` (транспорт:
CSRF, credentials, таймаут, 401/429, форма ошибки) и `web/js/core/format.js`
(подписи ролей, `GS.can`, даты, инициалы). `csrf.js`, `auth.js` и `dom.js` из
целевой схемы не заведены отдельными файлами: CSRF-заголовок добавляет клиент,
состояние сессии — тоже его политика, `dom.js` не нужен, потому что страницы
пишутся на ванильном DOM без обёртки. Скрипты страниц лежат плоско, в `web/js/`
(`dashboard.js`, `schedule.js`, `admin.js`, `account.js`, `auth.js`, `index.js`,
`modals.js`), а не в `js/pages/`: шесть файлов не тот объём, где каталог
оправдан. Шапка и подвал с второй половины T3.1 рисует `web/js/core/layout.js`
(было — 184 строки копипасты на шесть страниц; `ADR-0013`), страница 404 —
`web/404.html`, её отдаёт `Handlers.NotFound` со статусом 404.

## 3. Контракт модуля ✅ (реализован в T1.1, коммит `439a08b`)

Целевая схема из брифа:

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

Фактическая реализация (`internal/platform/module.go`, `routes.go`, `migrations.go`):

```go
type Module interface {
    Name() string
    Migrations() []platform.Migration          // {Version, SQL}
    Register(reg *platform.Registry)
}

type Route struct {
    Path        string               // точный путь, как в ServeMux
    Method      string               // "GET"/"POST"…; "" = любой метод
    Handler     http.Handler
    Perm        authz.Permission     // пусто = достаточно войти (Public=true — публичный)
    DenyMessage string               // текст отказа 403, пусто = "forbidden"
    Public      bool                 // доступен без сессии
    CSRF        bool                 // требовать X-CSRF-Token
}
```

Чем отличается от схемы и почему: `Method` — один метод вместо `Methods []string`,
потому что у одного пути разные методы различаются и по праву, и по CSRF
(`GET /api/courses` видит студент, `POST` — только преподаватель); список из
одного имени только спрятал бы эту разницу. `DenyMessage` добавлен, чтобы
переезд на реестр не поменял формат ответов: тексты отказов остались те же.
`Registry.Add` накапливает проблемы (пустой `Path`, путь без «/», метод с
пробелами, nil `Handler`, `Public` вместе с `Perm`, право отсутствующее в
`authz.Registry()`), а `Mount` отказывается собирать маршрутизатор, если список
непустой, — `main.go` на такой ошибке не стартует. Опечатка в маршруте больше не
живёт до первого запроса. Обёртки навешиваются на каждый метод отдельно: снаружи
`RequirePerm` (он сам вызывает `RequireAuth`, поэтому аноним получает 401, а
вошедший без права — 403 с текстом из `DenyMessage`), внутри него CSRF. Метод,
не перечисленный для известного пути, даёт 405 в том же JSON-формате, а
неизвестный путь обрабатывает `Handlers.NotFound`: `/api/*` получает `404` +
`{"error":"route not found"}`, остальное — `web/404.html` со статусом `404`
(`ADR-0013`; раньше это был серверный текст `404 page not found`).

Явный список модулей в одном месте (`cmd/server/modules.go`, функция
`moduleList`), без `plugin`/динамической загрузки (бриф, п.3). Добавление модуля =
новый пакет + одна строка в списке; проверка приёмки описана в
[`how-to-add-a-module.md`](how-to-add-a-module.md). Права проверяются на сервере
через `authz.Can(роль, право)`, а не сравнением строки роли в 15 местах —
`RequireRole`/`hasRole` удалены.

Вне реестра остались: HTML-страницы и статика (отказ там — редирект 302, а не
JSON, и middleware-цепочка для них своя) и `/api/health` (публичный, до БД
модулей). Формат ошибок §4 пока прежний: `{"error": "строка"}`.

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
audit_events(id, at, actor_user_id FK NULL, action, target_type, target_id,
             detail, ok, ip, user_agent)   -- ✅ сделана в T1.2 (26066e8)
```

Миграции — файлы SQL + таблица `schema_migrations(version text PK, applied_at)`; откат обязателен только для изменений, ломающих чтение. Правила: колонки времени — `timestamptz`, id — `bigserial`, FK — `ON DELETE` явно, чек-ограничения на `role` и `score`.

Единственная таблица, которая уже существует не в PostgreSQL, а в SQLite:
`audit_events` создана миграцией модуля `audit` (`internal/store/audit.go`,
версия `2026-10-24_audit_events`) и переедет на диалект и `timestamptz` вместе
с остальной схемой в `T2.1`. `actor_user_id` — `ON DELETE SET NULL`: удаление
учётки не должно стирать историю её действий, а журнал без автора остаётся
читаемым (в UI — «не установлен»).

## 7. Аутентификация и безопасность (целевое состояние)

Пароли — bcrypt (оставляем, аргон2ид не вносим: лишняя зависимость без выигрыша для MVP). Сессии — серверные, `__Host-gs_session` (HttpOnly, Secure, SameSite=Lax — совместимо с раздачей с одного домена). Права — `authz.Can(роль, право)` с реестром прав по модулям (T1.1).

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

Добавлено в T1.1 (ADR-0010): вместо `RequireRole`/`hasRole` — `RequirePerm`
(проверяет вход, затем `authz.Can`), а маршрут декларирует право и текст отказа.
Реестр прав на момент T1.1: 22 права, student 3 / teacher 10 / admin 21 (единственное, которого у
администратора нет — `schedule.scope.created`, потому что у него `schedule.scope.all`),
матрица зафиксирована тестом в обе стороны (право без роли и роль без права).
После `T1.2` в реестре 23 права — см. §7 ниже. Обход проверки владельца
строки формулируется явно правом `*.any` (`submission.grade.any`,
`assignment.create.any`, …) — в обработчике больше нет ветки «если admin».
Неизвестное право даёт 403, а не «разрешить всем», и `ValidPermission` не даёт
опечатке в константе остаться тихой: маршрут с таким правом не собирается.
`authz.ScheduleScope` заменил switch по ролям в `store/schedule.go`,
`assignableRoles` в админке строится из `authz.Roles()` — второго списка ролей в
коде нет.

Добавлено в T1.2 (`26066e8`, ADR-0012): журнал действий `audit_events` пишется
из 15 точек — вход (успех, неверный пароль, перебор по идентификатору,
блокировка бакета), выход, «выйти везде», смена пароля (включая частичный
исход «пароль сменили, сессии не отозвали»), регистрация, смена роли, оценка
работы. Читается администратором через `GET /api/admin/audit` с правом
`audit.view`: 23 права, student 3 / teacher 10 / admin 22, фильтры
`action`/`actor_user_id`, `limit` 50 по умолчанию и 200 потолок.
Запись в журнал — побочный эффект: сбой INSERT не меняет код ответа запроса,
а проверка «нет паролей, bcrypt-хешей, токенов сессий и CSRF-токенов» стоит
отдельным тестом, который проходит по всем строкам таблицы. Отказы 403 и
ошибки валидации 422 в журнал не пишутся намеренно.

Осталось: политика хранения журнала (сколько месяцев записи лежат и кто чистит)
— вопрос владельцу, `docs/03` §3; отдельный долг — `csrf_token` не может быть
`__Host-`, потому что токен читает JS для заголовка `X-CSRF-Token`
(double-submit), и `SameSite=Lax` остаётся единственной защитой cookie вне
TLS-сценариев.

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

**B. GitHub Pages + внешний backend.** Требует: относительные пути ресурсов под базовым путём репозитория, `API_BASE_URL` во фронтенде, CORS allowlist, `credentials: "include"` (первые два уже сделаны в T3.1 — `8fef933`: пути в HTML и в `SOS_CONFIG` относительные, `api-client` переключает `credentials` на `include`, когда базовый URL непустой; CORS-allowlist на сервере и `deploy-frontend.yml` — нет), и главное — cookie-авторизация становится кросс-доменной: нужен `SameSite=None; Secure`, а Safari по умолчанию блокирует такие сторонние cookie → вход на iPhone с высокой вероятностью сломается. Надёжная альтернатива в схеме B — bearer-токен в заголовке `Authorization` с хранением вне `localStorage`… что для ванильного фронтенда означает риск XSS вместо риска CSRF. Это реальная цена, а не формальность: бриф требует и Pages, и серверную авторизацию с БД одновременно.

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

Фактическое состояние после второй половины T3.1 (`ADR-0013`): unit-тест `authz`
есть и закрывает
матрицу «роль × право» в обе стороны (`authz_test.go`, 6 тестов, 23 права);
`ratelimit` —
8 сценариев; ядро маршрутизатора и runner миграций — `platform_test.go`
(10 + 9 подтестов, включая откат при битом SQL); `internal/store/audit_test.go`
(8 тестов на SQLite) — первые store-тесты в репозитории: порядок страниц,
NULL-актор, пагинация при одинаковом `at`, потолок `limit`, обрезка по рунам,
`ON DELETE SET NULL` после удаления учётки; handler-интеграционные —
`internal/handlers` (45 + 23 подкейса: register→login→me, CSRF 403, logout-all,
смена пароля, отзыв сессий, лимиты полей, куки, и 12 тестов журнала — точка
записи на каждый случай плюс «сбой INSERT не меняет ответ запроса» и «в журнале
нет паролей, хешей и токенов») и уровень выше: `cmd/server/modules_test.go` и
`cmd/server/audit_test.go` поднимают настоящий `moduleList()` на httptest +
SQLite и проверяют допуск на маршрутах, а не только внутри обработчиков
(6 тестов верхнего уровня, 4 из них про журнал), `cmd/server/notfound_test.go`
(5 тестов) — что неизвестная страница отдаёт `404` + `text/html`, глубокий путь —
тот же файл, `/api/*` — JSON, известные страницы остались `200`, в 404 нет ни
одного `<script`. Итого 88 тестов верхнего уровня и 32 подтеста в шести пакетах,
`go test -race -count=1 ./...` — все ok.
UI-проверки (`tests/ui/*.cjs`, jsdom) после второй половины T3.1 — пять
сьютов и 133 проверки: задания/модалки 20, роли админа 24 (в ней же — журнал),
панель аккаунта 21, истечение сессии 14, формы расписания и общая шапка 54.
Четыре прежних сьюта ходят к живому серверу; `schedule-forms.cjs` работает без
него (`fetch` подменён роутером) и потому не тратит бюджет лимитера входов.
Сьют истечения сессии и есть проверка приёмки T3.1 («не вечный
spinner»): сессия отзывается настоящим `POST /api/auth/logout`, и требуется, что
навигация ровно одна, только на вход, а на самом входе и в гостевом расписании —
ноль (иначе редирект зацикливается). Граница, о которой надо помнить: вход
лимитирован 20 попытками на IP за 10 минут, а `seed.sh` + четыре «живых» сьюта
делают 17 входов по логу сервера. Поэтому проверки журнала добавлены в сьют ролей (он и
так входит администратором), а не пятым отдельным сьютом.

Чего нет: store-интеграционные против PostgreSQL (журнал проверяется на SQLite —
PostgreSQL появится в T2.1) и «повторный импорт не создаёт
дубликат» (нет импорта, ADR-0004), parser fixtures (T4.1), Playwright (см. §9,
ждёт решения о размещении).

## 12. Что делает Агент 1 до Этапа 3

Принять решения из 03, зафиксировать ADR-0001 (тоположение развёртывания), ADR-0002 (PostgreSQL + runner миграций), ADR-0003 (формат ошибок API), ADR-0004 (распределение ролей и `authz`), написать `docs/how-to-add-a-module.md`, обновить README (сейчас он расходится с кодом: CSRF помечен нереализованным, схема БД — 4 таблицы вместо 8).

Состояние: ADR-0001…ADR-0012 зафиксированы (05-DECISIONS.md), `docs/how-to-add-a-module.md`
написан и проверен добавлением тестового модуля, README приведён в соответствие
и дополнен разделом «Права (`authz`)». Осталось не выполненным из этого списка:
ADR-0001 реализован только как решение (Pages + внешний backend), относительные
пути и `deploy-frontend.yml` — за `T3.1`; единый формат ошибок (ADR-0003) не
мигрирован намеренно — он отдельным изменением, чтобы смена каркаса и смена
формата ответов не шли одним незапускаемым коммитом.

## 13. Транспорт фронтенда и состояние сессии ✅ (реализовано в T3.1, коммит `8fef933`, ADR-0011)

Слои и порядок подключения (он же проверяется `scripts/check-frontend.mjs`):

```
config.js → core/api-client.js → core/format.js → core/layout.js → common.js → скрипт страницы
```

- `config.js` — `window.SOS_CONFIG`: `API_BASE_URL` (пусто = same-origin),
  пути страниц (`"login"`, `"dashboard"`, `"./"`, `"login?revoked=1"`),
  `REQUEST_TIMEOUT_MS`, хук `navigateTo` для проверок.
- `core/api-client.js` — единственный, кто вызывает `fetch` (во всём фронтенде
  один такой вызов). 24 обращения страниц идут через `GS.fetchJSON` →
  `GS.api.request`. Решает: базовый URL, `credentials`, CSRF-заголовок для
  небезопасных методов, таймаут, форму ошибки, политику 401.
- `core/format.js` — подписи и чипы ролей, `GS.can` (наборы «кому показывать
  кнопку»), даты, инициалы.
- `core/layout.js` — шапка и подвал (добавлено во второй половине T3.1,
  `ADR-0013`): страница задаёт вариант атрибутами `<body>` (`data-layout`,
  `data-brand-sub`, `data-footer-note`, `data-footer-stack`), компонент
  заполняет пустые `<header class="site-header">` / `<footer class="site-footer">`
  из таблицы `PAGES`. Рисует сразу при загрузке, повторный вызов защищён
  флагом `dataset.filled` — правки страничных скриптов (имя, скрытый `#adminLink`)
  не затираются. Идентификаторы сохранены прежние: на них смотрят `common.js`,
  скрипты страниц и jsdom-сьюты.
- `common.js` — `escapeHtml`, статусные сообщения, `fetchJSON`-обёртка,
  `bindLogout`; `GS` — свойство `window` (иначе core-файлы не смогли бы его
  дополнить: `const` верхнего уровня живёт в global lexical-области).

Политика 401: по умолчанию «уйти на вход»; там, где 401 — штатный ответ формы
(вход с неверным паролем, смена пароля с неверным текущим) или где страница
работает в гостевом режиме, вызов передаёт `on401: "throw"` (7 мест). Редирект
не срабатывает, если мы уже на странице входа — сравнение по имени последней
части пути, а не по хвосту (`endsWith` ловил бы `/relogin`, полное сравнение
ломается о подкаталог Pages).

Текст ошибки: сначала серверный (`{"error": "…"}`), при пустом — русская
формулировка по статусу; для 429 всегда наша, с секундами из `Retry-After`.
Формат ошибок с кодами (§4) от этого не изменился: `error.code` клиент читает,
сервер код не отдаёт.
