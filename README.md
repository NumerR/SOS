# SOS — Student OS

Учебная университетская платформа. Стек: HTML5 + CSS + Vanilla JavaScript на
фронте, Go (`net/http`, стандартная библиотека) на бэке, SQLite на чистом Go
(`modernc.org/sqlite`, без CGO). Без сборщиков, фреймворков и ORM.

Направление развития (по позиционированию репозитория): единое пространство для
студента — курсы, записи, задания, оценки, далее расписание и события.

## Реализовано

- регистрация и вход; пароль — только bcrypt-хэш;
- сессии в БД (`sessions`), токен в cookie `HttpOnly` + `SameSite=Lax`;
- роли `student` / `teacher` / `admin`; `admin` — только сидом из env, публичной
  регистрацией не выдаётся;
- каталог курсов: читают все авторизованные, создают только `teacher`/`admin`;
- записи на курсы (`enrollments`): студент записывается/отписывается,
  преподаватель видит список **своих** курсов, админ — любых;
- админ-панель `/admin` (список пользователей), закрыта `RequireRole`;
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
curl -b cookies.txt -X POST http://localhost:8080/api/enrollments \
  -H "Content-Type: application/json" -d '{"course_id":1}'
curl -b cookies.txt http://localhost:8080/api/enrollments/mine

# роль admin через публичную регистрацию отклоняется (ожидаем 422)
curl -i -X POST http://localhost:8080/api/auth/register \
  -H "Content-Type: application/json" \
  -d '{"username":"h","email":"h@example.com","full_name":"H","password":"password123","role":"admin"}'
```

## Что дальше (не реализовано)

- CSRF-токены для mutate-запросов;
- восстановление пароля, подтверждение email;