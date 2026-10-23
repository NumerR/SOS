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

## Структура

```txt
cmd/server/        точка входа, маршрутизация, сид админа
internal/database/ открытие SQLite + миграции схемы
internal/models/   доменные типы (User, Course, ...)
internal/store/    доступ к БД (users, sessions, courses, enrollments) + ошибки
internal/handlers/ HTTP-обработчики, middleware RequireAuth/RequireRole
web/               статика: HTML-страницы, css/, js/
data/              локальная база (создаётся при старте, в git не попадает)
```

## Схема БД

```sql
users(id, username UNIQUE, email UNIQUE, full_name, password_hash, role)
sessions(id PK, user_id FK->users, expires_at)
courses(id, title, description, teacher, created_by FK->users)
enrollments(id, user_id FK->users, course_id FK->courses, created_at, UNIQUE(user_id, course_id))
```

Миграции — `CREATE TABLE IF NOT EXISTS` в `internal/database/db.go`, применяются
при каждом старте; существующая `data/app.db` остаётся валидной.

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
go test ./...                            # лимитер + обработка аутентификации и аккаунта
cd tests/ui && npm install
npm run test:account                     # панель «Аккаунт и доступ» в jsdom
npm run test:all                         # UI заданий + роли админа + аккаунт
```

Прогон `tests/ui` требует сервер с админ-сидом и применённый `tests/ui/seed.sh`
(см. шапку скрипта).

## Что дальше (не реализовано)

- восстановление пароля, подтверждение email;