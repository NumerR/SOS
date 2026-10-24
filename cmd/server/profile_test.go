package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Тесты страницы профиля (остаток T3.1).
//
// Смысл ровно в уровне маршрутизатора: HTML-страницы идут мимо реестра прав,
// и «забыть зарегистрировать /profile» или «забыть проверку сессии» для
// страницы — тот же класс ошибки, который T1.1 закрыл для API (docs/02 §3).

// getPage — запрос к странице без перехода по редиректу и с куками сессии.
//
// srv.Client() редиректы следует, а здесь нужен именно первый ответ: только по
// нему видно, уводит ли сервер на вход, а не отдаёт ли чужую страницу.
func getPage(t *testing.T, srv *httptest.Server, path string, user *session) (int, string, string, string) {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("собрать запрос %s: %v", path, err)
	}
	if user != nil {
		for _, cookie := range user.cookies {
			req.AddCookie(cookie)
		}
	}

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("читать ответ %s: %v", path, err)
	}

	return resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Location"), string(raw)
}

func TestProfilePageRequiresSession(t *testing.T) {
	t.Chdir("../..")

	srv, users := newTestServer(t)

	// 1. Аноним: redirect на вход, и никакого тела профиля.
	status, _, location, body := getPage(t, srv, "/profile", nil)
	if status != http.StatusFound {
		t.Fatalf("аноним GET /profile: код %d, ожидалось 302", status)
	}
	if location != "/login" {
		t.Errorf("Location %q, ожидалось /login", location)
	}
	if strings.Contains(body, "passwordForm") {
		t.Error("анониму отдали разметку профиля вместо редиректа")
	}

	// 2. Студент: своя страница, HTML, панель безопасности и блок идентичности.
	student := loginAs(t, srv, users, "study1", "studentpass1")
	status, mime, _, html := getPage(t, srv, "/profile", student)
	if status != http.StatusOK {
		t.Fatalf("студент GET /profile: код %d, ожидалось 200", status)
	}
	if !strings.HasPrefix(mime, "text/html") {
		t.Errorf("Content-Type %q, ожидалось text/html", mime)
	}

	for _, marker := range []string{
		`data-layout="profile"`,   // шапку рисует общий компонент
		`id="profileIdentity"`,    // блок идентичности из GET /api/auth/me
		`id="passwordForm"`,       // панель смены пароля переехала сюда
		`id="logoutAllBtn"`,       // и «выйти везде» тоже
		`src="js/core/layout.js"`, // порядок загрузки соблюдён
		`src="js/profile.js"`,     // страничный скрипт подключён
	} {
		if !strings.Contains(html, marker) {
			t.Errorf("в ответе нет %q — страница собрана не из того файла?", marker)
		}
	}

	// 3. Секретов и чужих данных в разметке нет: страница статична, но проверка
	//    страхует от «впишем список пользователей прямо в HTML».
	for _, forbidden := range []string{"password_hash", "csrf_token=", "/api/admin/users"} {
		if strings.Contains(html, forbidden) {
			t.Errorf("в разметке профиля есть %q", forbidden)
		}
	}

	// 4. После выхода профиль снова redirect: сессию проверяет сервер, а не JS.
	if resp, body := post(srv, "/api/auth/logout", "", student); resp.StatusCode != http.StatusOK {
		t.Fatalf("logout: %d %s", resp.StatusCode, body)
	}
	if status, _, _, _ = getPage(t, srv, "/profile", student); status == http.StatusOK {
		t.Error("после выхода профиль отдаёт 200 — сессия проверялась не на сервере")
	}
}

func TestProfilePageServedForEveryRole(t *testing.T) {
	t.Chdir("../..")

	srv, users := newTestServer(t)

	// Профиль — своя страница для любой роли: права здесь нет, есть только
	// сессия. Провераем преподавателя и админа, чтобы «студент прошёл» не
	// означало «остальным отказали».
	for _, account := range []struct{ username, password string }{
		{"teach1", "teacherpass1"},
		{"root1", "adminpass1"},
	} {
		user := loginAs(t, srv, users, account.username, account.password)
		status, mime, _, html := getPage(t, srv, "/profile", user)
		if status != http.StatusOK {
			t.Errorf("%s GET /profile: код %d, ожидалось 200", account.username, status)
			continue
		}
		if !strings.HasPrefix(mime, "text/html") {
			t.Errorf("%s: Content-Type %q, ожидалось text/html", account.username, mime)
		}
		if !strings.Contains(html, `data-layout="profile"`) {
			t.Errorf("%s: в ответе нет страницы профиля", account.username)
		}
	}
}

func TestAdminPanelStillRedirectsNonAdminToDashboard(t *testing.T) {
	t.Chdir("../..")

	srv, users := newTestServer(t)
	student := loginAs(t, srv, users, "study1", "studentpass1")

	// Соседняя страница, чтобы правка страниц не «съела» существующий отказ:
	// /admin остаётся redirect на /dashboard, а не 403 в JSON.
	status, _, location, _ := getPage(t, srv, "/admin", student)
	if status != http.StatusFound || location != "/dashboard" {
		t.Errorf("студент GET /admin: %d %s, ожидалось 302 на /dashboard", status, location)
	}
}
