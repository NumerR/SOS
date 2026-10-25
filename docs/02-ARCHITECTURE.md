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
tools/schedule-parser/        приложенный Python-парсер источника (ADR-0021):
                              fetch.py — точка входа для Go-моста, api_client.py,
                              parser.py, groups.py, тесты; вызывается процессом, не импортируется
docs/                         этот набор + ADR + «как добавить модуль»
```

`internal/store/*` и `internal/handlers/*` распадаются по модулям; `models.go` раздается на модульные модели. Миграции модуля применяются тем же общим runner'ом, порядок — по имени файла `<модуль>_<0001>.sql`. Пакета `httpx/` в итоге нет: из задуманного в нём в отдельный пакет вырос только конверт отказа — `internal/apierror` (§4), а `writeJSON`/`readJSON` остались в `internal/handlers/response.go`.

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
`modals.js`, `profile.js`), а не в `js/pages/`: восемь файлов — не тот объём, где каталог
оправдан. Шапка и подвал с второй половины T3.1 рисует `web/js/core/layout.js`
(было — 184 строки копипасты на шесть страниц; `ADR-0013`), страница 404 —
`web/404.html`, её отдаёт `Handlers.NotFound` со статусом 404. Остаток T3.1 —
страница профиля: `web/profile.html` (131 строка) и `web/js/profile.js`
(78 строк, `ADR-0014`), маршрут `/profile` регистрирует `Handlers.ProfilePage`,
которая без сессии делает 302 на `/login`, как и `DashboardPage`. Панель смены
пароля и «выйти везде» теперь одна и живёт на профиле: из `dashboard.html` секция
удалена вместе с подключением `account.js`.

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
не перечисленный для известного пути, даёт 405 — и пишет его тот же конверт
отказа, что и обработчики (§4): `internal/platform/routes.go:105` подставляет
`apierror.Write`, когда `Middleware.Error` не передан. Неизвестный путь
обрабатывает `Handlers.NotFound` (`internal/handlers/pages.go:34`): `/api/*`
получает `404` + `{"error":{"code":"not_found","message":"route not found"}}`,
остальное — `web/404.html` со статусом `404` (`ADR-0013`; раньше это был
серверный текст `404 page not found`).

Явный список модулей в одном месте (`cmd/server/modules.go`, функция
`moduleList`), без `plugin`/динамической загрузки (бриф, п.3). Добавление модуля =
новый пакет + одна строка в списке; проверка приёмки описана в
[`how-to-add-a-module.md`](how-to-add-a-module.md). Права проверяются на сервере
через `authz.Can(роль, право)`, а не сравнением строки роли в 15 местах —
`RequireRole`/`hasRole` удалены.

Вне реестра остались: HTML-страницы и статика (отказ там — редирект 302, а не
JSON, и middleware-цепочка для них своя) и `/api/health` (публичный, до БД
модулей). Страница `/profile` (`ae291e8`, `ADR-0014`) следует той же модели:
доступ проверяет обработчик `Handlers.ProfilePage`, а не `Registry`, потому что
отказ для HTML — это «уйти на вход», а не JSON с кодом. Отказы API на этих
страницах подчиняются формату §4.

## 4. Формат ошибок API ✅ (реализован, ADR-0003)

Один конверт для всех отказов API:

```json
{
  "error": {
    "code": "validation",
    "message": "title is required",
    "fields": { "title": "title is required" }
  }
}
```

Живёт в `internal/apierror` (`apierror.go`): коды, `Body`, `Write`/`WriteField`/
`WriteCode` и таблица `CodeForStatus`. Пакет отдельный потому, что отказывать
клиенту приходится не только обработчикам: маршрутизатор ядра отвечает 405 для
неизвестного метода, минуя любой обработчик. Пока конверт лежал в `handlers`,
именно этот путь и остался плоским — его ловит
`platform.TestDefaultErrorWriterKeepsTheEnvelope`.

`internal/handlers/errors.go` — только короткие имена поверх пакета
(`writeError`, `writeFieldError`, `writeCodeError`, константы `Err*`), чтобы не
переписывать двести вызовов в обработчиках. `errorBody` при этом не копия, а
алиас `apierror.Body`: два описания одного ответа разошлись бы незаметно.

Коды стабильны: текст можно переформулировать, код менять нельзя — на него
опирается JS.

| HTTP | `code` | примечание |
| --- | --- | --- |
| 400 | `validation` | не разобран параметр или значение |
| 400 | `invalid_json` | тело не разбирается либо содержит неизвестное поле |
| 401 | `unauthorized` | сессии нет / истекла; сюда же «неверный текущий пароль» |
| 403 | `forbidden` | права нет (текст из `DenyMessage`) |
| 403 | `csrf_failed` | токен не сверился — «подкрути токен», а не «нет прав» |
| 404 | `not_found` | ресурса или маршрута нет |
| 405 | `method_not_allowed` | путь есть, метода нет (пишет платформа) |
| 409 | `conflict` | логин/почта/запись уже заняты |
| 422 | `validation` | валидация формы; вместе с `fields` |
| 429 | `rate_limited` | плюс заголовок `Retry-After` |
| 502, 503 | `source_unavailable` | источник расписания не ответил или не настроен |
| прочее | `internal` | сервер не справился |

`code` выводится из статуса в одном месте, поэтому соответствия статусов и кодов
 — это контракт, а не деталь реализации: его сторожат
`handlers/errors_test.go` (`TestErrorCodesMatchStatuses`,
`TestErrorCodesComeOnlyFromTheDeclaredSet`, `TestErrorEnvelopeShape`,
`TestInvalidJSONHasItsOwnCode`, `TestCSRFRejectionUsesItsOwnCode`,
`TestUnauthorizedFromMiddlewareKeepsCode`, `TestMethodNotAllowedKeepsCode`).
Явный `WriteCode` нужен там, где статус не классифицирует неисправность:
`invalid_json` при 400 и `csrf_failed` при 403.

`fields` заполняется только когда сервер назвал поле; ключ — имя из JSON
(`title`, `course_id`, `group`), значение — тот же текст, что и в `message`.
Фронтенд не угадывает подпись: `GS.fieldLabel` (`web/js/common.js:52`) берёт её
у самой формы по `name` контрола и его `<label>`, а `GS.describeServerError`
(там же, строка 68) печатает `message — поле: Название`. Так «title is required»
становится человеческим без словаря соответствия, который устарел бы на первом
же переименовании поля.

Клиент: `ApiError` (`web/js/core/api-client.js`) несёт `status`, `code`,
`message`, `fields`, `retryAfter`, `data`. Текст — сначала серверный, и только
если сервер ничего не сказал, русская формулировка по статусу; для 429 текст
всегда строится по `Retry-After`. `account.js` выбирает ветку по `code`
(пока только `csrf_failed`), а не по тексту.

Исключения из конверта (осознанные): `/api/health` отвечает документом о
состоянии `{"status":"ok","db":"ok"}` и при 503 — `{"status":"error","db":"error"}`;
это не отказ API, а результат проверки, и монитор его читает иначе. HTML-страницы
при отказе редиректят на вход (§3).

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

### 5.1 Что реально реализовано в T4.1 (расписание источника)

Контракты выше — целевые; расписание уже работает по другому пути, и он зафиксирован
здесь, чтобы следующий, кто будет читать документ, не искал несуществующий
`/api/schedule/entries`. Источник — АПЭКС-ВУЗ, токен держит сервер, наружу отдаются
только импортированные строки. Разбирает ответ приложенный Python-парсер (`ADR-0021`), а
не только код репозитория: это видно в `engine` админского статуса, и путь целиком —
в пункте «Про выбор движка» §8.

`GET /api/schedule/groups?q=&limit=` — публичный. Каталог групп:

```json
{ "source": "apeks", "source_name": "АПЭКС-ВУЗ", "synced_at": 1761317000,
  "count": 1, "source_page_url": "https://tksu.ru/…/rasp-stud.html",
  "groups": [ { "external_id": "494", "name": "Б-ИСиТ-22",
                "department": "…", "course": "2", "updated_at": 1761317000 } ] }
```

`GET /api/schedule/lessons?group=&month=&year=` (или `from=&to=` в unix-секундах) —
публичный. `group` принимает и `external_id`, и имя из каталога (регистр и лишние
пробелы не важны); если имя подходит нескольким группам — `422` со списком их
идентификаторов, а не «первая попавшаяся». Ответ сгруппирован по учебным дням,
`day_key` — `YYYYMMDD` (число, а не строка: так сортируется в SQL без функций),
день без точной даты — `99999999`:

```json
{ "source": "apeks", "group_id": "494", "group_name": "Б-ИСиТ-22",
  "range": { "from": 20261001, "to": 20261031 }, "updated_at": 1761317000,
  "imported_months": [ { "month": 10, "year": 2026, "lessons": 22,
                         "imported_at": 1761317000 } ],
  "days": [ { "day_key": 20261002, "date": "2026-10-02", "weekday": "Пт",
              "lessons": [ { "lesson_number": 3, "time": "12:20 – 13:55",
                             "start_time": "12:20", "end_time": "13:55",
                             "discipline": "…", "lesson_type": "Пр",
                             "room": "к. 3/314", "teacher": "…",
                             "subgroup": "1", "group_name": "Б-ИСиТ-22",
                             "imported_at": 1761317000 } ] } ],
  "source_page_url": "https://tksu.ru/…/rasp-stud.html" }
```

Три админских маршрута (право `schedule.source.manage`, запись — с CSRF):
`GET /api/schedule/source/status` отдаёт `{configured, engine, missing_config, hint, stats}` —
при неконфигурированном источнике это `200` с `configured: false` и пустым `engine`:
«не настроено» — состояние, а не ошибка запроса, и выдумывать движок там, где пути нет,
нельзя. `missing_config` и `hint` с ADR-0023 (`b28a036`) перестали быть константами: первый
называет переменную окружения, из-за которой источника нет (`APEKS_TOKEN`, `PYTHON_BIN`,
`APEKS_API_URL`, `SCHEDULE_SOURCE`), второй несёт готовую подсказку для панели (пустой — значит
причину не назвали, и панель формулирует прежним общим текстом). Тот же текст получает отказ
503 и то же имя переменной попадает в `audit_detail` отказа, поэтому панель, ответ и журнал
не могут разойтись. При `configured: true` оба поля пустые (`4e22b33`, поправка того же
дня, её нашёл живой прогон с чистого клона): «какая переменная отсутствует» там, где
ничего не отсутствует, — вопроса нет, и `missing_config: "APEKS_TOKEN"` рядом с
`configured: true` противоречил бы первому полю. Прежний общий текст `APEKS_TOKEN` остаётся
только там, где источника нет, а причину не передали. `POST /api/schedule/source/groups`
→ `{source, fetched, added}`; `POST /api/schedule/source/lessons` с телом
`{group, month, year, count}` (нули = «текущий месяц», `count` — сколько месяцев
вперёд) → `{source, group_id, group_name, months, lessons}`. Оба пишут запись в
журнал (`schedule.groups_sync`, `schedule.import`) и оба обращаются к чужому сайту:
частота обращений к источнику — административное решение, а не побочный эффект
открытой страницы.

Значений токена, `APEKS_TOKEN` и вообще чего-либо, кроме перечисленного, ни один
из этих ответов не содержит — проверка закреплена тестом. В `hint` и `missing_config`
(ADR-0023) уходят только имена переменных и готовые фразы из исходника: текст ошибки моста
в них не копируется, потому что содержит абсолютные пути процесса.

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

В T4.1 к тому же SQLite-набору добавились ещё две таблицы — их объявляет модуль
`schedule` (`internal/store/schedule_source.go`, версия
`2026-10-24_schedule_source`):

```
schedule_source_groups(id, source, external_id, name, department, course, updated_at,
                       UNIQUE (source, external_id))
CREATE INDEX idx_schedule_groups_name ON schedule_source_groups (source, name)

schedule_source_lessons(id, source, external_id, group_ext_id, month, year,
                        day_key, date, weekday, lesson_number, time,
                        start_time, end_time, group_name, subgroup, discipline,
                        lesson_type, room, teacher, imported_at,
                        UNIQUE (source, group_ext_id, external_id))
CREATE INDEX idx_schedule_lessons_group_day ON … (source, group_ext_id, day_key, lesson_number)
CREATE INDEX idx_schedule_lessons_batch    ON … (source, group_ext_id, month, year)
```

Это не целевая `schedule_entries`, а её рабочий эквивалент для одного источника:
`source` держится колонкой, а не FK на `schedule_sources`, потому что справочника
источников в базе нет и на один месяц он ровно один (ADR-0015). Уникальность
`(source, group_ext_id, external_id)` — тот самый ключ дедупликации: импорт месяца
делает upsert по нему и заменяет месяц целиком в одной транзакции, поэтому
повторный импорт не удваивает пары, а удалённая источником пара исчезает и у нас.
`day_key` вместо `starts_at`: у источника время пары приходит строкой
«12:20 – 13:55» без даты-во-всё-миры, а день занятия задаёт вуз.

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

Добавлено в T4.1 (ADR-0015): в реестре 24 права — `schedule.source.manage`
(только `admin`), student 3 / teacher 10 / admin 23. Это право стоят на всех
трёх админских маршрутах источника (`internal/modules/schedule/module.go`), а
чтение расписания (`/api/schedule/groups`, `/api/schedule/lessons`) объявлено
`Public: true` и права не требует: в ответе нет ничего персонального, а витрина
существует для гостя. Две новые точки журнала — `schedule.groups_sync` и
`schedule.import`: в `detail` попадают только идентификатор группы, месяцы и
счётчики, токена там нет и не может появиться, потому что токен не покидает
процесс сервера. Отдельным тестом закреплена инвариантность: ни один ответ API
и ни одна строка журнала не содержат значения `APEKS_TOKEN`, а фронтенд не
обращается к `tksu.ru` — весь внешний путь остаётся на сервере. Исходящий запрос
к источнику возможен только из обработчика под правом и с CSRF-заголовком,
поэтому частота обращений к чужому сайту — решение администратора, а не
побочный эффект открытой страницы.

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
TELEGRAM_BOT_TOKEN=<secret>      TELEGRAM_ALLOWED_CHANNELS=…   (T4.2, не реализовано)
APEKS_TOKEN=<secret>             APEKS_API_URL=…  (пусто = боевой адрес источника)
SCHEDULE_SOURCE=python|go        (пусто = python; ADR-0021 — кто разбирает ответ)
SCHEDULE_PARSER=<путь к fetch.py> PYTHON_BIN=python3  (переопределение для моста и тестов)
IMPORT_INTERVAL=15m                                          (не реализовано)
```

Про `APEKS_TOKEN` (T4.1): переменная читается ровно один раз — в
`newScheduleSourceClient` (`cmd/server/main.go`), и только как значение запроса к
источнику. Пустая — штатное состояние, а не авария: сервер стартует, страница
расписания показывает «данные ещё не импортированы», а админская панель подсказывает,
что задать (или назвать другую виноватую переменную — ADR-0023, см. ниже). Значение не печатается ни в одном логе (`slog`-сообщение говорит только
«не задан»), не попадает ни в один ответ API, ни в одну строку журнала, ни в URL
браузера; в `apeks.scrub` оно вырезается из текста ошибки, если чужая библиотека
всё-таки подставит полный URL с токеном в сообщение. `APEKS_API_URL` переопределяет
адрес (нужен тестам на локальном TLS-стабе), но http-схему клиент отвергает при
сборке — токен не уйдёт открытым текстом.

Про выбор движка (ADR-0021, `63f8cb3`). `SCHEDULE_SOURCE=python` — ответ источника
разбирает приложенный Python-парсер: мост `internal/apeks/python.go` запускает
`SCHEDULE_PARSER` (по умолчанию `tools/schedule-parser/fetch.py`) интерпретатором
`PYTHON_BIN` (по умолчанию `python3`) без оболочки, токен даёт ему **только окружением
процесса** — в аргументах командной строки секрет не оказывается, потому что строку
видно в `ps` и она попадает в логи падений. `SCHEDULE_SOURCE=go` — прежний клиент
`internal/apeks/client.go`; он оставлен рабочим именно как запасной путь на случай
хостинга без интерпретатора (вопрос владельцу в `docs/03` B2). Выбранный движок
пишется в лог старта и виден в `engine` из `GET /api/schedule/source/status`;
автоматической смены движка при ошибке нет — два разбора могут разойтись, и молчаливый
переход показал бы админу данные, собранные не тем кодом, который он настроил.
Значение `SCHEDULE_SOURCE` тоже читается один раз, при старте.

Про причину «источника нет» (ADR-0023, `b28a036`). `newScheduleSourceClient` возвращает не
только клиента, но и `sourceProblem{missing, hint}` — что именно в окружении помешало
источнику подняться. Пара живёт в `handlers.Deps` (`ScheduleSourceMissing`,
`ScheduleSourceHint`) и оттуда читается панелью статуса, текстом отказа 503 и `audit_detail`.
Раньше обработчик на любой «источника нет» отвечал `APEKS_TOKEN`, потому что при одном движке
другой причины не было; с двумя движками токен может быть задан, а импорта всё равно не быть,
и совет завести уже заведённый токен — ложный след, уводящий админа от `python3`. Знание
причины осталось там, где оно и появляется, — при сборке сервера из окружения; обработчик её
только показывает. Пустые поля Deps означают «причину не передали» (так собирают тесты), и
тогда включается прежний общий текст: обратно совместимо и не требует от каждой сборки
знать про источник. Общий текст живёт только по эту сторону клиента: поднявшийся источник
обязывает `scheduleSourceMissing` вернуть пустую строку (`4e22b33` — поправка того же дня,
что и ADR-0023; её нашёл прогон с чистого клона, где `configured: true` соседничал с
`missing_config: "APEKS_TOKEN"`). В панель не уходят ни значения переменных, ни текст ошибки моста с
абсолютными путями, ни свободное значение `SCHEDULE_SOURCE`, которое ввёл оператор.

Таймауты HTTP-сервера и дедлайн обращений к БД пока не настраиваются через env —
это константы в `cmd/server/main.go` и `internal/handlers/deadline.go`
(`dbRequestTimeout`). Делать их переменными смысла нет до `T2.1`: значения
подбираются под пул соединений PostgreSQL, а не под SQLite-файл.

Секреты только из окружения управляемого хостинга/GitHub Secrets; `data/`, `.env` уже в `.gitignore`, туда же добавлены `__pycache__/` и `*.pyc` — Python кэширует байткод рядом с исходниками парсера, и при каждом прогоне появляется папка, которую коммитить нельзя. Логи — `slog` JSON без значений секретов и без паролей.

## 9. Топология развёртывания (два варианта, решает владелец)

**A. Один Go-хост (рекомендую).** Статика и API с одного домена. Работает уже сейчас, cookie-модель остаётся `SameSite=Lax` + same-origin, CSP и CORS не усложняются. GitHub Pages в этой схеме не используется (или используется как зеркало лендинга). Дешевле по времени и надёжнее на мобильных браузерах.

**B. GitHub Pages + внешний backend.** Требует: относительные пути ресурсов под базовым путём репозитория, `API_BASE_URL` во фронтенде, CORS allowlist, `credentials: "include"` (первые два уже сделаны в T3.1 — `8fef933`: пути в HTML и в `SOS_CONFIG` относительные, `api-client` переключает `credentials` на `include`, когда базовый URL непустой; CORS-allowlist на сервере и `deploy-frontend.yml` — нет), и главное — cookie-авторизация становится кросс-доменной: нужен `SameSite=None; Secure`, а Safari по умолчанию блокирует такие сторонние cookie → вход на iPhone с высокой вероятностью сломается. Надёжная альтернатива в схеме B — bearer-токен в заголовке `Authorization` с хранением вне `localStorage`… что для ванильного фронтенда означает риск XSS вместо риска CSRF. Это реальная цена, а не формальность: бриф требует и Pages, и серверную авторизацию с БД одновременно.

Формулировка для владельца (бриф, п.5): Go-сервер и PostgreSQL на GitHub Pages не запускаются, поэтому либо backend внешний (вариант B с оговорками), либо размещение целиком на одном Go-хосте (вариант A).

## 10. CI (GitHub Actions) ⭐

`ci.yml` на каждый push/PR: `go vet`, `go build`, `go test ./...` с сервис-контейнером PostgreSQL (`services: postgres`), `golang-migrate` dry-run на чистой БД, статическая проверка фронтенда (`node --check` по всем `web/js/**/*.js` + проверка, что все подключаемые скрипты существуют — это поймало бы баг из отчёта 01 §3), `npm run lint` не вводим.
`deploy-frontend.yml` — если выбран вариант B (Pages). `deploy-backend.yml` — по выбранному варианту размещения backend.
Секреты через GitHub Environments, в логи не выводятся.

Фактическое состояние (T0.4, ADR-0009; состав проверок расширен в T0.5, ADR-0016, в ADR-0017, в ADR-0018, в ADR-0019, в ADR-0020 и в ADR-0021):
`.github/workflows/ci.yml` есть и состоит
из четырёх работ — `go` (gofmt, vet, build, `go test -race -count=1`; Python ставится
явно, иначе тест паритета разборов молча пропускался бы), `frontend`
(`node --check` + `scripts/check-frontend.mjs` + `scripts/check-docs.mjs` — ADR-0025,
согласованность документации с деревом; этот же шаг потребовал `fetch-depth: 0`, потому что в
shallow-клоне нет объектов истории для сверки хешей), `parser` (офлайн-проверки приложенного
парсера: `py_compile`, `test_parser.py`, `test_contract.py` — без сети, без токена и без
`requests`) и `ui` (jsdom-тесты против живого сервера — стек для них собирает и убирает сам
`tests/ui/run-live.sh`: сборка сервера, свободный порт, своя база в `mktemp`, сид, восемь
сьютов (девятый файл каталога, `tests/ui/schedule-source-python.cjs`, в этот набор не входит: ему
нужен настроенный источник — `APEKS_TOKEN` и `SCHEDULE_PARSER`, — а скрипт источник не настраивает,
поэтому вызванный здесь он краснеет, ничего не проверяя; у него свой харнесс, указанный ниже),
уборка по trap, поэтому локальный прогон и прогон на раннере — одна команда
`npm run test:live`; `npm ci` по закоммиченному
lockfile, плюс сквозная приёмка источника на подставном парсере
`bash tests/schedule-source-python.sh`, которая python не требует: дочерний процесс —
`/bin/sh`, и прогон состояний источника на собранном сервере
`bash tests/source-states-smoke.sh` — ADR-0024, шесть конфигураций окружения, семь входов в
лимитер, тоже без живого источника и без python). Живой АПЭКС-ВУЗ из CI не дёргается: нужен токен вуза в секретах репозитория,
а это и лишний канал утечки, и чужая нагрузка от чужого CI. `check-frontend.mjs` проверяет девять
групп: существующие `<script src>`/`<link href>`, подключение каждого JS-файла хотя
бы одной страницей, объявленность `getElementById`-мишеней, отсутствие инлайновых
`on*`-обработчиков, структуру доступности страницы (`lang`, `<title>`, один `<h1>`,
разрывы уровней, skip-link первой ссылкой и в фокусируемую цель, `scope` у `<th>`),
контраст пар токенов по формуле sRGB **и его покрытие** (каждая пара «текст + подложка»,
которую задаёт блок `style.css` или инлайновый `style=""` в HTML, обязана быть в списке
измеренных или в `CONTRAST_EXEMPT` с проверяемой причиной; подложка состояния
`:hover`/`:active`/`:focus`/`[disabled]` обязана встречаться в измеренных парах;
26 пар, 19 комбинаций в CSS, 1 исключение — ADR-0019), общность submit-механизма форм
создания (единственное определение `bindCreateForm` в `common.js`, вызов хотя бы одной
страницей, запрет страничного submit с блокировкой кнопки — ADR-0017) и связку
`aria-describedby` (каждая ссылка ведёт на существующий id, каждое поле формы со
статус-строкой на неё ссылается, подсказка с id подключена, сгенерированное в JS поле
без атрибута — красное; плюс два стража против вырождения проверки — ADR-0018) и
**различимость индикатора фокуса** (каждое правило `:focus`/`:focus-visible`/`:focus-within`,
снявший системный контур или рисующий `outline`/`box-shadow`/`border`, обязано задать цвет
с контрастом ≥3:1 против каждой из трёх подложек — `--parchment`, `--parchment-deep`,
`--surface`; одно освобождение с причиной — `.main:focus`, цель skip-link; три стража против
вырождения — ADR-0020).
Отдельно от CI живёт третий слой приёмки доступности — `scripts/check-focus.mjs`
(ADR-0020, дополнен ADR-0022): он поднимает headless Chromium по DevTools Protocol, обходит
восемь страниц и пять состояний открытого диалога на `/dashboard` реальными нажатиями `Tab`
и сравнивает снимки элемента с фокусом и без, поэтому требует запущенный сервер, сид и
браузер. Обход один и тот же (`runPasses`): у страницы проверяется совпадение порядка Tab с
порядком DOM, у модалки — что Tab из неё не выходит и что фокус после открытия уехал
внутрь. В `ci.yml` его нет намеренно: на раннере нет браузера,
а смысл проверки — отрисовка, которой на голом чекауте не сделать. Это ручной шаг, и
регулярность его держится на этом тексте и на ADR, а не на красном CI.
Триггеры —
`pull_request`, push в
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
одного `<script>`; `cmd/server/profile_test.go` (3 теста, `ADR-0014`) — что
аноним на `/profile` получает `302` на `/login`, студент и преподаватель — `200`
с маркерами разметки профиля, а в ответе нет ни `password_hash`, ни `csrf_token=`,
ни админских маршрутов, и после настоящего `POST /api/auth/logout` страница
перестаёт отдаваться.
Итого 176 тестов верхнего уровня и 74 подтеста в семи пакетах (было 158 и 52 до ADR-0021,
171 и 63 до ADR-0023, 175 и 70 до инварианта полей статуса; `internal/apeks` — 36),
`go test -race -count=1 ./...` — все ok. Четверо подтестов добавила поправка:
`TestScheduleStatusFieldsAgreeInEveryState` проходит все четыре сборки состояния
источника (собран с пустыми Deps, собран с заполненными, не собран с причиной, не собран
без причины) и требует, чтобы `configured`, `missing_config`, `engine` и `hint` не
противоречили друг другу ни в одном сочетании — именно такое противоречие и проскочило
ручную проверку.

Добавлено ADR-0024 (`a1e5fa6`): между этими двумя слоями лежал незакрытый сегмент — проводка
`main.go:141-142`, где `sourceProblem` из сборки клиента уходит в `handlers.Deps`. Ни один
Go-тест её не видел: `cmd/server/schedule_source_test.go` проверяет `newScheduleSourceClient`
до `Deps`, а тесты обработчика задают эти поля руками. Срез закрыт прогоном собранного сервера
— `tests/source-states-smoke.sh` (229 строк, 38 проверок): шесть конфигураций окружения
(нет токена, `PYTHON_BIN` не найден, адрес не по HTTPS, `go` поднялся, `python` поднялся на
подставном парсере, `SCHEDULE_SOURCE` с опечаткой) плюс отказ импорта, и по HTTP сверяются
`configured` / `engine` / `missing_config` / `hint`, текст 503 и запись журнала — все три места
обязаны назвать одну переменную. Проверено подстановкой: с пустыми полями в `Deps`
`go test -race -count=1 ./...` остаётся зелёным (семь пакетов `ok`), а слой падает — 29/38.
Шаг подключён к CI в job `ui`; живого источника и python слой не требует.

Добавлено ADR-0025 (`08c7104` + `5bde07a`): пятый слой проверки, и первый, который читает документацию.
`scripts/check-docs.mjs` (403 строки) сверяет README и шесть файлов `docs/*.md` с деревом по шести
группам — пути файлов (голое имя с номером строки отвергается, если ведёт к двум файлам), номера
строк (координата обязана попадать внутрь файла), хеши коммитов (`git cat-file`), заявления «N
строк» (по семантике `wc -l`), команды `npm run` (по `tests/ui/package.json`) и номера решений: на
каждое упоминание ADR-NNNN обязана приходиться запись журнала `docs/05-DECISIONS.md`, а индексный
диапазон обязан называть последнее принятое решение. Освобождения
подписаны причиной и проверяются на то, что они ещё нужны, — правило выведено в ADR-0019 и
ADR-0020. Группа объёмов действует только на тексты про состояние «сейчас» (README, отчёт 01,
целевая часть этого документа): `docs/04` и `docs/05` — журналы, их цифры суть снимки дня работы.
Смысл не в четырёх находках, которые правка сняла (несуществовавший тест-файл в README,
двенадцать неоднозначных координат в отчёте 01, пять устаревших объёмов и индекс решений этого
документа, который со дня ADR-0016 отставал от журнала на десять записей), а в том, что расхождение
документации с деревом стало красным шагом. Ограничение: сверяется координата с длиной файла, а не
содержимое строки — переезд кода внутри файла проверка не видит.
UI-проверки (`tests/ui/*.cjs`, jsdom) — девять сьютов и 401 проверка
(восемь из них — 380 — гоняет `npm run test:all`; девятый, `schedule-source-python.cjs`,
поднимает собственный сервер с подставным парсером, поэтому вызывается отдельно —
`npm run test:source`; после ADR-0018 было 377, после ADR-0017 — 364, после T0.5 — 339):
доступность структуры,
модального окна и связки поля с сообщением 112,
задания/модалки 24, роли админа 24 (в ней же — журнал),
панель аккаунта 29 (она проверяет уже `/profile`), истечение сессии 14,
формы создания и общая шапка 80 (сьют расписания с ADR-0017 покрывает и кабинет),
страница профиля 35, таблица расписания 62, источник расписания сквозняком 21.
Из них 289 работают офлайн — `accessibility.cjs`, `schedule-forms.cjs`,
`profile-page.cjs` и `schedule-timetable.cjs` подменяют `fetch` роутером или ошибкой
и потому не тратят бюджет лимитера входов; остальные четыре (91 проверка) работают с
живым сервером и сидом, и двадцать одна проверка источника — со своим сервером на
свободном порту и своей базой в `mktemp` (ADR-0021).
Сьют истечения сессии и есть проверка приёмки T3.1 («не вечный
spinner»): сессия отзывается настоящим `POST /api/auth/logout`, и требуется, что
навигация ровно одна, только на вход, а на самом входе и в гостевом расписании —
ноль (иначе редирект зацикливается). Граница, о которой надо помнить: вход
лимитирован 20 попытками на IP за 10 минут, а `seed.sh` + четыре «живых» сьюта
делают 17 входов по логу сервера. Границу снимает `tests/ui/run-live.sh` (и `npm run test:live`):
каждый его запуск — новый процесс сервера с новым лимитером и новая база в `mktemp`, поэтому
второй прогон подряд не даёт `429` и не наследует изменённую роль: `admin-roles.cjs` по роле
не идемпотентен, и прогон, оборвавшийся после повышения `s_stud`, на той же базе у следующего
запуска даёт «Роль не изменилась». Поэтому проверки журнала добавлены в сьют ролей (он и
так входит администратором), а не пятым отдельным сьютом. Сьют ждёт, пока `admin.js`
закончит перезагрузку журнала после смены роли: на время этого запроса в `#auditBody` лежит
заглушка «Загрузка журнала…», и проверка, прочитавшая панель раньше, падала не всегда
(поправка к ADR-0023, `8a73426`; продуктовый код не менялся — перезагрузка журнала после
действия намеренная, админ обязан видеть запись сразу).
Обратная сторона того же правила: сьют не должен догружать страницу руками. Ручной
`dispatchEvent(new Event("DOMContentLoaded"))` добавляет второй запуск обработчика — jsdom стреляет
этим событием сам на следующем тике после разбора: страница перерисовывается дважды, строки, которые
сьют успел снять, отсоединяются, и клик по такой строке не порождает запроса. Правка ADR-0026 — ручной
диспатч убран из `tests/ui/admin-roles.cjs`, `tests/ui/assignments-modal.cjs` и
`tests/ui/schedule-source-python.cjs`; приём был описан в `tests/ui/session-expiry.cjs:106-108`, но
обосновывался только счётчиком навигаций. Продуктовый код не тронут: в настоящем браузере страница
грузится один раз, двойной запуск придумывал тест.

Появились в T4.1: parser fixtures —
`internal/apeks/testdata/apeks-2026-10-week.json` (живой ответ источника за неделю,
из папки парсера владельца) и `TestParseMonthRealSample`, который сверяет с ним
каждое разобранное поле; сценарий «повторный импорт не создаёт дубликат» —
`TestReplaceMonthIsRepeatable`, плюс `TestReplaceMonthDropsStaleLessons` (пара,
убранная источником, исчезает и у нас), `TestReimportMovesLessonBetweenMonths`
(перенос занятия между месяцами не оставляет хвост) и
`TestScheduleSourceMigrationIsIdempotent`. Сбой источника тоже проверяется:
битый ответ не затирает уже импортированный месяц
(`TestImportBrokenPayloadKeepsOldMonth`), а текст ошибки источника не выдаёт
подробностей наружу (`TestImportWhenSourceFailsHidesDetails`). Публичность чтения
и закрытость записи — `TestScheduleGroupsAPIIsPublicAndHonestWhenEmpty`,
`TestScheduleStatusAPIOnlyForAdmin`, `TestSyncGroupsDeniedForTeacher`,
`TestSyncGroupsDeniedWithoutCSRF`, `TestSyncGroupsRequiresSession`; секретность
токена — `TestErrorMessagesCarryNoSecret` и `TestTransportErrorIsScrubbed`.

Добавлено ADR-0021 (`63f8cb3`): второй срез живого ответа —
`internal/apeks/testdata/apeks-groups-tree.json` (дерево «институт → курс → группа»,
в котором пустой уровень пришёл списком `[]`) с `TestParseGroupsOnLiveExcerpt` и
`TestParseGroupsRejectsUnknownNodeShape`; запасной Go-путь на этой форме проверяется
тестом `TestGetGroupsSurvivesEmptyLevels`, а пустые словари месяца —
`TestParseMonthAcceptsEmptyDictionaries`. Мост в дочерний процесс закрыт
`internal/apeks/python_test.go` (9 тестов: проверка среды до первого импорта, договор
аргументов и окружения, коды выхода, недостающий `external_id`, каталог групп, токен
спрятан от сообщений парсера, границы месяца общие с Go-клиентом; в том же файле —
две проверки паритета, `TestPythonParserMatchesGoParser` (пары) и
`TestPythonGroupsMatchGoParser` (каталог), которые действительно вызывают `python3` и
потому пропускаются без интерпретатора). Со стороны Python договор CLI держит
`tools/schedule-parser/test_contract.py` (17 проверок на обеих фикстурах), а всю связку
целиком — `tests/schedule-source-python.sh` с подставным парсером
`tests/fixtures/schedule-source.sh` и сьютом `tests/ui/schedule-source-python.cjs`.

Формат отказа (§4) проверяется с двух сторон. `internal/handlers/errors_test.go`
(7 тестов) — что конверт однопольный, `fields` отсутствует когда его нет, код
соответствует статусу по таблице, `invalid_json`, `csrf_failed` и `unauthorized`
из middleware сохраняют свой код при чужом для них статусе, и что в ответе больше
нет плоского `{"error": "строка"}`. `internal/platform/platform_test.go`
(`TestUnknownMethodIs405`, `TestDefaultErrorWriterKeepsTheEnvelope`) — то же от
имени транспорта, включая случай «`Middleware.Error` не передан». На уровень выше
`cmd/server/modules_test.go` (проверки 11 и 12) и `notfound_test.go` разбирают
конверт с настоящего `httptest`-сервера. Отдельно — коды на живых путях:
`TestLoginBruteForceIsLockedPerAccount` (429 → `rate_limited`),
`TestImportWhenSourceIsNotConfigured` и `TestImportWhenSourceFailsHidesDetails`
(503/502 → `source_unavailable`). `internal/apierror` своих тестов не имеет: весь
его контракт — таблица и конверт — проверяются теми же тестами там, где он
используется, и дублировать это вторым пакетом значило бы разъезжать двумя
копиями одного ожидания.

Прежние проверки формата искали в ответе подстроку `"error"` — она оставалась
справедливой и для старого плоского формата, поэтому именно она и пропустила
405 вне конверта. Теперь все эти места разбирают JSON.

Чего нет: store-интеграционные против PostgreSQL (и журнал, и расписание
проверяются на SQLite — PostgreSQL появится в T2.1), фоновый импорт по
расписанию (`IMPORT_INTERVAL` не реализован, месяц тянет человек кнопкой), и
Playwright (см. §9, ждёт решения о размещении).

## 12. Что делает Агент 1 до Этапа 3

Принять решения из 03, зафиксировать ADR-0001 (тоположение развёртывания), ADR-0002 (PostgreSQL + runner миграций), ADR-0003 (формат ошибок API), ADR-0004 (распределение ролей и `authz`), написать `docs/how-to-add-a-module.md`, обновить README (сейчас он расходится с кодом: CSRF помечен нереализованным, схема БД — 4 таблицы вместо 8).

Состояние: ADR-0001…ADR-0026 зафиксированы (05-DECISIONS.md), `docs/how-to-add-a-module.md`
написан и проверен добавлением тестового модуля, README приведён в соответствие
и дополнен разделом «Права (`authz`)». Осталось не выполненным из этого списка:
ADR-0001 реализован только как решение (Pages + внешний backend), относительные
пути и `deploy-frontend.yml` — за `T3.1`. Единый формат ошибок (ADR-0003)
выполнен отдельно от каркаса и позже него — `fcfc6c9` + `2f68b4e`: смена каркаса
и смена формата ответов шли двумя разными изменениями, как и договаривались, а
405 транспорта оказался тем местом, где миграция без отдельного прохода остаётся
незамеченной.

## 13. Транспорт фронтенда и состояние сессии ✅ (реализовано в T3.1, коммит `8fef933`, ADR-0011)

Слои и порядок подключения (он же проверяется `scripts/check-frontend.mjs`):

```
config.js → core/api-client.js → core/format.js → core/layout.js → common.js → скрипт страницы
```

- `config.js` — `window.SOS_CONFIG`: `API_BASE_URL` (пусто = same-origin),
  пути страниц (`"login"`, `"dashboard"`, `"./"`, `"login?revoked=1"`),
  `REQUEST_TIMEOUT_MS`, хук `navigateTo` для проверок.
- `core/api-client.js` — единственный, кто вызывает `fetch` (во всём фронтенде
  один такой вызов). 22 обращения страничных скриптов идут через `GS.fetchJSON` →
  `GS.api.request` (из них одно — `profile.js`, добавленное в `ae291e8`). Решает: базовый URL, `credentials`, CSRF-заголовок для
  небезопасных методов, таймаут, форму ошибки, политику 401.
- `core/format.js` — подписи и чипы ролей, `GS.can` (наборы «кому показывать
  кнопку»), даты, инициалы.
- `core/layout.js` — шапка и подвал (добавлено во второй половине T3.1,
  `ADR-0013`): страница задаёт вариант атрибутами `<body>` (`data-layout`,
  `data-brand-sub`, `data-footer-note`, `data-footer-stack`), компонент
  заполняет пустые `<header class="site-header">` / `<footer class="site-footer">`
  из таблицы `PAGES`. Вариантов семь: `index`, `login`, `register`, `dashboard`,
  `schedule`, `admin` и `profile` — последний добавлен в `ae291e8` (`ADR-0014`);
  у `dashboard` в навигации появилась ссылка «Профиль», у `admin` — тоже, а
  `profile` идёт в паре «В кабинет / Расписание». Рисует сразу при загрузке, повторный вызов защищён
  флагом `dataset.filled` — правки страничных скриптов (имя, скрытый `#adminLink`)
  не затираются. Идентификаторы сохранены прежние: на них смотрят `common.js`,
  скрипты страниц и jsdom-сьюты.
- `common.js` — `escapeHtml`, статусные сообщения, `fetchJSON`-обёртка,
  `bindLogout`, `describeServerError`/`fieldLabel` (подпись поля из конверта 422) и
  `bindCreateForm` — общий обработчик форм «создать запись и перезагрузить список»
  (три вызова: ресурс и событие на `schedule`, курс на `dashboard`; переехал из
  `schedule.js` сюда в ADR-0017, `3a6c8e9`); `GS` — свойство `window` (иначе
  core-файлы не смогли бы его
  дополнить: `const` верхнего уровня живёт в global lexical-области).

Политика 401: по умолчанию «уйти на вход»; там, где 401 — штатный ответ формы
(вход с неверным паролем, смена пароля с неверным текущим) или где страница
работает в гостевом режиме, вызов передаёт `on401: "throw"` (7 мест). Редирект
не срабатывает, если мы уже на странице входа — сравнение по имени последней
части пути, а не по хвосту (`endsWith` ловил бы `/relogin`, полное сравнение
ломается о подкаталог Pages).

Текст ошибки: сначала серверный (`error.message` из конверта §4), при пустом —
русская формулировка по статусу; для 429 всегда наша, с секундами из
`Retry-After`. `ApiError` несёт `status`, `code`, `message`, `fields`,
`retryAfter` и `data`; клиент читает `code` там, где различает случаи
(`account.js` — `csrf_failed`), а `fields` переводит в подпись поля через
`GS.fieldLabel` (`ADR-0003`, `fcfc6c9`). Разбор старого плоского формата в
клиенте сохранён нарочно: прокси и сервер более старой сборки отвечают именно
так, и «404 page not found» от прокси пользователь должен увидеть текстом, а не
пустой строкой.

## 14. Доступность фронтенда ✅ (реализовано в T0.5, коммит `1256959`, ADR-0016; связка поля с сообщением — `11ac07f`, ADR-0018; покрытие контраста — `487af91`, ADR-0019; состояния фокуса — `304f0cc`, ADR-0020)

Это контракт для новых страниц, а не «рекомендация»: пункты 1–4, 7 и 8 (в части статики)
проверяет `scripts/check-frontend.mjs` (красный CI), пункты 5 и 6 —
`tests/ui/accessibility.cjs` (работает офлайн, подменённым `fetch`), пункт 8 ещё и
живым замером — `scripts/check-focus.mjs` (вне CI, прогон ручной).

1. `<html lang="ru">`, заполненный `<title>`, ровно один `<h1>` и заголовки без
   перескока уровня (`h1` → `h3` не допускается; подписи секций — `h2`, а не `span`).
2. Первая ссылка страницы — `<a class="skip-link" href="#main">`, её цель существует
   и фокусируема (`<main id="main" tabindex="-1">`). `display:none` для такой ссылки
   не используется: вместе с ней исчезла бы и возможность ей воспользоваться.
3. Каждый `<th>` с `scope`. Таблицы в проекте — в `admin.html` (пользователи,
   журнал).
4. Текстовые пары токенов проходят WCAG 2.1 AA (4,5:1), не-текстовые (граница
   контрола `--line-strong`, пиктограмма `.btn-icon`) — 3:1 по 1.4.11. Значения
   берутся из `:root`, поэтому правка цвета в CSS замечается, а не проходит незаметно.
   Список из 26 пар закрыт проверкой покрытия (ADR-0019, `487af91`): любая комбинация
   «текст + подложка», которую задаёт блок `style.css` (в том числе через
   `--btn-fg`/`--btn-bg`) или инлайновый `style=""` в HTML, обязана быть в списке
   измеренных или в `CONTRAST_EXEMPT` с проверяемой причиной; подложка состояния
   (`:hover`, `:active`, `:focus`, `[disabled]`) обязана встречаться в измеренных парах.
   Сейчас в CSS 19 таких комбинаций, инлайновых пар в HTML ноль. Единственное
   освобождение — `#b3aa97` на `--parchment-deep` (1,58:1): подпись отключённой кнопки,
   которую WCAG 1.4.3 не требует, и которую в проекте `disabled` почти всегда прячет
   совсем (`.btn.is-loading { color: transparent }` + спиннер).
5. Шапка любой страницы с `data-layout` содержит `<nav>` с доступным именем —
   ориентир рисует `core/layout.js`, и на рабочих страницах он появился только в
   T0.5 (раньше там был `<div>`).
6. Модальное окно: `role="dialog"`, `aria-modal="true"`, `aria-labelledby`,
   ловушка фокуса на Tab/Shift+Tab, закрытие по Esc и возврат фокуса элементу,
   который открыл окно. Реализовано в `web/js/modals.js` с T0.1; сьютом проверяется,
   что это не разовое состояние, а поведение (`accessibility.cjs`, раздел 3).

7. Связь «поле → подсказка и строка сообщения» через `aria-describedby` (`ADR-0018`,
   `11ac07f`). В каждой форме, где есть строка с `role="status"`, на неё ссылается
   каждое поле с `class="control"` (`type="hidden"` не в счёт); подсказки
   `span.helper` тоже получают id и входят в ту же ссылку — сначала подсказка, потом
   сообщение. Радиогруппа ссылается на строку через `fieldset`, а не каждой меткой.
   Атрибут ставится в HTML, а не навешивается из JS: `404.html` скриптов не
   подключает намеренно, да и проверка иначе проверяла бы код, а не контракт.
   Разметку модалок и админки правило добирает по шаблонам JS: 7 атрибутов и 9 ссылок
   (6 и 8 в `modals.js`, 1 и 1 в `admin.js`).

8. Состояние фокуса показано тем, что видно (`ADR-0020`, `304f0cc`). Кольцо рисуется
   `box-shadow` или `outline` цветом из токенов, и этот цвет обязан держать 3:1 по
   WCAG 1.4.11 против **каждой** подложки, на которой компонент может стоять
   (`--parchment`, `--parchment-deep`, `--surface`) — держать «ту, что была под рукой
   при замере» нельзя: панель и фон страницы различаются, а `--teal-tint` даёт с ними
   1,03–1,10:1. Правило держат два слоя: группа 9 `check-frontend.mjs` (статика, CI —
   краснит новое невидимое кольцо и снятие контура без замены) и `check-focus.mjs`
   (живой замер: до каждого элемента на восьми страницах доходит `Tab`, и кадр элемента
   ±18 px меняется минимум на 24 пикселя с контрастом ≥3:1 к окружению). Снимать
   системный контур (`outline: none`) можно только если вместо него нарисовано кольцо из
   списка выше; освобождение только одно и с причиной — `.main:focus` (контейнер-цель
   skip-link, `tabindex="-1"`, компонентом управления не является). Новая страница со
   своим цветом фокуса обязана добавить пару в `FOCUS_SURFACES`-проверку, а не обходить
   её: `--focus-ring` как «свой оттенок на один блок» — это ровно тот путь, которым
   невидимое кольцо вернётся.

Чего сознательно нет: `aria-current` — ссылок на текущую страницу в шапке нет ни на
одной из семи страниц с `data-layout` (навигация действующая, а не разделы), и
атрибут был бы мёртвым кодом. Живой скринридер тоже не подключён: проверены
атрибуты, DOM, картинка состояний фокуса, но не то, как связка озвучивается в
NVDA/VoiceOver (последний открытый пункт ADR-0016 — он требует человека, а не машины
разработки). В живом прогоне фокуса модалки открываются (ADR-0022, `86edfe9`): пять
состояний диалога на `/dashboard`, 14 колец внутри окон, ловушка проверяется живым `Tab`, —
но возврат фокуса в кнопку-открыватель картинкой по-прежнему не меряется (это держат
статика и jsdom), а новый вид диалогового содержимого обязан получить свой случай в наборе
`MODALS` вручную. Ссылка
«одно поле → несколько сообщений» не заводилась: сейчас на поле приходится одна строка,
и если их станет несколько, порядок чтения — отдельное решение, а не правка атрибута.
Слабый переход «до → после» у светлых колец полей (18 замечаний по WCAG 2.2 SC 2.4.13 —
14 на страницах и 4 внутри открытых диалогов,
максимум 1,9:1) оставлен замечанием, а не провалом: проект заявляет 2.1 AA, и чтобы это
стало требованием, нужен ответ владельца (`docs/03` §3).
Из покрытия контраста осознанно выпадают пары, собранные через наследование (текст
в правиле, подложка у родителя или `body`): их 8 из 26, и держатся они на списке пар,
а не на разборе CSS; `rgba()`-подложки, оверлеи и градиенты в расчёт не берутся —
в `style.css` это тени и внутренние подсветки (ADR-0019, «что это не закрыло»).
