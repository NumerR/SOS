#!/usr/bin/env python3
"""Проверка договора `fetch.py` с Go-мостом — без сети и без `requests`.

Зачем отдельный файл, если есть `test_parser.py`: тот гоняет разбор по словарю,
собранному руками, и ничего не знает о том, что реально происходит на границе
процессов. А граница и есть то место, где ломается вся связка: Go вызывает
дочерний процесс и ждёт ровно одну строку JSON с заранее оговорёнными полями.
Здесь проверяется именно она:

  * `--groups --from-file` отдаёт каталог и не требует установленных пакетов
    (именно так каталог разбирается в CI и в Go-тесте паритета);
  * `--from-file` с парами отдаёт объект нужной формы, и у каждой пары есть
    `external_id` — по нему импорт месяца не плодит копии;
  * `external_id` детерминирован: два запуска одного файла дают одно и то же
    значение, иначе повторный импорт считался бы новым набором строк;
  * stdout чист: всё лишнее пишется в stderr, иначе Go не распарсит ответ.

Фикстуры — срезы живого ответа источника (internal/apeks/testdata), значения в
них не выдуманы. Запуск: python3 tools/schedule-parser/test_contract.py
"""

from __future__ import annotations

import json
import re
import subprocess
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
REPO = HERE.parent.parent
FETCH = HERE / "fetch.py"
GROUPS_FIXTURE = REPO / "internal" / "apeks" / "testdata" / "apeks-groups-tree.json"
MONTH_FIXTURE = REPO / "internal" / "apeks" / "testdata" / "apeks-2026-10-week.json"

# Поля строки расписания, которые читает страница: если парсер перестанет
# отдавать какое-то из них, Go-мост молча положит в базу пустую колонку.
LESSON_FIELDS = ("date", "lesson_number", "time", "group", "subgroup",
                 "discipline", "lesson_type", "room", "teacher", "external_id")
EXTERNAL_ID = re.compile(r"^py:[0-9a-f]{16}$")

failures: list[str] = []


def ok(condition: bool, name: str, detail: str = "") -> None:
    print(f"PASS  {name}" + (f" — {detail}" if detail else ""))
    if not condition:
        failures.append(name)


def run(args: list[str]) -> tuple[int, str, str]:
    proc = subprocess.run([sys.executable, str(FETCH), *args],
                          capture_output=True, text=True)
    return proc.returncode, proc.stdout, proc.stderr


def check_groups() -> None:
    code, out, err = run(["--groups", "--from-file", str(GROUPS_FIXTURE)])
    ok(code == 0, "каталог групп разобран без ошибки", f"код {code} {err.strip()[:120]}")
    if code != 0:
        return

    data = json.loads(out)
    rows = data.get("groups", [])
    ok(len(rows) == 13, "в каталоге ровно группы фикстуры", f"получено {len(rows)}")
    named = [g for g in rows if g.get("id") and g.get("name")]
    ok(len(named) == len(rows), "у каждой группы есть id и имя",
       f"{len(named)} из {len(rows)}")
    known = next((g for g in rows if g.get("id") == "494"), None)
    ok(known is not None and known["name"] == "Б-ИСиТ-22",
       "группа 494 названа как в источнике", json.dumps(known, ensure_ascii=False))
    ok(all(g.get("department") and g.get("course") for g in rows),
       "заполнены институт и курс — страница показывает их в подсказке")


def check_month() -> None:
    code, out, err = run(["--from-file", str(MONTH_FIXTURE), "--group-id", "494"])
    ok(code == 0, "месяц разобран без ошибки", f"код {code} {err.strip()[:120]}")
    if code != 0:
        return

    data = json.loads(out)
    for key in ("group_id", "group_name", "start_date", "end_date", "lessons"):
        ok(key in data, f"в ответе есть поле {key} — его ждёт Go-мост")
    lessons = data.get("lessons", [])
    ok(len(lessons) == 22, "все пары фикстуры разобраны", f"получено {len(lessons)}")
    if not lessons:
        return

    missing = [key for key in LESSON_FIELDS if any(key not in row for row in lessons)]
    ok(not missing, "у каждой пары есть все поля строки расписания",
       f"нет: {missing}" if missing else "")
    bad = [row.get("external_id") for row in lessons
           if not EXTERNAL_ID.match(str(row.get("external_id")))]
    ok(not bad, "external_id в формате py:<16 hex>",
       f"под подозрением: {bad[:3]}" if bad else str(lessons[0]["external_id"]))

    again = json.loads(run(["--from-file", str(MONTH_FIXTURE), "--group-id", "494"])[1])
    ok([row["external_id"] for row in again.get("lessons", [])] ==
       [row["external_id"] for row in lessons],
       "повторный запуск даёт те же идентификаторы — импорт месяца идемпотентен")


def check_stdout_is_clean() -> None:
    # Go читает stdout целиком: одна лишняя строка текста = неразобранный ответ.
    code, out, err = run(["--from-file", str(MONTH_FIXTURE), "--group-id", "494"])
    ok(code == 0 and len(out.strip().splitlines()) == 1,
       "на stdout ровно одна строка JSON", f"строк {len(out.strip().splitlines())}")
    # Сообщение об ошибке не должно выглядеть как данные.
    bad_code, bad_out, bad_err = run(["--from-file", str(FETCH)])
    ok(bad_code != 0 and not bad_out.strip(),
       "на неверном входе — ненулевой код и пустой stdout",
       f"код {bad_code}, stdout {len(bad_out.strip())} симв., stderr {bad_err.strip()[:60]}")


def main() -> None:
    if sys.version_info < (3, 10):
        print(f"нужен Python 3.10+, запустили {sys.version.split()[0]}", file=sys.stderr)
        raise SystemExit(2)
    for fixture in (GROUPS_FIXTURE, MONTH_FIXTURE):
        if not fixture.exists():
            print(f"нет фикстуры {fixture}", file=sys.stderr)
            raise SystemExit(2)

    check_groups()
    check_month()
    check_stdout_is_clean()

    total = len(failures)
    print(f"\n{'ВАЖНО: провалено ' + str(total) if total else 'все проверки договора пройдены'}")
    raise SystemExit(1 if total else 0)


if __name__ == "__main__":
    main()
