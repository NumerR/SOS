from __future__ import annotations

import argparse
from datetime import date
from pathlib import Path

from api_client import ApexScheduleAPI, ApiError, DEFAULT_API_URL
from parser import ScheduleParser
from renderer import load_colors, render_html


def main():
    base_dir = Path(__file__).resolve().parent
    ap = argparse.ArgumentParser(description="Умный парсер расписания ТКГУ / АПЭКС-ВУЗ")
    ap.add_argument("--group", required=True, help="Группа, например Б-ИСиТ-22 или Б-ИСиТ-22/1")
    ap.add_argument("--month", type=int, default=date.today().month, help="Месяц, 1-12")
    ap.add_argument("--year", type=int, default=date.today().year, help="Год")
    ap.add_argument("--api-url", default=None, help="URL API, если сайт изменит endpoint")
    ap.add_argument("--token", default=None, help="API token; можно задать APEKS_TOKEN")
    ap.add_argument("--colors", default=str(base_dir / "colors.json"), help="JSON с цветами пар")
    ap.add_argument("--output", default=str(base_dir / "schedule.html"), help="Готовый HTML")
    args = ap.parse_args()

    if not 1 <= args.month <= 12:
        raise SystemExit("Месяц должен быть от 1 до 12.")

    api = ApexScheduleAPI(api_url=args.api_url or DEFAULT_API_URL, token=args.token)

    try:
        group, desired_subgroup = api.find_group(args.group)
        print(f"Группа: {group.name}")
        print(f"ID группы: {group.id}")
        if desired_subgroup:
            print(f"Фильтр: {desired_subgroup}")
        print(f"Запрашиваю расписание: {args.month:02d}.{args.year}")
        data = api.get_schedule(group.id, args.month, args.year)
    except ApiError as exc:
        raise SystemExit(str(exc))

    parser = ScheduleParser(group.name, desired_subgroup)
    lessons = parser.parse_api(data)
    if not lessons:
        raise SystemExit(
            "Для выбранной группы за указанный месяц занятий не найдено. "
            "Попробуйте другой месяц или проверьте название группы."
        )

    colors = load_colors(args.colors if Path(args.colors).exists() else None)
    title_group = args.group
    out = render_html(lessons, colors, title=f"Расписание — {title_group}", month=args.month, year=args.year)
    Path(args.output).write_text(out, encoding="utf-8")

    print(f"Готово: {Path(args.output).resolve()}")
    print(f"Найдено занятий: {len(lessons)}")


if __name__ == "__main__":
    main()
