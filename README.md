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
- админ-панель `/admin` (список пользователей, выдача ролей и журнал действий),
  закрыта правом `admin.panel.view` / `user.list` + `RequireCSRF`; сама панель
  читает журнал отдельным правом `audit.view`;
- журнал действий (`audit_events`): вход и выход, «выйти везде», смена пароля,
  регистрация, смена роли и оценка работы пишутся в базу — успешно и неуспешно.
  Читает их только администратор через `GET /api/admin/audit` (фильтры по
  действию и автору, 50 записей по умолчанию, 200 потолок). Паролей,
  bcrypt-хешей, токенов сессий и CSRF-токенов в журнале нет — это проверяет
  тест, обходящий всю таблицу;
- права доступа описаны реестром (`internal/authz`): 24 права, у каждой роли свой
  набор, маршрут декларирует право и текст отказа, а обработчик проверяет только
  «эта строка моя» (обход — отдельные права `*.any`). Ролевые сравнения вида
  `user.Role == "admin"` из обработчиков удалены;
- расписание группы из официального источника (T4.1): Go-клиент АПЭКС-ВУЗ
  (`internal/apeks`) тянет каталог групп и пары за месяц, импорт лежит в базе
  (`schedule_source_groups` + `schedule_source_lessons`), страница `/schedule`
  показывает таблицу по дням с аудиториями, временем и датой последней загрузки —
  читается без входа. Токен источника только в `APEKS_TOKEN`, в логах, ответах API
  и журнале его нет (ADR-0015);
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
  обращения к БД, ответ `422` конвертом с именем поля (§ «Ошибки API»);
- фронтенд ходит в API через один транспорт (`web/js/core/api-client.js`,
  252 строки): базовый URL из `web/js/config.js` (пустой = same-origin),
  `credentials` для кросс-origin, CSRF-заголовок на небезопасных методах,
  таймаут через `AbortController`, ошибка одной формы (`status`/`code`/`message`/
  `fields`/`retryAfter`/`data`). Политику 401 («уйти на вход») решает клиент — формы со
  штатным 401 (вход, смена пароля, гостевые эндпоинты) передают
  `on401: "throw"`; подписи ролей, права для кнопок и даты собраны в
  `web/js/core/format.js`, пути страниц относительные — см. ADR-0011;
- шапка и подвал одним файлом: `web/js/core/layout.js` (244 строки) рендерит
  их по атрибутам `<body data-layout / data-brand-sub / data-footer-note /
  data-footer-stack>`, семь страниц перестали быть семью копиями разметки;
  навигация остаётся ссылками на страницы, id кнопок и ссылок сохранены,
  повторный вызов не затирает правки страничного скрипта (`dataset.filled`);
  неизвестный путь отдаёт оформленную `web/404.html` со статусом 404, а для `/api/*` —
  JSON — см. ADR-0013;
- дизайн-система: тёплый пергамент + neumorphism + единственный акцент
  (petrol-teal), спецификационная типографика, состояния controls
  (pressed/disabled/loading/focus/error), empty-state, скелетоны, таблица;
  контраст текстовых пар и границы поля проверены численно (см. раздел
  «Доступность интерфейса»);
- доступность интерфейса: skip-link с фокусируемой целью, один `<h1>` на страницу,
  `<h2>` вместо `<span>` в подписях секций, `scope` у `<th>`, `<nav>` с доступным
  именем в шапке, модальные окна с `role="dialog"`, ловушкой и возвратом фокуса.
  Падает в CI, если это пропадёт, — см. ADR-0016 и раздел «Доступность интерфейса».

## Матрица прав

| Действие                              | anonymous | student | teacher | admin |
|---------------------------------------|-----------|---------|---------|-------|
| `/`, `/login`, `/register`            | ✅        | ✅      | ✅      | ✅    |
| `/dashboard`                          | ↪ login   | ✅      | ✅      | ✅    |
| `/profile`                             | ↪ login   | ✅      | ✅      | ✅    |
| `/admin`                              | ↪ login   | ↪ dash  | ↪ dash  | ✅    |
| `GET /api/courses`                    | 401       | ✅      | ✅      | ✅    |
| `POST /api/courses`                   | 401       | 403     | ✅      | ✅    |
| `GET /api/enrollments/mine`           | 401       | ✅      | ✅(пусто)| ✅   |
| `POST /api/enrollments` (записаться)  | 401       | ✅      | 403     | ✅    |
| `POST /api/enrollments/cancel`        | 401       | ✅      | 403     | ✅    |
| `GET /api/enrollments/course?course_id=N` | 401   | 403     | ✅ свой | ✅ любой |
| `GET /api/admin/users`                | 401       | 403     | 403     | ✅    |
| `POST /api/admin/users/role`          | 401       | 403     | 403     | ✅ (не себе) |
| `GET /api/admin/audit`                | 401       | 403     | 403     | ✅    |
| `GET /api/schedule/groups`            | ✅        | ✅      | ✅      | ✅    |
| `GET /api/schedule/lessons`           | ✅        | ✅      | ✅      | ✅    |
| `GET /api/schedule/source/status`     | 401       | 403     | 403     | ✅    |
| `POST /api/schedule/source/groups`    | 401       | 403     | 403     | ✅ (+CSRF) |
| `POST /api/schedule/source/lessons`   | 401       | 403     | 403     | ✅ (+CSRF) |
| `POST /api/auth/logout`               | ✅ (без куки 200) | ✅ | ✅ | ✅ |
| `POST /api/auth/logout-all`           | 401       | ✅      | ✅      | ✅    |
| `POST /api/auth/change-password`      | 401       | ✅      | ✅      | ✅    |

## Права (`authz`)

Ролевые сравнения в обработчиках заменены реестром прав: маршрут декларирует
право, `RequirePerm` проверяет вход и право, обработчик — только владельца
строки. Всего 24 права: student 3, teacher 10, admin 23 (единственное, чего у
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
| `GET /api/admin/audit` | `audit.view` | admin (текст отказа: «only administrators can read the audit log») |
| `GET /api/schedule/groups`, `GET /api/schedule/lessons` | — (публичные, вход не нужен) | любой: это копии строк расписания для просмотра, а не персональные данные — см. ADR-0015 |
| `GET /api/schedule/source/status` | `schedule.source.manage` | admin |
| `POST /api/schedule/source/groups`, `POST /api/schedule/source/lessons` | `schedule.source.manage` | admin (+CSRF): синхронизация каталога групп и импорт пар из АПЭКС-ВУЗ |
| `/admin` (страница) | `admin.panel.view` | admin (иначе 302 на `/dashboard`) |
| `/dashboard`, `/profile` (страницы) | — (нужен вход) | все роли; отказа нет: обработчик страницы без сессии делает 302 на `/login` — HTML-страницы идут мимо реестра прав (`docs/02` §3) |
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

## Журнал действий

Каждый вход, выход, «выйти везде», смена пароля, регистрация, смена роли и
оценка работы (успешно и нет) пишутся в таблицу `audit_events`; видят их только
администраторы — на странице `/admin` (панель «Журнал действий») и через API:

```bash
curl -s -b cookies.txt "http://localhost:8080/api/admin/audit?limit=50"
curl -s -b cookies.txt "http://localhost:8080/api/admin/audit?action=admin.role_change&actor_user_id=1"
```

Метод — GET, поэтому `X-CSRF-Token` не нужен. Неверное имя действия, нечисловой
`actor_user_id` или `limit` дают `422` (в тексте ответа — список допустимых
имён); 50 записей по умолчанию и потолок 200 — чтобы нельзя было выгрузить весь
журнал одним запросом. Полей `password`, `password_hash`, токена сессии и
CSRF-токена в ответе нет, и в базе их быть не может: это проверяет тест, обходящий
всю таблицу (`internal/handlers/audit_test.go`). Причина отказа у события — в
колонке `ok`, а не в отдельном имени действия: `auth.login` и `ok=0` — это
неудачный вход.

## Расписание из официального источника

Страница `/schedule` показывает не ссылку на виджет вуза, а импортированные пары.
Источник — АПЭКС-ВУЗ; читается он сервером, браузер с вузовским API не знаком.

```bash
APEKS_TOKEN=<токен источника> go run ./cmd/server   # без него импорт выключен
APEKS_API_URL=https://…   # необязательное переопределение адреса источника
```

Токен берётся только из окружения и только на сервере. Он не попадает ни в одну
строку лога, ни в один ответ API, ни в журнал действий; в тексте ошибок источника
его тоже нет — `scrub` в `internal/apeks` вырезает URL запроса целиком, потому что
чужая HTTP-библиотека могла вернуть его внутри ошибки. Сам запрос идёт по HTTPS, и
клиент на http-схеме не конструируется (ADR-0015).

Без `APEKS_TOKEN` сервер стартует нормально: `GET /api/schedule/source/status`
отвечает `configured: false`, панель источника подсказывает, что настроить, а
ранее импортированные данные читаются как обычно.

```bash
curl -s "http://localhost:8080/api/schedule/groups"                       # каталог групп, без входа
curl -s "http://localhost:8080/api/schedule/lessons?group=494&month=10&year=2026"
curl -s -b cookies.txt -H "X-CSRF-Token: $CSRF" -X POST \
  "http://localhost:8080/api/schedule/source/groups"                      # админ: обновить каталог
curl -s -b cookies.txt -H "X-CSRF-Token: $CSRF" -X POST \
  "http://localhost:8080/api/schedule/source/lessons" \
  -H "Content-Type: application/json" -d '{"count":3}'                    # админ: импорт месяцев
```

Импорт месяца — замена всего батча в одной транзакции: повторный запуск не
создаёт дублей, а испорченный ответ не оставляет половину данных. Группа ищется
сначала по id, потом по имени; если имя подходит нескольким группам — `422` с
просьбой уточнить, а не «первая попавшаяся». Расписание по дню/неделе как
отдельный вид, планировщик импорта по `IMPORT_INTERVAL` и история запусков
(`import_runs`) пока не сделаны — месяц тянет человек кнопкой (docs/04, T4.1).

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

## Доступность интерфейса

Реализовано и проверяется автоматически (T0.5, `1256959`, ADR-0016):

- **Контраст** текстовых пар посчитан по формуле WCAG 2.1, а не «на глаз»: `--muted`
  был `#8d8676` — 2,81:1 на parchment и 2,48:1 на parchment-deep при пороге AA 4,5:1;
  стал `#625b4c` (5,22 / 5,56 / 4,62 / 6,73). `--danger` — `#a13f35` (текст отказа
  4,38 → 4,74 на своей подложке), для границы поля ввода добавлен `--line-strong`
  (3,08 и 3,28 при пороге 3 — раньше контур различался только по тени). Палитра
  не менялась: тон тот же, изменилась только светлота трёх значений.
- **Клавиатура**: skip-link первой ссылкой на всех восьми страницах и
  `<main id="main" tabindex="-1">` — без `tabindex` переход по якорю прокручивает
  страницу, но фокус остаётся в шапке.
- **Структура**: ровно один `<h1>` на страницу, `<h2>` для подписей секций (без
  перескоков уровня), `scope` у всех `<th>` в админке, `<nav aria-label="Навигация
  страницы">` в общей шапке — на «Кабинете», «Расписании», «Профиле» и в админке
  ориентира навигации не было вовсе.
- **Модальные окна**: `role="dialog"`, `aria-modal`, `aria-labelledby`, ловушка фокуса
  на Tab/Shift+Tab, Esc и возврат фокуса — это было сделано раньше (T0.1), теперь
  закрыто тестом, чтобы не «уехало» обратно.
- **Поле знает, где его подсказка и сообщение** (ADR-0018, `11ac07f`): `aria-describedby`
  в HTML ссылается на `span.helper` и на строку `role="status"` этой же формы — 22 атрибута
  и 29 ссылок на страницах, плюс 7 атрибутов в разметке, которую генерирует JS (`modals.js`,
  `admin.js`). Радиогруппа ролей описана один раз через `fieldset`, а не каждой меткой.
- **Чего нет сознательно**: `aria-current` — ни одна ссылка шапки не ведёт на страницу,
  где стоит (это панель действий, а не разделы), поэтому атрибут был бы мёртвым кодом.

Проверяется двумя слоями: `node scripts/check-frontend.mjs` (статика: падает в CI,
если заголовок убрали, токен посветлели или ссылка `aria-describedby` повела в
несуществующий id) и `npm run test:a11y` (поведение в DOM:
112 проверок, работает офлайн). Не проверено: как это озвучивает настоящий скринридер
и как выглядят новые состояния фокуса — браузер в этой работе не использовался.

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
                   schedule, audit — в каждом объявлены его маршруты
internal/apeks/    клиент и разбор официального источника расписания (T4.1):
                   HTTPS-гард, токен из окружения, `scrub` ошибок, фикстуры формата
                   в testdata/
internal/database/ открытие SQLite + миграции схемы
internal/models/   доменные типы (User, Course, ...)
internal/store/    доступ к БД (users, sessions, courses, enrollments,
                   assignments, submissions, resources, events, schedule,
                   audit, schedule_source) + ошибки
internal/ratelimit/ токен-бакет: лимиты на IP и счётчик неудачных входов
internal/handlers/ HTTP-обработчики и middleware: RequireAuth / RequirePerm /
                   RequireCSRF / RateLimit, логи + request id (logging.go),
                   дедлайн БД (deadline.go), границы полей (fields.go),
                   куки сессии с `__Host-` (handlers.go)
web/               статика: HTML-страницы (плюс 404.html), css/, js/ (config.js,
                   common.js, скрипты страниц; core/ — api-client.js, format.js
                   и layout.js: общая шапка и подвал)
data/              локальная база (создаётся при старте, в git не попадает)
tests/ui/          jsdom-тесты фронтенда: восемь сьютов — четыре офлайновых
                   (accessibility, schedule-forms, schedule-timetable, profile-page:
                   ответы подменяет fetch-заглушка) и четыре против живого сервера + seed
scripts/           check-frontend.mjs — восемь групп: связи HTML ↔ JS, id-мишени,
                   инлайновые обработчики, структура доступности страницы, контраст
                   токенов, единственность submit-механизма и ссылки aria-describedby;
                   всё без сборки
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
audit_events(id, at int, actor_user_id FK->users NULL, action, target_type, target_id,
             detail, ok int, ip, user_agent)   -- журнал действий (T1.2)
schedule_source_groups(id PK, source, external_id, name, department, course, updated_at,
             UNIQUE(source, external_id))   -- каталог групп источника (T4.1)
schedule_source_lessons(id PK, source, external_id, group_ext_id, month, year,
             day_key int (YYYYMMDD, 99999999 = даты нет), date, weekday, lesson_number,
             time, start_time, end_time, group_name, subgroup, discipline, lesson_type,
             room, teacher, imported_at, UNIQUE(source, group_ext_id, external_id))
                                        -- импортированные пары (T4.1)
```

Миграции — `CREATE TABLE IF NOT EXISTS` в `internal/database/db.go`, применяются
при каждом старте; существующая `data/app.db` остаётся валидной. Версионируемых
миграций и PostgreSQL пока нет — это T2.1 (ADR-0002). Путь к файлу базы —
`DB_PATH` (по умолчанию `data/app.db`).

`platform_module_migrations` — журнал самого runner'а (`internal/platform/migrations.go`):
модуль заявляет `Migrations()` с
номером версии и SQL, runner применяет каждую в отдельной транзакции, повторяющий
запуск пропускает уже применённые, ошибка SQL откатывает миграцию и не даёт
серверу стартовать. Из семи модулей `Migrations()` пусты у пяти — их таблицы
относятся к core-схеме выше. Реальные миграции у двух: модуль `audit`
(`2026-10-24_audit_events`) создаёт `audit_events`, модуль `schedule`
(`2026-10-24_schedule_source`) — две таблицы импорта расписания. Ни та, ни другая
не прописывается в `internal/database/db.go`: таблица появляется вместе с модулем,
который её читает (ADR-0010, ADR-0015).

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

## Ошибки API

Все отказы API выглядят одинаково (`ADR-0003`, `internal/apierror`):

```json
{
  "error": {
    "code": "validation",
    "message": "title is required",
    "fields": { "title": "title is required" }
  }
}
```

`code` читает код (он стабильный, текст — нет), `message` печатается человеку,
`fields` есть только когда сервер назвал поле. Ключ `fields` — имя из JSON
(`title`, `course_id`, `group`), а подпись к ней берёт у самой формы:
`GS.fieldLabel` ищет контрол по `name` и читает текст его `<label>`, поэтому
«title is required» показывается как «title is required — поле: Название» без
словаря соответствия.

| HTTP | `code` | |
| --- | --- | --- |
| 400 | `validation`, `invalid_json` | плохое значение / неразбираемое тело |
| 401 | `unauthorized` | сессии нет; сюда же «неверный текущий пароль» |
| 403 | `forbidden`, `csrf_failed` | нет права / не сверился токен |
| 404 | `not_found` | нет ресурса или маршрута |
| 405 | `method_not_allowed` | путь есть, метода нет — пишет платформа |
| 409 | `conflict` | логин или почта заняты |
| 422 | `validation` | валидация формы, вместе с `fields` |
| 429 | `rate_limited` | плюс заголовок `Retry-After` |
| 502, 503 | `source_unavailable` | источник расписания не ответил или не настроен |

Проверить живьём:

```bash
curl -s -X DELETE localhost:8080/api/events
# {"error":{"code":"method_not_allowed","message":"method not allowed"}}
curl -s localhost:8080/api/courses
# {"error":{"code":"unauthorized","message":"unauthorized"}}
curl -s localhost:8080/api/net-takogo
# {"error":{"code":"not_found","message":"route not found"}}
```

Отказ источника (`source_unavailable`, 502/503) появляется у auth-маршрута
расписания и требует сессию — например `POST /api/schedule/source/lessons` без
заданного `APEKS_TOKEN` даёт 503 с текстом «источник расписания не настроен».
Проверяется тестами `TestImportWhenSourceIsNotConfigured` и
`TestImportWhenSourceFailsHidesDetails`.

Единственное исключение — `/api/health`: он отвечает документом о состоянии
(`{"status":"error","db":"error"}` при 503), а не отказом API, потому что его
читает мониторинг.

## Тесты

```bash
gofmt -l . && go vet ./...               # должно быть пусто / чисто
go test ./...                            # лимитер + аутентификация, аккаунт,
                                         # логи/дедлайн, лимиты полей, куки
cd tests/ui && npm install
npm run test:timetable                   # таблица расписания (без сервера, 59 проверок)
npm run test:forms                       # формы создания: расписание + кабинет, и общая шапка
                                         # (без сервера, 80 проверок)
npm run test:profile                     # страница /profile (без сервера)
npm run test:a11y                        # доступность структуры, модалок и связки поля
                                         # с сообщением (без сервера, 112 проверок)
npm run test:account                     # панель «Аккаунт и доступ» на /profile
npm run test:session                     # истечение сессии: редирект, а не spinner
npm run test:all                         # все восемь сьютов: 286 офлайн + 91 против сервера
```

Go-тесты: `internal/ratelimit/limiter_test.go` (8 сценариев),
`internal/handlers/auth_test.go`, `account_test.go`, `fields_test.go`,
`logging_test.go`, `cookie_test.go` — 45 верхнеуровневых сценария и 23 подкейса
в `internal/handlers`; из них 15 сценариев (29 проверок) добавлены в T0.3,
12 — журнал действий в T1.2.
Добавлены в T1.1: `internal/authz/authz_test.go` (6 — матрица «роль × право» в
обе стороны, отказ неизвестному праву, целостность реестра, `ScheduleScope`),
`internal/platform/platform_test.go` (10 + 9 подтестов — диспетчер по методам,
405, catch-all, 7 случаев некорректного маршрута, прогоны миграций с откатом),
`cmd/server/modules_test.go` (2 — 14 живых проверок на настоящем списке модулей:
401 анониму, 403 студенту с текстом отказа, 201 преподавателю, 403 чужому
преподавателю, 201 админу через `*.any`, 405 на PUT, 404 на неизвестный путь,
CSRF 403→201).
Добавлены в T1.2: `internal/store/audit_test.go` (8 — первые store-тесты в
репозитории: порядок, NULL-актор, фильтры, пагинация при одинаковом `at`,
не-положительный `limit`, обрезка по рунам, `ON DELETE SET NULL` после удаления
учётки, валидация имён действий), `internal/handlers/audit_test.go` (12 — точка
записи на каждый случай, «сбой записи не меняет ответ запроса», «в журнале нет
паролей, хешей и токенов»), `cmd/server/audit_test.go` (4 — доступ только
администратору, цепочка оценки, фильтры и потолок `limit`, маршрут объявлен
модулем).
Добавлены во вторую половину T3.1 (`8b3a027`): `cmd/server/notfound_test.go` (5 —
неизвестная страница отдаёт `web/404.html` со статусом 404, глубокий путь —
то же, `/api/*` — JSON, известные страницы остались `200`, в 404 нет `<script>`
и `onclick=`). Добавлен в остатке T3.1 (`ae291e8`, ADR-0014):
`cmd/server/profile_test.go` (3 — аноним на `/profile` получает `302` на вход,
студент и преподаватель `200` с маркерами разметки, в ответе нет `password_hash`,
`csrf_token=` и админских маршрутов, после logout страница перестаёт отдаваться,
и несменённой осталась граница `/admin`).
Добавлены в T4.1 (`1baa480`, ADR-0015): `internal/apeks/client_test.go` и
`parser_test.go` (23 — разбор фикстуры, конфиг и HTTPS-гард, токен в запросе и его
отсутствие в текстах ошибок, таймаут и отмена, лимит размера ответа),
`internal/store/schedule_source_test.go` (15 — каталог, разрешение группы и
неоднозначность, замена месяца, диапазоны и порядок, статистика, идемпотентность
миграции), `internal/handlers/schedule_source_test.go` (21 — публичность чтения,
отказ записи без права/CSRF/сессии, импорт и запись в журнал, неконфигурированный
источник, скрытие подробностей источника). Добавлено в ADR-0003 (`fcfc6c9`,
`2f68b4e`): `internal/apierror/apierror_test.go` и тесты кодов в обработчиках —
всего по репозиторию 158
верхнеуровневых тестов и 52 подтеста в семи пакетах (6737 строк тестового Go
против 9122 строк продуктового). T0.5 (`1256959`), ADR-0017 (`3a6c8e9`) и ADR-0018
(`11ac07f`) продуктового Go не касались: числа те же, добавлены только JS-сьюты.

Четыре сьюта работают без сервера — 286 проверок: `accessibility.cjs` (112),
`schedule-forms.cjs` (80), `schedule-timetable.cjs` (59) и `profile-page.cjs` (35)
подменяют `fetch` роутером
или ошибкой, поэтому лимитер входов на них не тратится.
Остальные четыре (91 проверка) гоняют jsdom против живого сервера и требуют запущенного
`go run ./cmd/server`, `ADMIN_USERNAME`/`ADMIN_PASSWORD` в окружении
(иначе `admin-roles.cjs` не сможет войти) и применённого `tests/ui/seed.sh`
(см. его шапку — сид выдаёт роль `teacher` админом): `assignments-modal.cjs` 24,
`admin-roles.cjs` 24 (13 из
них — журнал: доступ, отрисовка, запись смены роли, отсутствие секретов),
`account-security.cjs` 29 (с `ae291e8` она проверяет `/profile`, а не кабинет) и
`session-expiry.cjs` 14. Всего 377 проверок в восьми сьютах.
«Живой» прогон — 17 входов (посчитано по логу сервера: `seed.sh` делает 4, четыре
живых сьюта — 13), а лимитер пропускает 20 на IP за
10 минут, поэтому второй прогон подряд на том же сервере падает на 429. Для чистого
повтора перезапускайте сервер (бакеты живут в памяти процесса) или поднимайте его на
свежей базе.
`schedule-forms.cjs` проверяет общий `GS.bindCreateForm` на трёх формах создания —
ресурс и событие на расписании, курс на кабинете (добавлена `3a6c8e9`): валидация до
запроса, блокировка и восстановление кнопки со SVG, сброс формы
только при успехе, перезагрузка списка, серверная ошибка не названа успехом,
401 уводит на вход; плюс разбор даты события и отрисовку шапки/подвала из
`core/layout.js`. `profile-page.cjs` проверяет страницу профиля: четыре строки
идентичности, подпись и чип роли, фолбэк имени на логин, «Загрузка…» не остаётся
при 500, сетевом сбое и мусорном теле, 401 уводит ровно одной навигацией, а смена
пароля по-прежнему обслуживается `account.js` уже на новом месте.
`schedule-timetable.cjs` проверяет таблицу расписания: гость видит данные и его
не уводит на вход (это и есть приёмка «не вечный spinner» для нового пути),
plural-формы и переход декабрь → январь, разрешение имени группы в id, экранирование
`<img onerror>` из источника, состояния 502/404/«источник не настроен», payload
импорта (без `count` и с `count:3`), и отдельно — что со страницы расписания не
уходит ни одного запроса на `tksu.ru`: браузер говорит только с нашим API.

Про `429` при повторном прогоне: вход ограничен 20 попытками на IP за 10 минут
(ADR-0006), а `./seed.sh` + `npm run test:all` — это 17 POST
`/api/auth/login` (посчитано по логу сервера: 14×200 + 3×401, неудачная попытка
тоже расходует бакет; три из них — проверки неверным паролем). Запас в три
попытки: второй прогон «поверх» первого
упирается в лимит на первом же логине; `account-security.cjs`
из-за этого возвращает seed-ный пароль и в ветке сбоя (иначе сорванный прогон
оставляет `s_stud` с временным паролем, и следующий запуск выглядит как
«пользователь не входит»).

## CI

`.github/workflows/ci.yml` — три работы:

| Работа | Команды | Что ловит |
|---|---|---|
| `go` | `gofmt -l .`, `go vet ./...`, `go build ./...`, `go test -race -count=1 ./...` | компиляцию, формат, гонки (в лимитере есть конкурентный тест) |
| `frontend` | `node --check` по каждому `web/js/**/*.js` (`find`), `node scripts/check-frontend.mjs` | синтаксис и связи: неподключённый скрипт, несуществующий `<script src>`/`<link href>`, `getElementById` на отсутствующий id, инлайновые `on*`-обработчики — и с T0.5 структура доступности (`lang`, `<title>`, один `<h1>`, разрывы уровней, skip-link и его фокусируемая цель, `scope` у `<th>`) и контраст пар токенов по формуле sRGB; с `3a6c8e9` ещё и единственность submit-механизма (`GS.bindCreateForm` определён один раз в `common.js`, страницы его вызывают, страничный submit с блокировкой кнопки — красный); с `11ac07f` ещё и связка `aria-describedby` (ссылка ведёт на существующий id, каждое поле формы со статус-строкой на неё ссылается, подсказка с id подключена, поле в шаблоне `modals.js` без атрибута — красное, и ноль ссылок или ноль статус-строк тоже красный) |
| `ui` | сервер + `bash tests/ui/seed.sh` + `npm ci` + `npm run test:all` | поведение страниц в jsdom против живого API |

Триггеры: `pull_request`, push в `main`, ручной запуск. Push в feature-ветку сам
по себе не запускает работу — прогоны, на которые ссылки в `docs/04`, это
события `pull_request` PR #2, который слит в `main` коммитом `64a1232`
([прогон на `main` — run 40071046674](https://github.com/NumerR/SOS/actions/runs/40071046674),
`completed / success`). Коммиты, сделанные после слияния (`0e230ad`, `1baa480`, `a54dc5d`,
`fcfc6c9`, `2f68b4e`, `b385770`, `1256959` + `fbc2401` — T0.5 и документация к нему,
`3a6c8e9` + документация за ним — ADR-0017, `11ac07f` + документация за ним — ADR-0018),
лежат в той же ветке и ждут нового
pull request'а: пока его нет, GitHub их не
проверял, и `docs/04` говорит об этом прямо, а не «CI зелёный». На `origin` ветка
запушена лишь до `a54dc5d` — push здесь делается по явной просьбе владельца.
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
(документация, `2f79eb8`), [40041370917](https://github.com/NumerR/SOS/actions/runs/40041370917)
(T1.1, `439a08b`), [40042500419](https://github.com/NumerR/SOS/actions/runs/40042500419)
(документация, `7796033`), [40044462737](https://github.com/NumerR/SOS/actions/runs/40044462737)
(T3.1, `8fef933`), [40045063515](https://github.com/NumerR/SOS/actions/runs/40045063515)
(документация, `20055c5`) и [40047906544](https://github.com/NumerR/SOS/actions/runs/40047906544)
(T1.2, `26066e8`) — все `completed / success`. Дальше по той же ветке:
[40049706093](https://github.com/NumerR/SOS/actions/runs/40049706093) (документация
к T1.2, `136c3ee`) и [40053487121](https://github.com/NumerR/SOS/actions/runs/40053487121)
(T3.1, вторая половина, `8b3a027`) — тоже `completed / success`.

## Что дальше (не реализовано)

- PostgreSQL и версионируемые миграции core-таблиц — T2.1 (ADR-0002), после него —
  сервис-контейнер PostgreSQL и dry-run миграций в CI, пересмотр дедлайна БД
  и размера пула; runner модульных миграций уже есть (`internal/platform`), но
  `Migrations()` пусты у шести модулей из семи, а SQL написан под SQLite —
  и `audit_events` вместе с остальной схемой переедет в T2.1;
- у журнала действий (T1.2 закрыт — `26066e8`, ADR-0012: `audit_events` пишется
  из 15 точек обработчиков и читается администратором) осталось два пункта, и оба
  не технические: срок хранения записей — в них идентификаторы, `ip` и
  `user_agent`, то есть персональные данные, — ждёт решения владельца (`docs/03`
  §3); и пагинация с фильтрами в панели администратора — API их отдаёт, страница
  показывает первые 50;
- раздача фронтенда через GitHub Pages и `deploy-frontend.yml` — остаток T3.1
  (ADR-0001, пункт 5: при публикации выбрать каталоги `login/index.html` или
  `404.html`-роутер); клиент, конфиг, политика 401 и относительные пути
  реализованы в `8fef933` (ADR-0011);
- публикация на Pages — остаток T3.1; сделаны общая шапка
  (`web/js/core/layout.js`), страница 404, один submit-обработчик вместо двух
  копий в `schedule.js` (`8b3a027`, ADR-0013) и страница `/profile` с панелью
  доступа (`ae291e8`, ADR-0014). Группа и предпочтения на ней не показаны:
  полей нет в схеме, а номер группы — часть источника расписания (`docs/03` B2);
- расписание из официального источника — T4.1 закрыт (`1baa480`, ADR-0015):
  каталог групп, импорт месяцев, хранение в базе, страница с таблицей по дням.
  Не закрыто внутри темы: режим «день/неделя» как отдельный вид просмотра,
  планировщик импорта (`IMPORT_INTERVAL`) и история запусков (`import_runs`),
  и — главное — живой ответ `apeks.tksu.ru`: сеть вуза с машины разработки
  недоступна, формат подтверждён фикстурами парсера и локальным TLS-стабом.
  Подписка студента на «свою» группу тоже не сделана: групповые сущности
  (`groups` с участниками) — это вопрос владельца, а не код;
- восстановление пароля, подтверждение email;
- распад `internal/handlers` и `internal/store` по модулям (каркас T1.1 к этому
  готов, но обработчики пока общий пакет); единый формат ошибки с `code` — §4
  docs/02, отдельным изменением.

Реализовано в T1.1 (`439a08b`): `authz.Can(роль, право)` вместо ролевых
сравнений, реестр маршрутов `internal/platform`, шесть модулей в
`internal/modules/*` — см. раздел «Права (`authz`)» и
[docs/how-to-add-a-module.md](docs/how-to-add-a-module.md).

Реализовано в T1.2 (`26066e8`): седьмой модуль `audit` — первый, который сам
приносит схему (миграция `2026-10-24_audit_events`) и маршрут
`GET /api/admin/audit` с правом `audit.view`; 15 точек записи в обработчиках,
панель «Журнал действий» в `/admin`. Ядро под это не правилось — см. ADR-0012.

Реализовано в T4.1 (`1baa480`): модуль `schedule` — второй, который приносит свою
схему (миграция `2026-10-24_schedule_source`: каталог групп и импортированные пары),
плюс пакет `internal/apeks` с клиентом официального источника. Страница `/schedule`
перестала быть ссылкой на виджет вуза. Ядро под это не правилось: новый маршрут
объявлен модулем, новое право — реестром.
