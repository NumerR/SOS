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
| [05-DECISIONS.md](05-DECISIONS.md) | ADR-0001…ADR-0015: размещение, PostgreSQL, модульность, источник расписания, события, роли и rate limit, остаток T0.2, production-вид HTTP-сервера, CI, реестр модулей и права, транспорт фронтенда и политика 401, журнал действий в базе рядом с логом, общая шапка + 404 + один submit-обработчик, страница `/profile`, импорт расписания из АПЭКС-ВУЗ |
| [how-to-add-a-module.md](how-to-add-a-module.md) | Приёмка T1.1: как добавить модуль за пять шагов (пакет + право + миграция + одна строка в списке); в T1.2 проверена повторно на настоящем модуле `audit` |

Статус (2026-10-24): исследование завершено, решения владельца получены (ADR-0001, ADR-0002, ADR-0004, ADR-0005).
Выполнено и запушено: T0.1 (модальное окно заданий) и T0.2 целиком (роли, rate limit,
тайминг, отзыв и очистка сессий, «выйти везде», смена пароля, `TRUST_PROXY`).
Оформлено двумя pull request'ами: [#1 слит в `main`](https://github.com/NumerR/SOS/pull/1),
[**#2 слит в `main` (`64a1232`, 2026-10-24)**](https://github.com/NumerR/SOS/pull/2).
Коммиты, сделанные после этого слияния (`0e230ad` документация и `1baa480` T4.1),
ждут новый pull request — на GitHub их ещё не проверял CI, прогон локальный.
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
Документация после T1.1 — `7796033`
([run 40042500419](https://github.com/NumerR/SOS/actions/runs/40042500419), success).
T3.1 закрыт почти полностью. Первая половина — коммитом `8fef933` (ADR-0011):
конфигурация, API-клиент, политика 401, сбор дублей в core/format.js,
относительные пути и сьют `session-expiry.cjs`
([run 40044462737](https://github.com/NumerR/SOS/actions/runs/40044462737), `completed / success`).
Документация после неё — `20055c5`
([run 40045063515](https://github.com/NumerR/SOS/actions/runs/40045063515), success).
Вторая половина — коммитом `8b3a027` (ADR-0013): общая шапка и подвал в
`web/js/core/layout.js`, страница `web/404.html` со статусом 404 (JSON для
`/api/*`), один `bindCreateForm` вместо двух копий submit-обработчика и офлайн-сьют
`tests/ui/schedule-forms.cjs` (54 проверки)
([run 40053487121](https://github.com/NumerR/SOS/actions/runs/40053487121), `completed / success`).
Остаток T3.1 закрыт коммитом `ae291e8` (ADR-0014): страница `/profile` из тех
полей, которые реально хранятся, панель смены пароля и «выйти везде» переехала на
неё с кабинета, седьмой вариант навигации в `core/layout.js`, три теста на уровне
маршрутизатора и офлайн-сьют `tests/ui/profile-page.cjs` (35 проверок) —
[run 40055769250](https://github.com/NumerR/SOS/actions/runs/40055769250), `completed / success`.
В остатке по T3.1 осталась только публикация на Pages (`deploy-frontend.yml`
+ собственный 404 с base-относительными путями).
T1.2 (журнал действий `audit_events`: таблица от модуля `audit`, 15 точек записи
в обработчиках, `GET /api/admin/audit` с правом `audit.view`, панель в `/admin`)
закрыт коммитом `26066e8` (ADR-0012) —
[run 40047906544](https://github.com/NumerR/SOS/actions/runs/40047906544), `completed / success`.
T4.1 (источник расписания) закрыт коммитом `1baa480` (ADR-0015): артефакт владельца —
готовый Python-парсер — снял блок B2, и вместо ссылки на виджет вуза `/schedule`
отдаёт импортированные пары. Go-клиент АПЭКС-ВУЗ (`internal/apeks`, токен только из
`APEKS_TOKEN`, HTTPS обязателен, ошибки вычищены от URL и токена), хранение импорта
(`schedule_source_groups` + `schedule_source_lessons`, миграция модуля `schedule`),
пять маршрутов (чтение публичное, запись под новым правом `schedule.source.manage` —
24-е в реестре), панель источника в кабинете администратора и офлайн-сьют
`tests/ui/schedule-timetable.cjs` (59 проверок). Формат ответа подтверждён фикстурами и
локальным TLS-стабом: боевой `apeks.tksu.ru` с машины разработки недоступен, как и
прежде.
Дальше по плану — T2.1 (PostgreSQL + версионные миграции, локально не проверяема:
нет Docker и утилит PostgreSQL). Новые вопросы владельцу: срок хранения журнала
(`docs/03` §3, от T1.2) и кто выдаёт токен источника с каким сроком жизни (`docs/03` B2,
от T4.1).
Ничего из перечисленного не развёрнуто: публикуется только код в git, сервер поднят локально на `:8080`.
