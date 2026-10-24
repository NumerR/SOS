"""Каталог групп АПЭКС-ВУЗа: обход дерева «институт -> направление -> группа».

Вынесено из api_client.py в отдельный файл без сторонних зависимостей. Причина
одна: api_client импортирует requests, а режим `fetch.py --groups --from-file`
обязан работать без сети и без установленной библиотеки — ровно как разбор
расписания за месяц. Иначе проверка каталога с диска зависела бы от pip.

Правила обхода перенесены из исходного get_groups() без изменений по смыслу;
дополнены только проверки формы (см. комментарий в обходе).
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any


@dataclass(frozen=True)
class GroupInfo:
    id: str
    name: str
    department: str = ""
    course: str = ""


def groups_from_data(data: dict[str, Any]) -> list[GroupInfo]:
    """Обход дерева групп из поля "data" ответа источника.

    Пустой уровень источник отдаёт списком [], а не словарем {}: на живом
    ответе 24.10.2026 так выглядели 14 узлов из 659. Отсюда `or {}` и проверка
    isinstance на каждом спуске — без них один пустой отдел обнулил бы весь
    каталог, а Go-сторона на том же месте падала с ошибкой «неизвестный формат».

    Порядок — тот же, что у источника: словарь JSON сохраняет порядок ключей. На
    страницу это не влияет: чтение каталога идёт со своим ORDER BY.
    """
    result: list[GroupInfo] = []
    groups = data.get("groups") or {}
    if not isinstance(groups, dict):
        return result

    for department in groups.values():
        if not isinstance(department, dict):
            continue
        department_name = str(department.get("name", ""))
        courses = department.get("items") or {}
        if not isinstance(courses, dict):
            continue

        for course in courses.values():
            if not isinstance(course, dict):
                continue
            course_name = str(course.get("name", ""))
            leaves = course.get("items") or {}
            if not isinstance(leaves, dict):
                continue

            for group in leaves.values():
                if not isinstance(group, dict):
                    continue
                gid = group.get("id")
                name = group.get("name")
                if gid is not None and name:
                    result.append(GroupInfo(str(gid), str(name), department_name, course_name))
    return result
