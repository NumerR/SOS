package main

import (
	"strings"
	"testing"
)

// Тесты страницы карты (ADR-0041).
//
// Уровень — настоящий маршрутизатор, как у профиля: HTML-страницы идут мимо
// реестра прав, и «забыть зарегистрировать /map» или «забыть проверку сессии»
// для страницы — тот же класс ошибки, который T1.1 закрыл для API, а для
// страниц закрывает только такой тест.

func TestMapPageRequiresSession(t *testing.T) {
	t.Chdir("../..")

	srv, users := newTestServer(t)

	// 1. Аноним уходит на вход и разметку карты не получает.
	status, _, location, body := getPage(t, srv, "/map", nil)
	if status != 302 {
		t.Fatalf("аноним GET /map: код %d, ожидалось 302", status)
	}
	if location != "/login" {
		t.Errorf("Location %q, ожидалось /login", location)
	}
	if strings.Contains(body, "mapCanvas") {
		t.Error("анониму отдали разметку карты вместо редиректа")
	}

	// 2. Студент: своя страница, и подключены ровно те файлы, из которых она
	//    собирается — библиотека локальна, а не с CDN (бриф п.6).
	student := loginAs(t, srv, users, "study1", "studentpass1")
	status, mime, _, html := getPage(t, srv, "/map", student)
	if status != 200 {
		t.Fatalf("студент GET /map: код %d, ожидалось 200", status)
	}
	if !strings.HasPrefix(mime, "text/html") {
		t.Errorf("Content-Type %q, ожидалось text/html", mime)
	}

	for _, marker := range []string{
		`data-layout="map"`,          // навигацию рисует общий компонент
		`id="mapCanvas"`,             // контейнер, в который садится Leaflet
		`src="js/vendor/leaflet.js?`, // библиотека из репозитория
		`href="css/leaflet.css?`,     // её стили тоже свои
		`src="js/map.js?`,            // страничный скрипт подключён
		`id="mapUnplaced"`,           // блок «чего на карте нет»
	} {
		if !strings.Contains(html, marker) {
			t.Errorf("в ответе нет %q — страница собрана не из того файла?", marker)
		}
	}

	// 3. Адрес подложки в разметке отсутствует: он приходит из переменной
	//    окружения через GET /api/map/config, а не вшивается в HTML и не
	//    уезжает в JS-константу (бриф п.2 и п.6).
	for _, forbidden := range []string{"tiles.test", "{z}/{x}/{y}", "https://", "APEKS_TOKEN"} {
		if strings.Contains(html, forbidden) {
			t.Errorf("в разметке карты есть %q — адрес подложки или секрет оказались в статике", forbidden)
		}
	}

	// 4. После выхода карта снова redirect: сессию проверяет сервер, а не JS.
	if resp, body := post(srv, "/api/auth/logout", "", student); resp.StatusCode != 200 {
		t.Fatalf("logout: %d %s", resp.StatusCode, body)
	}
	if status, _, _, _ = getPage(t, srv, "/map", student); status == 200 {
		t.Error("после выхода карта отдаёт 200 — сессия проверялась не на сервере")
	}
}

func TestMapPageServedForEveryRole(t *testing.T) {
	t.Chdir("../..")

	srv, users := newTestServer(t)

	// Права на странице нет — есть только факт входа. Проверено на обеих
	// «не-студенческих» ролях, чтобы «преподаватель прошёл» не означало, что
	// админу отказали, и наоборот.
	for _, account := range []struct{ username, password string }{
		{"teach1", "teacherpass1"},
		{"root1", "adminpass1"},
	} {
		user := loginAs(t, srv, users, account.username, account.password)
		status, _, _, html := getPage(t, srv, "/map", user)
		if status != 200 {
			t.Errorf("%s GET /map: код %d, ожидалось 200", account.username, status)
		}
		if !strings.Contains(html, "Карта кампуса") {
			t.Errorf("%s: в ответе нет заголовка страницы", account.username)
		}
	}
}
