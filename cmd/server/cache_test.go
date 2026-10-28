package main

import (
	"net/http"
	"testing"
)

// Тесты адресов и кэша страниц (ADR-0037).
//
// Проверяются через настоящий маршрутизатор: смысл в том, какой обработчик
// выбирает ServeMux для «/schedule.html» и какие заголовки получает браузер,
// а не в поведении отдельной функции.

func TestOldHTMLAddressesRedirectToCleanRoutes(t *testing.T) {
	t.Chdir("../..")

	srv, _ := newTestServer(t)

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	for _, alias := range [][2]string{
		{"/index.html", "/"},
		{"/login.html", "/login"},
		{"/register.html", "/register"},
		{"/dashboard.html", "/dashboard"},
		{"/profile.html", "/profile"},
		{"/admin.html", "/admin"},
		{"/schedule.html", "/schedule"},
	} {
		resp, err := client.Get(srv.URL + alias[0])
		if err != nil {
			t.Fatalf("GET %s: %v", alias[0], err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusMovedPermanently {
			t.Errorf("%s: код %d, ожидалось 301 на %s", alias[0], resp.StatusCode, alias[1])
		}
		if location := resp.Header.Get("Location"); location != alias[1] {
			t.Errorf("%s: Location %q, ожидалось %q", alias[0], location, alias[1])
		}
	}

	// Строка запроса — часть адреса расписания (например, номер группы):
	// редирект не имеет права её терять.
	resp, err := client.Get(srv.URL + "/schedule.html?group=B-ISiT-22")
	if err != nil {
		t.Fatalf("GET с параметром: %v", err)
	}
	defer resp.Body.Close()
	if location := resp.Header.Get("Location"); location != "/schedule?group=B-ISiT-22" {
		t.Errorf("строка запроса потеряна: Location %q", location)
	}
}

func TestPagesAndAssetsAskBrowserToRevalidate(t *testing.T) {
	t.Chdir("../..")

	srv, _ := newTestServer(t)

	// Страница и два вида статики: без no-cache браузер кэширует их
	// эвристически, и правка не видна до жёсткой перезагрузки.
	for _, path := range []string{"/schedule", "/css/style.css", "/js/core/theme.js"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()

		if got := resp.Header.Get("Cache-Control"); got != "no-cache" {
			t.Errorf("%s: Cache-Control %q, ожидалось no-cache", path, got)
		}
		if resp.Header.Get("Last-Modified") == "" {
			t.Errorf("%s: нет Last-Modified — перепроверка не сможет ответить 304", path)
		}
	}

	// Перепроверка действительно дешёвая: неизменённая страница отвечает 304,
	// а не 200 с телом.
	first, err := http.Get(srv.URL + "/schedule")
	if err != nil {
		t.Fatalf("первый GET /schedule: %v", err)
	}
	first.Body.Close()
	lastModified := first.Header.Get("Last-Modified")

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/schedule", nil)
	if err != nil {
		t.Fatalf("собрать запрос: %v", err)
	}
	req.Header.Set("If-Modified-Since", lastModified)

	second, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("повторный GET /schedule: %v", err)
	}
	defer second.Body.Close()

	if second.StatusCode != http.StatusNotModified {
		t.Errorf("повторный GET: код %d, ожидалось 304", second.StatusCode)
	}
}
