# SOS — Student OS

Учебная университетская платформа. Стек: HTML5 + CSS + Vanilla JavaScript на
фронте, Go (`net/http`, стандартная библиотека) на бэке, SQLite на чистом Go
(`modernc.org/sqlite`, без CGO). Без сборщиков, фреймворков и ORM.

Направление развития (по позиционированию репозитория): единое пространство для
студента — курсы, записи, задания, оценки, далее расписание и события.

## Реализовано

- регистрация и вход; пароль — только bcrypt-хэш; сравнение выполняется и для
  неизвестного логина (фиктивный хэш), чтобы ответ не различался по таймингу;
- ограничение частоты запросов на `POST /api/auth/login` и `/register`:
  токен-бакет в памяти процесса (`internal/ratelimit`), ответ `429` +
  `Retry-After`; отдельно считается бакет неудачных попыток на аккаунт;
- сессии в БД (`sessions`), токен в cookie `HttpOnly` + `SameSite=Lax`;
  просроченные сессии чистит фоновый сборщик (`cmd/server`: раз в час,
  старт сразу при запуске);
- «выйти везде» (`POST /api/auth/logout-all`) отзывает все сессии аккаунта,
  смена пароля (`POST /api/auth/change-password`) требует текущий пароль и
  отзывает все остальные сессии, оставляя текущую;
- роли `student` / `teacher` / `admin`. Публичная регистрация выдаёт только
  `student`; `teacher` и `admin` назначает администратор (`/admin`,
  `POST /api/admin/users/role`), `admin` дополнительно создаётся сидом из env.
  При смене роли все сессии пользователя отзываются;
- каталог курсов: читают все авторизованные, создают только `teacher`/`admin`;
- записи на курсы (`enrollments`): студент записывается/отписывается,
  преподаватель видит список **своих** курсов, админ — любых;
- админ-панель `/admin` (список пользователей и выдача ролей), закрыта
  `RequireRole` + `RequireCSRF`;
- CSRF для всех mutate-запросов: double-submit cookie `csrf_token` +
  заголовок `X-CSRF-Token`, токен выдаётся вместе с сессией на `register`
  и `login`; сравнение — constant-time;
- cookie сессии: `HttpOnly`, `SameSite=Lax`, `Secure` и имя `__Host-gs_session`
  при защищённом запросе (на локальном HTTP — `gs_session`, см. раздел
  «Работа за обратным прокси»);
- HTTP-сервер с таймаутами (заголовок/тело/запись/идеал/размер заголовков),
  graceful shutdown по `SIGINT`/`SIGTERM`, `context.WithTimeout` на каждый
  запрос к БД, `slog` JSON + `request_id`, `/api/health` с `db.PingContext`;
- границы длины всех текстовых полей и шкала оценки 0–10 — проверка до
  обращения к БД, ответ `422` с именем поля;
- дизайн-система: тёплый пергамент + neumorphism + единственный акцент
  (petrol-teal), спецификационная типографика, состояния controls
  (pressed/disabled/loading/focus/error), empty-state, скелетоны, таблица.

## Матрица прав

| Действие                              | anonymous | student | teacher | admin |
|---------------------------------------|-----------|---------|---------|-------|
| `/`, `/login`, `/register`            | ✅        | ✅      | ✅      | ✅    |
| `/dashboard`                          | ↪ login   | ✅      | ✅      | ✅    |
| `/admin`                              | ↪ login   | ↪ dash  | ↪ dash  | ✅    |
| `GET /api/courses`                    | 401       | ✅      | ✅      | ✅    |
| `POST /api/courses`                   | 401       | 403     | ✅      | ✅    |
| `GET /api/enrollments/mine`           | 401       | ✅      | ✅(пусто)| ✅   |
| `POST /api/enrollments` (записаться)  | 401       | ✅      | 403     | ✅    |
| `POST /api/enrollments/cancel`        | 401       | ✅      | 403     | ✅    |
| `GET /api/enrollments/course?course_id=N` | 401   | 403     | ✅ свой | ✅ любой |
| `GET /api/admin/users`                | 401       | 403     | 403     | ✅    |
| `POST /api/admin/users/role`          | 401       | 403     | 403     | ✅ (не себе) |
| `POST /api/auth/logout`               | ✅ (без куки 200) | ✅ | ✅ | ✅ |
| `POST /api/auth/logout-all`           | 401       | ✅      | ✅      | ✅    |
| `POST /api/auth/change-password`      | 401       | ✅      | ✅      | ✅    |

## Запуск

Из корня (сервер ищет `web/` и `data/` по относительному пути):

```bash
go mod tidy
go run ./cmd/server
```

Открыть `http://localhost:8080`. Двойным кликом по `index.html` (`file://`)
не открывать — статика и API не найдутся.

Порт и уровень логов берутся из окружения (`ADDR=:8090 go run ./cmd/server`,
`LOG_LEVEL=debug`), остальное — константы, см. раздел «Логи, таймауты и
остановка сервера».

## Администратор

Публичная регистрация роль `admin` не выдаёт. Админ создаётся сидом при старте,
если заданы env-переменные. Сид идемпотентен (`Ensure`): повторный запуск не
дублирует аккаунт.

После входа под `ADMIN_USERNAME` роль `teacher` (и любая другая) выдаётся
на странице `/admin` — в списке пользователей есть селект роли и кнопка
«Сохранить». Тот же механизм доступен и через API:

```bash
curl -b cookies.txt -X POST http://localhost:8080/api/admin/users/role \
  -H "Content-Type: application/json" -H "X-CSRF-Token: $CSRF" \
  -d '{"user_id":2,"role":"teacher"}'
```

Сменить собственную роль нельзя (409) — иначе админ может потерять доступ к
странице посреди сеанса.

Linux / macOS / Git Bash:

```bash
ADMIN_USERNAME=root ADMIN_EMAIL=root@example.com \
ADMIN_FULL_NAME="Главный администратор" ADMIN_PASSWORD="change-me-please" \
go run ./cmd/server
```

Windows PowerShell:

```powershell
$env:ADMIN_USERNAME="root"
$env:ADMIN_EMAIL="root@example.com"
$env:ADMIN_FULL_NAME="Главный администратор"
$env:ADMIN_PASSWORD="change-me-please"
go run ./cmd/server
```

## Сессии, «выйти везде», смена пароля

Три разных действия — и у каждого своя семантика, путать их нельзя:

| Запрос | Что делает | Текущая сессия |
|---|---|---|
| `POST /api/auth/logout` | выходить отсюда | отзывается |
| `POST /api/auth/logout-all` | выходить везде (компрометация) | отзывается, редирект на `/login?revoked=1` |
| `POST /api/auth/change-password` | сменить пароль | сохраняется, остальные отзываются |

Смена пароля требует текущий пароль — без этой проверки любая открытая вкладка
или XSS умел бы менять пароль. Ответ содержит `revoked_sessions`, интерфейс
показывает, сколько устройств было выброшено.

```bash
CSRF=$(awk '/csrf_token/ {print $7}' cookies.txt)

curl -b cookies.txt -H "X-CSRF-Token: $CSRF" -X POST http://localhost:8080/api/auth/change-password \
  -H "Content-Type: application/json" \
  -d '{"current_password":"password123","new_password":"another-secret1"}'

curl -b cookies.txt -H "X-CSRF-Token: $CSRF" -X POST http://localhost:8080/api/auth/logout-all
```

Оба эндпоинта закрыты `RequireAuth` + `RequireCSRF`, а смена пароля ещё и
лимитером по IP (6 запросов за 15 минут): текущий пароль внутри открытой сессии
тоже можно перебирать.

## Работа за обратным прокси

Ключ лимитера по умолчанию берётся из адреса TCP-соединения, а cookie `Secure`
ставится только при `r.TLS`. За реверс-прокси это неправильно (весь трафик
приходит с одного адреса), но и верить `X-Forwarded-*` без условий нельзя —
их подделывает любой клиент. Поэтому включается явно:

```bash
TRUST_PROXY=true go run ./cmd/server
```

С этим флагом `X-Forwarded-For` (первый адрес цепочки, только валидный IP)
становится ключом лимитера, а `X-Forwarded-Proto: https` — основанием выдавать
`Secure`-куку. Требование: прямой доступ к порту приложения должен быть закрыт,
иначе подмена заголовка обходит все лимиты. Без флага поведение честное и
консервативное: лимиты по IP прокси, куки по факту TLS.

При защищённом запросе сессия выдаётся под именем `__Host-gs_session`
(`HttpOnly`, `Secure`, `Path=/`, без `Domain`) — префикс запрещает поддоменам
и `Domain`-атрибуту подменить куку. На plain HTTP остаётся
`gs_session`: браузеры требуют `Secure` для `__Host-`, и строгое имя сломало бы
локальный прогон без сертификата. Чтение перебирает оба имени (предпочитая
префиксное), снятие всегда отдаёт оба, поэтому переход HTTP → HTTPS не оставляет
двойной сессии. `csrf_token` префикса не получает осознанно: его читает JS для
заголовка `X-CSRF-Token`.

## Логи, таймауты и остановка сервера

Логи — `slog` в JSON на stdout, по одной строке на запрос (msg — `http request`):

```json
{"level":"INFO","msg":"http request","request_id":"2daeeb370f2aed3b","method":"POST",
 "path":"/api/auth/login","status":200,"duration_ms":91,"bytes":99,"ip":"::1"}
```

Уровни: раздача статики — `DEBUG`, 2xx/3xx — `INFO`, 4xx — `WARN`, 5xx — `ERROR`
(`LOG_LEVEL=debug` включает `/css/`, `/js/`, `/favicon.ico`). В лог не пишутся
тела запросов, пароли и значения куки — только метаданные транспорта.
`request_id` генерируется на каждый запрос и возвращается в заголовке
`X-Request-Id`; чужой `X-Request-Id` принимается только при `TRUST_PROXY=true`,
только длиной ≤ 64 и без управляющих символов — иначе это канал лог-инъекции,
и идентификатор генерируется заново.

Таймауты (константы `cmd/server/main.go`, `internal/handlers/deadline.go`):
`ReadHeaderTimeout` 5 с — клиент, не досылающий заголовки (Slowloris) обрывается,
не успев занять горутину; `ReadTimeout` 10 с; `WriteTimeout` 30 с;
`IdleTimeout` 90 с; `MaxHeaderBytes` 64 КиБ. Все обращения к БД идут через
`WithDBDeadline` — `context.WithTimeout` 5 с на запрос, поэтому висячий SQL
не держит соединение (`db.SetMaxOpenConns(1)` делает это особенно заметным
на SQLite).

Остановка: `SIGINT`/`SIGTERM` → `server.Shutdown` даёт активным запросам
10 с долиться, затем `server.Close()`; фоновый сборщик протухших сессий
останавливается тем же контекстом, и процесс ждёт его выхода (не более 5 с).
Проверка:

```bash
go build -o /tmp/sos-server ./cmd/server
/tmp/sos-server & pid=$!
curl -s localhost:8080/api/health   # {"status":"ok","db":"ok"}
kill -TERM $pid; wait $pid; echo $? # «сервер остановлен корректно», код выхода 0
```

`/api/health` не статичен: он выполняет `db.PingContext` с таймаутом 1 с, и при
недоступной базе отвечает `503 {"status":"error","db":"error"}`.

Границы длины полей (`internal/handlers/fields.go`) проверяются до обращения к БД
и отвечают `422` с именем поля: название задания ≤ 200, описание ≤ 2000, текст сдачи
≤ 20000, комментарий оценки ≤ 2000, имя ≤ 200, категория ≤ 64, URL ≤ 2000,
оценка — шкала 0–10 (совпадает с `min`/`max` в `web/js/modals.js`). Счёт в рунах,
а не байтах: кириллический текст в 200 символов проходит.

## Структура

```txt
cmd/server/        точка входа: маршруты, таймауты сервера, graceful shutdown,
                   health с ping базы, сид админа, сборщик протухших сессий
internal/database/ открытие SQLite + миграции схемы
internal/models/   доменные типы (User, Course, ...)
internal/store/    доступ к БД (users, sessions, courses, enrollments,
                   assignments, submissions, resources, events, schedule) + ошибки
internal/ratelimit/ токен-бакет: лимиты на IP и счётчик неудачных входов
internal/handlers/ HTTP-обработчики и middleware: RequireAuth / RequireRole /
                   RequireCSRF / RateLimit, логи + request id (logging.go),
                   дедлайн БД (deadline.go), границы полей (fields.go),
                   куки сессии с `__Host-` (handlers.go)
web/               статика: HTML-страницы, css/, js/
data/              локальная база (создаётся при старте, в git не попадает)
tests/ui/          jsdom-тесты фронтенда против живого сервера + seed
scripts/           check-frontend.mjs — связи HTML ↔ JS без сборки
.github/workflows/ ci.yml: go (gofmt/vet/build/test -race), frontend, ui
```

## Схема БД

```sql
users(id, username UNIQUE, email UNIQUE, full_name, password_hash, role)
sessions(id PK, user_id FK->users, expires_at)
courses(id, title, description, teacher, created_by FK->users)
enrollments(id, user_id FK->users, course_id FK->courses, created_at, UNIQUE(user_id, course_id))
assignments(id, course_id FK->courses, title, description, due_date int, created_by FK->users)
submissions(id, assignment_id FK->assignments, user_id FK->users, content, score real DEFAULT -1,
            comment, submitted_at int, graded_at int, UNIQUE(assignment_id, user_id))
resources(id, title, category, url, description, created_by FK->users, created_at int)
events(id, title, description, event_date int, created_by FK->users, created_at int)
```

Миграции — `CREATE TABLE IF NOT EXISTS` в `internal/database/db.go`, применяются
при каждом старте; существующая `data/app.db` остаётся валидной. Версионируемых
миграций и PostgreSQL пока нет — это T2.1 (ADR-0002). Путь к файлу базы —
`DB_PATH` (по умолчанию `data/app.db`).

## Шрифт

В CSS первым в стеке стоит `Plus Jakarta Sans`, но CDN-подключение намеренно
убрано ради стабильности (офлайн/блокировка не должны ломать макет). Без
загрузки шрифта текст уходит в метрически близкий fallback
(`Segoe UI / system-ui`) — цвета, тени и раскладка не меняются. Чтобы вернуть
Jakarta, добавь в `<head>` каждой страницы:

```html
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Plus+Jakarta+Sans:wght@400;500;600;700;800&display=swap">
```

или положи `.woff2` в `web/fonts/` и опиши `@font-face` в `style.css`.

## Проверка API (curl)

```bash
curl http://localhost:8080/api/health

# студент регистрируется и записывается на курс 1
curl -c cookies.txt -X POST http://localhost:8080/api/auth/register \
  -H "Content-Type: application/json" \
  -d '{"username":"s1","email":"s1@example.com","full_name":"S","password":"password123","role":"student"}'

# mutate-запросы требуют double-submit CSRF: заголовок должен равняться cookie
CSRF=$(awk '/csrf_token/ {print $7}' cookies.txt)
curl -b cookies.txt -H "X-CSRF-Token: $CSRF" -X POST http://localhost:8080/api/enrollments \
  -H "Content-Type: application/json" -d '{"course_id":1}'
curl -b cookies.txt http://localhost:8080/api/enrollments/mine

# любая роль кроме student через публичную регистрацию отклоняется (ожидаем 422)
curl -i -X POST http://localhost:8080/api/auth/register \
  -H "Content-Type: application/json" \
  -d '{"username":"h","email":"h@example.com","full_name":"H","password":"password123","role":"teacher"}'
```

## Тесты

```bash
gofmt -l . && go vet ./...               # должно быть пусто / чисто
go test ./...                            # лимитер + аутентификация, аккаунт,
                                         # логи/дедлайн, лимиты полей, куки
cd tests/ui && npm install
npm run test:account                     # панель «Аккаунт и доступ» в jsdom
npm run test:all                         # UI заданий + роли админа + аккаунт
```

Go-тесты: `internal/ratelimit/limiter_test.go` (8 сценариев),
`internal/handlers/auth_test.go`, `account_test.go`, `fields_test.go`,
`logging_test.go`, `cookie_test.go` — 33 верхнеуровневых сценария и 23 подкейса
в `internal/handlers`; из них 15 сценариев (29 проверок) добавлены в T0.3.
UI-тесты гоняют jsdom против живого сервера, поэтому требуют запущенного
`go run ./cmd/server`, `ADMIN_USERNAME`/`ADMIN_PASSWORD` в окружении
(иначе `admin-roles.cjs` не сможет войти) и применённого `tests/ui/seed.sh`
(см. его шапку — сид выдаёт роль `teacher` админом).

## CI

`.github/workflows/ci.yml` — три работы:

| Работа | Команды | Что ловит |
|---|---|---|
| `go` | `gofmt -l .`, `go vet ./...`, `go build ./...`, `go test -race -count=1 ./...` | компиляцию, формат, гонки (в лимитере есть конкурентный тест) |
| `frontend` | `node --check` по каждому `web/js/*.js`, `node scripts/check-frontend.mjs` | синтаксис и связи: неподключённый скрипт, несуществующий `<script src>`/`<link href>`, `getElementById` на отсутствующий id, инлайновые `on*`-обработчики |
| `ui` | сервер + `bash tests/ui/seed.sh` + `npm ci` + `npm run test:all` | поведение страниц в jsdom против живого API |

Триггеры: `pull_request`, push в `main`, ручной запуск. Push в feature-ветку CI
не гоняется — для открытого PR это был бы второй прогон того же коммита.
`ADMIN_USERNAME`/`ADMIN_PASSWORD` в работе `ui` — пароль одноразовой базы внутри
раннера, а не секрет; настоящие учётные данные (будущий `DATABASE_URL`, токен
бота) в workflow не появляются.

Шаги можно прогнать локально — теми же командами:

```bash
gofmt -l . && go vet ./... && go test -race -count=1 ./...
for f in web/js/*.js; do node --check "$f"; done && node scripts/check-frontend.mjs
```

Детектор бага T0.1 работает в обе стороны: если убрать из `dashboard.html` строку
`<script src="/js/modals.js"></script>`, `check-frontend.mjs` падает с
`FAIL modals.js не подключён ни к одной странице`; если удалить сам файл —
`FAIL подключён несуществующий скрипт`.

Миграции на чистой PostgreSQL в CI появятся вместе с T2.1: сейчас отдельного
SQL-пакета нет, схему применяет код (`internal/database/db.go`).

Первый прогон на GitHub — [run 40037668304](https://github.com/NumerR/SOS/actions/runs/40037668304),
`success`, все три работы зелёные (Go / Фронтенд / UI на `ubuntu-latest`).

## Что дальше (не реализовано)

- PostgreSQL и версионируемые миграции — T2.1 (ADR-0002), после него —
  сервис-контейнер PostgreSQL и dry-run миграций в CI, пересмотр дедлайна БД
  и размера пула;
- `audit_events` (запись входа/выхода/оценки/смены роли в базу, а не в лог) — T1.2;
- конфиг `API_BASE_URL` во фронтенде, относительные пути и раздача статики через
  GitHub Pages — T1.1 (вариант размещения, ADR-0001);
- модуль расписания по группе/дате и импорт из официального источника — T4.1,
  заблокирован: источник не предоставлен (ADR-0004);
- восстановление пароля, подтверждение email;
- единый `authz.Can(user, perm)` вместо ролевых проверок по обработчикам — T3.1.