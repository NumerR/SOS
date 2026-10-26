from __future__ import annotations

import json
import os
from typing import Any
from urllib.parse import urlencode

import requests

from groups import GroupInfo, groups_from_data

# Разнесено с исходным файлом парсера (см. README в этой папке):
#
#   1. Токен. В исходной версии был DEFAULT_TOKEN, вписанный прямо в код.
#      Здесь его нет: доступ берётся только из APEKS_TOKEN (или из аргумента
#      вызывающего кода) — секрет в репозитории пережил бы любой срок жизни
#      токена и попал бы в историю git.
#   2. Схема адреса. Источник доступен только по HTTPS: токен едет в строке
#      запроса, поэтому открытый http отклоняется здесь, а не первым запросом.
#   3. Обход дерева групп переехал в groups.py — он нужен режиму
#      --groups --from-file, который не требует requests.

DEFAULT_API_URL = "https://apeks.tksu.ru/api/call/schedule-schedule/student"
TOKEN_ENV = "APEKS_TOKEN"


class ApiError(RuntimeError):
    pass


class ApexScheduleAPI:
    """Small client for the API used by the university schedule page.

    The browser page itself exposes this endpoint in script.js. We use the
    same endpoint directly instead of automating clicks in jsTree.
    """

    def __init__(self, api_url: str = DEFAULT_API_URL, token: str | None = None, timeout: int = 30):
        base = (api_url or DEFAULT_API_URL).rstrip("?")
        if not base.lower().startswith("https://"):
            raise ApiError("Адрес источника расписания должен быть по HTTPS (токен едет в строке запроса).")

        self.api_url = base
        self.token = (token or os.getenv(TOKEN_ENV) or "").strip()
        if not self.token:
            raise ApiError(f"Токен источника не задан: передайте --token или переменную окружения {TOKEN_ENV}.")
        self.timeout = timeout
        self.session = requests.Session()
        self.session.headers.update({
            "Accept": "application/json",
            "User-Agent": "schedule-parser/2.0",
        })

    def _get(self, **params: Any) -> dict[str, Any]:
        query = {"token": self.token, **params}
        url = self.api_url + "?" + urlencode(query)
        try:
            response = self.session.get(url, timeout=self.timeout)
            response.raise_for_status()
            payload = response.json()
        except requests.RequestException as exc:
            raise ApiError(f"Ошибка запроса к АПЭКС-ВУЗ: {exc}") from exc
        except ValueError as exc:
            raise ApiError("Сервер вернул не JSON.") from exc

        if not isinstance(payload, dict) or "data" not in payload:
            raise ApiError(f"Неожиданный ответ API: {json.dumps(payload, ensure_ascii=False)[:500]}")
        return payload["data"]

    def get_groups(self) -> list[GroupInfo]:
        return groups_from_data(self._get())

    def find_group(self, name: str) -> tuple[GroupInfo, str | None]:
        requested = name.strip()
        desired_subgroup = None
        base_name = requested
        if requested.endswith("/1"):
            base_name, desired_subgroup = requested[:-2], "1 подгруппа"
        elif requested.endswith("/2"):
            base_name, desired_subgroup = requested[:-2], "2 подгруппа"

        groups = self.get_groups()
        exact = [g for g in groups if g.name.strip().casefold() == requested.casefold()]
        if exact:
            if len(exact) > 1:
                details = "\n".join(f"  - {g.id}: {g.department} / {g.course}" for g in exact)
                raise ApiError(f"Найдено несколько групп «{requested}»:\n{details}")
            return exact[0], desired_subgroup

        base = [g for g in groups if g.name.strip().casefold() == base_name.casefold()]
        if len(base) == 1:
            return base[0], desired_subgroup
        if len(base) > 1:
            details = "\n".join(f"  - {g.id}: {g.department} / {g.course}" for g in base)
            raise ApiError(f"Найдено несколько групп «{base_name}»:\n{details}")

        suggestions = [g for g in groups if requested.casefold() in g.name.casefold()]
        suggestions = suggestions[:10]
        extra = "\nПохожие группы:\n" + "\n".join(f"  - {g.name} ({g.id})" for g in suggestions) if suggestions else ""
        raise ApiError(f"Группа «{requested}» не найдена.{extra}")

    def get_schedule(self, group_id: str, month: int, year: int) -> dict[str, Any]:
        return self._get(group_id=group_id, month=month, year=year)
