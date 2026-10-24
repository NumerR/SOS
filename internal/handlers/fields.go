package handlers

import (
	"net/http"
	"strings"
	"unicode/utf8"
)

// Границы длины текстовых полей.
//
// readJSON уже режет тело целиком (1 МиБ), но это не мешает прислать
// «название курса» в 900 килобайт: такая строка ляжет в БД, в выдачу API и
// в DOM каждой страницы списка. Числа взяты из фронтовых полей (title у
// курсов, заданий и событий один и тот же), а не «с потолка»: интерфейс и
// сервер должны отклонять одно и то же.
const (
	maxTitleLength       = 200
	maxDescriptionLength = 2000

	// Имя преподавателя в курсе — это человек, а не позиция каталога;
	// граница та же, что у заголовков, иначе «преподаватель с длинным ФИО»
	// теряет курс.
	maxPersonNameLength = 200
	maxCategoryLength   = 64
	maxURLLength        = 2000

	// Сдача работы — текст студента: ссылка на репозиторий, список, абзац
	// пояснения. Полтысячи символов тут мало, а десять тысяч — уже больше,
	// чем любой разумный ответ на домашнее задание.
	maxSubmissionLength = 20000

	// Шкала оценок задана интерфейсом (modals.js: input min=0 max=10),
	// сервер проверяет ровно её — иначе «оценка 4000» остаётся в базе и
	// ломает средний балл.
	minScore = 0.0
	maxScore = 10.0
)

// runeLen считает символы, а не байты: в русском тексте «ё» — один символ и
// два байта, поэтому байтовая граница превратила бы «200 символов» в ~100.
func runeLen(value string) int {
	return utf8.RuneCountInString(strings.TrimSpace(value))
}

// validateLength пишет 422 и возвращает false, если поле длиннее границы.
// Формулировка ошибки совпадает со старой в курсах, ресурсах и событиях,
// чтобы фронтенд и тесты не пришлось переписывать.
func validateLength(w http.ResponseWriter, field, value string, limit int) bool {
	if runeLen(value) > limit {
		writeError(w, http.StatusUnprocessableEntity, field+" is too long")
		return false
	}
	return true
}

// validateScore проверяет, что оценка попадает в шкалу. NaN сюда физически не
// доходит: JSON не умеет ни NaN, ни Infinity, а декодер отклонит такое тело
// раньше, чем мы увидим req.Score.
func validateScore(w http.ResponseWriter, score float64) bool {
	if score < minScore || score > maxScore {
		writeError(w, http.StatusUnprocessableEntity, "score must be between 0 and 10")
		return false
	}
	return true
}
