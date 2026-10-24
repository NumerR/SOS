#!/usr/bin/env python3
"""Точка входа, которую вызывает сервер платформы.

Сама связка парсера — `api_client.py` (доступ к АПЭКС-ВУЗ) и `parser.py`
(разбор ответа) — лежит в этой папке без изменений, и `main.py` по-прежнему
делает свою исходную работу: собирает автономную HTML-страницу. Этот файл
добавляет только то, чего в CLI не было: вывод списком пар в JSON, чтобы
расписание можно было показать на странице `/schedule` платформы, а не в
отдельном файле.

Что важно знать, если вы читаете этот файл впервые:

  * Токен источника берётся только из окружения (`APEKS_TOKEN`) или из
    `--token`. В аргументах сервер его не передаёт: командная строка видна
    в `ps` и попадает в логи падений процессов, а окружение дочернего
    процесса — нет.
  * `--from-file` читает ответ источника с диска и не трогает сеть. Нужно
    это для проверок: разбор можно сверить с реальным ответом вуза,
    не дёргая чужой API и не устанавливая `requests`.
  * На stdout — ровно одна строка JSON. Всё, что не данные, — на stderr.
  * `parser.py` не сохраняет id пары у источника, поэтому `external_id`
    считается здесь детерминированно из содержимого строки (см. lesson_id).
    Он нужен только для дедупликации импорта: повторный импорт того же
    месяца обновляет те же строки, а не плодит копии.

Коды выхода: 0 — данные напечатаны, 1 — источник или разбор не удались,
2 — неверный запуск (нет токена, конфликт аргументов, нечитаемый файл).
"""

from __future__ import annotations

import argparse
import hashlib
import json
import sys
from pathlib import Path

from parser import ScheduleParser, lessons_to_dicts


def fail(message: str, code: int = 1) -> None:
    print(message, file=sys.stderr)
    raise SystemExit(code)


def lesson_id(row: dict) -> str:
    """Стабильный идентификатор строки: импорт месяца заходит дважды — строки
    должны остаться теми же. Считается по содержательным полям, потому что
    порядковый номер зависел бы от того, что ещё вернул источник."""
    canonical = "|".join(
        str(row.get(key, ""))
        for key in ("date", "lesson_number", "time", "group", "subgroup",
                    "discipline", "lesson_type", "room", "teacher")
    )
    return "py:" + hashlib.sha1(canonical.encode("utf-8")).hexdigest()[:16]


def read_payload(path: str) -> dict:
    """Ответ источника с диска: либо объект "data", либо конверт с полем "data"."""
    try:
        raw = json.loads(Path(path).read_text(encoding="utf-8"))
    except OSError as exc:
        fail(f"не удалось прочитать {path}: {exc}", code=2)
    except json.JSONDecodeError as exc:
        fail(f"{path} не является JSON: {exc}", code=2)

    if isinstance(raw, dict) and isinstance(raw.get("data"), dict):
        return raw["data"]
    if isinstance(raw, dict) and ("lessons" in raw or "lessonTimes" in raw or "groups" in raw):
        return raw
    fail("в файле нет ни "
         "полей ответа источника (lessons/lessonTimes/groups), ни обёртки data", code=2)


def emit_groups(rows) -> None:
    """Каталог групп одной строкой JSON — и для сети, и для разбора с диска."""
    json.dump(
        {
            "groups": [
                {"id": g.id, "name": g.name, "department": g.department, "course": g.course}
                for g in rows
            ]
        },
        sys.stdout,
        ensure_ascii=False,
    )


def emit(payload: dict, group: str | None) -> None:
    lessons = ScheduleParser().parse_api(payload)
    rows = []
    for row in lessons_to_dicts(lessons):
        row["external_id"] = lesson_id(row)
        rows.append(row)

    meta = payload.get("group") if isinstance(payload.get("group"), dict) else {}
    out = {
        "group_id": str(payload.get("group_id") or meta.get("id") or group or ""),
        "group_name": str(meta.get("name") or ""),
        "start_date": str(payload.get("start_date") or ""),
        "end_date": str(payload.get("end_date") or ""),
        "lessons": rows,
    }
    json.dump(out, sys.stdout, ensure_ascii=False)


def main() -> None:
    ap = argparse.ArgumentParser(description="Расписание АПЭКС-ВУЗ в JSON для платформы")
    ap.add_argument("--groups", action="store_true", help="каталог групп вместо расписания")
    ap.add_argument("--group-id", help="id группы у источника (именно id — каталог платформы хранит его)")
    ap.add_argument("--month", type=int)
    ap.add_argument("--year", type=int)
    ap.add_argument("--api-url", default=None, help="адрес эндпоинта; по умолчанию — из APEKS_API_URL")
    ap.add_argument("--token", default=None, help="обычно не нужен: сервер передаёт APEKS_TOKEN окружением")
    ap.add_argument("--from-file", help="разобрать ответ источника с диска, без сети")
    args = ap.parse_args()

    if args.from_file:
        # Сеть не трогается, и `requests` не обязателен: импорт только там, где
        # он нужен. Каталог по файлу разбирается тем же обходом дерева, что и
        # сетевой ответ, — на нём проверяется паритет с Go-разбором.
        payload = read_payload(args.from_file)
        if args.groups:
            from groups import groups_from_data

            emit_groups(groups_from_data(payload))
            return
        emit(payload, args.group_id)
        return

    if not args.groups and not args.group_id:
        fail("нужно --group-id (и --month/--year) либо --groups, либо --from-file", code=2)
    if not args.groups and (args.month is None or args.year is None):
        fail("для импорта месяца нужны --month и --year", code=2)

    import os

    from api_client import ApiError, ApexScheduleAPI, DEFAULT_API_URL

    api_url = args.api_url or os.getenv("APEKS_API_URL") or DEFAULT_API_URL
    try:
        api = ApexScheduleAPI(api_url=api_url, token=args.token)
    except ApiError as exc:
        # Сообщение может упомянуть переменную окружения — токена в нём быть не должно.
        fail(f"источник расписания недоступен: {exc}", code=2)

    try:
        if args.groups:
            emit_groups(api.get_groups())
            return
        payload = api.get_schedule(args.group_id, args.month, args.year)
    except ApiError as exc:
        fail(f"источник расписания недоступен: {exc}", code=1)

    emit(payload, args.group_id)


if __name__ == "__main__":
    main()
