package main

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/NumerR/SOS/internal/apierror"
)

// Тесты страницы-заглушки для неизвестного пути (T3.1, ADR-0013).
//
// Проверяются через настоящий маршрутизатор, а не через обработчик напрямую:
// смысл ровно в том, какой обработчик выбирает маршрутизатор для пути, которого
// нет в реестре, и какой код он при этом отдаёт.

func getRaw(t *testing.T, url string) (int, string, string) {
	t.Helper()

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("читать ответ %s: %v", url, err)
	}

	return resp.StatusCode, resp.Header.Get("Content-Type"), string(body)
}

func TestUnknownPagePathServesNotFoundPage(t *testing.T) {
	// Раздача страниц идёт относительно рабочего каталога процесса (web/...),
	// поэтому тест обязан стоять в корне репозитория — там же, где живой
	// сервер запускает README.
	t.Chdir("../..")

	srv, _ := newTestServer(t)

	status, contentType, body := getRaw(t, srv.URL+"/net-takoi-stranitsy")
	if status != http.StatusNotFound {
		t.Fatalf("код ответа: %d, ожидалось 404", status)
	}
	if !strings.HasPrefix(contentType, "text/html") {
		t.Errorf("несуществующая страница должна отдаваться HTML, получено %q", contentType)
	}
	if !strings.Contains(body, "Страница не найдена") {
		t.Errorf("в теле нет заголовка страницы: %s", firstRunes(body, 200))
	}

	// Навигация есть: человек, попавший на 404, должен остаться в приложении.
	for _, href := range []string{`href="/"`, `href="/login"`, `href="/schedule"`} {
		if !strings.Contains(body, href) {
			t.Errorf("на 404 нет ссылки %s", href)
		}
	}
}

func TestUnknownDeepPathStillServesNotFoundPage(t *testing.T) {
	t.Chdir("../..")

	srv, _ := newTestServer(t)

	// Глубина пути — ровно тот случай, из-за которого на 404.html ссылки
	// абсолютные: относительный «login» разрешился бы в /a/b/login.
	status, _, body := getRaw(t, srv.URL+"/a/b/c/d")
	if status != http.StatusNotFound {
		t.Fatalf("код ответа: %d, ожидалось 404", status)
	}
	if !strings.Contains(body, "Такой страницы на сервере нет") {
		t.Errorf("глубокий путь получил не страницу 404: %s", firstRunes(body, 200))
	}
}

func TestUnknownAPIPathAnswersJSON(t *testing.T) {
	t.Chdir("../..")

	srv, _ := newTestServer(t)

	status, contentType, body := getRaw(t, srv.URL+"/api/net-takogo")
	if status != http.StatusNotFound {
		t.Fatalf("код ответа: %d, ожидалось 404", status)
	}
	if !strings.HasPrefix(contentType, "application/json") {
		t.Errorf("API-отказ должен быть JSON, получено %q (%s)", contentType, body)
	}
	// Конверт ADR-0003, а не просто «JSON с полем error»: у неизвестного пути
	// тот же код, что у неизвестного курса — клиент различает их по статусу.
	if code := errorCode(t, body); code != apierror.ErrNotFound {
		t.Errorf("код отказа %q, ожидалось %q", code, apierror.ErrNotFound)
	}

	// Тот же ответ для метода, который у существующего маршрута неизвестен,
	// не отдаёт 404: это задача маршрутизатора (405), проверяется в
	// modules_test.go — здесь только убедимся, что не сломали.
	resp, err := http.Post(srv.URL+"/api/net-takogo", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST неизвестного API-пути: %d, ожидалось 404", resp.StatusCode)
	}
}

func TestKnownPagesStayServed(t *testing.T) {
	t.Chdir("../..")

	srv, _ := newTestServer(t)

	// Ловушка, которую ловит этот тест: catch-all «/» с NotFound вместо
	// http.NotFound легко срезает и главную.
	for path, marker := range map[string]string{
		"/":         "<title>SOS",
		"/login":    "Вход",
		"/schedule": "Расписание",
	} {
		status, contentType, body := getRaw(t, srv.URL+path)
		if status != http.StatusOK {
			t.Errorf("%s: код %d, ожидалось 200", path, status)
		}
		if !strings.HasPrefix(contentType, "text/html") {
			t.Errorf("%s: Content-Type %q, ожидалось HTML", path, contentType)
		}
		if !strings.Contains(body, marker) {
			t.Errorf("%s: в ответе нет «%s» — страница подменилась?", path, marker)
		}
	}
}

func TestNotFoundPageHasNoScripts(t *testing.T) {
	t.Chdir("../..")

	srv, _ := newTestServer(t)

	_, _, body := getRaw(t, srv.URL+"/net-takoi-stranitsy")
	if strings.Contains(body, "<script") {
		t.Error("страница 404 не должна зависеть от JS: её задача работать, когда приложение не работает")
	}
	if strings.Contains(body, "onclick=") {
		t.Error("инлайновых обработчиков на 404 быть не должно")
	}
}

func firstRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
