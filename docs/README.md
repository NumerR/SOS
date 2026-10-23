# Документация этапа 1 — исследование и план

Рабочая копия репозитория: https://github.com/NumerR/SOS.git (клон в `./SOS`).
Дата исследования: 2026-10-23. Команда: 4 агента (Tech Lead, Backend/DB, Frontend, Integrations/DevOps/QA).

Документы описывают и анализ, и уже сделанное: с 2026-10-23 изменения коммитятся в ветку `fix/t0-frontend-modals` и пушатся в `origin` (см. строку статуса ниже).

| Документ | Содержание |
|---|---|
| [01-STATE-REPORT.md](01-STATE-REPORT.md) | Что реально работает в проекте, что сломано, security-находки с file:line |
| [02-ARCHITECTURE.md](02-ARCHITECTURE.md) | Архитектура MVP: модули, структура каталогов, контракты API, схема БД, конфигурация |
| [03-UNKNOWN-AND-DECISIONS.md](03-UNKNOWN-AND-DECISIONS.md) | Неизвестные требования, решения, требующие слова владельца, принятые допущения |
| [04-TASK-BREAKDOWN.md](04-TASK-BREAKDOWN.md) | Первый конкретный набор задач по агентам и порядок проверки |
| [05-DECISIONS.md](05-DECISIONS.md) | ADR-0001…ADR-0010: размещение, PostgreSQL, модульность, источник расписания, события, роли и rate limit, остаток T0.2, production-вид HTTP-сервера, CI, реестр модулей и права |
| [how-to-add-a-module.md](how-to-add-a-module.md) | Приёмка T1.1: как добавить модуль за пять шагов (пакет + право + миграция + одна строка в списке) |

Статус (2026-10-24): исследование завершено, решения владельца получены (ADR-0001, ADR-0002, ADR-0004, ADR-0005).
Выполнено и запушено: T0.1 (модальное окно заданий) и T0.2 целиком (роли, rate limit,
тайминг, отзыв и очистка сессий, «выйти везде», смена пароля, `TRUST_PROXY`).
Оформлено двумя pull request'ами: [#1 слит в `main`](https://github.com/NumerR/SOS/pull/1),
[#2 открыт](https://github.com/NumerR/SOS/pull/2).
T0.3 (таймауты `http.Server` и graceful shutdown, `WithDBDeadline`, `slog` + request id,
лимиты длины полей, `__Host-` кука, `LOG_LEVEL`) закрыт коммитом `863b4e5`
в той же ветке `fix/t0-frontend-modals` → PR #2.
T0.4 (CI: `go` + `frontend` + `ui`, `scripts/check-frontend.mjs`) закрыт коммитом
`d4a2141` кроме PostgreSQL-позиции, перенесённой в T2.1 (ADR-0009). Первый прогон
CI на GitHub — [run 40037668304](https://github.com/NumerR/SOS/actions/runs/40037668304),
`completed / success`, три работы зелёные. Документация после T0.4 — `2f79eb8`
([run 40038860104](https://github.com/NumerR/SOS/actions/runs/40038860104), success).
T1.1 (реестр модулей `internal/platform`, права `internal/authz`, шесть модулей в
`internal/modules/*`) закрыт коммитом `439a08b` (ADR-0010) —
[run 40041370917](https://github.com/NumerR/SOS/actions/runs/40041370917), `completed / success`.
Дальше по плану — T2.1 (PostgreSQL + версионные миграции, локально не проверяема:
нет Docker и утилит PostgreSQL) и T3.1 (API-клиент, конфигурация, Pages).
Ничего из перечисленного не развёрнуто: публикуется только код в git, сервер поднят локально на `:8080`.
