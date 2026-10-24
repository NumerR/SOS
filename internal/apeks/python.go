package apeks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Мост к приложенному Python-парсеру (tools/schedule-parser).
//
// Зачем он нужен: у владельца есть рабочий парсер на Python, и требование
// «не переписывать его на Go» — не придирка к нашему коду, а здравый смысл:
// разбор живёт там, где его правил уже никто не менял, а Go-версия остаётся
// запасным путём (SCHEDULE_SOURCE=go).
//
// Как это выглядит в процессе: сервер запускает python3 tools/schedule-parser/
// fetch.py как дочерний процесс, передаёт ему режим и параметры аргументами,
// читает одну строку JSON со stdout и разбирает её в те же структуры, что и
// Go-клиент. Ни HTTP-клиента, ни правил разбора на стороне Python сервер не
// трогает и не дублирует.
//
// Три вещи, которые здесь важнее удобства:
//
//   - Токен уходит только окружением дочернего процесса. Аргументы командной
//     строки видно в ps и в дампах упавшего процесса, а текст ошибки чужого
//     кода может их напечатать.
//   - Любая строка, приехавшая от парсера, проходит через scrub: если Python
//     вдруг подставит токен в сообщение, в наш лог он не попадёт.
//   - stdout и stderr ограничены по размеру. Парсер — чужой код, и его
//     болтовня не должна съедать память процесса, который обслуживает людей.

const (
	// Имена движков попадают в статус источника: по ним видно, каким кодом
	// привезено расписание.
	EngineGo     = "go"
	EnginePython = "python"

	// defaultPythonBin — интерпретатор по умолчанию. Меняется переменной
	// PYTHON_BIN: на хостинге python3 может называться иначе и не иметь
	// установленных зависимостей.
	defaultPythonBin = "python3"

	// defaultParserScript — путь в репозитории, относительно рабочей директории
	// процесса. Сервер запускают из корня репозитория (так же, как тесты CI);
	// абсолютный путь задаётся переменной SCHEDULE_PARSER.
	defaultParserScript = "tools/schedule-parser/fetch.py"

	// maxParserOutput — потолок stdout парсера. Месяц одной группы весит
	// десятки килобайт (замер на фикстуре: 22 пары), миБ — с запасом на
	// каталог групп в 587 записей.
	maxParserOutput = 8 << 20

	// maxParserStderr — сколько текста ошибки парсера мы вообще храним.
	maxParserStderr = 4 << 10

	// pythonTimeout — потолок одного запуска парсера. Он включает сетевой
	// запрос источника, поэтому совпадает с defaultTimeout клиента: смысл
	// ограничителя один — чужой сайт не должен держать наш импорт вечно.
	pythonTimeout = defaultTimeout
)

// ErrParserUnavailable — парсер не запускается: нет интерпретатора, нет
// скрипта, не установлены зависимости. От ErrNotConfigured отличается
// тем, кто виноват: тот — отсутствие токена у оператора, этот — среда,
// в которой развёрнут сервер.
var ErrParserUnavailable = errors.New("apeks: парсер расписания не запускается")

// PythonConfig — параметры моста. Обязательны токен и существующий скрипт.
type PythonConfig struct {
	// Token — доступ к эндпоинту. Только из окружения, только в окружение
	// дочернего процесса.
	Token string

	// BaseURL — адрес эндпоинта. Пустой = DefaultBaseURL.
	BaseURL string

	// Script — путь к fetch.py. Пустой = defaultParserScript.
	Script string

	// Python — интерпретатор. Пустой = python3.
	Python string

	// Timeout — потолок одного запуска. Ноль = pythonTimeout.
	Timeout time.Duration
}

// Python — источник расписания, разобранный приложенным Python-парсером.
type Python struct {
	python  string
	script  string
	token   string
	baseURL string
	timeout time.Duration
}

// NewPython собирает мост и проверяет то, что нельзя починить на месте.
//
// Проверка идёт на старте, а не первым импортом: админская панель по полю
// «configured» показывает либо кнопки импорта, либо подсказку, и врать про
// «источник настроен», когда парсер не найдется, — значит оставить админа
// один на один с 502 без объяснений.
func NewPython(cfg PythonConfig) (*Python, error) {
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
	if !strings.EqualFold(parsed.Scheme, "https") {
		return nil, fmt.Errorf("apeks: источник расписания доступен только по HTTPS (получено %q)", parsed.Scheme)
	}

	script := strings.TrimSpace(cfg.Script)
	if script == "" {
		script = defaultParserScript
	}
	abs, err := filepath.Abs(script)
	if err != nil {
		return nil, fmt.Errorf("apeks: путь к парсеру расписания не прочитать: %s", script)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("%w: скрипт %s недоступен (%v)", ErrParserUnavailable, abs, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%w: %s — каталог, а нужен fetch.py", ErrParserUnavailable, abs)
	}

	python := strings.TrimSpace(cfg.Python)
	if python == "" {
		python = defaultPythonBin
	}
	// LookPath проверяется здесь: сообщение об отсутствии интерпретатора
	// из exec выглядит как «fork/exec …: no such file or directory» и
	// приходит только в момент импорта.
	if _, err := exec.LookPath(python); err != nil {
		return nil, fmt.Errorf("%w: интерпретатор %s не найден (задайте PYTHON_BIN)", ErrParserUnavailable, python)
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = pythonTimeout
	}

	return &Python{python: python, script: abs, token: token, baseURL: base, timeout: timeout}, nil
}

// Engine — «python»: ответ разобран приложенным парсером.
func (p *Python) Engine() string { return EnginePython }

// Endpoint — адрес источника, который получит дочерний процесс.
func (p *Python) Endpoint() string { return p.baseURL }

// Script — фактический путь к fetch.py (абсолютный: относительно чего
// запускать сервер, если рабочая директория не корень репозитория).
func (p *Python) Script() string { return p.script }

// GetGroups — каталог групп: --groups, разбор плоского списка.
func (p *Python) GetGroups(ctx context.Context) ([]Group, error) {
	if p == nil {
		return nil, ErrNotConfigured
	}

	out, err := p.run(ctx, "--groups")
	if err != nil {
		return nil, err
	}

	return p.decodeGroups(out)
}

// decodeGroups — JSON парсера в каталог групп.
//
// Вынесено на тех же правах, что и decodeMonth: тест паритета зовёт этот же
// разбор на строке от живого python3 и сравнивает с Go-разбором каталога.
func (p *Python) decodeGroups(out []byte) ([]Group, error) {
	var data struct {
		Groups []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Department string `json:"department"`
			Course     string `json:"course"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(out, &data); err != nil {
		return nil, errors.New("apeks: парсер вернул каталог групп в неизвестном формате")
	}

	groups := make([]Group, 0, len(data.Groups))
	for _, item := range data.Groups {
		id, name := norm(item.ID), norm(item.Name)
		if id == "" || name == "" {
			continue
		}
		groups = append(groups, Group{
			ID:         id,
			Name:       name,
			Department: norm(item.Department),
			Course:     norm(item.Course),
		})
	}
	if len(groups) == 0 {
		return nil, errors.New("apeks: парсер вернул пустой каталог групп")
	}

	return groups, nil
}

// GetMonth — пары группы за месяц: --group-id/--month/--year.
//
// Как и у клиента, пустой список пар — не ошибка: месяцы каникул у источника
// выглядят ровно так, и страница обязана показать «пар нет».
func (p *Python) GetMonth(ctx context.Context, groupID string, month, year int) (*Month, error) {
	if p == nil {
		return nil, ErrNotConfigured
	}
	if err := checkMonthArgs(groupID, month, year); err != nil {
		return nil, err
	}

	out, err := p.run(ctx,
		"--group-id", groupID,
		"--month", fmt.Sprintf("%d", month),
		"--year", fmt.Sprintf("%d", year),
	)
	if err != nil {
		return nil, err
	}

	return p.decodeMonth(out, groupID)
}

// decodeMonth — JSON парсера в структуры источника.
//
// Вынесено из GetMonth отдельно ради теста паритета: он подаёт этот же разбор
// строке, полученной от живого python3 на фикстуре, и сравнивает с Go-разбором.
// Без выноса паритет проверялся бы только через запуск процесса, а значит
// зависел бы от того, найден ли интерпретатор.
func (p *Python) decodeMonth(out []byte, groupID string) (*Month, error) {
	var data struct {
		GroupID   string         `json:"group_id"`
		GroupName string         `json:"group_name"`
		StartDate string         `json:"start_date"`
		EndDate   string         `json:"end_date"`
		Lessons   []pythonLesson `json:"lessons"`
	}
	if err := json.Unmarshal(out, &data); err != nil {
		return nil, errors.New("apeks: парсер вернул расписание в неизвестном формате")
	}

	lessons := make([]Lesson, 0, len(data.Lessons))
	for i, raw := range data.Lessons {
		date := norm(raw.Date)
		start, end := splitTime(raw.Time)
		externalID := norm(raw.ExternalID)
		if externalID == "" {
			// Без идентификатора строку нечем дедуплицировать: повторный
			// импорт наплодил бы копий. Считаем id по содержимому — тот же
			// рецепт, что в fetch.py, если бы поле пропало.
			externalID = lessonFingerprint(date, i, raw)
		}

		lessons = append(lessons, Lesson{
			ExternalID:   externalID,
			Date:         date,
			DayKey:       dayKey(date),
			Weekday:      weekday(date),
			LessonNumber: raw.LessonNumber,
			Time:         norm(raw.Time),
			Start:        start,
			End:          end,
			Group:        norm(raw.Group),
			Subgroup:     norm(raw.Subgroup),
			Discipline:   norm(raw.Discipline),
			Type:         norm(raw.LessonType),
			// Аудитория вырезается из HTML так же, как в Go-разборе: Python
			// оставляет разметку иконки у источника, а фронтенд экранирует
			// текст, и «<i class=…>» ушло бы на страницу буквами.
			Room:    stripHTML(raw.Room),
			Teacher: norm(raw.Teacher),
		})
	}

	id := norm(data.GroupID)
	if id == "" {
		id = groupID
	}

	return &Month{
		GroupID:   id,
		GroupName: norm(data.GroupName),
		StartDate: norm(data.StartDate),
		EndDate:   norm(data.EndDate),
		Lessons:   lessons,
	}, nil
}

// pythonLesson — одна строка вывода fetch.py. Имена полей совпадают с
// полями dataclassа Lesson из parser.py: мост ничего не переименовывает.
type pythonLesson struct {
	ExternalID   string `json:"external_id"`
	Date         string `json:"date"`
	Day          string `json:"day"`
	LessonNumber int    `json:"lesson_number"`
	Time         string `json:"time"`
	Group        string `json:"group"`
	Subgroup     string `json:"subgroup"`
	Discipline   string `json:"discipline"`
	LessonType   string `json:"lesson_type"`
	Room         string `json:"room"`
	Teacher      string `json:"teacher"`
}

// run запускает парсер и возвращает его stdout.
//
// Аргументы собираются списком, без shell: путь к скрипту и id группы приходят
// из конфигурации и каталога, и собирать их строкой значило бы однажды
// обнаружить, что имя группы с пробелом ломает импорт.
func (p *Python) run(ctx context.Context, args ...string) ([]byte, error) {
	runCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, p.python, append([]string{p.script}, args...)...)
	cmd.Env = parserEnv(os.Environ(), p.token, p.baseURL)
	// Контекст сам по себе убивает только ожидающий процесс; Cancel нужен,
	// чтобы вместе с парсером ушёл и его ребёнок, если тот поднимется
	// (requests держит соединение, а зависший python3 пережил бы запрос).
	cmd.Cancel = func() error { return cmd.Process.Kill() }
	cmd.WaitDelay = 2 * time.Second

	var stdout cappedWriter
	var stderr cappedWriter
	stdout.limit = maxParserOutput
	stderr.limit = maxParserStderr
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	if runCtx.Err() != nil && errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("apeks: парсер расписания не завершился за %s", p.timeout)
	}
	if err != nil {
		return nil, p.parserError(err, &stderr)
	}
	if stdout.overflow {
		return nil, fmt.Errorf("apeks: вывод парсера больше %d МиБ", maxParserOutput>>20)
	}
	if len(stdout.buf) == 0 {
		return nil, errors.New("apeks: парсер расписания ничего не вывел")
	}

	return stdout.buf, nil
}

// parserError превращает неудачный запуск в ошибку с фиксированной формой.
//
// Коды выхода заданы в fetch.py: 2 — неверный запуск или не задан токен,
// 1 — источник не ответил либо ответ не разобрался. Текст stderr дописывается
// одной строкой: он нужен админу в логе процесса, но в ответ браузеру не
// уходит (обработчик пишет свой общий текст).
func (p *Python) parserError(err error, stderr *cappedWriter) error {
	code := -1
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	}

	reason := p.scrub(strings.Join(strings.Fields(string(stderr.buf)), " "))
	if reason == "" {
		reason = err.Error()
	}

	switch code {
	case 2:
		return fmt.Errorf("%w (код 2): %s", ErrParserUnavailable, reason)
	case 1:
		return fmt.Errorf("apeks: парсер не привёз расписание (код 1): %s", reason)
	default:
		return fmt.Errorf("apeks: запуск парсера не удался: %s", reason)
	}
}

// scrub вычищает токен из текста, приехавшего от чужого кода.
//
// Python-часть его не печатает, но полагаться на чужую аккуратность в вопросе,
// где цена — доступ к API вуза в логе процесс-менеджера, нельзя: сообщение об
// ошибке HTTP может содержать полный URL с query.
func (p *Python) scrub(text string) string {
	if p.token != "" {
		text = strings.ReplaceAll(text, p.token, "[токен скрыт]")
	}
	return text
}

// parserEnv возвращает окружение дочернего процесса с одним изменением:
// APEKS_TOKEN и APEKS_API_URL берутся из собранного клиента, а не от наследника.
//
// Дубликаты вырезаются, а не дописываются: getenv оставляет первое или
// последнее вхождение в зависимости от библиотеки, и полагаться на это значит
// однажды отправить парсеру чужой токен из окружения деплоя.
func parserEnv(environ []string, token, baseURL string) []string {
	out := make([]string, 0, len(environ)+2)
	for _, item := range environ {
		if strings.HasPrefix(item, "APEKS_TOKEN=") || strings.HasPrefix(item, "APEKS_API_URL=") {
			continue
		}
		out = append(out, item)
	}
	return append(out, "APEKS_TOKEN="+token, "APEKS_API_URL="+baseURL)
}

// splitTime разбирает «08:30 – 10:05» на начало и конец.
//
// Тире — такое же, как у источника (en dash), поэтому разделитель ищется среди
// трёх вариантов, а не берётся литералом: с другим знаком пара не должна
// превращаться в «время неизвестно».
func splitTime(value string) (start string, end string) {
	text := norm(value)
	if text == "" {
		return "", ""
	}
	for _, sep := range []string{" – ", " — ", " - ", "~"} {
		if parts := strings.SplitN(text, sep, 2); len(parts) == 2 {
			return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		}
	}
	return text, ""
}

// checkMonthArgs — границы запроса, общие для обоих движков.
//
// Проверка вынесена из Client.GetMonth без изменения формулировок: мост обязан
// отклонять тот же мусор по тем же причинам, иначе два движка начали бы
// по-разному отвечать на одинаково кривой запрос админа.
func checkMonthArgs(groupID string, month, year int) error {
	groupID = norm(groupID)
	if groupID == "" {
		return errors.New("apeks: пустой идентификатор группы")
	}
	if month < 1 || month > 12 {
		return fmt.Errorf("apeks: месяц %d вне диапазона 1..12", month)
	}
	if year < 2000 || year > 2100 {
		return fmt.Errorf("apeks: год %d вне диапазона 2000..2100", year)
	}
	return nil
}

// cappedWriter — приёмник вывода с потолком.
//
// buf растёт не больше limit, overflow говорит отдельно: молча обрезать вывод
// значило бы разобрать неполный JSON и отдать админу ошибку «неизвестный
// формат» вместо честного «парсер принёс слишком много».
type cappedWriter struct {
	buf      []byte
	limit    int
	overflow bool
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	written := len(p)
	room := w.limit - len(w.buf)
	switch {
	case room <= 0:
		w.overflow = true
	case len(p) > room:
		w.buf = append(w.buf, p[:room]...)
		w.overflow = true
	default:
		w.buf = append(w.buf, p...)
	}
	return written, nil
}

var _ io.Writer = (*cappedWriter)(nil)

// lessonFingerprint — идентификатор строки, если парсер его не прислал.
//
// FNV-1a, а не sha1: это ключ дедупликации, а не защита, и поднимать
// криптографический пакет ради хеша строки не за что.
func lessonFingerprint(date string, index int, raw pythonLesson) string {
	hash := fnv.New64a()
	_, _ = fmt.Fprintf(hash, "%s|%d|%s|%s|%s|%s|%s|%s|%s",
		date, index, raw.Time, raw.Group, raw.Subgroup, raw.Discipline, raw.LessonType, raw.Room, raw.Teacher)
	return fmt.Sprintf("py:%016x", hash.Sum64())
}
