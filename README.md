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
  правом `admin.panel.view` / `user.list` + `RequireCSRF`;
- права доступа описаны реестром (`internal/authz`): 22 права, у каждой роли свой
  набор, маршрут декларирует право и текст отказа, а обработчик проверяет только
  «эта строка моя» (обход — отдельные права `*.any`). Ролевые сравнения вида
  `user.Role == "admin"` из обработчиков удалены;
- модульный каркас (`internal/platform` + `internal/modules/*`): маршруты каждого
  модуля объявлены в его пакете, список модулей — одна функция
  `moduleList()` в `cmd/server/modules.go`, маршрутизатор не стартует при
  некорректной декларации маршрута; у модулей есть миграции через общий runner
  (`platform_module_migrations`);
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
- фронтенд ходит в API через один транспорт (`web/js/core/api-client.js`,
  252 строки): базовый URL из `web/js/config.js` (пустой = same-origin),
  `credentials` для кросс-origin, CSRF-заголовок на небезопасных методах,
  таймаут через `AbortController`, ошибка одной формы (`status`/`message`/
  `retryAfter`/`code`). Политику 401 («уйти на вход») решает клиент — формы со
  штатным 401 (вход, смена пароля, гостевые эндпоинты) передают
  `on401: "throw"`; подписи ролей, права для кнопок и даты собраны в
  `web/js/core/format.js`, пути страниц относительные — см. ADR-0011;
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

## Права (`authz`)

Ролевые сравнения в обработчиках заменены реестром прав: маршрут декларирует
право, `RequirePerm` проверяет вход и право, обработчик — только владельца
строки. Всего 22 права: student 3, teacher 10, admin 21 (единственное, чего у
админа нет — `schedule.scope.created`, потому что есть `schedule.scope.all`).
Матрица зафиксирована тестом в обе стороны: `internal/authz/authz_test.go`.

| Маршрут | Право | Кто |
|---|---|---|
| `GET /api/courses`, `GET /api/schedule`, `GET /api/submissions/mine`, `GET /api/auth/me` | — (нужен вход) | все роли |
| `POST /api/courses`, `POST /api/resources`, `POST /api/events` | `course.create`, `resource.create`, `event.create` | teacher, admin |
| `POST /api/assignments` | `assignment.create` | teacher, admin (только свой курс; чужой — `assignment.create.any`, то есть admin) |
| `POST /api/enrollments`, `POST /api/enrollments/cancel` | `enrollment.manage` | student, admin |
| `GET /api/enrollments/course` | `course.students.view` | teacher, admin (свои курсы; admin — любые через `.any`) |
| `GET /api/submissions` | `submission.view` | teacher, admin (свои задания; admin — любые) |
| `POST /api/submissions/grade` | `submission.grade` | teacher, admin |
| `POST /api/submissions` | — (нужен вход) | все; без записи на курс — только admin (`submission.create.any`) |
| `GET /api/admin/users` | `user.list` | admin |
| `POST /api/admin/users/role` | `user.role.manage` | admin (не себе) |
| `/admin` (страница) | `admin.panel.view` | admin (иначе 302 на `/dashboard`) |
| `POST /api/auth/register`, `/login`, `/logout` | — (публичные) | любой |
| `POST /api/auth/logout-all`, `/change-password` | — (вход + CSRF) | все роли |

Права `*.any` (`submission.grade.any`, `assignment.view.any`, …) — единственный
способ обойти проверку «строка моя», и они видны в таблице прав, а не в `if`.
`event.source.manage` объявлено и выдано admin заранее — под источник событий
из T4.2; ни один текущий маршрут его не требует.

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
cmd/server/        точка входа: список модулей (modules.go), сборка маршрутизатора,
                   таймауты сервера, graceful shutdown, health с ping базы,
                   сид админа, сборщик протухших сессий
internal/platform/ ядро: Module/Migration/Route, Registry с валидацией, Mount
                   (диспетчер по методу, 405/404), runner модульных миграций
internal/authz/    реестр прав: роль → набор прав, Can, ValidPermission,
                   ScheduleScope (для ленты расписания)
internal/modules/  модули: accounts, courses, assignments, resources, events,
                   schedule — в каждом объявлены его маршруты
internal/database/ открытие SQLite + миграции схемы
internal/models/   доменные типы (User, Course, ...)
internal/store/    доступ к БД (users, sessions, courses, enrollments,
                   assignments, submissions, resources, events, schedule) + ошибки
internal/ratelimit/ токен-бакет: лимиты на IP и счётчик неудачных входов
internal/handlers/ HTTP-обработчики и middleware: RequireAuth / RequirePerm /
                   RequireCSRF / RateLimit, логи + request id (logging.go),
                   дедлайн БД (deadline.go), границы полей (fields.go),
                   куки сессии с `__Host-` (handlers.go)
web/               статика: HTML-страницы, css/, js/ (config.js, common.js,
                   скрипты страниц; core/ — api-client.js и format.js)
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
platform_module_migrations(module, version, applied_at, PK(module, version))  -- журнал runner'а T1.1
```

Миграции — `CREATE TABLE IF NOT EXISTS` в `internal/database/db.go`, применяются
при каждом старте; существующая `data/app.db` остаётся валидной. Версионируемых
миграций и PostgreSQL пока нет — это T2.1 (ADR-0002). Путь к файлу базы —
`DB_PATH` (по умолчанию `data/app.db`).

Таблица `platform_module_migrations` — единственная, которую создаёт общий runner
(`internal/platform/migrations.go`): модуль заявляет `Migrations()` с номером
версии и SQL, runner применяет каждую в отдельной транзакции, повторяющий
запуск пропускает уже применённые, ошибка SQL откатывает миграцию и не даёт
серверу стартовать. Сейчас `Migrations()` пусты у всех шести модулей — их
таблицы относятся к core-схеме выше.

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
npm run test:session                     # истечение сессии: редирект, а не spinner
npm run test:all                         # все четыре сьюта (20 + 11 + 21 + 14)
```

Go-тесты: `internal/ratelimit/limiter_test.go` (8 сценариев),
`internal/handlers/auth_test.go`, `account_test.go`, `fields_test.go`,
`logging_test.go`, `cookie_test.go` — 33 верхнеуровневых сценария и 23 подкейса
в `internal/handlers`; из них 15 сценариев (29 проверок) добавлены в T0.3.
Добавлены в T1.1: `internal/authz/authz_test.go` (6 — матрица «роль × право» в
обе стороны, отказ неизвестному праву, целостность реестра, `ScheduleScope`),
`internal/platform/platform_test.go` (10 + 9 подтестов — диспетчер по методам,
405, catch-all, 7 случаев некорректного маршрута, прогоны миграций с откатом),
`cmd/server/modules_test.go` (2 — 14 живых проверок на настоящем списке модулей:
401 анониму, 403 студенту с текстом отказа, 201 преподавателю, 403 чужому
преподавателю, 201 админу через `*.any`, 405 на PUT, 404 на неизвестный путь,
CSRF 403→201). Всего по репозиторию: 59 верхнеуровневых тестов и 32 подтеста.
UI-тесты гоняют jsdom против живого сервера, поэтому требуют запущенного
`go run ./cmd/server`, `ADMIN_USERNAME`/`ADMIN_PASSWORD` в окружении
(иначе `admin-roles.cjs` не сможет войти) и применённого `tests/ui/seed.sh`
(см. его шапку — сид выдаёт роль `teacher` админом). Всего 66 проверок:
`assignments-modal.cjs` 20, `admin-roles.cjs` 11, `account-security.cjs` 21,
`session-expiry.cjs` 14.

Про `429` при повторном прогоне: вход ограничен 20 попытками на IP за 10 минут
(ADR-0006), а `./seed.sh` + `npm run test:all` — это 17 POST
`/api/auth/login` (посчитано по логу сервера: 16×200 + 1×401, неудачная попытка
тоже расходует бакет). Запас в три входа: второй прогон «поверх» первого
упирается в лимит на первом же логине; `account-security.cjs`
из-за этого возвращает seed-ный пароль и в ветке сбоя (иначе сорванный прогон
оставляет `s_stud` с временным паролем, и следующий запуск выглядит как
«пользователь не входит»).

## CI

`.github/workflows/ci.yml` — три работы:

| Работа | Команды | Что ловит |
|---|---|---|
| `go` | `gofmt -l .`, `go vet ./...`, `go build ./...`, `go test -race -count=1 ./...` | компиляцию, формат, гонки (в лимитере есть конкурентный тест) |
| `frontend` | `node --check` по каждому `web/js/**/*.js` (`find`), `node scripts/check-frontend.mjs` | синтаксис и связи: неподключённый скрипт, несуществующий `<script src>`/`<link href>`, `getElementById` на отсутствующий id, инлайновые `on*`-обработчики |
| `ui` | сервер + `bash tests/ui/seed.sh` + `npm ci` + `npm run test:all` | поведение страниц в jsdom против живого API |

Триггеры: `pull_request`, push в `main`, ручной запуск. Push в feature-ветку сам
по себе не запускает работу — прогоны, на которые ссылки в `docs/04`, это
события `pull_request` открытого PR #2 (для уже залитого коммита GitHub не
дублирует прогон).
`ADMIN_USERNAME`/`ADMIN_PASSWORD` в работе `ui` — пароль одноразовой базы внутри
раннера, а не секрет; настоящие учётные данные (будущий `DATABASE_URL`, токен
бота) в workflow не появляются.

Шаги можно прогнать локально — теми же командами:

```bash
gofmt -l . && go vet ./... && go test -race -count=1 ./...
found=0; while IFS= read -r f; do found=$((found+1)); node --check "$f"; done < <(find web/js -name '*.js' | sort); echo "файлов: $found"
node scripts/check-frontend.mjs
```

Детектор бага T0.1 работает в обе стороны: если убрать из `dashboard.html` строку
`<script src="/js/modals.js"></script>`, `check-frontend.mjs` падает с
`FAIL modals.js не подключён ни к одной странице`; если удалить сам файл —
`FAIL подключён несуществующий скрипт`.

Миграции на чистой PostgreSQL в CI появятся вместе с T2.1: сейчас отдельного
SQL-пакета нет, схему применяет код (`internal/database/db.go`).

Первый прогон на GitHub — [run 40037668304](https://github.com/NumerR/SOS/actions/runs/40037668304),
`success`, все три работы зелёные (Go / Фронтенд / UI на `ubuntu-latest`).
Последующие прогоны той же ветки: [40038860104](https://github.com/NumerR/SOS/actions/runs/40038860104)
(документация, `2f79eb8`) и [40041370917](https://github.com/NumerR/SOS/actions/runs/40041370917)
(T1.1, `439a08b`) — оба `completed / success`.

## Что дальше (не реализовано)

- PostgreSQL и версионируемые миграции core-таблиц — T2.1 (ADR-0002), после него —
  сервис-контейнер PostgreSQL и dry-run миграций в CI, пересмотр дедлайна БД
  и размера пула; runner модульных миграций уже есть (`internal/platform`), но
  `Migrations()` у всех шести модулей пусты, а SQL написан под SQLite;
- `audit_events` (запись входа/выхода/оценки/смены роли в базу, а не в лог) — T1.2;
- раздача фронтенда через GitHub Pages и `deploy-frontend.yml` — остаток T3.1
  (ADR-0001, пункт 5: при публикации выбрать каталоги `login/index.html` или
  `404.html`-роутер); клиент, конфиг, политика 401 и относительные пути
  реализованы в `8fef933` (ADR-0011);
- общий шапка/футер (`web/js/core/layout.js`), `profile.html` и страница 404,
  повторяющийся submit-обработчик в `schedule.js` — остаток T3.1;
- модуль расписания по группе/дате и импорт из официального источника — T4.1,
  заблокирован: источник не предоставлен (ADR-0004);
- восстановление пароля, подтверждение email;
- распад `internal/handlers` и `internal/store` по модулям (каркас T1.1 к этому
  готов, но обработчики пока общий пакет); единый формат ошибки с `code` — §4
  docs/02, отдельным изменением.

Реализовано в T1.1 (`439a08b`): `authz.Can(роль, право)` вместо ролевых
сравнений, реестр маршрутов `internal/platform`, шесть модулей в
`internal/modules/*` — см. раздел «Права (`authz`)» и
[docs/how-to-add-a-module.md](docs/how-to-add-a-module.md).