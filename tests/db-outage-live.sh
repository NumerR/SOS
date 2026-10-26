#!/usr/bin/env bash
# Живая проверка отказа базы на пути проверки входа: сервер поднимается на
# настоящей SQLite-базе, пользователь входит по-настоящему, затем таблица
# sessions снимается вторым процессом, и сверяются два ответа и две строки лога.
#
# Чем этот слой отличается от тестов Go: `go test` работает на базе `:memory:` и
# снимает таблицу тем же подключением, а тут — файл, живой `net/http`, настоящая
# кука из браузера-курла и `DROP TABLE` извне. ADR-0029 записал честное
# «живой отказ базы по-прежнему проверяется только руками»; ADR-0031 — про тот
# же класс сбоев, но на пути входа, где цена ошибки выше (401 вместо 500 ещё и
# редиректит на форму, которая не может сработать). Руки этот прогон заменяют.
#
# Проверяется инвариант: сбой хранилища сессий отвечает 500 и называется в логе
# причиной, а отсутствие сессии — по-прежнему 401 для API и 302 на /login для
# страниц. Второй половиной это держит и правку, и её обратную подмену: если бы
# «нет строки» тоже стал 500, прогон бы упал.
#
# Запуск: bash tests/db-outage-live.sh
# Нужны go (собрать сервер), node (разобрать JSON) и python3 (модуль sqlite3 —
# снять таблицу у файла, пока сервер живой).
#
# База и порт одноразовые: порт ищется свободным в диапазоне 8250-8279, база
# создаётся в mktemp и удаляется вместе с ним. Ничего в репозитории прогон не
# трогает и к запущенному серверу разработчика не подключается.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

for tool in node python3 curl go; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "нужен $tool, но его нет в PATH" >&2
    exit 1
  fi
done

PORT=""
for candidate in $(seq 8250 8279); do
  if ! curl -s -o /dev/null --max-time 1 "http://127.0.0.1:$candidate/"; then
    PORT=$candidate
    break
  fi
done
if [ -z "$PORT" ]; then
  echo "не нашёлся свободный порт в диапазоне 8250-8279" >&2
  exit 1
fi

BASE="http://127.0.0.1:$PORT"
DB="$WORK/app.db"
LOG="$WORK/server.log"
# Пароль одноразовый и живёт только в этой директории: нужен, чтобы seedAdmin
# создал учётку, а вход выдал настоящую сессию.
LOGIN="dboutage-admin"
PASSWORD="db-outage-only-pass-123"

PASS=0
FAIL=0
ok() { PASS=$((PASS + 1)); echo "OK  $1"; }
no() { FAIL=$((FAIL + 1)); echo "НЕ  $1"; }

expect_eq() { # ожидалось, получилось, подпись
  if [ "$1" = "$2" ]; then ok "$3"; else no "$3: ожидалось «$1», получилось «$2»"; fi
}

expect_has() { # подстрока, строка, подпись
  case "$2" in
    *"$1"*) ok "$3" ;;
    *) no "$3: в «$2» нет «$1»" ;;
  esac
}

# jsonget — поле ответа одной строкой (то же, что в smoke-слое источника).
jsonget() {
  node -e '
    const fs = require("fs");
    const body = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
    const path = process.argv[2].split(".");
    let value = body;
    for (const key of path) {
      if (value == null) break;
      value = value[key];
    }
    process.stdout.write(typeof value === "string" ? value : (value == null ? "" : String(value)));
  ' "$1" "$2"
}

echo "=== сборка сервера ==="
go build -o "$WORK/sos-db-outage" ./cmd/server

ADMIN_USERNAME="$LOGIN" ADMIN_PASSWORD="$PASSWORD" ADDR=":$PORT" DB_PATH="$DB" \
  "$WORK/sos-db-outage" >"$LOG" 2>&1 &
SERVER_PID=$!
stop_server() {
  kill "$SERVER_PID" 2>/dev/null || true
  wait "$SERVER_PID" 2>/dev/null || true
}
trap 'stop_server; rm -rf "$WORK"' EXIT

READY=0
for _ in $(seq 1 60); do
  if curl -s -o /dev/null --max-time 1 "$BASE/"; then
    READY=1
    break
  fi
  sleep 0.25
done
if [ "$READY" -ne 1 ]; then
  echo "сервер не поднялся на $BASE; лог:" >&2
  tail -20 "$LOG" >&2
  exit 1
fi
ok "сервер поднялся на порту $PORT"

CODE=$(curl -s -c "$WORK/jar" -H 'Content-Type: application/json' \
  -d "{\"identifier\":\"$LOGIN\",\"password\":\"$PASSWORD\"}" \
  -o "$WORK/login.json" -w '%{http_code}' "$BASE/api/auth/login")
expect_eq "200" "$CODE" "вход выдал настоящую сессию (кука в jar)"

echo "=== до сбоя ==="
CODE=$(curl -s -b "$WORK/jar" -o "$WORK/dashboard.html" -w '%{http_code}' "$BASE/dashboard")
expect_eq "200" "$CODE" "/dashboard с сессией отдаёт страницу"
CODE=$(curl -s -b "$WORK/jar" -o "$WORK/courses.json" -w '%{http_code}' "$BASE/api/courses")
expect_eq "200" "$CODE" "/api/courses с сессией отдаёт список"

echo "=== снимаем таблицу sessions ==="
python3 - "$DB" <<'PY'
import sqlite3, sys
con = sqlite3.connect(sys.argv[1], timeout=10)
con.execute("PRAGMA foreign_keys = OFF")
con.execute("DROP TABLE sessions")
con.commit()
con.close()
PY
ok "таблица sessions снята вторым процессом"

echo "=== после сбоя ==="
CODE=$(curl -s -b "$WORK/jar" -o "$WORK/dashboard.out" -D "$WORK/dashboard.headers" -w '%{http_code}' "$BASE/dashboard")
expect_eq "500" "$CODE" "/dashboard при сбое базы отвечает 500, а не redirect"
if grep -qi '^location:' "$WORK/dashboard.headers"; then
  no "отказ спрятан за redirect: в заголовках есть Location"
else
  ok "отказ не превращается в redirect на вход"
fi
expect_eq "internal" "$(jsonget "$WORK/dashboard.out" 'error.code')" "/dashboard: код конверта internal"
expect_eq "failed to verify session" "$(jsonget "$WORK/dashboard.out" 'error.message')" "/dashboard: текст причины"

CODE=$(curl -s -b "$WORK/jar" -o "$WORK/courses.out" -D "$WORK/courses.headers" -w '%{http_code}' "$BASE/api/courses")
expect_eq "500" "$CODE" "/api/courses при сбое базы отвечает 500, а не 401"
REQUEST_ID=$(grep -i '^x-request-id:' "$WORK/courses.headers" | tr -d '\r' | awk '{print $2}')
if [ -n "$REQUEST_ID" ]; then
  ok "отказ назван request_id ($REQUEST_ID) — жалобу можно связать со строкой лога"
else
  no "в ответе нет X-Request-Id"
fi

echo "=== «входа нет» при сломанной базе остаётся «входа нет» ==="
CODE=$(curl -s -o "$WORK/anon.json" -w '%{http_code}' "$BASE/api/courses")
expect_eq "401" "$CODE" "/api/courses без куки: 401, а не 500"
expect_eq "unauthorized" "$(jsonget "$WORK/anon.json" 'error.code')" "/api/courses без куки: код конверта unauthorized"
CODE=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/dashboard")
expect_eq "302" "$CODE" "/dashboard без куки: redirect на вход"
LOCATION=$(curl -s -o /dev/null -D - "$BASE/dashboard" | tr -d '\r' | grep -i '^location:' | awk '{print $2}' || true)
expect_eq "/login" "$LOCATION" "/dashboard без куки ведёт на /login"

echo "=== строки отказа в логе ==="
FAILURE_LINES=$(grep -c '"msg":"отказ сервера"' "$LOG" || true)
expect_eq "2" "$FAILURE_LINES" "отказов в логе ровно по числу сбойных запросов"
ONE=$(grep '"msg":"отказ сервера"' "$LOG" | head -1 || true)
expect_has '"reason":"failed to verify session"' "$ONE" "строка отказа называет причину тем же текстом, что клиент"
expect_has 'no such table: sessions' "$ONE" "строка отказа называет настоящий сбой"
expect_has '"request_id":"'"$REQUEST_ID"'"' "$(grep '"msg":"отказ сервера"' "$LOG" || true)" "строка отказа несёт request_id из ответа"
ERROR_LINES=$(grep -c '"level":"ERROR"' "$LOG" || true)
expect_eq "4" "$ERROR_LINES" "на каждый отказ — две Error-строки (отказ и строка доступа)"
expect_has '"path":"/api/courses","status":500' "$(grep '"msg":"http request"' "$LOG" || true)" "строка доступа поднимает сбойный запрос до 500"
if grep '"msg":"http request"' "$LOG" | grep -q '"path":"/dashboard","status":401'; then
  no "сбой входа записан как 401: мониторинг прочитал бы это как перебор паролей"
else
  ok "сбой базы не притворяется 401 в строках доступа"
fi

echo "=== база возвращается ==="
python3 - "$DB" <<'PY'
import sqlite3, sys
con = sqlite3.connect(sys.argv[1], timeout=10)
con.executescript("""
CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    user_id INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
);
""")
con.commit()
con.close()
PY
CODE=$(curl -s -b "$WORK/jar" -o /dev/null -w '%{http_code}' "$BASE/dashboard")
if [ "$CODE" = "500" ]; then
  no "после восстановления таблицы отказ остался: сервер кэш-запросом держит старую схему"
else
  ok "после восстановления sessions ответ снова про доступ, а не про базу ($CODE)"
fi

stop_server

echo
echo "$PASS/$((PASS + FAIL)) проверок пройдено"
if [ "$FAIL" -ne 0 ]; then
  echo "Живая проверка отказа базы на пути входа не прошла." >&2
  # Указатель на файл был бы пустым: trap снимает рабочую директорию вместе с
  # логом. Показываем строки отказа прямо здесь.
  echo "--- Error-строки лога ---" >&2
  grep '"level":"ERROR"' "$LOG" >&2 || true
  exit 1
fi
echo "Сбой хранилища сессий отвечает 500 с причиной, отсутствие сессии — как и раньше."
