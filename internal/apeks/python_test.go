package apeks

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// Тесты моста к приложенному Python-парсеру.
//
// Три слоя, и каждый закрывает свой риск:
//
//   1. Проверка конфигурации на старте: отсутствие токена, скрипта или
//      интерпретатора должно быть видно до первого импорта, а не в виде 502
//      без объяснений.
//   2. Сам запуск — на подставном «интерпретаторе» (POSIX sh). Он проверяет
//      договорённость между Go и Python: первым аргументом идёт fetch.py,
//      режим — следующими, токен приходит только окружением, коды выхода
//      превращаются в те ошибки, которые обещаны в fetch.py. Живой Python для
//      этого не нужен, и тест не падает там, где его нет.
//   3. Паритет разбора — уже настоящим python3 на той же фикстуре, что и
//      Go-разбор. Это единственная проверка, которая подтверждает, что два
//      движка привозят одинаковые строки; если интерпретатора нет, тест
//      говорит «не проверено», а не «ок».

// parserScript — путь к fetch.py из корня пакета: мост обязан находить его
// относительно рабочей директории сервера (корень репозитория).
func parserScript(t *testing.T) string {
	t.Helper()

	path, err := filepath.Abs(filepath.Join("..", "..", "tools", "schedule-parser", "fetch.py"))
	if err != nil {
		t.Fatalf("путь к fetch.py не собрать: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("парсер не найден по %s: %v", path, err)
	}
	return path
}

// fakeInterpreter — подставный «python3»: скрипт видит аргументы и окружение
// ровно так, как их передаёт мост, и отвечает заготовленным выводом.
func fakeInterpreter(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "fake-python")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("подставной интерпретатор не записать: %v", err)
	}
	return path
}

// newTestPython собирает мост с подставным интерпретатором и настоящим
// путём к fetch.py: проверка «первым аргументом пришёл скрипт» имеет смысл
// только так.
func newTestPython(t *testing.T, script string, timeout time.Duration) *Python {
	t.Helper()

	parser, err := NewPython(PythonConfig{
		Token:   "test-token-494",
		Script:  parserScript(t),
		Python:  script,
		Timeout: timeout,
	})
	if err != nil {
		t.Fatalf("мост не собран: %v", err)
	}
	return parser
}

func TestNewPythonChecksEnvironmentBeforeFirstImport(t *testing.T) {
	script := parserScript(t)

	t.Run("токен обязателен", func(t *testing.T) {
		if _, err := NewPython(PythonConfig{Script: script}); !errors.Is(err, ErrNotConfigured) {
			t.Errorf("без токена получили %v, ожидали ErrNotConfigured", err)
		}
	})

	t.Run("источник только по HTTPS", func(t *testing.T) {
		_, err := NewPython(PythonConfig{Token: "t", BaseURL: "http://apeks.example", Script: script})
		if err == nil || !strings.Contains(err.Error(), "HTTPS") {
			t.Errorf("открытый адрес принят: %v", err)
		}
	})

	t.Run("скрипт должен существовать", func(t *testing.T) {
		_, err := NewPython(PythonConfig{Token: "t", Script: filepath.Join(t.TempDir(), "net-fetch.py")})
		if !errors.Is(err, ErrParserUnavailable) {
			t.Errorf("отсутствующий скрипт дал %v, ожидали ErrParserUnavailable", err)
		}
	})

	t.Run("каталог вместо скрипта", func(t *testing.T) {
		_, err := NewPython(PythonConfig{Token: "t", Script: filepath.Dir(script)})
		if !errors.Is(err, ErrParserUnavailable) {
			t.Errorf("каталог дал %v, ожидали ErrParserUnavailable", err)
		}
	})

	t.Run("интерпретатор должен найтись", func(t *testing.T) {
		_, err := NewPython(PythonConfig{Token: "t", Script: script, Python: "sos-python3-kotoroy-net"})
		if !errors.Is(err, ErrParserUnavailable) {
			t.Errorf("без интерпретатора получили %v, ожидали ErrParserUnavailable", err)
		}
	})

	t.Run("штатная сборка", func(t *testing.T) {
		parser, err := NewPython(PythonConfig{Token: "t", Script: script})
		if err != nil {
			t.Fatalf("мост не собран: %v", err)
		}
		if parser.Engine() != EnginePython {
			t.Errorf("движок %q, ожидали %q", parser.Engine(), EnginePython)
		}
		if parser.Endpoint() != DefaultBaseURL {
			t.Errorf("адрес источника %q, ожидали %q", parser.Endpoint(), DefaultBaseURL)
		}
		if !strings.HasSuffix(parser.Script(), "fetch.py") {
			t.Errorf("путь к парсеру %q не ведёт к fetch.py", parser.Script())
		}
	})
}

func TestPythonGetMonthPassesContractToParser(t *testing.T) {
	// Договорённость с fetch.py: аргументы режима, токен окружением и никогда
	// в командной строке (ps переживает любой срок жизни токена).
	parser := newTestPython(t, fakeInterpreter(t, `
		if [ "$1" != "$EXPECT_FETCH" ]; then
			echo "первым аргументом ожидали fetch.py, получили $1" >&2; exit 2
		fi
		shift
		if [ "$*" != "--group-id 494 --month 10 --year 2026" ]; then
			echo "аргументы не те: $*" >&2; exit 2
		fi
		case " $* " in
			*" $APEKS_TOKEN "*) echo "токен просочился в аргументы" >&2; exit 2 ;;
		esac
		if [ -z "$APEKS_TOKEN" ]; then
			echo "APEKS_TOKEN пуст" >&2; exit 2
		fi
		printf '%s' '{"group_id":"494","group_name":"Б-ИСиТ-22","start_date":"01.10.2026","end_date":"31.10.2026","lessons":[{"external_id":"py:e9d3","date":"02.10.2026","day":"Пт","lesson_number":3,"time":"12:20 – 13:55","group":"Б-ИСиТ-22","subgroup":"Поточка","discipline":"Элективные курсы","lesson_type":"Лек","room":"<i class=\"fa fa-times\"></i> без ауд.","teacher":"Иванов И.И."}]}'
	`), 0)

	t.Setenv("EXPECT_FETCH", parser.Script())

	month, err := parser.GetMonth(context.Background(), "494", 10, 2026)
	if err != nil {
		t.Fatalf("импорт месяца не удался: %v", err)
	}

	if month.GroupID != "494" || month.GroupName != "Б-ИСиТ-22" {
		t.Errorf("группа прочитана как %q / %q, ожидали 494 / Б-ИСиТ-22", month.GroupID, month.GroupName)
	}
	if len(month.Lessons) != 1 {
		t.Fatalf("пар %d, ожидали 1", len(month.Lessons))
	}

	lesson := month.Lessons[0]
	if lesson.ExternalID != "py:e9d3" {
		t.Errorf("идентификатор строки %q, ожидали py:e9d3 — дедупликация импорта на нём", lesson.ExternalID)
	}
	if lesson.DayKey != 20261002 || lesson.Weekday != "Пт" {
		t.Errorf("день прочитан как %d (%s), ожидали 20261002 (Пт)", lesson.DayKey, lesson.Weekday)
	}
	if lesson.Start != "12:20" || lesson.End != "13:55" {
		t.Errorf("время разобрано как %q…%q, ожидали 12:20…13:55", lesson.Start, lesson.End)
	}
	// Единственное расхождение с выводом парсера: аудитория без HTML-разметки,
	// как в Go-разборе (страница экранирует текст, тег ушёл бы буквами).
	if lesson.Room != "без ауд." {
		t.Errorf("аудитория %q, ожидали «без ауд.» без разметки", lesson.Room)
	}
}

func TestPythonGetMonthFillsMissingExternalID(t *testing.T) {
	parser := newTestPython(t, fakeInterpreter(t, `
		printf '%s' '{"group_id":"494","lessons":[{"date":"02.10.2026","lesson_number":1,"time":"08:30 – 10:05","discipline":"Матан"}]}'
	`), 0)

	month, err := parser.GetMonth(context.Background(), "494", 10, 2026)
	if err != nil {
		t.Fatalf("импорт месяца не удался: %v", err)
	}
	if len(month.Lessons) != 1 {
		t.Fatalf("пар %d, ожидали 1", len(month.Lessons))
	}

	// Store пропускает строку с пустым external_id: молча потерять половину
	// месяца хуже, чем придумать ей стабильный ключ.
	if id := month.Lessons[0].ExternalID; !strings.HasPrefix(id, "py:") || len(id) != 19 {
		t.Errorf("идентификатор строки %q, ожидали py:<16 шестнадцатеричных>", id)
	}
	if month.Lessons[0].DayKey != 20261002 {
		t.Errorf("день %d, ожидали 20261002", month.Lessons[0].DayKey)
	}
}

func TestPythonGetGroups(t *testing.T) {
	parser := newTestPython(t, fakeInterpreter(t, `
		shift
		if [ "$*" != "--groups" ]; then echo "ожидали --groups, получили $*" >&2; exit 2; fi
		printf '%s' '{"groups":[{"id":"494","name":"Б-ИСиТ-22","department":"ИТ","course":"ПО"},{"id":"","name":"мусор"},{"id":"495","name":"Б-ИСиТ-23"}]}'
	`), 0)

	groups, err := parser.GetGroups(context.Background())
	if err != nil {
		t.Fatalf("каталог групп не получен: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("групп %d, ожидали 2: запись без id в каталоге не нужна", len(groups))
	}
	if groups[0].ID != "494" || groups[0].Name != "Б-ИСиТ-22" || groups[0].Course != "ПО" {
		t.Errorf("первая группа %+v, ожидали 494 / Б-ИСиТ-22 / ПО", groups[0])
	}
}

func TestPythonMapsExitCodesToErrors(t *testing.T) {
	t.Run("код 2 — парсер не запускается", func(t *testing.T) {
		parser := newTestPython(t, fakeInterpreter(t, `echo "Токен источника не задан" >&2; exit 2`), 0)
		_, err := parser.GetMonth(context.Background(), "494", 10, 2026)
		if !errors.Is(err, ErrParserUnavailable) {
			t.Errorf("получили %v, ожидали ErrParserUnavailable", err)
		}
	})

	t.Run("код 1 — источник не ответил", func(t *testing.T) {
		parser := newTestPython(t, fakeInterpreter(t, `echo "Ошибка запроса к АПЭКС-ВУЗ: 403" >&2; exit 1`), 0)
		_, err := parser.GetMonth(context.Background(), "494", 10, 2026)
		if err == nil || !strings.Contains(err.Error(), "403") {
			t.Errorf("получили %v, ожидали причину от парсера", err)
		}
		if errors.Is(err, ErrParserUnavailable) {
			t.Errorf("отказ источника записан как «парсер не запускается»: %v", err)
		}
	})

	t.Run("пустой вывод", func(t *testing.T) {
		parser := newTestPython(t, fakeInterpreter(t, "exit 0"), 0)
		if _, err := parser.GetMonth(context.Background(), "494", 10, 2026); err == nil {
			t.Errorf("молчание парсера принято за успех")
		}
	})

	t.Run("нечитаемый вывод", func(t *testing.T) {
		parser := newTestPython(t, fakeInterpreter(t, `echo "не json"; exit 0`), 0)
		_, err := parser.GetMonth(context.Background(), "494", 10, 2026)
		if err == nil || !strings.Contains(err.Error(), "неизвестном формате") {
			t.Errorf("получили %v, ожидали ошибку формата", err)
		}
	})

	t.Run("потолок по времени", func(t *testing.T) {
		parser := newTestPython(t, fakeInterpreter(t, "sleep 2"), 100*time.Millisecond)
		start := time.Now()
		_, err := parser.GetMonth(context.Background(), "494", 10, 2026)
		if err == nil || !strings.Contains(err.Error(), "не завершился") {
			t.Errorf("получили %v, ожидали ошибку таймаута", err)
		}
		if spent := time.Since(start); spent > time.Second {
			t.Errorf("таймаут сработал через %s, ожидали около 100ms", spent)
		}
	})
}

func TestPythonHidesTokenFromParserErrors(t *testing.T) {
	// Парсер может напечатать токен в тексте ошибки — например, вместе с
	// полным URL запроса. В наш лог и в ответ админу он уйти не должен.
	parser := newTestPython(t, fakeInterpreter(t, `
		echo "запрос не удался: https://example/api?token=$APEKS_TOKEN" >&2
		exit 1
	`), 0)

	_, err := parser.GetMonth(context.Background(), "494", 10, 2026)
	if err == nil {
		t.Fatalf("ошибки нет, хотя парсер завершился с кодом 1")
	}
	if strings.Contains(err.Error(), "test-token-494") {
		t.Errorf("токен виден в тексте ошибки: %v", err)
	}
	if !strings.Contains(err.Error(), "[токен скрыт]") {
		t.Errorf("вместо токена не появилось «[токен скрыт]»: %v", err)
	}
}

func TestPythonSharesMonthBoundsWithGoClient(t *testing.T) {
	parser := newTestPython(t, fakeInterpreter(t, `echo "не должно запускаться" >&2; exit 9`), 0)

	for _, args := range []struct {
		groupID     string
		month, year int
		why         string
	}{
		{"", 10, 2026, "пустая группа"},
		{"494", 13, 2026, "месяц вне календаря"},
		{"494", 10, 1999, "год вне диапазона"},
	} {
		_, err := parser.GetMonth(context.Background(), args.groupID, args.month, args.year)
		if err == nil {
			t.Errorf("%s: запрос прошёл", args.why)
			continue
		}
		if strings.Contains(err.Error(), "запускать") {
			t.Errorf("%s: границы проверил парсер, а не мост: %v", args.why, err)
		}
	}
}

// TestPythonParserMatchesGoParser — паритет двух движков на реальном срезе
// ответа источника.
//
// Это единственная проверка, которая подтверждает обещание «расписание от
// Python-парсера такое же, как от Go-разбора»: один и тот же файл проходит
// через parser.py и через parser.go, и строки обязаны сойтись до последнего
// поля. Идентификаторы строк сравниваются отдельно: у Go-версии это id пары у
// источника, у Python-моста — отпечаток содержимого (parser.py его не сохраняет).
func TestPythonParserMatchesGoParser(t *testing.T) {
	python, err := exec.LookPath(defaultPythonBin)
	if err != nil {
		t.Skipf("%s не найден: паритет Python-разбора и Go-разбора не проверен", defaultPythonBin)
	}

	script := parserScript(t)
	fixture, err := filepath.Abs(fixturePath())
	if err != nil {
		t.Fatalf("путь до фикстуры не собрать: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// --from-file не трогает сеть и не требует requests: разбор проверяется
	// там, где стоит только интерпретатор.
	cmd := exec.CommandContext(ctx, python, script, "--from-file", fixture)
	cmd.Dir = filepath.Dir(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("python3 %s --from-file: %v\n%s", script, err, out)
	}

	parser, err := NewPython(PythonConfig{Token: "t", Script: script, Python: python})
	if err != nil {
		t.Fatalf("мост не собран: %v", err)
	}

	fromPython, err := parser.decodeMonth(out, "494")
	if err != nil {
		t.Fatalf("вывод парсера не разобран: %v", err)
	}
	fromGo := parseFixture(t)

	if fromPython.GroupID != fromGo.GroupID || fromPython.GroupName != fromGo.GroupName {
		t.Errorf("группа: python %q/%q, go %q/%q",
			fromPython.GroupID, fromPython.GroupName, fromGo.GroupID, fromGo.GroupName)
	}
	if fromPython.StartDate != fromGo.StartDate || fromPython.EndDate != fromGo.EndDate {
		t.Errorf("период: python %q–%q, go %q–%q",
			fromPython.StartDate, fromPython.EndDate, fromGo.StartDate, fromGo.EndDate)
	}
	if len(fromPython.Lessons) != len(fromGo.Lessons) {
		t.Fatalf("пар: python %d, go %d", len(fromPython.Lessons), len(fromGo.Lessons))
	}

	for i := range fromGo.Lessons {
		py, goLesson := fromPython.Lessons[i], fromGo.Lessons[i]

		// ExternalID не сравнивается: причины разные у обоих движков, и
		// сходятся они только по смыслу «одна строка — один ключ».
		py.ExternalID, goLesson.ExternalID = "", ""
		if py != goLesson {
			t.Errorf("пара %d разошлась:\n  python %+v\n  go     %+v", i, py, goLesson)
		}
	}
}

// TestPythonGroupsMatchGoParser — паритет двух движков на каталоге групп.
//
// Второй срез живого ответа: пары за месяц сверяются тестом выше, здесь —
// дерево «институт -> курс -> группа», в котором пустой уровень пришёл списком.
// На этой форме Go-разбор каталога и падал, поэтому проверка стоит на фикстуре
// из живого среза, а не на синтетическом образце.
//
// Порядок групп сравниваться не может: Go обходит map, Python идёт по порядку
// ключей источника. Сходиться обязан набор, а не последовательность — отсюда
// сортировка перед сравнением.
func TestPythonGroupsMatchGoParser(t *testing.T) {
	python, err := exec.LookPath(defaultPythonBin)
	if err != nil {
		t.Skipf("%s не найден: паритет Python-разбора и Go-разбора каталога не проверен", defaultPythonBin)
	}

	script := parserScript(t)
	fixture, err := filepath.Abs(groupsFixturePath())
	if err != nil {
		t.Fatalf("путь до фикстуры не собрать: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, python, script, "--groups", "--from-file", fixture)
	cmd.Dir = filepath.Dir(script)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			t.Fatalf("python %s --groups --from-file: %v\n%s", script, err, exitErr.Stderr)
		}
		t.Fatalf("python %s --groups --from-file: %v", script, err)
	}

	parser, err := NewPython(PythonConfig{Token: "t", Script: script, Python: python})
	if err != nil {
		t.Fatalf("мост не собран: %v", err)
	}

	fromPython, err := parser.decodeGroups(out)
	if err != nil {
		t.Fatalf("вывод парсера не разобран: %v", err)
	}
	fromGo := parseGroupsFixture(t)

	sortGroups := func(in []Group) []Group {
		out := append([]Group(nil), in...)
		sort.Slice(out, func(i, j int) bool {
			if out[i].ID != out[j].ID {
				return out[i].ID < out[j].ID
			}
			return out[i].Name < out[j].Name
		})
		return out
	}

	fromPython, fromGo = sortGroups(fromPython), sortGroups(fromGo)
	if len(fromPython) != len(fromGo) {
		t.Fatalf("групп: python %d, go %d", len(fromPython), len(fromGo))
	}

	for i := range fromGo {
		if fromPython[i] != fromGo[i] {
			t.Errorf("группа %d (%s) разошлась:\n  python %+v\n  go     %+v",
				i, fromGo[i].ID, fromPython[i], fromGo[i])
		}
	}
}
