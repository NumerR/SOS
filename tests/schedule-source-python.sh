#!/usr/bin/env bash
# Сквозная проверка источника расписания на подставном парсере.
#
# Что она доказывает (и чего — нет):
#
#   доказывается, что цепочка работает целиком: Go-мост запускает дочерний
#   процесс, получает JSON, кладёт строки в базу, публичные маршруты отдают их
#   странице, и страница рисует таймтейбл гостю без входа;
#
#   не доказывается, что живой АПЭКС-ВУЗ отвечает так же. Для этого есть
#   фикстура apeks-groups-tree.json и проверка паритета разборов; обращение к
#   чужому API из CI было бы либо мёртвым без сети, либо публикующим токен вуза.
#
# Запуск: bash tests/schedule-source-python.sh
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO"

if [ ! -d tests/ui/node_modules/jsdom ]; then
  echo "не установлены зависимости UI: cd tests/ui && npm ci" >&2
  exit 1
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# Порт: первый свободный из диапазона. Занятость проверяется ответом curl —
# «соединение отклонено» значит свободен.
PORT=""
for candidate in $(seq 8199 8219); do
  if ! curl -s -o /dev/null --max-time 1 "http://127.0.0.1:$candidate/" ; then
    PORT=$candidate
    break
  fi
done
if [ -z "$PORT" ]; then
  echo "не нашёлся свободный порт в диапазоне 8199-8219" >&2
  exit 1
fi

echo "=== сборка сервера ==="
go build -o "$WORK/sos-source" ./cmd/server

echo "=== запуск на :$PORT (движок python, парсер — подставной) ==="
# Токен здесь одноразовый и фальшивый: он нужен, чтобы мост вообще запустился,
# и чтобы заглушка проверила его доставку окружением. В репозитории такой секрет
# не лежит: это переменная запущенного процесса.
ADDR=":$PORT" \
DB_PATH="$WORK/app.db" \
ADMIN_USERNAME=sourceadmin \
ADMIN_PASSWORD='source-pass-123' \
ADMIN_EMAIL=source@example.com \
ADMIN_FULL_NAME='Проверка источника' \
APEKS_TOKEN='stub-access-token' \
SCHEDULE_SOURCE=python \
SCHEDULE_PARSER="$REPO/tests/fixtures/schedule-source.sh" \
PYTHON_BIN=/bin/sh \
LOG_LEVEL="${LOG_LEVEL:-info}" \
  "$WORK/sos-source" > "$WORK/server.log" 2>&1 &
SERVER_PID=$!
trap 'kill "$SERVER_PID" 2>/dev/null || true; rm -rf "$WORK"' EXIT

for _ in $(seq 1 30); do
  if curl -sf "http://127.0.0.1:$PORT/api/health" > /dev/null; then break; fi
  sleep 1
done

if ! curl -sf "http://127.0.0.1:$PORT/api/health" > /dev/null; then
  echo "сервер не поднялся за 30 секунд" >&2
  cat "$WORK/server.log" >&2
  exit 1
fi

echo "=== прогон проверок ==="
# set -e прервал бы скрипт на первой же упавшей проверке, и лог сервера
# остался бы неотпечатанным — поэтому статус снимается явно.
CODE=0
SOS_BASE_URL="http://127.0.0.1:$PORT" \
ADMIN_USERNAME=sourceadmin \
ADMIN_PASSWORD='source-pass-123' \
  node tests/ui/schedule-source-python.cjs || CODE=$?

echo "=== токен не должен попасть в лог ==="
if grep -q 'stub-access-token' "$WORK/server.log"; then
  echo "в логе сервера найден токен источника" >&2
  CODE=1
else
  echo "OK  в логе токена нет"
fi

if [ "$CODE" -ne 0 ]; then
  echo "=== лог сервера (последние 40 строк) ===" >&2
  tail -40 "$WORK/server.log" >&2
  exit "$CODE"
fi

echo
echo "Источник расписания через подставной парсер работает сквозняком."
