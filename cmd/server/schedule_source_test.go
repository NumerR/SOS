package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NumerR/SOS/internal/apeks"
)

// Тесты причины «источника нет» (ADR-0023).
//
// Админская панель обязана называть настоящую причину, по которой импорта нет,
// а не повторять одно и то же слово. Единственное место, где эта причина
// рождается, — newScheduleSourceClient: до теста она существовала только в
// комментариях и в логе старта, то есть проверялась глазами.
//
// Здесь проверяются три вещи, и все они стоят друг другу поперёк:
//
//   - имя переменной в missing всегда то, которое оператор обязан править
//     (нет python3 → PYTHON_BIN, а не APEKS_TOKEN);
//   - в missing и hint нет значения токена и абсолютных путей процесса —
//     панель показывает эти строки человеку через браузер, и уносить в них
//     текст ошибки моста нельзя;
//   - при поднявшемся источнике причина пустая: «причины нет» — тоже часть
//     контракта, иначе 503 или панель сочинят её заново.
//
// Живой python в тесте не запускается: PYTHON_BIN указывает на крошечный
// скрипт-заглушку во временном каталоге, а проверка интерпретатора в мосте —
// LookPath, то есть «файл существует и исполняется». Тест от этого становится
// независимым от того, стоит ли на машине разработчика python3 и requests.

// quietSourceLogs глушит лог старта на время теста: newScheduleSourceClient
// пишет в него свою причину, и в выводе `go test` эти строки только шум —
// проверяем мы возвращаемое значение, а не то, как оно отформатировано.
func quietSourceLogs(t *testing.T) {
	t.Helper()

	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
}

// setSourceEnv задаёт окружение источника целиком. Пустое значение == «переменной
// нет»: и токен, и движок, и путь к парсеру в коде трактуют пустую строку как
// отсутствие, поэтому сбрасывать окружение через t.Setenv("", "") безопасно и
// не оставляет зависимости от того, что было выставлено в оболочке.
func setSourceEnv(t *testing.T, token, engine, apiURL, parser, python string) {
	t.Helper()

	t.Setenv("APEKS_TOKEN", token)
	t.Setenv("SCHEDULE_SOURCE", engine)
	t.Setenv("APEKS_API_URL", apiURL)
	t.Setenv("SCHEDULE_PARSER", parser)
	t.Setenv("PYTHON_BIN", python)
}

// stubParser — исполняемый файл вместо fetch.py. Мост проверяет существование
// скрипта и интерпретатора, но не запускает их на старте, поэтому для «парсер
// есть» достаточно обычного файла.
func stubParser(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "fetch.py")
	if err := os.WriteFile(path, []byte("# заглушка: тест не запускает парсер\n"), 0o600); err != nil {
		t.Fatalf("записать заглушку парсера: %v", err)
	}

	return path
}

// stubPython — исполняемая заглушка интерпретатора: LookPath требует файл с
// битом исполнения.
func stubPython(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "python3")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("записать заглушку python: %v", err)
	}

	return path
}

func TestNewScheduleSourceClientNamesTheReason(t *testing.T) {
	quietSourceLogs(t)

	const token = "sekret-токен-не-попадается-в-ответ"

	cases := []struct {
		name        string
		noToken     bool
		engine      string
		apiURL      string
		python      string // "" — заглушка, путь — заведомо отсутствующий интерпретатор
		parser      bool   // есть ли путь к парсеру в окружении
		wantClient  bool
		wantMissing string
		wantHintHas []string
	}{
		{
			// Случай, который панель всегда описывала верно: токена нет, и
			// совет «задайте APEKS_TOKEN» — это про неё. hint остаётся пустым
			// именно поэтому.
			name:        "токена нет",
			noToken:     true,
			wantMissing: "APEKS_TOKEN",
		},
		{
			name:        "токен есть, python не найден",
			python:      "/no/such/python3",
			parser:      true,
			wantMissing: "PYTHON_BIN",
			wantHintHas: []string{"APEKS_TOKEN задан", "PYTHON_BIN", "SCHEDULE_PARSER", "SCHEDULE_SOURCE=go"},
		},
		{
			name:        "токен есть, адрес не по HTTPS",
			apiURL:      "http://apeks.example.invalid",
			parser:      true,
			wantMissing: "APEKS_API_URL",
			wantHintHas: []string{"APEKS_TOKEN задан", "HTTPS"},
		},
		{
			name:        "движок go, адрес не по HTTPS",
			engine:      apeks.EngineGo,
			apiURL:      "http://apeks.example.invalid",
			wantMissing: "APEKS_API_URL",
			wantHintHas: []string{"SCHEDULE_SOURCE=go", "HTTPS"},
		},
		{
			// Значение переменной в подсказку не подставляется: это свободная
			// строка окружения, а panel показывает строку человеку.
			name:        "SCHEDULE_SOURCE с опечаткой",
			engine:      "apex",
			wantMissing: "SCHEDULE_SOURCE",
			wantHintHas: []string{"python или go"},
		},
		{
			name:       "python поднялся",
			parser:     true,
			wantClient: true,
		},
		{
			name:       "go поднялся",
			engine:     apeks.EngineGo,
			wantClient: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parser := ""
			if tc.parser {
				parser = stubParser(t)
			}

			python := tc.python
			if tc.engine != apeks.EngineGo && tc.wantClient && python == "" {
				python = stubPython(t)
			}

			var tok string
			if !tc.noToken {
				tok = token
			}
			setSourceEnv(t, tok, tc.engine, tc.apiURL, parser, python)

			client, problem := newScheduleSourceClient()

			if tc.wantClient {
				if client == nil {
					t.Fatalf("источник обязан подняться, получили nil при причине %q", problem.missing)
				}
				if problem.missing != "" || problem.hint != "" {
					t.Errorf("при работающем источнике причина обязана быть пустой, получили %q / %q",
						problem.missing, problem.hint)
				}
				return
			}

			if client != nil {
				t.Fatalf("источник не собран, но клиент вернули: %T", client)
			}
			if problem.missing != tc.wantMissing {
				t.Errorf("missing = %q, ожидалось %q", problem.missing, tc.wantMissing)
			}
			for _, want := range tc.wantHintHas {
				if !strings.Contains(problem.hint, want) {
					t.Errorf("в подсказке нет %q: %q", want, problem.hint)
				}
			}
			if len(tc.wantHintHas) > 0 && strings.Contains(problem.hint, "задайте APEKS_TOKEN на сервере") {
				t.Errorf("подсказка советует завести токен, который задан: %q", problem.hint)
			}
			// При пустом hint панель остаётся со своей прежней фразой, и она
			// верна только когда токена правда нет.
			if len(tc.wantHintHas) == 0 && problem.hint != "" {
				t.Errorf("подсказку не просили, но она пришла: %q", problem.hint)
			}

			// Гигиена ответа: ни токен, ни путь процесса в панель не едут.
			if strings.Contains(problem.hint, token) || strings.Contains(problem.missing, token) {
				t.Errorf("значение токена попало в ответ панели: %q", problem.hint)
			}
			if parser != "" && strings.Contains(problem.hint, parser) {
				t.Errorf("абсолютный путь парсера попал в подсказку: %q", problem.hint)
			}
			if tc.engine == "apex" && strings.Contains(problem.hint, "apex") {
				t.Errorf("значение SCHEDULE_SOURCE из окружения подставлено в подсказку: %q", problem.hint)
			}
		})
	}
}

// TestNewScheduleSourceClientEngineIsReported — движок, который причина
// подразумевает, и движок, который клиент называет, должны совпадать: оператор
// выбирает путь по логу старта, а проверяет его по полю engine админского
// статуса, и расхождение этих двух значений оставляет админа с вопросом
// «что же у меня работает».
func TestNewScheduleSourceClientEngineIsReported(t *testing.T) {
	quietSourceLogs(t)

	setSourceEnv(t, "sekret-токен", apeks.EngineGo, "", "", "")

	client, problem := newScheduleSourceClient()
	if client == nil {
		t.Fatalf("движок go обязан подняться без python, причина %q", problem.missing)
	}
	if client.Engine() != apeks.EngineGo {
		t.Errorf("Engine() = %q, ожидалось %q", client.Engine(), apeks.EngineGo)
	}

	setSourceEnv(t, "sekret-токен", "", "", stubParser(t), stubPython(t))

	client, problem = newScheduleSourceClient()
	if client == nil {
		t.Fatalf("движок python с заглушками обязан подняться, причина %q", problem.missing)
	}
	if client.Engine() != apeks.EnginePython {
		t.Errorf("Engine() = %q, ожидалось %q", client.Engine(), apeks.EnginePython)
	}
}
