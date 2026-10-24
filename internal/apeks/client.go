// Package apeks — клиент внешнего источника расписания (АПЭКС-ВУЗ ТКСУ).
//
// Пакет возник из T4.1: страница расписания показывала только ссылку на сайт
// вуза, потому что встраивание запрещено X-Frame-Options, а своих данных о
// парах у платформы не было. Готовый Python-парсер (api_client.py + parser.py)
// доказал, что источник отдаёт то же самое, что рисует его собственная
// страница, — через один GET-эндпоинт с токеном. Здесь этот путь переписан на
// Go: разбор ответа (parser.go) и сетевой доступ (этот файл).
//
// Почему пакет лежит отдельно от модуля, а не внутри internal/modules/schedule.
// План docs/02 §7 предполагал «пакет source внутри модуля», но он невыполним:
// модуль импортирует internal/handlers, а обработчику импорта нужны клиент и
// парсер. Импорт handlers из модуля — цикл, который Go не собирает. Поэтому
// адаптер источника живёт в leaf-пакете, а internal/modules/schedule по-прежнему
// сам объявляет миграции и маршруты: ядро он не трогает, и именно там видно,
// какие пути у источника появились.
//
// Секрет и как с ним обходиться.
//
// Источник принимает токен только в строке запроса — другого транспорта у него
// нет (проверено на живом эндпоинте: без token запрос возвращает 403). Это
// противоречит правилу брифа «не включать токены доступа в URL», и снять его
// мы не можем: ограничение на стороне вуза. Компромисс зафиксирован здесь:
//
//   - токен берётся только из окружения (APEKS_TOKEN); в исходниках и в
//     репозитории его нет — в отличие от Python-оригинала, где он был вписан
//     прямо в api_client.py;
//   - адрес запроса никогда не попадает ни в лог, ни в текст ошибки: все
//     ошибки клиента собираются из фиксированных формулировок, а url.Error,
//     который Go наполняет полным URL включая query, разворачивается до
//     внутренней причины и дополнительно вычищается (Client.scrub);
//   - запрос идёт только по HTTPS, иначе клиент отказывается стартовать —
//     токен в query по плоскому HTTP утёк бы каждому, кто слушает участок
//     сети. Для тестов это не послабление: они ходят на httptest.NewTLSServer.
package apeks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL — эндпоинт, который использует страница расписания вуза.
// Значение снято с живого ответа и совпадает с DEFAULT_API_URL в parser.py.
const DefaultBaseURL = "https://apeks.tksu.ru/api/call/schedule-schedule/student"

// Source — имя источника в колонке source: одна строка в базе говорит, откуда
// взялась пара, и правка формата АПЭКС не превратится в «непонятно что».
const Source = "apeks"

// DisplayName — как называть источник для человека: подпись «обновлено» на
// странице и тексты ошибок.
const DisplayName = "АПЭКС-ВУЗ"

// SourcePageURL — публичная страница расписания вуза. Отдаётся API вместе с
// данными, чтобы на странице всегда была ссылка на первоисточник: платформа
// показывает копию, и читатель должен иметь возможность свериться с оригиналом.
const SourcePageURL = "https://tksu.ru/bitrix/js/api-apeks-vuz/rasp-stud/rasp-stud.html"

// defaultUserAgent — мы не выдаём себя за браузер: у источника нет требования
// к User-Agent, а честная подпись запроса полезнее, когда вуз захочет понять,
// кто к нему ходит.
const defaultUserAgent = "SOS-schedule-import/1.0 (+https://github.com/NumerR/SOS)"

const (
	// defaultTimeout — источник отвечает за секунды (замер: месяц одной группы
	// приходит за ~1 с). 20 с — запас на холодный кэш, больше не нужно: импорт
	// запускает админ из браузера, и висящий запрос он не ждёт.
	defaultTimeout = 20 * time.Second

	// maxResponseSize — ограничитель чтения ответа.
	//
	// Месяц одной группы весит ~260 КиБ, каталог групп — ~1.5 МиБ. 8 МиБ с
	// запасом покрывают растущий источник и при этом не дают чужому серверу
	// (или ошибке в URL) сожрать память процесса одним ответом.
	maxResponseSize = 8 << 20

	// envelopeField — имя поля-обёртки ответа: {"data": {...}}.
	envelopeField = "data"
)

// ErrNotConfigured — токен источника не задан.
//
// Отдельная ошибка, а не пустой клиент: вызывающий код (обработчик импорта)
// обязан отличать «источник не настроен» от «источник не ответил» — первый
// случай лечится администратором, второй ретраем.
var ErrNotConfigured = errors.New("apeks: токен источника не задан (APEKS_TOKEN)")

// Config — параметры клиента. Обязателен только Token.
type Config struct {
	// Token — доступ к эндпоинту. Только из окружения; в лог и ошибки не попадает.
	Token string

	// BaseURL — адрес эндпоинта. Пустой = DefaultBaseURL. Нужен тестам на
	// httptest-сервере и, в перспективе, зеркалу источника.
	BaseURL string

	// Timeout — ограничитель одного запроса. Ноль = defaultTimeout.
	Timeout time.Duration

	// HTTP — транспорт. Ноль = http.Client с Timeout. Тестам сюда даётся
	// клиент httptest-сервера с его же корнем.
	HTTP *http.Client
}

// Client — доступ к эндпоинту расписания.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
	timeout time.Duration
}

// New собирает клиента по конфигурации.
//
// Ошибкой возвращаются только причины, которые нельзя починить на месте:
// пустой токен, неизвестная схема адреса. Неверный URL (не разбираемый) тоже
// ошибка — иначе она всплыла бы первым же запросом, уже в рантайме.
func New(cfg Config) (*Client, error) {
	token := strings.TrimSpace(cfg.Token)
	if token == "" {
		return nil, ErrNotConfigured
	}

	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		base = DefaultBaseURL
	}

	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" {
		return nil, errors.New("apeks: адрес источника расписания неразбирается")
	}

	// Требование HTTPS проверяется здесь, а не перед первым запросом: сервер
	// должен упасть на старте с внятным текстом, а не молча передавать токен
	// открытым текстом в проде.
	if !strings.EqualFold(parsed.Scheme, "https") {
		return nil, fmt.Errorf("apeks: источник расписания доступен только по HTTPS (получено %q)", parsed.Scheme)
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	httpClient := cfg.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}

	return &Client{baseURL: base, token: token, http: httpClient, timeout: timeout}, nil
}

// BaseURL — фактический адрес источника: DefaultBaseURL либо переопределение
// из APEKS_API_URL. Нужен логу старта: печатать константу, когда оператор
// сменил адрес переменной окружения, — значит врать в журнале именно там, где
// им отлаживают конфигурацию. Токена в этом значении нет: он подставляется
// только в запрос.
func (c *Client) BaseURL() string { return c.baseURL }

// Group — учебная группа из каталога источника.
//
// ID остаётся строкой, хотя источник отдаёт числа: идентификатор нигде не
// складывают и не сравнивают арифметически, а при переходе через URL, базу и
// JSON строка не теряет форму («007» не превращается в 7).
type Group struct {
	ID         string
	Name       string
	Department string
	Course     string
}

// Month — ответ источника по одной группе за один месяц.
type Month struct {
	GroupID   string
	GroupName string
	StartDate string // "01.10.2026" — как в источнике
	EndDate   string
	Lessons   []Lesson
}

// GetGroups — каталог групп (587 записей на момент 2026-10-24).
//
// Ответ без group_id/month/year тот же эндпоинт отдаёт как дерево
// «кафедра -> направление -> группа»; обход здесь ровно трёхуровневый, как в
// get_groups() у Python-версии.
func (c *Client) GetGroups(ctx context.Context) ([]Group, error) {
	payload, err := c.request(ctx, nil)
	if err != nil {
		return nil, err
	}

	var data struct {
		Groups map[string]groupNode `json:"groups"`
	}
	if err := json.Unmarshal(payload, &data); err != nil {
		return nil, errors.New("apeks: каталог групп пришёл в неизвестном формате")
	}

	groups := make([]Group, 0, 64)
	for _, department := range data.Groups {
		for _, course := range department.Items {
			for _, group := range course.Items {
				id := group.ID.String()
				name := norm(group.Name)
				if id == "" || name == "" {
					continue
				}
				groups = append(groups, Group{
					ID:         id,
					Name:       name,
					Department: norm(department.Name),
					Course:     norm(course.Name),
				})
			}
		}
	}

	if len(groups) == 0 {
		return nil, errors.New("apeks: источник вернул пустой каталог групп")
	}

	return groups, nil
}

// groupNode — узел дерева групп. Одна и та же форма на всех трёх уровнях,
// поэтому рекурсивный тип вместо трёх структур.
type groupNode struct {
	ID    flex                 `json:"id"`
	Name  string               `json:"name"`
	Items map[string]groupNode `json:"items"`
}

// GetMonth — расписание группы за месяц.
//
// Month и Year проверяются на стороне источника, поэтому здесь только границы
// формата: запрос вне 1..12 / 2000..2100 — наша ошибка, а не ответ вуза.
func (c *Client) GetMonth(ctx context.Context, groupID string, month, year int) (*Month, error) {
	groupID = norm(groupID)
	if groupID == "" {
		return nil, errors.New("apeks: пустой идентификатор группы")
	}
	if month < 1 || month > 12 {
		return nil, fmt.Errorf("apeks: месяц %d вне диапазона 1..12", month)
	}
	if year < 2000 || year > 2100 {
		return nil, fmt.Errorf("apeks: год %d вне диапазона 2000..2100", year)
	}

	params := url.Values{
		"group_id": {groupID},
		"month":    {fmt.Sprintf("%d", month)},
		"year":     {fmt.Sprintf("%d", year)},
	}

	payload, err := c.request(ctx, params)
	if err != nil {
		return nil, err
	}

	lessons, err := ParseMonth(payload)
	if err != nil {
		return nil, err
	}

	return lessons, nil
}

// request делает один GET и возвращает содержимое поля "data".
//
// Токен добавляется в query — иначе источник не отвечает. Query-строка
// строится заново на каждом вызове, поэтому клиента можно использовать из
// нескольких горутин: общего состояния у запроса нет.
func (c *Client) request(ctx context.Context, params url.Values) (json.RawMessage, error) {
	if c == nil {
		return nil, ErrNotConfigured
	}

	query := url.Values{}
	for key, values := range params {
		for _, value := range values {
			query.Add(key, value)
		}
	}
	query.Set("token", c.token)

	requestURL := c.baseURL
	if strings.Contains(requestURL, "?") {
		requestURL += "&" + query.Encode()
	} else {
		requestURL += "?" + query.Encode()
	}

	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, requestURL, nil)
	if err != nil {
		// Ошибка http.NewRequest содержит полный URL, то есть токен.
		// Поэтому причина не оборачивается — текст остаётся фиксированным.
		return nil, errors.New("apeks: не удалось собрать запрос к источнику")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", defaultUserAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, c.scrub(err, requestURL)
	}
	defer func() { _, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10)) }()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, statusError(resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return nil, c.scrub(err, requestURL)
	}
	if len(body) > maxResponseSize {
		return nil, fmt.Errorf("apeks: ответ источника больше %d МиБ", maxResponseSize>>20)
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, errors.New("apeks: источник вернул не JSON")
	}

	payload, ok := envelope[envelopeField]
	if !ok || len(payload) == 0 || string(payload) == "null" {
		// Текст ответа не печатаем: его формат нам неизвестен, а в логи
		// чужого API попадать нечем.
		return nil, errors.New("apeks: в ответе источника нет поля data")
	}

	return payload, nil
}

// statusError — отказ по коду, без деталей ответа.
//
// 403 получает отдельную формулировку: на живом эндпоинте это ровно то, что
// возвращает запрос без токена или с протухшим, то есть подсказка про
// APEKS_TOKEN полезна админу напрямую.
func statusError(code int) error {
	switch code {
	case http.StatusForbidden, http.StatusUnauthorized:
		return fmt.Errorf("apeks: источник отклонил запрос (HTTP %d) — проверьте APEKS_TOKEN", code)
	case http.StatusTooManyRequests:
		return errors.New("apeks: источник ограничил частоту запросов (HTTP 429), повторите позже")
	default:
		return fmt.Errorf("apeks: источник ответил ошибкой HTTP %d", code)
	}
}

// scrub убирает секрет из текстов ошибок сетевого уровня.
//
// Защищает в три приёма, потому что цена одной оплошности — токен в логе
// процесс-менеджера:
//
//  1. url.Error разворачивается до внутренней причины: Go печатает в нём полный
//     URL запроса, то есть строку «...?token=<секрет>»;
//  2. найденный requestURL заменяется на адрес без query — так не утечёт и
//     случайное упоминание URL внутри чужого сообщения об ошибке;
//  3. само значение токена вычищается из текста, если оно всё же осталось.
//
// Функция возвращает новые ошибки с фиксированными формулировками, а не
// перевыпущенные исходные: вызывающий код пишет их в лог и в ответ админу.
func (c *Client) scrub(err error, requestURL string) error {
	if err == nil {
		return nil
	}

	cause := err
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		cause = urlErr.Err
	}

	switch {
	case errors.Is(cause, context.DeadlineExceeded):
		return errors.New("apeks: источник не ответил за отведённое время")
	case errors.Is(cause, context.Canceled):
		return errors.New("apeks: запрос к источнику отменён")
	}

	text := cause.Error()
	if requestURL != "" {
		text = strings.ReplaceAll(text, requestURL, c.baseURL)
	}
	if c.token != "" {
		text = strings.ReplaceAll(text, c.token, "[токен скрыт]")
	}

	return fmt.Errorf("apeks: запрос к источнику не удался: %s", text)
}
