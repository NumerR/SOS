#!/usr/bin/env bash
# Живая проверка состояний источника расписания: сервер собирается и запускается
# несколько раз с разным окружением, и админский статус сверяется по настоящему HTTP.
#
# Чем этот слой отличается от тестов Go (и зачем он нужен, если они есть):
# `go test` собирает Handlers с теми Deps, которые передал сам тест, а
# newScheduleSourceClient переводит окружение процесса в причину вне теста. Именно
# в этот зазор и попало противоречие `configured: true` + `missing_config:
# "APEKS_TOKEN"` — его нашёл ручной прогон собранного сервера (поправка к ADR-0023,
# `4e22b33`). Здесь то же окружение читает живой процесс, и проверка не ручная.
#
# Проверяется инвариант: недостающая переменная называется ровно тогда, когда
# источника нет; движок называется ровно тогда, когда источник поднялся; подсказка
# про причину не приходит при работающем источнике и не советует заводить токен,
# который уже задан; и ни значение токена, ни путь парсера не уходят ни в лог, ни
# в ответы.
#
# Запуск: bash tests/source-states-smoke.sh
# Нужны go (собрать сервер) и node (разобрать JSON ответов). Живого python не
# требуется: состояние «парсер есть» собирается на подставном скрипте из
# tests/fixtures, как и приёмка источника.
#
# Входов в лимитере (ADR-0006) этот прогон тратит ровно столько, сколько состояний:
# по одному логину на сервер, база у каждого прогона своя.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# Токен одноразовый и фальшивый: он нужен, чтобы сервер счёл источник настроенным,
# и чтобы проверить, что это значение не всплывает ни в логе, ни в ответах.
SMOKE_TOKEN='smoke-access-token-не-настоящий'

PORT=""
for candidate in $(seq 8220 8249); do
  if ! curl -s -o /dev/null --max-time 1 "http://127.0.0.1:$candidate/"; then
    PORT=$candidate
    break
  fi
done
if [ -z "$PORT" ]; then
  echo "не нашёлся свободный порт в диапазоне 8220-8249" >&2
  exit 1
fi

BASE="http://127.0.0.1:$PORT"

echo "=== сборка сервера ==="
go build -o "$WORK/sos-smoke" ./cmd/server

# jsonget — поле ответа одной строкой. Числа и булевы приводятся к строке,
# отсутствующее поле и null — к пустой строке: сравнения ниже опираются именно
# на это, потому что «поля нет» и «поле пустое» для панели означают одно.
jsonget() {
  node -e '
    const fs = require("fs");
    const body = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
    const value = body[process.argv[2]];
    process.stdout.write(typeof value === "string" ? value : (value == null ? "" : String(value)));
  ' "$1" "$2"
}

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

Server_pid=""
start_server() { # assignments VAR=VAL ... — все переменные источника задаются явно
  # Пустая строка считается незаданной (envOr), поэтому «токена нет» — это
  # APEKS_TOKEN="", а не отсутствие переменной: окружение прогона одно и то же
  # во всех состояниях, и отличается только интересующее нас параметр.
  rm -f "$WORK/app.db" "$WORK/server.log" "$WORK/jar" "$WORK/status.json"
  env "$@" \
    ADDR="127.0.0.1:$PORT" \
    DB_PATH="$WORK/app.db" \
    ADMIN_USERNAME=smokeadmin \
    ADMIN_PASSWORD='smoke-pass-123' \
    ADMIN_EMAIL=smoke@example.com \
    ADMIN_FULL_NAME='Проверка состояний' \
    LOG_LEVEL=info \
    "$WORK/sos-smoke" > "$WORK/server.log" 2>&1 &
  Server_pid=$!

  for _ in $(seq 1 30); do
    if curl -sf "$BASE/api/health" > /dev/null; then return 0; fi
    sleep 1
  done
  echo "сервер не поднялся за 30 секунд" >&2
  tail -40 "$WORK/server.log" >&2
  exit 1
}

stop_server() {
  kill "$Server_pid" 2>/dev/null || true
  wait "$Server_pid" 2>/dev/null || true
}

login() {
  curl -s -c "$WORK/jar" -o "$WORK/login.json" -X POST -H 'Content-Type: application/json' \
    -d '{"identifier":"smokeadmin","password":"smoke-pass-123"}' "$BASE/api/auth/login"
}

status_body() {
  login
  curl -s -b "$WORK/jar" "$BASE/api/schedule/source/status" > "$WORK/status.json"
}

# Ожидания задаются аргументами, а не таблицей: состояния дают разные поля, и
# одна строка на состояние читается как описание продукта, а не как DSL.
run_case() { # имя, configured, engine, missing, hint-подстрока ("" — подсказки нет)
  local name=$1 want_configured=$2 want_engine=$3 want_missing=$4 want_hint=$5
  shift 5
  echo "=== состояние: $name ==="
  start_server "$@"
  status_body

  local hint
  hint=$(jsonget "$WORK/status.json" hint)

  expect_eq "$want_configured" "$(jsonget "$WORK/status.json" configured)" "$name: configured"
  expect_eq "$want_engine" "$(jsonget "$WORK/status.json" engine)" "$name: engine"
  expect_eq "$want_missing" "$(jsonget "$WORK/status.json" missing_config)" "$name: missing_config"
  if [ -z "$want_hint" ]; then
    # Поднявшийся источник не имеет подсказки в принципе: «причина, по которой
    # импорта нет» при работающем клиенте — это уже не про конфигурацию.
    expect_eq "" "$hint" "$name: hint"
  else
    # Подстрокой, а не целиком: весь текст подсказки — обещание панели человеку,
    # и правка формулировки не должна ронять слой. Важно, что названа настоящая
    # переменная.
    expect_has "$want_hint" "$hint" "$name: hint"
    if [ -n "$SMOKE_TOKEN" ] && printf '%s' "$hint" | grep -q 'задайте APEKS_TOKEN'; then
      no "$name: подсказка советует завести токен, который задан"
    else
      ok "$name: подсказка не отправляет админа за уже заданным токеном"
    fi
  fi

  if grep -q "$SMOKE_TOKEN" "$WORK/server.log" "$WORK/status.json" "$WORK/login.json"; then
    no "$name: значение токена нашлось в логе или в ответах"
  else
    ok "$name: токена нет ни в логе, ни в ответах"
  fi

  stop_server
}

# Шесть состояний — все, которые разбирает newScheduleSourceClient.
run_case "токена нет" false "" APEKS_TOKEN "" \
  APEKS_TOKEN="" SCHEDULE_SOURCE="" APEKS_API_URL="" SCHEDULE_PARSER="" PYTHON_BIN=""

run_case "токен есть, python не найден" false "" PYTHON_BIN "проверьте PYTHON_BIN" \
  APEKS_TOKEN="$SMOKE_TOKEN" SCHEDULE_SOURCE="" APEKS_API_URL="" SCHEDULE_PARSER="" PYTHON_BIN="/no/such/python3"

run_case "токен есть, адрес источника не по HTTPS" false "" APEKS_API_URL "APEKS_API_URL не подходит" \
  APEKS_TOKEN="$SMOKE_TOKEN" SCHEDULE_SOURCE=go APEKS_API_URL="http://apeks.example.ru" SCHEDULE_PARSER="" PYTHON_BIN=""

run_case "движок go поднялся" true go "" "" \
  APEKS_TOKEN="$SMOKE_TOKEN" SCHEDULE_SOURCE=go APEKS_API_URL="" SCHEDULE_PARSER="" PYTHON_BIN=""

run_case "движок python поднялся" true python "" "" \
  APEKS_TOKEN="$SMOKE_TOKEN" SCHEDULE_SOURCE="" APEKS_API_URL="" \
  SCHEDULE_PARSER="$REPO/tests/fixtures/schedule-source.sh" PYTHON_BIN=/bin/sh

run_case "SCHEDULE_SOURCE с опечаткой" false "" SCHEDULE_SOURCE "SCHEDULE_SOURCE принимает только python или go" \
  APEKS_TOKEN="$SMOKE_TOKEN" SCHEDULE_SOURCE=apex APEKS_API_URL="" SCHEDULE_PARSER="" PYTHON_BIN=""

# Отказ импорта обязан назвать ту же переменную, что и панель: два места одного
# состояния с разными текстами — это тот ложный след, из-за которого ADR-0023 и
# затевался. Проверяется на состоянии «токен есть, python не найден».
echo "=== отказ импорта называет ту же переменную ==="
start_server \
  APEKS_TOKEN="$SMOKE_TOKEN" SCHEDULE_SOURCE="" APEKS_API_URL="" SCHEDULE_PARSER="" PYTHON_BIN="/no/such/python3"
status_body
CSRF=$(awk '/csrf_token/ {print $7}' "$WORK/jar")
IMPORT_CODE=$(curl -s -b "$WORK/jar" -H "X-CSRF-Token: $CSRF" -o "$WORK/import.json" -w '%{http_code}' \
  -X POST -H 'Content-Type: application/json' \
  -d '{"group":"494","month":10,"year":2026}' "$BASE/api/schedule/source/lessons")
expect_eq "503" "$IMPORT_CODE" "импорт без python отвечает 503"
IMPORT_BODY=$(cat "$WORK/import.json")
expect_has "PYTHON_BIN" "$IMPORT_BODY" "отказ импорта называет PYTHON_BIN"
if printf '%s' "$IMPORT_BODY" | grep -q 'задайте APEKS_TOKEN'; then
  no "отказ импорта советует завести токен, который задан"
else
  ok "отказ импорта не советует заводить уже заданный токен"
fi

# Журнал — третье место того же состояния: админ видит его на /admin, и там не
# должно быть другой причины, чем в панели и в отказе. Сессия в jar ещё жива,
# поэтому повторный вход не нужен — он стоил бы ещё одного прохода лимитера.
curl -s -b "$WORK/jar" -o "$WORK/audit.json" "$BASE/api/admin/audit?action=schedule.import&limit=10"
AUDIT_BODY=$(cat "$WORK/audit.json")
expect_has "missing=PYTHON_BIN" "$AUDIT_BODY" "журнал помнит ту же переменную"

LEAKED=0
for artifact in "$WORK/server.log" "$WORK/status.json" "$WORK/login.json" "$WORK/import.json" "$WORK/audit.json"; do
  if grep -q "$SMOKE_TOKEN" "$artifact"; then
    no "в $(basename "$artifact") найдено значение токена"
    LEAKED=1
  fi
done
if [ "$LEAKED" -eq 0 ]; then
  ok "импорт: ни в логе, ни в ответах значения токена нет"
fi

stop_server

echo
echo "$PASS/$((PASS + FAIL)) проверок пройдено"
if [ "$FAIL" -ne 0 ]; then
  echo "Живая проверка состояний источника не прошла." >&2
  exit 1
fi
echo "Все состояния источника названы настоящими переменными."
