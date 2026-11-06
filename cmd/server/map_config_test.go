package main

import "testing"

// Тест чтения конфигурации подложки из окружения.
//
// Проверяется одно: имя переменной в коде совпадает с именем, которое увидит
// оператор, и значение доходит до обработчика целым. Опечатка в `MAP_TILES_URL`
// дала бы карту без тайлов на правильно настроенном сервере — состояние, которое
// человек объяснил бы себе поломкой карты, а не пустой строкой в своём env.

func TestMapTilesFromEnvReadsBothVariables(t *testing.T) {
	quietSourceLogs(t)

	t.Setenv("MAP_TILES_URL", "https://tiles.example.test/{z}/{x}/{y}.png")
	t.Setenv("MAP_TILES_ATTRIBUTION", "Тестовый кампус")

	tiles := mapTilesFromEnv()

	if env, hint := tiles.Problem(); env != "" {
		t.Fatalf("годная конфигурация отвергнута (%s): %s", env, hint)
	}
	if tiles.Attribution != "Тестовый кампус" {
		t.Errorf("подписка не дочитана: %q", tiles.Attribution)
	}
}

func TestMapTilesFromEnvWithoutTilesIsNotAFailure(t *testing.T) {
	quietSourceLogs(t)

	// Пустое значение == «переменной нет»: сброс через t.Setenv безопасен, и
	// тест не зависит от того, что было экспортировано в оболочке.
	t.Setenv("MAP_TILES_URL", "")
	t.Setenv("MAP_TILES_ATTRIBUTION", "")

	tiles := mapTilesFromEnv()

	if env, _ := tiles.Problem(); env != "MAP_TILES_URL" {
		t.Errorf("конфигурация %q, ожидалось имя переменной", env)
	}
	if tiles.URL != "" {
		t.Errorf("пустая переменная превратилась в адрес %q", tiles.URL)
	}
}
