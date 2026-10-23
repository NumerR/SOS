#!/usr/bin/env bash
# Сид для ручной и автоматической проверки UI: преподаватель, студент, курс,
# задание с «вредным» названием (проверка экранирования) и запись на курс.
#
# Сервер должен быть запущен с админ-сидом — роль преподавателя больше нельзя
# получить при регистрации, её выдаёт администратор:
#
#   ADMIN_USERNAME=admin ADMIN_PASSWORD='admin-pass-123' go run ./cmd/server
#   cd tests/ui && ./seed.sh && npm test
#
# CSRF-токен сервер теперь выдаёт вместе с сессией (login/register), поэтому
# отдельный GET /api/auth/me больше не нужен.
set -u
BASE="${SOS_BASE_URL:-http://localhost:8080}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

: "${ADMIN_USERNAME:?ADMIN_USERNAME не задан — см. шапку этого скрипта}"
: "${ADMIN_PASSWORD:?ADMIN_PASSWORD не задан — см. шапку этого скрипта}"

printf '%s' '{"username":"t_teacher","email":"t1@example.com","full_name":"Petrov P. P.","password":"password123","role":"student"}' > "$TMP/teacher.json"
printf '%s' '{"username":"s_stud","email":"s1@example.com","full_name":"Sidorov S. S.","password":"password123","role":"student"}' > "$TMP/student.json"
printf '%s' '{"identifier":"'"$ADMIN_USERNAME"'","password":"'"$ADMIN_PASSWORD"'"}' > "$TMP/admin_login.json"
printf '%s' '{"title":"Matanaliz","teacher":"Petrov P. P.","description":"Kurs 1"}' > "$TMP/course.json"
printf '%s' '{"course_id":1,"title":"HW '"'"'tests'"'"' <img src=x onerror=alert(1)>","description":"Pervaya rabota","due_date":"2026-11-01T10:00:00.000Z"}' > "$TMP/assignment.json"
printf '%s' '{"course_id":1}' > "$TMP/enroll.json"

csrf() { awk '/csrf_token/ {print $7}' "$1"; }

post() { # cookiejar payload url — POST с CSRF-заголовком
  local jar="$1" payload="$2" url="$3"
  curl -s -b "$jar" -H "X-CSRF-Token: $(csrf "$jar")" -o /dev/null -w "%{http_code} $url\n" \
    -X POST "$BASE$url" -H "Content-Type: application/json" --data @"$payload"
}

# login <cookiejar> <payload> — только код ответа в stdout; подписи печатает вызывающий код.
login() {
  curl -s -c "$1" -o /dev/null -w "%{http_code}" \
    -X POST "$BASE/api/auth/login" -H "Content-Type: application/json" --data @"$2"
}

echo "=== аккаунты: создаются один раз, дальше просто вход ==="
# Регистрация ограничена по IP (10/час), поэтому повторный прогон сида должен
# входить в существующие учётки, а не пытаться создать новые.
printf '%s' '{"identifier":"t_teacher","password":"password123"}' > "$TMP/teacher_login.json"
printf '%s' '{"identifier":"s_stud","password":"password123"}' > "$TMP/student_login.json"

if [ "$(login "$TMP/teacher.cookies" "$TMP/teacher_login.json")" = "200" ]; then
  echo "   преподаватель уже существует — регистрация пропущена"
else
  curl -s -c "$TMP/teacher.cookies" -o /dev/null -w "%{http_code} register t_teacher\n" \
    -X POST "$BASE/api/auth/register" -H "Content-Type: application/json" --data @"$TMP/teacher.json"
fi

if [ "$(login "$TMP/student.cookies" "$TMP/student_login.json")" = "200" ]; then
  echo "   студент уже существует — регистрация пропущена"
else
  curl -s -c "$TMP/student.cookies" -o /dev/null -w "%{http_code} register s_stud\n" \
    -X POST "$BASE/api/auth/register" -H "Content-Type: application/json" --data @"$TMP/student.json"
fi

echo "=== вход администратора и выдача роли ==="
curl -s -c "$TMP/admin.cookies" -o /dev/null -w "%{http_code} admin login\n" \
  -X POST "$BASE/api/auth/login" -H "Content-Type: application/json" --data @"$TMP/admin_login.json"

TEACHER_ID=$(curl -s -b "$TMP/admin.cookies" "$BASE/api/admin/users" | python3 -c '
import json, sys
users = json.load(sys.stdin)
print(next((u["id"] for u in users if u["username"] == "t_teacher"), ""))')

if [ -z "$TEACHER_ID" ]; then
  echo "не удалось найти t_teacher в списке пользователей" >&2
  exit 1
fi

printf '%s' '{"user_id":'"$TEACHER_ID"',"role":"teacher"}' > "$TMP/role.json"
post "$TMP/admin.cookies" "$TMP/role.json" /api/admin/users/role

# Смена роли отзывает сессии пользователя — преподавателю нужно войти заново.
echo "=== повторный вход преподавателя (после отзыва сессий) ==="
echo "   $(login "$TMP/teacher.cookies" "$TMP/teacher_login.json") teacher login"

echo "=== курс, задание и запись (без дублей) ==="
if curl -s -b "$TMP/teacher.cookies" "$BASE/api/courses" | grep -q '"title":"Matanaliz"'; then
  echo "   курс уже есть — пропуск"
else
  post "$TMP/teacher.cookies" "$TMP/course.json" /api/courses
fi

if curl -s -b "$TMP/teacher.cookies" "$BASE/api/assignments?course_id=1" | grep -q 'onerror'; then
  echo "   проверочное задание уже есть — пропуск"
else
  post "$TMP/teacher.cookies" "$TMP/assignment.json" /api/assignments
fi

# /api/enrollments/mine отдаёт курсы (поле id), а не записи о зачислении.
if curl -s -b "$TMP/student.cookies" "$BASE/api/enrollments/mine" | grep -q '"title":"Matanaliz"'; then
  echo "   студент уже записан — пропуск"
else
  post "$TMP/student.cookies" "$TMP/enroll.json" /api/enrollments
fi

echo "=== состояние ==="
curl -s -b "$TMP/teacher.cookies" "$BASE/api/assignments?course_id=1"
echo
