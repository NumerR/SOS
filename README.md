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
- расписание группы из официального источника (T4.1, ADR-0021): каталог групп и пары
  за месяц разбирает приложенный владельцем Python-парсер (`tools/schedule-parser/`,
  сервер запускает его как отдельный процесс), прежний Go-клиент (`internal/apeks`)
  остался запасным движком и включается `SCHEDULE_SOURCE=go`; импорт лежит в базе
  (`schedule_source_groups` + `schedule_source_lessons`), страница `/schedule`
  показывает таблицу по дням с аудиториями, временем и датой последней загрузки —
  читается без входа. Токен источника только в `APEKS_TOKEN` — он не попадает ни в
  аргументы запущенного процесса, ни в логи, ни в ответы API, ни в журнал (ADR-0015,
  ADR-0021);
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
  263 строки): базовый URL из `web/js/config.js` (пустой = same-origin),
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
  контраст текстовых пар, границ поля и подложек состояний проверен численно, и
  список пар обязан покрывать CSS (см. раздел «Доступность интерфейса» и ADR-0019);
- доступность интерфейса: skip-link с фокусируемой целью, один `<h1>` на страницу,
  `<h2>` вместо `<span>` в подписях секций, `scope` у `<th>`, `<nav>` с доступным
  именем в шапке, модальные окна с `role="dialog"`, ловушкой и возвратом фокуса,
  и кольцо фокуса, различимое на любой подложке — вид восьми страниц замерен живым
  браузером (ADR-0020).
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

`python3` серверу для старта не нужен: парсер расписания вызывается только при
импорте, и только если задан `APEKS_TOKEN` (см. раздел «Расписание из официального
источника»). Нет `python3` на хостинге — источник переводится на Go-движок переменной
`SCHEDULE_SOURCE=go`; сама страница при этом работает в любом случае, просто без импорта.

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
Разбирает ответ приложенный владельцем Python-парсер: сервер запускает
`tools/schedule-parser/fetch.py` отдельным процессом и читает его JSON. Прежде
писавшийся для этого Go-разбор (`internal/apeks`) не удалён, а стал запасным движком
(ADR-0021).

```bash
APEKS_TOKEN=<токен источника> go run ./cmd/server   # без него импорт выключен
APEKS_API_URL=https://…      # необязательное переопределение адреса источника
SCHEDULE_SOURCE=python       # по умолчанию; SCHEDULE_SOURCE=go — запасной движок
SCHEDULE_PARSER=…/fetch.py   # путь до парсера (по умолчанию tools/schedule-parser/fetch.py)
PYTHON_BIN=python3           # интерпретатор, если он называется иначе
```

Токен берётся только из окружения и только на сервере. Он не попадает ни в одну
строку лога, ни в один ответ API, ни в журнал действий; в тексте ошибок источника
его тоже нет — `scrub` в `internal/apeks` вырезает URL запроса целиком, потому что
чужая HTTP-библиотека могла вернуть его внутри ошибки. Сам запрос идёт по HTTPS, и
клиент на http-схеме не конструируется (ADR-0015). В дочерний процесс парсера токен
уезжает через `cmd.Env`, а не аргументом командной строки: argv видно в `ps` и он
попадает в логи падений (ADR-0021).

Движок выбирает оператор, а не упавшая попытка: автоматического отката с `python`
на `go` нет, потому что два разбора могут разойтись, и молчаливый переход показал бы
админу расписание, собранное не тем кодом, который он настроил. Выбранное имя
пишется в лог старта и отдаётся полем `engine` из `GET /api/schedule/source/status`.

Без `APEKS_TOKEN` — или с ним, но без `python3` и скрипта, если выбран движок
`python` — сервер стартует нормально: `GET /api/schedule/source/status` отвечает
`configured: false`, в `engine` при этом пустая строка (выдать имя движка там, где
пути нет, — значит нарисовать рабочий источник на странице, где его нет), а ранее
импортированные данные читаются как обычно. Причину сборки сервер знает сам и
называет её (ADR-0023): `newScheduleSourceClient` возвращает вместе с клиентом имя
переменной, из-за которой источника нет (`missing_config` в ответе — `APEKS_TOKEN`,
`PYTHON_BIN`, `APEKS_API_URL` или `SCHEDULE_SOURCE`), и готовую фразу для человека
(`hint`). Один текст читают панель источника, отказ 503 и запись в журнале действий,
поэтому при отсутствующем `python3` админ видит «проверьте `PYTHON_BIN` и
`SCHEDULE_PARSER`, запасной путь — `SCHEDULE_SOURCE=go`», а не совет завести токен,
который уже заведён. В браузер не уходят ни значения переменных, ни текст ошибки
дочернего процесса с абсолютными путями, ни то значение `SCHEDULE_SOURCE`, которое
ввёл оператор. Симметрия доведена до конца поправкой того же дня: при
поднявшемся источнике (`configured: true`) `missing_config` и `hint` пустые — «какая
переменная отсутствует» там, где ничего не отсутствует, вопроса не имеет, и `APEKS_TOKEN`
в ответе рядом с `configured: true` противоречил бы первому полю. Это нашёл не тест, а
прогон собранного сервера с чистого клона: панель поле в таком сочетании не показывает,
поэтому ни один слой его не сверял.

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
- **Покрытие контраста** (ADR-0019, `487af91`): считалось 13 вручную выбранных пар,
  теперь их 26, и список обязан накрывать CSS. Проверка разбирает `style.css` и
  инлайновые `style=""` в HTML: каждая комбинация «текст + подложка», которую задаёт
  блок (в том числе через `--btn-fg`/`--btn-bg`), обязана быть измерена или внесена в
  `CONTRAST_EXEMPT` с проверяемой причиной; подложка состояния (`:hover`, `:active`,
  `:focus`, `[disabled]`) обязана встречаться в измеренных парах — текст в таких
  правилах наследуется от базы. Посчитаны впервые состояния кнопки (8,57 и 10,30:1),
  подложка `--teal-tint`, три светлых литерала подписей на бирюзовом и пиктограмма
  `.btn-icon` (6,33:1 при пороге 3 по 1.4.11). Единственное освобождение — подпись
  отключённой кнопки (1,58:1): WCAG 1.4.3 её не требует, и в проекте `disabled` почти
  всегда означает отправку со скрытым текстом. Значения токенов не менялись.
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
- **Кольцо фокуса видно, а не только стоит** (ADR-0020, `304f0cc`): живой прогон headless
  браузером обошёл восемь страниц настоящими нажатиями Tab — 77 фокусируемых элементов, до
  всех доходит, `:focus-visible` совпал на всех — и сравнил два снимка одной области. Одна
  находка оказалась настоящей: у радиокнопки роли на `/register` кольцо рисовал только
  `--teal-tint` поверх `--surface` — 211 изменённых пикселей и ноль из них различимы
  (максимум 1,33:1), то есть фокус был, но не виделся. Кольцо стало тем же приёмом, что у
  кнопки (`--teal-tint` + внешний `--teal` 5 px): 148 пикселей с контрастом ≥3:1 при 6,23:1.
  Остальные 76 элементов правку не потребовали; продуктовый JS и HTML не менялись.
- **Замер дошёл до открытых модалок** (ADR-0022, `86edfe9`): тот же обход научился открывать
  диалог настоящими кликами и замерять внутри него — пять состояний на `/dashboard`, 14 колец
  у полей окна, ловушка фокуса проверена реальным `Tab` (держит в пяти случаях из пяти).
  Находка оказалась не про кольцо: обе формы диалога начинаются скрытым полем
  (`assignment_id`, `course_id`), оно было первым кандидатом на вход фокуса, а `focus()` на
  `input[type="hidden"]` фокус не переносит — то есть человек, открывший форму с клавиатуры,
  получал окно, которого он не видел: первый `Tab` шёл по странице под оверлеем. Скрытые поля
  исключены из кандидатов, и добавлена страховка — если фокус не принял ни один кандидат,
  `ModalManager` заходит в `#modalDialog` сам (у него есть `tabindex="-1"` и своё кольцо).
- **Чего нет сознательно**: `aria-current` — ни одна ссылка шапки не ведёт на страницу,
  где стоит (это панель действий, а не разделы), поэтому атрибут был бы мёртвым кодом.

Проверяется тремя слоями: `node scripts/check-frontend.mjs` (статика: падает в CI,
если заголовок убрали, токен посветлели, новая пара «текст + подложка» осталась без
расчёта, ссылка `aria-describedby` повела в несуществующий id, или фокус-правило показало
кольцо с контрастом меньше 3:1 хотя бы на одной из трёх подложек), `npm run test:a11y`
(поведение в DOM:
112 проверок, работает офлайн) и `node scripts/check-focus.mjs --base=http://127.0.0.1:8080`
(живой замер: 891 строка, npm-зависимостей нет, Chrome DevTools Protocol напрямую; обходит
восемь страниц и пять состояний открытого диалога одним и тем же кодом; коды
выхода — 0 видно везде, 1 есть невидимые, 2 проверка не выполнялась). Последний слой в
`ci.yml` сознательно не подключён: на раннере GitHub Actions нет браузера, а смысл проверки
в отрисовке, поэтому он ручной и вызывается перед релизом, трогающим CSS.
Не проверено: как это озвучивает настоящий скринридер (в NVDA/VoiceOver не смотрели) и куда
возвращается фокус после закрытия окна — внутрь открытого диалога обход заходит, а его
кольцо и замкнутость `Tab` замеряет, но возврат в кнопку-открыватель по-прежнему держат
только статика и jsdom. 18 замечаний живого прогона (14 на страницах и 4 внутри окон) — это
контраст перехода «до → после» (WCAG 2.2 SC 2.4.13); проектом заявлен 2.1 AA, и они не
провал.
Из статического покрытия контраста выпадают пары, собранные через наследование (текст
в правиле, подложка у родителя или `body`) — 8 из 26, их держит только список пар;
`rgba()`-подложки, оверлеи и градиенты в расчёт не берутся.

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
internal/apeks/    официальный источник расписания (T4.1, ADR-0021): интерфейс
                   `Backend` и два движка разбора — `Client` (Go: HTTPS-гард, `scrub`
                   ошибок) и `Python` (мост в дочерний процесс `fetch.py`: токен только
                   `cmd.Env`, лимиты вывода, таймаут, коды выхода); фикстуры формата
                   в testdata/ (срез живого ответа, токена в файлах нет)
tools/schedule-parser/
                   приложенный владельцем Python-парсер АПЭКС-ВУЗ: `parser.py`,
                   `api_client.py` (без зашитого токена), `groups.py`, точка входа
                   `fetch.py`, договор CLI и команды в `README.md`, офлайновые проверки
                   `test_parser.py` и `test_contract.py`; `main.py`/`renderer.py` лежат
                   как пришли и сервером не вызываются, `scraper.py` из архива не
                   перенесён (резёрвный сбор через headless-браузер платформе не нужен)
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
tests/ui/          jsdom-тесты фронтенда: девять сьютов — четыре офлайновых
                   (accessibility, schedule-forms, schedule-timetable, profile-page:
                   ответы подменяет fetch-заглушка) и пять против живого сервера + seed
tests/             schedule-source-python.sh — сквозная приёмка источника расписания:
                   свой сервер на свободном порту, своя база в mktemp, подставной
                   парсер вместо живого API; вызывается `npm run test:source` и в CI
                   это шаг job `ui` (fixtures/ — тот самый подставной парсер).
                   source-states-smoke.sh — шесть состояний источника на собранном
                   сервере по HTTP (панель, отказ импорта и журнал называют одну
                   переменную); `npm run test:source-states`, тоже шаг job `ui`
                   (ADR-0024)
scripts/           check-frontend.mjs — девять групп: связи HTML ↔ JS, id-мишени,
                   инлайновые обработчики, структура доступности страницы, контраст
                   токенов и его покрытие (каждая пара CSS обязана быть измерена),
                   единственность submit-механизма, ссылки aria-describedby и различимость
                   колец фокуса (цвет каждого фокус-правила обязан давать ≥3:1 против
                   каждой из трёх подложек); всё без сборки
                   check-focus.mjs — живой замер состояний фокуса headless браузером
                   по CDP без npm-зависимостей: обход настоящими Tab + два снимка одной
                   области, одним и тем же кодом для страницы и для открытого диалога
                   (MODALS — пять состояний на /dashboard, с проверкой ловушки фокуса);
                   требует запущенный сервер, сид и браузер, поэтому в CI не
                   стоит и прогоняется вручную
                   check-docs.mjs — согласованность документации с деревом (ADR-0025):
                   шесть групп — пути файлов (голое имя с номером строки не может вести
                   к двум файлам), координаты строк, хеши коммитов, заявления «N строк»,
                   команды npm и номера решений — на каждое упоминание ADR-NNNN обязана
                   быть запись в docs/05, а индексный диапазон обязан называть последнее
                   принятое решение; освобождения подписаны причиной и красные, если стали
                   не нужны, а пороги по числу найденных ссылок не дают проверки
                   притвориться зелёной при сломанном разборе
.github/workflows/ ci.yml: go (gofmt/vet/build/test -race, + python3 для паритета
                   двух движков), frontend (node --check, check-frontend, check-docs —
                   с полной историей ради сверки хешей), parser (офлайновые проверки
                   парсера),
                   ui (run-live.sh: свой сервер, база и сид + восемь сьютов +
                   приёмка источника + состояния источника)
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
заданного `APEKS_TOKEN` даёт 503 с текстом «источник расписания не настроен: …», где
подсказка после ADR-0023 называет настоящую причину сборки, а не всегда один и тот же
токен.
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
npm run test:timetable                   # таблица расписания (без сервера, 62 проверки)
npm run test:forms                       # формы создания: расписание + кабинет, и общая шапка
                                         # (без сервера, 80 проверок)
npm run test:profile                     # страница /profile (без сервера)
npm run test:a11y                        # доступность структуры, модалок и связки поля
                                         # с сообщением (без сервера, 112 проверок)
npm run test:account                     # панель «Аккаунт и доступ» на /profile
npm run test:session                     # истечение сессии: редирект, а не spinner
npm run test:all                         # восемь сьютов: 289 офлайн + 91 против сервера
npm run test:live                        # те же восемь, но на собственном стеке:
                                         # `tests/ui/run-live.sh` собирает сервер, берёт
                                         # свободный порт, поднимает свою базу в mktemp,
                                         # применяет сид и убирает всё по trap
npm run test:source                      # приёмка источника расписания: свой сервер
                                         # и подставной парсер, 21 проверка
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
`2f68b4e`): `internal/handlers/errors_test.go` (7 тестов — коды соответствуют статусам,
берутся только из объявленного набора, форма конверта, отдельные коды для битого JSON,
отказа CSRF, 401 из middleware и 405) и проверки кода на живых путях в
`internal/handlers/auth_test.go` и `schedule_source_test.go`; тест-файла у самого пакета
`internal/apierror` нет — конверт там собирается, а проверяется со стороны обработчика.
Добавлено в ADR-0021 (`63f8cb3`): `internal/apeks/python_test.go` (9 — договор моста
с `fetch.py`: аргументы и окружение, коды выхода 0/1/2 в разные ошибки, лимиты
вывода, таймаут, проверка среды до первого импорта, плюс два паритетных теста,
сверяющих разбор Python-парсера с Go-разбором на паре за месяц и на каталоге групп),
4 новых случая в `client_test.go`/`parser_test.go` на живую форму каталога (пустые
уровни приходят списком `[]`) и `internal/apeks/testdata/apeks-groups-tree.json` —
срез живого ответа, токена в файле нет.
Всего по репозиторию 176
верхнеуровневых тестов и 74 подтеста в семи пакетах (7785 строк тестового Go
против 9928 строк продуктового; ADR-0023 дал 175 и 70, поправка к нему —
расширенный `TestScheduleStatusHintSilentWhenSourceWorks`, а `2b4a685` —
четыре подтеста инварианта полей статуса). ADR-0024 (`a1e5fa6`) Go-чисел не сдвинула:
её слой — 229 строк шелла и 38 проверок против собранного сервера, продуктовый код не тронут.
ADR-0025 (`08c7104` + `5bde07a`) тоже не сдвинула ни Go, ни JS: её слой — `scripts/check-docs.mjs`,
403 строки, и читает он markdown, а не код.
T0.5 (`1256959`), ADR-0017 (`3a6c8e9`), ADR-0018
(`11ac07f`) и ADR-0019 (`487af91`) продуктового Go не касались: числа были те же; ADR-0018
добавил только JS-сьюты, а ADR-0019 — лишь статическую проверку и комментарии в CSS.
Паритетные тесты требуют `python3` на машине, где их гоняют, и без интерпретатора
пропускаются — поэтому `ci.yml` ставит `python3` в работу `go`: зелёный прогон не
должен означать «проверка не запускалась».

Четыре сьюта работают без сервера — 289 проверок: `accessibility.cjs` (112),
`schedule-forms.cjs` (80), `schedule-timetable.cjs` (62) и `profile-page.cjs` (35)
подменяют `fetch` роутером
или ошибкой, поэтому лимитер входов на них не тратится.
Остальные четыре (91 проверка) гоняют jsdom против живого сервера и требуют запущенного
`go run ./cmd/server`, `ADMIN_USERNAME`/`ADMIN_PASSWORD` в окружении
(иначе `admin-roles.cjs` не сможет войти) и применённого `tests/ui/seed.sh`
(см. его шапку — сид выдаёт роль `teacher` админом): `assignments-modal.cjs` 24,
`admin-roles.cjs` 24 (13 из
них — журнал: доступ, отрисовка, запись смены роли, отсутствие секретов),
`account-security.cjs` 29 (с `ae291e8` она проверяет `/profile`, а не кабинет) и
`session-expiry.cjs` 14. Всего 380 проверок в восьми сьютах, которые и собирает
`npm run test:all`. Девятый сьют (`schedule-source-python.cjs`, 21 проверка) в него
сознательно не входит: он поднимает собственный сервер на свободном порту и свою базу
в `mktemp`, чтобы не расходовать бюджет лимитера на чужом прогоне; вызывается
`npm run test:source` и стоит отдельным шагом в CI (см. § «CI»).
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
`schedule-source-python.cjs` (`63f8cb3`, ADR-0021) проверяет сам путь источника
сквозняком — от дочернего процесса до нарисованной страницы: статус отвечает
`engine: "python"`, импорт каталога и месяца доходит до базы, публичные маршруты
отдают строки гостю, `/schedule` в jsdom на настоящем `fetch` рисует 3 пары на 2 дня,
подсказка «обновлено» показывает имя импортированной группы, HTML из полей источника
вырезан на импорте и не стал элементом DOM, повторный импорт оставляет 3 строки, а не
6, панель источника гостю скрыта, пустой месяц различим от «нет данных», к источнику
напрямую страница не обращается, и в логе сервера нет значения токена — это
дополнительная проверка самого раннера, а не сьюта. Движком в этом прогоне выступает `/bin/sh` с подставным парсером
`tests/fixtures/schedule-source.sh` — живого API прогон не трогает и токен вуза не требует.

Про `429` при повторном прогоне: вход ограничен 20 попытками на IP за 10 минут
(ADR-0006), а `./seed.sh` + `npm run test:all` — это 17 POST
`/api/auth/login` (посчитано по логу сервера: 14×200 + 3×401, неудачная попытка
тоже расходует бакет; три из них — проверки неверным паролем). Запас в три
попытки: второй прогон «поверх» первого
упирается в лимит на первом же логине; `account-security.cjs`
из-за этого возвращает seed-ный пароль и в ветке сбоя (иначе сорванный прогон
оставляет `s_stud` с временным паролем, и следующий запуск выглядит как
«пользователь не входит»). Повторяемость прогона теперь не зависит от этой границы:
`bash tests/ui/run-live.sh` (он же `npm run test:live`, он же шаг работы `ui` в CI) поднимает
свой сервер на свободном порту и своей базой в `mktemp`, так что лимитер и сид каждый раз новые,
и убирает стек по trap.

## CI

`.github/workflows/ci.yml` — четыре работы:

| Работа | Команды | Что ловит |
|---|---|---|
| `go` | `gofmt -l .`, `go vet ./...`, `go build ./...`, `go test -race -count=1 ./...`; перед тестами ставится `python3` (`actions/setup-python`) | компиляцию, формат, гонки (в лимитере есть конкурентный тест); с `63f8cb3` — ещё и паритет двух движков разбора расписания: без интерпретатора `internal/apeks` пропускает два теста, и «зелёная» работа читалась бы как «движки совпадают», поэтому python3 в неё входит отдельным шагом |
| `frontend` | `node --check` по каждому `web/js/**/*.js` (`find`), `node scripts/check-frontend.mjs`, `node scripts/check-docs.mjs` (клон с `fetch-depth: 0`) | синтаксис и связи: неподключённый скрипт, несуществующий `<script src>`/`<link href>`, `getElementById` на отсутствующий id, инлайновые `on*`-обработчики — и с T0.5 структура доступности (`lang`, `<title>`, один `<h1>`, разрывы уровней, skip-link и его фокусируемая цель, `scope` у `<th>`) и контраст пар токенов по формуле sRGB, а с `487af91` и его покрытие (любая пара «текст + подложка» из блока `style.css` или инлайнового `style=""` обязана быть в списке измеренных или в `CONTRAST_EXEMPT` с причиной, подложка состояния — встречаться в измеренных парах); с `3a6c8e9` ещё и единственность submit-механизма (`GS.bindCreateForm` определён один раз в `common.js`, страницы его вызывают, страничный submit с блокировкой кнопки — красный); с `11ac07f` ещё и связка `aria-describedby` (ссылка ведёт на существующий id, каждое поле формы со статус-строкой на неё ссылается, подсказка с id подключена, поле в шаблоне `modals.js` без атрибута — красное, и ноль ссылок или ноль статус-строк тоже красный); с `304f0cc` ещё и различимость кольца фокуса (каждое правило с `:focus`/`:focus-visible`/`:focus-within`, снявшее системный контур или рисующее своё, обязано задать цвет с контрастом ≥3:1 против каждой из трёх подложек; рисуемый индикатор без посчитанного цвета и устаревшее именованное освобождение — красные); с ADR-0025 (`08c7104` и `5bde07a`) — и сама документация: ссылка на файл, которого нет в дереве, голое имя, ведущее к двум файлам, координата за концом файла, хеш несуществующего коммита, устаревшее число строк, команда npm, которой нет в `tests/ui/package.json`, упоминание решения, записи о котором в `docs/05` нет, и индексный диапазон, отставший от журнала решений; подписанные освобождения краснеют, когда стали не нужны, а пороги по числу найденных ссылок не дают проверке притвориться зелёной при сломанном разборе |
| `parser` | `python3 -m py_compile tools/schedule-parser/*.py`, `python3 test_parser.py`, `python3 tools/schedule-parser/test_contract.py` | приложенный Python-парсер: синтаксис всех модулей, его собственный тест из архива и договор CLI с Go-мостом на двух фикстурах живого ответа (17 проверок). `requests` не ставится: офлайновым проверкам он не нужен, а живого запроса из CI нет — токен вуза в настройках репозитория не публикуется |
| `ui` | `npm ci` + `npm run test:live` (это `bash tests/ui/run-live.sh`: сборка сервера, свободный порт, своя база в mktemp, сид, восемь сьютов, уборка по trap) + `bash tests/schedule-source-python.sh` + `bash tests/source-states-smoke.sh` | поведение страниц в jsdom против живого API; предыдущий шаг — сквозная приёмка источника расписания на подставном парсере (свой сервер, своя база, python для шага не нужен — дочерним процессом выступает `/bin/sh`); последний — шесть конфигураций источника на собранном сервере (`a1e5fa6`, ADR-0024): панель, текст 503 и запись журнала обязаны назвать одну и ту же переменную. Слой ловит то, чего не видит `go test`: проводку `main.go` → `handlers.Deps` (с пустыми полями в Deps все семь пакетов остаются `ok`, а слой падает — 29/38) |

Триггеры: `pull_request`, push в `main`, ручной запуск. Push в feature-ветку сам
по себе не запускает работу — прогоны, на которые ссылки в `docs/04`, это
события `pull_request` PR #2, который слит в `main` коммитом `64a1232`
([прогон на `main` — run 40071046674](https://github.com/NumerR/SOS/actions/runs/40071046674),
`completed / success`). Коммиты, сделанные после слияния (`0e230ad`, `1baa480`, `a54dc5d`,
`fcfc6c9`, `2f68b4e`, `b385770`, `1256959` + `fbc2401` — T0.5 и документация к нему,
`3a6c8e9` + документация за ним — ADR-0017, `11ac07f` + `54669b7` — ADR-0018 и документация
к нему, `487af91` + `319201c` — ADR-0019 и покрытие контраста, `304f0cc` + `7a8cfc6` —
ADR-0020 и приёмка состояний фокуса, `63f8cb3` + `2c09b03` — ADR-0021 и
приложенный Python-парсер, `86edfe9` + `b68f2a4` — ADR-0022 и приёмка фокуса
внутри открытых модалок, `b28a036` + `0337239` — ADR-0023 и настоящая причина
«источника нет», и поправка к ней: `4e22b33` (пустой `missing_config` при поднявшемся
источнике) + `8a73426` (ожидание перезагрузки журнала в сьюте ролей) + `e52cb69` (документация
к обоим) + `2b4a685` (тест инварианта полей статуса между состояниями) + `a242dcf`
(документация к нему) + `a1e5fa6` (ADR-0024: живые состояния источника как слой проверки)
+ `00773de` (документация к ADR-0024) + `08c7104` (ADR-0025: `scripts/check-docs.mjs` сверяет
документацию с деревом) + `5bde07a` (шестая группа того же слоя — номера решений) + этот
коммит (`admin-roles.cjs` различает таймаут и ответ сервера),
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
node scripts/check-docs.mjs               # сверяет документацию с деревом (ADR-0025)
python3 -m py_compile tools/schedule-parser/*.py
(cd tools/schedule-parser && python3 test_parser.py) && python3 tools/schedule-parser/test_contract.py
bash tests/schedule-source-python.sh    # сам поднимает сервер и базу, ничего не требует
bash tests/source-states-smoke.sh       # то же: шесть конфигураций источника на собранном сервере
ADMIN_USERNAME=admin ADMIN_PASSWORD='admin-pass-123' go run ./cmd/server &
ADMIN_USERNAME=admin ADMIN_PASSWORD='admin-pass-123' bash tests/ui/seed.sh
(cd tests/ui && npm install && npm run test:all)
```

Первые четыре строки — работа `go` и `frontend`, следующие две — `parser`, последние пять —
`ui`. Приёмка источника (`schedule-source-python.sh`) и состояния источника
(`source-states-smoke.sh`) не требуют ни сервера, ни сида:
они поднимают и убирают всё сами, на своём порту и в своей базе. Остальные UI-проверки
просят запущенный сервер, а `test:all` ещё и свежий бюджет лимитера входов. Последние три
строки можно заменить одним шагом — `(cd tests/ui && npm run test:live)`: тот же стек, что
и в работе `ui`, собирается и убирается сам, и повторный прогон не упирается ни в лимитер,
ни в изменённую предыдущим прогоном роль (`tests/ui/run-live.sh`).

Проверка, которой в `ci.yml` нет, — живой замер состояний фокуса: он требует
запущенный сервер, применённый сид и найденный браузер, а на GitHub-раннере браузера
нет. Прогоняется отдельно, когда правка касается CSS или разметки фокусируемых
элементов, — и в том числе разметки модального окна, потому что пять его состояний
открываются в этом же прогоне настоящими кликами (ADR-0022).

```bash
ADDR=:8174 DB_PATH=/tmp/sos-focus.db ADMIN_USERNAME=admin ADMIN_PASSWORD=… \
  go run ./cmd/server &            # порт и база — свои, чтобы не трогать :8080
SOS_BASE_URL=http://127.0.0.1:8174 ADMIN_USERNAME=admin ADMIN_PASSWORD=… \
  bash tests/ui/seed.sh
ADMIN_PASSWORD=… node scripts/check-focus.mjs --base=http://127.0.0.1:8174
ADMIN_PASSWORD=… node scripts/check-focus.mjs --base=http://127.0.0.1:8174 --page=/dashboard
ADMIN_PASSWORD=… node scripts/check-focus.mjs --base=http://127.0.0.1:8174 --no-modals
```

Скрипт сам ищет headless-оболочку Chromium (`--chrome=`, `CHROME_PATH`, кэш Playwright,
`/Applications`) и поднимает отдельный профиль во временном каталоге; если ничего не
найдено — код 2 и текст «проверка не выполнялась», а не зелёный результат. Учётки
проверяются до запуска браузера: страница `/admin` без `ADMIN_PASSWORD` не замеряется.
Диалоги живут на одной странице, поэтому `--no-modals` оставляет только восемь страниц,
а `--page=` с любой другой страницей печатает «модалки пропущены» — молча измерять
половину набора скрипт не должен. Ни одна из пяти цепочек ничего не отправляет: кнопка
«сохранить» только фокусируется и замеряется, поэтому прогон не создаёт и не меняет
данных. Живой прогон тратит бюджет лимитера входов (ADR-0006): 4 запроса у сида и
4 у скрипта — вход случается на каждой смене роли, а модалки идут после страниц и
требуют свою, поэтому с ADR-0022 их стало больше (было 2). После полного
`npm run test:all` (17 входов) прогону нужен перезапуск сервера или свежая база.

Детектор бага T0.1 работает в обе стороны: если убрать из `dashboard.html` строку
`<script src="/js/modals.js"></script>`, `check-frontend.mjs` падает с
`FAIL modals.js не подключён ни к одной странице`; если удалить сам файл —
`FAIL подключён несуществующий скрипт`.

Миграции на чистой PostgreSQL в CI появятся вместе с T2.1: сейчас отдельного
SQL-пакета нет, схему применяет код (`internal/database/db.go`).

Первый прогон на GitHub — [run 40037668304](https://github.com/NumerR/SOS/actions/runs/40037668304),
`success`, все три работы зелёные (Go / Фронтенд / UI на `ubuntu-latest`; на том срезе
их было три, с `63f8cb3` — четыре, и новая работа `parser` на GitHub ещё не
запускалась).
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
  полей нет в схеме, а номер группы — часть источника расписания, не профиль
  (`docs/03` B2 закрыт артефактом владельца в T4.1);
- расписание из официального источника — T4.1 закрыт (`1baa480`, ADR-0015; разбор
  переложен на приложенный Python-парсер — `63f8cb3`, ADR-0021): каталог групп, импорт
  месяцев, хранение в базе, страница с таблицей по дням. Формат ответа подтверждён уже
  не только фикстурами: живой прогон вручную принёс 587 групп и 98 пар за октябрь, и
  наборы строк обоих движков совпали. Не закрыто внутри темы: `python3` и `requests` на
  боевом хостинге (если их поставить нельзя — работает `SCHEDULE_SOURCE=go`), кто выдаёт
  токен источника и на какой срок (`docs/03` B2), режим «день/неделя» как отдельный вид
  просмотра, планировщик импорта (`IMPORT_INTERVAL`) и история запусков (`import_runs`).
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
плюс пакет `internal/apeks` с доступом к официальному источнику. Страница `/schedule`
перестала быть ссылкой на виджет вуза. Ядро под это не правилось: новый маршрут
объявлен модулем, новое право — реестром.

Дополнено в ADR-0021 (`63f8cb3`): `internal/apeks` — это больше не «клиент», а
интерфейс `Backend` с двумя движками, и разбор по умолчанию делает приложенный
Python-парсер из `tools/schedule-parser/`, который сервер запускает отдельным
процессом. Продуктового ядра правка не тронула: права, CSRF, частоту обращений и
запись в базу по-прежнему держит обработчик, а движок про сессию не знает.
