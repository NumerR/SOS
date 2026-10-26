package handlers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Замок ADR-0030: отказ 500 обязан идти через internalError, а не через сырой
// writeError.
//
// Правка прошла сорок семь мест, и следующее же добавление в обработчик
// способно молча вернуться к прежнему поведению: `writeError(w, 500, "что-то
// не получилось")` компилируется, проходит все тесты и не оставляет в логе
// ничего, кроме строки доступа. Поэтому проверка здесь не поведенческая, а
// статическая — она читает исходники пакета и живёт рядом с тем, что охраняет:
// в `go test ./...`, без нового шага в CI и без расширений чужеродного
// скрипта.
//
// Освобождения (см. exemptFiles) подписаны причиной и краснеют, если стали
// не нужны — тот же порядок, что у CONTRAST_EXEMPT (ADR-0019) и у PATH_EXEMPT
// в scripts/check-docs.mjs (ADR-0025).

// exemptFiles — файлы, где литерал 500 имеет право быть вне internalError.
var exemptFiles = map[string]string{
	// Сам helper: здесь writeError(w, 500, …) и должен вызываться.
	"errors.go": "определение internalError — единственное место, где 500 пишется напрямую",
	// Классификация ответа для строки доступа: сравнивает код, а не пишет его.
	"logging.go": "statusRecorder и уровень лога: `recorder.status >= 500`, а не выдача отказа",
	// Известный остаток, записанный в docs/01 §4: writeJSON не имеет доступа к
	// запросу, а заголовок ответа к моменту отказа уже отправлен.
	"response.go": "writeJSON: отказ кодирования — отдельная находка, у helper'а нет request-контекста",
}

func handlerSourceFiles(t *testing.T) []string {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("прочитать каталог пакета: %v", err)
	}

	names := []string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		names = append(names, entry.Name())
	}
	if len(names) < 10 {
		t.Fatalf("найден %d файлов исходников, а их десятки — разбор выродился", len(names))
	}
	return names
}

func TestInternalFailuresGoThroughHelper(t *testing.T) {
	used := map[string]int{}
	helperCalls := 0

	for _, name := range handlerSourceFiles(t) {
		body, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("прочитать %s: %v", name, err)
		}

		helperCalls += strings.Count(string(body), "internalError(w, r,")

		for index, line := range strings.Split(string(body), "\n") {
			if !strings.Contains(line, "http.StatusInternalServerError") {
				continue
			}
			reason, exempt := exemptFiles[name]
			if !exempt {
				t.Errorf("%s:%d: сырой 500 мимо internalError — причина не попадёт в лог (%s)", name, index+1, line)
				continue
			}
			used[name]++
			t.Logf("%s освобождено: %s", name, reason)
		}
	}

	if helperCalls < 40 {
		t.Fatalf("вызовов internalError %d, а после правки ADR-0030 их было 47 — проверка теряет смысл", helperCalls)
	}

	// Мёртвое освобождение красное: если литерал из файла ушёл, надо или
	// убрать строку из exemption, или объяснить, почему она осталась.
	for name, reason := range exemptFiles {
		if used[name] == 0 {
			t.Errorf("освобождение %s не использовано (%s) — оно больше не нужно", name, reason)
		}
	}
}

func TestHelperLogsCauseAndWritesEnvelope(t *testing.T) {
	body, err := os.ReadFile("errors.go")
	if err != nil {
		t.Fatalf("прочитать errors.go: %v", err)
	}
	source := string(body)

	start := strings.Index(source, "func internalError(")
	if start < 0 {
		t.Fatal("в errors.go нет internalError — helper переименован или удалён")
	}
	end := strings.Index(source[start:], "\n}")
	if end < 0 {
		t.Fatal("тело internalError не находится — правь тест вместе с helper'ом")
	}
	definition := source[start : start+end]

	for _, want := range []string{
		`requestLogger(r).Error("отказ сервера"`,
		`slog.Any("error", cause)`,
		`slog.String("reason", message)`,
		"writeError(w, http.StatusInternalServerError, message)",
	} {
		if !strings.Contains(definition, want) {
			t.Errorf("internalError больше не делает %s — лог или конверт рассинхронизируются", want)
		}
	}

	if strings.Contains(definition, "slog.String(\"error\"") {
		t.Error("причина записана строкой: error= теряет тип и текст сбоя, нужен slog.Any")
	}
}
