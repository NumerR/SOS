package apeks

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Тесты клиента источника.
//
// Всё ходит на httptest.NewTLSServer, а не на обычный http-тестсервер: клиент
// требует HTTPS (токен едет в query, и по плоскому каналу его слать нельзя),
// и проверка этого требования должна идти тем же путём, что и остальные
// запросы, — иначе «разрешить http в тестах» стало бы дырой в проде.

const testToken = "test-token-не-настоящий-000000"

// stubSource поднимает TLS-сервер с одним обработчиком и возвращает конфиг
// клиента, которому этот сервер принадлежит.
func stubSource(t *testing.T, handler http.HandlerFunc) (Config, *httptest.Server) {
	t.Helper()

	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)

	return Config{Token: testToken, BaseURL: server.URL, HTTP: server.Client()}, server
}

func envelope(t *testing.T, data any) string {
	t.Helper()

	raw, err := json.Marshal(map[string]any{"data": data})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestNewValidatesConfiguration(t *testing.T) {
	if _, err := New(Config{}); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("без токена ожидалась ErrNotConfigured, получили %v", err)
	}

	if _, err := New(Config{Token: "  "}); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("пробелы вместо токена не считаются настроенным: %v", err)
	}

	// Плоский HTTP запрещён именно на старте: дальше каждый запрос уносил бы
	// секрет открытым текстом.
	if _, err := New(Config{Token: testToken, BaseURL: "http://apeks.example.test/api"}); err == nil {
		t.Error("клиент собран по http:// — секрет ушёл бы в открытом тексте")
	} else if !strings.Contains(err.Error(), "HTTPS") {
		t.Errorf("текст ошибки %q не объясняет причину", err)
	}

	if _, err := New(Config{Token: testToken, BaseURL: "https://"}); err == nil {
		t.Error("адрес без хоста принят без ошибки")
	}

	client, err := New(Config{Token: testToken})
	if err != nil {
		t.Fatalf("конфигурация по умолчанию не собралась: %v", err)
	}
	if client.baseURL != DefaultBaseURL {
		t.Errorf("адрес %q, ожидалось значение по умолчанию", client.baseURL)
	}
	if client.timeout != defaultTimeout {
		t.Errorf("таймаут %v, ожидалось %v", client.timeout, defaultTimeout)
	}
}

func TestNilClientIsNotConfigured(t *testing.T) {
	var client *Client

	if _, err := client.GetGroups(context.Background()); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("nil-клиент вернул %v, ожидалось ErrNotConfigured", err)
	}
	if _, err := client.GetMonth(context.Background(), "494", 10, 2026); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("nil-клиент для месяца вернул %v", err)
	}
}

func TestGetMonthSendsTokenAndParsesData(t *testing.T) {
	var gotQuery url.Values
	var gotHeader http.Header

	cfg, _ := stubSource(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		gotHeader = r.Header
		_, _ = io.WriteString(w, envelope(t, map[string]any{
			"group":      map[string]any{"id": 494, "name": "Б-ИСиТ-22"},
			"group_id":   494,
			"start_date": "01.10.2026",
			"end_date":   "31.10.2026",
			"lessonTimes": []map[string]any{
				{"id": 1, "hour_from": 8, "minute_from": 30, "hour_to": 10, "minute_to": 5},
			},
			"lessons": []map[string]any{
				{"id": 178760, "date": "02.10.2026", "lesson_time_id": 1, "discipline": "Базы данных", "classroom": "к. 3/314"},
			},
		}))
	})

	client, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	month, err := client.GetMonth(context.Background(), "494", 10, 2026)
	if err != nil {
		t.Fatalf("GetMonth: %v", err)
	}

	if gotQuery.Get("token") != testToken {
		t.Error("источник не получил токен — запрос был бы отклонён как 403")
	}
	if gotQuery.Get("group_id") != "494" || gotQuery.Get("month") != "10" || gotQuery.Get("year") != "2026" {
		t.Errorf("параметры запроса: %v", gotQuery)
	}
	if gotHeader.Get("Accept") != "application/json" || gotHeader.Get("User-Agent") == "" {
		t.Errorf("заголовки запроса: %v", gotHeader)
	}

	if len(month.Lessons) != 1 || month.Lessons[0].ExternalID != "178760" {
		t.Fatalf("пары разобраны неверно: %+v", month.Lessons)
	}
	if month.GroupName != "Б-ИСиТ-22" || month.StartDate != "01.10.2026" {
		t.Errorf("метаданные месяца: %+v", month)
	}
}

func TestGetMonthValidatesArgumentsBeforeNetwork(t *testing.T) {
	cfg, _ := stubSource(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("запрос ушёл до проверки аргументов")
	})

	client, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		group string
		month int
		year  int
	}{
		{"пустая группа", "", 10, 2026},
		{"месяц 0", "494", 0, 2026},
		{"месяц 13", "494", 13, 2026},
		{"год 1999", "494", 10, 1999},
		{"год 2101", "494", 10, 2101},
	}

	for _, tc := range tests {
		if _, err := client.GetMonth(context.Background(), tc.group, tc.month, tc.year); err == nil {
			t.Errorf("%s: ошибка не возвращена", tc.name)
		}
	}
}

func TestGetGroupsWalksThreeLevels(t *testing.T) {
	cfg, _ := stubSource(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, envelope(t, map[string]any{
			"groups": map[string]any{
				"1": map[string]any{
					"name": "Институт математики и наук о данных",
					"items": map[string]any{
						"11": map[string]any{
							"name": "Информатика и вычислительная техника",
							"items": map[string]any{
								"494": map[string]any{"id": 494, "name": "Б-ИСиТ-22"},
								// Узел без имени: в Python-версии пропускается, и здесь
								// должен пропускаться, а не превращаться в пустую строку.
								"999": map[string]any{"id": 999},
							},
						},
					},
				},
			},
		}))
	})

	client, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	groups, err := client.GetGroups(context.Background())
	if err != nil {
		t.Fatalf("GetGroups: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("групп %d, ожидалась одна (узел без имени пропускается)", len(groups))
	}

	want := Group{ID: "494", Name: "Б-ИСиТ-22", Department: "Институт математики и наук о данных", Course: "Информатика и вычислительная техника"}
	if groups[0] != want {
		t.Errorf("группа %+v, ожидалось %+v", groups[0], want)
	}
}

// TestGetGroupsSurvivesEmptyLevels — пустые уровни дерева не роняют каталог.
//
// Поймано живой проверкой 24.10.2026: источник сериализует пустой словарь как
// [], и у 14 узлов из 659 «items» — пустой массив. Разбор с map[string]groupNode
// спотыкался на первом же таком узле и выдавал «каталог групп пришёл в
// неизвестном формате», то есть импорт каталога не работал вообще, хотя данные
// в ответе были полные.
func TestGetGroupsSurvivesEmptyLevels(t *testing.T) {
	cfg, _ := stubSource(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, envelope(t, map[string]any{
			"groups": map[string]any{
				// Отдел без курсов: ровно та форма, на которой разбор падал.
				"51": map[string]any{"name": "Факультеты, Медицинский институт", "items": []any{}},
				"1": map[string]any{
					"name": "Инженерно-технологический институт",
					"items": map[string]any{
						// Курс без групп — тот же quirk на уровень ниже.
						"8":  map[string]any{"name": "Аспирантура", "items": []any{}},
						"11": map[string]any{"name": "Информатика", "items": map[string]any{"494": map[string]any{"id": 494, "name": "Б-ИСиТ-22"}}},
					},
				},
			},
		}))
	})

	client, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	groups, err := client.GetGroups(context.Background())
	if err != nil {
		t.Fatalf("пустой уровень обнулил каталог: %v", err)
	}
	if len(groups) != 1 || groups[0].ID != "494" {
		t.Fatalf("группы %+v, ожидалась одна 494", groups)
	}
}

func TestGetGroupsRejectsEmptyCatalogue(t *testing.T) {
	// Пустой каталог — не «данных нет», а сломанный источник: страница после
	// такой синхронизации потеряла бы все группы.
	cfg, _ := stubSource(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, envelope(t, map[string]any{"groups": map[string]any{}}))
	})

	client, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetGroups(context.Background()); err == nil {
		t.Error("пустой каталог групп принят без ошибки")
	}
}

func TestErrorMessagesCarryNoSecret(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wants   []string
		notWant string
	}{
		{"403 — не принят токен", http.StatusForbidden, []string{"APEKS_TOKEN", "403"}, testToken},
		{"429 — частота", http.StatusTooManyRequests, []string{"429"}, testToken},
		{"500 — сбой источника", http.StatusInternalServerError, []string{"500"}, testToken},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, _ := stubSource(t, func(w http.ResponseWriter, r *http.Request) {
				// Текст ошибки источника намеренно содержит секрет: проверка в
				// том, что клиент его не копирует.
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, `{"error":"token "+`+`"`+testToken+`"`+`}`)
			})

			client, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}

			_, err = client.GetMonth(context.Background(), "494", 10, 2026)
			if err == nil {
				t.Fatalf("HTTP %d принят успешным", tc.status)
			}
			for _, want := range tc.wants {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("текст %q не содержит %q", err.Error(), want)
				}
			}
			if strings.Contains(err.Error(), tc.notWant) {
				t.Errorf("в тексте ошибки секрет: %q", err.Error())
			}
		})
	}
}

func TestTransportErrorIsScrubbed(t *testing.T) {
	cfg, server := stubSource(t, func(w http.ResponseWriter, r *http.Request) {})
	client, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Сервер гасим до запроса: получаем url.Error, в котором Go печатает полный
	// адрес включая query с токеном.
	server.Close()

	_, err = client.GetMonth(context.Background(), "494", 10, 2026)
	if err == nil {
		t.Fatal("запрос к мёртвому источнику не вернул ошибку")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("токен утёк в текст ошибки: %q", err.Error())
	}
	if strings.Contains(err.Error(), "token=") {
		t.Errorf("в тексте ошибки остался query источника: %q", err.Error())
	}
	if !strings.HasPrefix(err.Error(), "apeks: ") {
		t.Errorf("ошибка источника без префикса пакета: %q", err.Error())
	}

	// Отдельно проверяем сам scrub на строке, где секрет всё-таки есть:
	// защита не должна зависеть от того, какую форму выбрала транспортная ошибка.
	full := client.baseURL + "?token=" + testToken + "&group_id=494"
	scrubbed := client.scrub(errors.New("случайная ошибка: "+full), full)
	if strings.Contains(scrubbed.Error(), testToken) || strings.Contains(scrubbed.Error(), "group_id=494") {
		t.Errorf("scrub не вычистил query: %q", scrubbed.Error())
	}
}

func TestTimeoutMessageIsReadable(t *testing.T) {
	cfg, _ := stubSource(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(120 * time.Millisecond)
		_, _ = io.WriteString(w, envelope(t, map[string]any{"lessons": []any{}}))
	})
	cfg.Timeout = 40 * time.Millisecond

	client, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Транспорт из теста (srv.Client()) таймаута не знает, поэтому проверим
	// обрезание контекста: requestCtx живёт cfg.Timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	_, err = client.GetMonth(ctx, "494", 10, 2026)
	if err == nil {
		t.Fatal("просроченный контекст не стал ошибкой")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("секрет в тексте: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "не ответил") && !strings.Contains(err.Error(), "отменён") {
		t.Errorf("текст %q не похож на «источник не ответил»", err.Error())
	}
}

func TestContextCancellationStopsRequest(t *testing.T) {
	cfg, _ := stubSource(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})

	client, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := client.GetMonth(ctx, "494", 10, 2026); err == nil {
		t.Error("отменённый контекст проигнорирован")
	} else if strings.Contains(err.Error(), testToken) {
		t.Errorf("секрет в тексте: %q", err.Error())
	}
}

func TestResponseWithoutDataOrNotJsonIsRejected(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"нет поля data", `{"result":[]}`, "нет поля data"},
		{"не JSON", `<html>капча</html>`, "не JSON"},
		{"data пустое", `{"data":null}`, "нет поля data"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, _ := stubSource(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.body)
			})

			client, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}

			_, err = client.GetMonth(context.Background(), "494", 10, 2026)
			if err == nil {
				t.Fatalf("ответ %q принят успешным", tc.body)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("текст %q, ожидалось упоминание %q", err.Error(), tc.want)
			}
			// HTML-страница источника (капча, редирект на вход) не должна
			// копироваться в ответ админу целиком.
			if strings.Contains(err.Error(), "<html>") {
				t.Errorf("тело ответа источника попало в ошибку: %q", err.Error())
			}
		})
	}
}

func TestOversizedResponseIsRejected(t *testing.T) {
	cfg, _ := stubSource(t, func(w http.ResponseWriter, r *http.Request) {
		// Больше лимита, но поле data — в начале, чтобы проверка сработала по
		// размеру, а не по разбору.
		_, _ = io.WriteString(w, `{"data":{"lessons":[`)
		_, _ = io.WriteString(w, strings.Repeat(" ", maxResponseSize))
		_, _ = io.WriteString(w, `]}}`)
	})

	client, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.GetMonth(context.Background(), "494", 10, 2026)
	if err == nil {
		t.Fatal("ответ больше лимита принят")
	}
	if !strings.Contains(err.Error(), "МиБ") {
		t.Errorf("текст %q не объясняет, что ответ слишком большой", err.Error())
	}
}
