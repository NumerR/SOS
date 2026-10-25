#!/usr/bin/env bash
# Живой UI-прогон на собственном стеке: свой порт, своя база, свой сид — и никаких
# «второй прогон падает, потому что первый уже израсходовал лимитер входов».
#
# Зачем: `npm run test:all` требует вручную поднятого сервера и применённого
# `tests/ui/seed.sh`, и это дважды ломалось на одном и том же дереве:
#   1) лимитер входов пропускает 20 попыток на IP за 10 минут, поэтому второй
#      прогон подряд на том же сервере даёт 429 в `account-security.cjs` — красный
#      шаг, который ничего не проверял;
#   2) `admin-roles.cjs` меняет роль `s_stud` и возвращает её, но если прогон
#      оборвался, роль остаётся изменённой, и следующий запуск падает с «Роль не
#      изменилась».
# Оба лечатся одним и тем же — свежей базой и свежим сервером на каждый прогон,
# ровно как в CI (job `ui` в `.github/workflows/ci.yml`), только здесь это одна
# команда и без ручного контроля портов.
#
# Запуск:
#   bash tests/ui/run-live.sh                    # все восемь сьютов
#   bash tests/ui/run-live.sh admin-roles        # только названные
#   PORT=8080 bash tests/ui/run-live.sh          # свой порт (по умолчанию свободный)
#
# Девятый сьют каталога (`schedule-source-python.cjs`) сюда не входит: ему нужен стек
# с настроенным источником (APEKS_TOKEN + SCHEDULE_PARSER), а этот скрипт источник не
# настраивает — запущенный здесь, он упадёт красным, ничего не проверяя. У него свой
# харнесс: `bash tests/schedule-source-python.sh`.
#
# Код выхода 0 — все названные сьюты прошли; 1 — хотя бы один упал (или стек не
# поднялся). Сервер, база и лог живут в mktemp-каталоге и удаляются по trap,
# поэтому повторный запуск всегда начинается с чистого состояния.
#
# ADMIN_PASSWORD здесь — одноразовый пароль базы, которая создаётся и удаляется
# вместе с каталогом в /tmp; в репозитории лежать нельзя только настоящим
# учётным данным (то же правило, что у `ci.yml`).
set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
UI="$ROOT/tests/ui"
ADMIN_USERNAME="${ADMIN_USERNAME:-admin}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-local-only-pass-123}"
export ADMIN_USERNAME ADMIN_PASSWORD

TMP="$(mktemp -d)"
SERVER=""
PID=""

cleanup() {
  if [ -n "$PID" ] && kill -0 "$PID" 2>/dev/null; then
    kill "$PID" 2>/dev/null
    for _ in $(seq 1 20); do
      kill -0 "$PID" 2>/dev/null || break
      sleep 0.25
    done
    kill -9 "$PID" 2>/dev/null || true
  fi
  rm -rf "$TMP"
}
trap cleanup EXIT

free_port() {
  node -e 'const s = require("net").createServer(); s.listen(0, "127.0.0.1", () => { console.log(s.address().port); s.close(); });'
}

if [ -n "${PORT:-}" ]; then
  BASE="http://127.0.0.1:$PORT"
else
  PORT="$(free_port)"
  [ -n "$PORT" ] || { echo "не удалось найти свободный порт (нужен node)" >&2; exit 1; }
  BASE="http://127.0.0.1:$PORT"
fi

echo "== сборка сервера"
SERVER="$TMP/sos-live"
(cd "$ROOT" && go build -o "$SERVER" ./cmd/server) || { echo "сборка упала" >&2; exit 1; }

echo "== сервер: $BASE (база $TMP/app.db)"
# Статика раздаётся из `web/` относительно cwd, поэтому сервер стартует из корня
# репозитория — иначе страницы отдавали бы 404 независимо от правок.
(
  cd "$ROOT" || exit 1
  ADDR=":$PORT" DB_PATH="$TMP/app.db" exec "$SERVER"
) > "$TMP/server.log" 2>&1 &
PID=$!

for _ in $(seq 1 60); do
  if curl -sf "$BASE/api/health" > /dev/null 2>&1; then
    break
  fi
  if ! kill -0 "$PID" 2>/dev/null; then
    echo "сервер умер, не поднявшись:" >&2
    tail -20 "$TMP/server.log" >&2
    exit 1
  fi
  sleep 0.5
done
if ! curl -sf "$BASE/api/health" > /dev/null 2>&1; then
  echo "сервер не поднялся за 30 секунд:" >&2
  tail -20 "$TMP/server.log" >&2
  exit 1
fi

echo "== сид"
SOS_BASE_URL="$BASE" bash "$UI/seed.sh" > "$TMP/seed.log" 2>&1
# seed.sh печатает коды ответов, но сам почти всегда выходит с нулём, поэтому
# состояние проверяется отдельно: если сид не лёг, сьюты упадут непонятно где.
if ! curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/auth/login" \
     -H 'Content-Type: application/json' \
     --data '{"identifier":"s_stud","password":"password123"}' | grep -q '^200$'; then
  echo "сид не применился (вход s_stud не проходит):" >&2
  tail -20 "$TMP/seed.log" >&2
  exit 1
fi

SUITES=("$@")
if [ ${#SUITES[@]} -eq 0 ]; then
  # Порядок тот же, что у `npm run test:all`: сначала офлайновые (не тратят
  # лимитер), потом живые.
  SUITES=(accessibility schedule-forms schedule-timetable profile-page assignments-modal admin-roles account-security session-expiry)
fi

TOTAL=0
FAILED_LIST=""
echo "== прогоны"
for s in "${SUITES[@]}"; do
  TOTAL=$((TOTAL+1))
  file="$UI/$s.cjs"
  if [ ! -f "$file" ]; then
    echo "нет такого сьюта: $s (ожидается $file)" >&2
    FAILED_LIST="$FAILED_LIST $s"
    continue
  fi
  echo "-- $s"
  (cd "$UI" && SOS_BASE_URL="$BASE" node "$s.cjs") || FAILED_LIST="$FAILED_LIST $s"
done

if [ -n "$FAILED_LIST" ]; then
  echo ""
  echo "упало сьютов:$FAILED_LIST (из $TOTAL)"
  echo "-- хвост лога сервера --"
  tail -40 "$TMP/server.log"
  exit 1
fi

echo ""
echo "ок: сьютов $TOTAL на $BASE, стек и база удалены"
