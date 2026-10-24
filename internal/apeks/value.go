package apeks

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode"
)

// Нормализация значений источника.
//
// АПЭКС-ВУЗ отдаёт одно и то же поле в разных типах: идентификатор группы —
// числом (494) или строкой ("494"), subgroup_id — числом или null, час —
// числом. Python-версия парсера обходила это str(value or ""), то есть
// приводила любое значение к строке и не падала ни на чём.
//
// Здесь тот же принцип, но заявленный типами: flex и flag принимают любую из
// форм, поэтому неизвестное значение поля не превращается в ошибку разбора
// всего ответа. Это важно не для удобства: один unexpected type на одном поле
// у одной пары обнулил бы импорт месяца целиком, и страница осталась бы без
// расписания из-за строчки, которая нам не нужна.

// flex — скаляр, ставший строкой после нормализации.
type flex struct {
	value string
}

// UnmarshalJSON принимает строку, число, булево и null.
//
// Для null поле остаётся пустым — ровно как None у Python-версии. Строковое
// значение разворачивается из JSON-экранирования, остальное берётся как есть
// (число в JSON уже каноничная последовательность цифр).
func (f *flex) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))

	switch {
	case trimmed == "null" || trimmed == "":
		f.value = ""
		return nil
	case trimmed[0] == '"':
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		f.value = norm(s)
		return nil
	case trimmed[0] == '{' || trimmed[0] == '[':
		// Сложное значение в скалярном поле — не повод ронять разбор:
		// считаем его пустым, вызывающий код увидит «поля нет».
		f.value = ""
		return nil
	default:
		f.value = norm(trimmed)
		return nil
	}
}

// String — значение поля после нормализации.
func (f flex) String() string { return f.value }

// Int — числовое значение поля; 0, если числом оно не является.
func (f flex) Int() int {
	if f.value == "" {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(f.value))
	if err != nil {
		// «8.0» тоже число: источник отдаёт часы и в такой форме.
		v, ferr := strconv.ParseFloat(strings.TrimSpace(f.value), 64)
		if ferr != nil {
			return 0
		}
		return int(v)
	}
	return n
}

// flag — булево поле, пришедшее числом (0/1), булевом или строкой.
//
// Python проверял is_empty == 1. Здесь — «любое значение, отличное от нуля»,
// и это намеренное смягчение: если источник начнёт отдаать 2 для нового вида
// пустой пары, пара не поедет на страницу как несуществующая.
type flag bool

// UnmarshalJSON разбирает 0/1/true/false/"1"/null.
func (f *flag) UnmarshalJSON(data []byte) error {
	var value flex
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}

	raw := strings.TrimSpace(strings.ToLower(value.value))
	switch raw {
	case "", "0", "false", "null", "no":
		*f = false
		return nil
	}

	if n, err := strconv.ParseFloat(raw, 64); err == nil {
		*f = n != 0
		return nil
	}

	*f = true
	return nil
}

// Bool — значение флага.
func (f flag) Bool() bool { return bool(f) }

// list — поле, которое может быть массивом, а может одинарным значением.
type list []string

// UnmarshalJSON принимает массив скаляров, одиночный скаляр и null.
//
// Одиночное значение становится списком из одного элемента: так поведение
// совпадает с Python-версией, где staffNames мог быть строкой.
func (l *list) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*l = nil
		return nil
	}

	if trimmed[0] == '[' {
		var items []flex
		if err := json.Unmarshal(data, &items); err != nil {
			return err
		}
		out := make(list, 0, len(items))
		for _, item := range items {
			if item.value == "" {
				continue
			}
			out = append(out, item.value)
		}
		*l = out
		return nil
	}

	var single flex
	if err := json.Unmarshal(data, &single); err != nil {
		return err
	}
	if single.value == "" {
		*l = nil
		return nil
	}
	*l = list{single.value}
	return nil
}

// Values — содержимое списка.
func (l list) Values() []string { return []string(l) }

// norm — пробелы в один, без BOM и по краям (аналог _norm из parser.py).
//
// Разбор идёт по рунам, а не по байтам, и это не педантизм. Первая версия
// функции делала strings.NewReplacer("\xa0", " "), где "\xa0" — один байт
// 0xA0, а не символ U+00A0: в UTF-8 этим байтом заканчивается каждая вторая
// буква кириллицы, включая «Р» (D0 A0). На реальных данных такая замена
// превращала «Ранняя» в «\ufffdанняя» — поймано тестом на живом срезе
// ответа источника.
//
// неразрывный пробел (U+00A0) покрывает unicode.IsSpace, как и \t, \r, \n;
// BOM (U+FEFF) — нет, поэтому он убирается отдельной веткой.
func norm(value string) string {
	var b strings.Builder
	b.Grow(len(value))

	space := false
	for _, r := range value {
		switch {
		case r == '\ufeff':
			continue
		case unicode.IsSpace(r):
			space = true
		default:
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		}
	}

	return b.String()
}

// stripHTML — вырезает теги из строки источника.
//
// Отклонение от Python-версии, и сознательное: в classroom встречаются
// фрагменты вида «<i class="fa fa-times"></i> без ауд.». Парсер на Python их
// оставлял как есть, потому что его renderer вставлял значение в HTML. Наш
// фронтенд печатает текст, экранируя его (escapeHtml), поэтому тег показался бы
// пользователю буквальным «<i class=...>». Здесь остаётся только смысл —
// «без ауд.».
func stripHTML(value string) string {
	var b strings.Builder
	b.Grow(len(value))

	inTag := false
	for _, r := range value {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}

	return norm(b.String())
}
