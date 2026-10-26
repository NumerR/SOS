from __future__ import annotations

from dataclasses import dataclass, asdict
from datetime import date
import re
from typing import Any, Iterable


DATE_RE = re.compile(r"^(\d{1,2})\.(\d{1,2})\.(\d{4})$")


@dataclass
class Lesson:
    date: str
    day: str
    lesson_number: int
    time: str
    group: str
    subgroup: str
    discipline: str
    lesson_type: str
    room: str
    teacher: str

    def to_dict(self):
        return asdict(self)


class ScheduleParser:
    WEEKDAYS = ["Пн", "Вт", "Ср", "Чт", "Пт", "Сб", "Вс"]

    def __init__(self, target_group: str | None = None, target_subgroup: str | None = None):
        self.target_group = self._norm(target_group) if target_group else None
        self.target_subgroup = self._norm(target_subgroup) if target_subgroup else None

    @staticmethod
    def _norm(value: Any) -> str:
        return re.sub(r"\s+", " ", str(value or "").replace("\xa0", " ")).strip()

    @staticmethod
    def _date_key(value: str) -> date:
        m = DATE_RE.match(value.strip())
        if not m:
            return date.max
        return date(int(m.group(3)), int(m.group(2)), int(m.group(1)))

    @classmethod
    def _weekday(cls, date_text: str) -> str:
        d = cls._date_key(date_text)
        return "" if d == date.max else cls.WEEKDAYS[d.weekday()]

    @staticmethod
    def _time_text(slot: dict[str, Any]) -> str:
        start = f"{int(slot.get('hour_from', 0)):02d}:{int(slot.get('minute_from', 0)):02d}"
        end = f"{int(slot.get('hour_to', 0)):02d}:{int(slot.get('minute_to', 0)):02d}"
        return f"{start} – {end}"

    @staticmethod
    def _start_minutes(slot: dict[str, Any]) -> int:
        return int(slot.get("hour_from", 0)) * 60 + int(slot.get("minute_from", 0))

    @classmethod
    def determine_subgroup(cls, lesson: dict[str, Any], data: dict[str, Any]) -> str:
        subgroup_id = cls._norm(lesson.get("subgroup_id"))
        if subgroup_id:
            subgroup_names = data.get("subgroupNames") or {}
            name = subgroup_names.get(subgroup_id) or subgroup_names.get(str(subgroup_id))
            if name:
                return cls._norm(name)
            groups_list = data.get("groupsList") or {}
            subgroups = groups_list.get("Подгруппы") or {}
            name = subgroups.get(f"subgroup-{subgroup_id}")
            if name:
                return cls._norm(name)

        # A superflow containing more than two groups is the site's notion of
        # a lecture/stream. The same information is explicitly used by the JS.
        superflow_groups = lesson.get("superflowGroupsIds") or []
        if isinstance(superflow_groups, list) and len(superflow_groups) > 2:
            return "Поточка"

        group_name = cls._norm(lesson.get("groupName"))
        tokens = [x.strip() for x in re.split(r"[,;\n]+", group_name) if x.strip()]
        if len(tokens) > 2:
            return "Поточка"
        if "/1" in group_name:
            return "1 подгруппа"
        if "/2" in group_name:
            return "2 подгруппа"
        return "—"

    @classmethod
    def _group_display(cls, lesson: dict[str, Any], data: dict[str, Any]) -> str:
        group = cls._norm(lesson.get("groupName"))
        extra: list[str] = []
        for value in lesson.get("superflowGroupsIds") or []:
            name = (data.get("groupNames") or {}).get(str(value))
            if name and cls._norm(name) not in group:
                extra.append(cls._norm(name))
        for value in lesson.get("superflowSubgroupsIds") or []:
            name = (data.get("subgroupNames") or {}).get(str(value))
            if name:
                extra.append(cls._norm(name))
        return ", ".join([x for x in [group, *extra] if x])

    def parse_api(self, data: dict[str, Any]) -> list[Lesson]:
        # JS builds lessonTimes by ID and then sorts IDs by actual start time.
        lesson_times: dict[str, dict[str, Any]] = {}
        for slot in data.get("lessonTimes") or []:
            if isinstance(slot, dict) and slot.get("id") is not None:
                lesson_times[str(slot["id"])] = slot

        sorted_slots = sorted(lesson_times.values(), key=self._start_minutes)
        slot_order = {str(slot["id"]): i + 1 for i, slot in enumerate(sorted_slots)}

        lessons: list[Lesson] = []
        for item in data.get("lessons") or []:
            if not isinstance(item, dict):
                continue
            group = self._norm(item.get("groupName"))
            if self.target_group and not self._group_matches(group, self.target_group):
                continue

            subgroup = self.determine_subgroup(item, data)
            if self.target_subgroup and subgroup.casefold() != self.target_subgroup.casefold():
                continue

            date_text = self._norm(item.get("date"))
            slot = lesson_times.get(str(item.get("lesson_time_id")), {})
            lesson_number = slot_order.get(str(item.get("lesson_time_id")), 0)
            time_text = self._time_text(slot) if slot else ""

            if item.get("is_empty") == 1:
                discipline = "Пустая пара"
            elif item.get("self_work") == 1:
                discipline = "Самоподготовка"
            else:
                discipline = self._norm(item.get("discipline"))

            teacher = item.get("staffNames") or []
            if isinstance(teacher, list):
                teacher_text = ", ".join(self._norm(x) for x in teacher if self._norm(x))
            else:
                teacher_text = self._norm(teacher)

            lessons.append(Lesson(
                date=date_text,
                day=self._weekday(date_text),
                lesson_number=lesson_number,
                time=time_text,
                group=self._group_display(item, data),
                subgroup=subgroup,
                discipline=discipline,
                lesson_type=self._norm(item.get("class_type_name")),
                room=self._norm(item.get("classroom")),
                teacher=teacher_text,
            ))

        lessons.sort(key=lambda x: (self._date_key(x.date), x.lesson_number, x.time, x.discipline, x.room))
        return lessons

    @staticmethod
    def _group_matches(group_text: str, target: str) -> bool:
        g = group_text.casefold().strip()
        t = target.casefold().strip()
        tokens = [x.strip() for x in re.split(r"[,;\n]+", g) if x.strip()]
        return g == t or t in tokens or t in g


def lessons_to_dicts(lessons: Iterable[Lesson]):
    return [x.to_dict() for x in lessons]
