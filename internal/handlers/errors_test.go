package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Тесты единого формата ошибки (ADR-0003).
//
// Проверяется не текст, а форма ответа: код, сообщение и поля. Текст менять
// можно, код — нельзя: на него опирается JS-клиент.

// decodeError разбирает конверт и попутно убеждается, что старого плоского
// вида {"error": "строка"} больше нет: у новой формы значение `error` —
// объект, а кавычка на его начале означает, что формат откатили.
func decodeError(t *testing.T, rec *httptest.ResponseRecorder) errorBody {
	t.Helper()

	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("ответ не является JSON: %v (%s)", err, rec.Body.String())
	}
	if len(envelope.Error) == 0 {
		t.Fatalf("в ответе нет поля error: %s", rec.Body.String())
	}
	if envelope.Error[0] == '"' {
		t.Fatalf("ошибка вернулась в старом плоском виде: %s", rec.Body.String())
	}

	var body errorBody
	if err := json.Unmarshal(envelope.Error, &body); err != nil {
		t.Fatalf("разобрать объект ошибки: %v (%s)", err, rec.Body.String())
	}
	return body
}

func TestErrorCodesMatchStatuses(t *testing.T) {
	// Каждая пара обязана совпадать: если статус начнёт отдавать другой код,
	// это меняющее контракт решение, а не деталь реализации.
	cases := []struct {
		status int
		code   string
	}{
		{http.StatusBadRequest, ErrValidation},
		{http.StatusUnauthorized, ErrUnauthorized},
		{http.StatusForbidden, ErrForbidden},
		{http.StatusNotFound, ErrNotFound},
		{http.StatusMethodNotAllowed, ErrMethod},
		{http.StatusConflict, ErrConflict},
		{http.StatusUnprocessableEntity, ErrValidation},
		{http.StatusTooManyRequests, ErrRateLimited},
		{http.StatusBadGateway, ErrSourceUnavailable},
		{http.StatusServiceUnavailable, ErrSourceUnavailable},
		{http.StatusInternalServerError, ErrInternal},
		{http.StatusTeapot, ErrInternal},
	}

	for _, tc := range cases {
		rec := httptest.NewRecorder()
		writeError(rec, tc.status, "текст для человека")

		if rec.Code != tc.status {
			t.Errorf("статус %d подменён на %d", tc.status, rec.Code)
		}
		body := decodeError(t, rec)
		if body.Code != tc.code {
			t.Errorf("статус %d: ожидался код %q, получен %q", tc.status, tc.code, body.Code)
		}
		if body.Message != "текст для человека" {
			t.Errorf("статус %d: текст сообщения потерян: %q", tc.status, body.Message)
		}
	}
}

func TestErrorCodesComeOnlyFromTheDeclaredSet(t *testing.T) {
	// Неизвестный код ломает контракт: JS-клиент на него падает в ветку
	// «просто напечатай текст», и программное различие случаев теряется.
	declared := map[string]bool{
		ErrInvalidJSON: true, ErrValidation: true, ErrUnauthorized: true,
		ErrForbidden: true, ErrNotFound: true, ErrConflict: true,
		ErrRateLimited: true, ErrCSRF: true, ErrMethod: true,
		ErrSourceUnavailable: true, ErrInternal: true,
	}

	// StatusTeapot и StatusAccepted сюда попали намеренно: это статусы, которых
	// в приложении нет, и они проверяют ветку по умолчанию.
	statuses := []int{
		http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
		http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusConflict,
		http.StatusUnprocessableEntity, http.StatusTooManyRequests,
		http.StatusBadGateway, http.StatusServiceUnavailable,
		http.StatusInternalServerError, http.StatusTeapot,
		http.StatusGatewayTimeout, http.StatusAccepted,
	}

	for _, status := range statuses {
		if code := codeForStatus(status); !declared[code] {
			t.Errorf("статус %d дал необъявленный код %q", status, code)
		}
	}
}

func TestErrorEnvelopeShape(t *testing.T) {
	rec := httptest.NewRecorder()
	writeFieldError(rec, http.StatusUnprocessableEntity, "title", "title is required")

	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("разобрать ответ: %v", err)
	}
	if len(raw) != 1 {
		t.Fatalf("в конверте должно быть ровно одно поле, получено %v", raw)
	}

	body := decodeError(t, rec)
	if body.Code != ErrValidation {
		t.Errorf("код %q, ожидался %q", body.Code, ErrValidation)
	}
	if body.Message != "title is required" {
		t.Errorf("сообщение %q", body.Message)
	}
	if body.Fields["title"] != "title is required" {
		t.Errorf("поля не подписаны: %v", body.Fields)
	}

	// omitempty: у ошибки без привязки к конкретному полю ключа `fields` в JSON
	// нет — иначе клиент видел бы пустую карту там, где ничего неизвестно.
	plain := httptest.NewRecorder()
	writeError(plain, http.StatusNotFound, "course not found")
	if strings.Contains(plain.Body.String(), "fields") {
		t.Fatalf("пустые fields попали в ответ: %s", plain.Body.String())
	}
}

func TestInvalidJSONHasItsOwnCode(t *testing.T) {
	// Тот же 400, что и у «course_id is required», но другой код: битое тело и
	// неверное значение поля — разные неисправности для клиента.
	rec := httptest.NewRecorder()
	if ok := readJSON(rec, request(http.MethodPost, "/api/courses", `{`, "10.0.0.1:1", nil), &struct{}{}); ok {
		t.Fatal("readJSON принял битый JSON")
	}
	if body := decodeError(t, rec); body.Code != ErrInvalidJSON {
		t.Fatalf("код %q, ожидался %q", body.Code, ErrInvalidJSON)
	}

	// Неизвестное поле — тоже invalid_json: DisallowUnknownFields отбивает тело
	// целиком, а не отдельное поле.
	var destination struct {
		Title string `json:"title"`
	}
	rec = httptest.NewRecorder()
	if ok := readJSON(rec, request(http.MethodPost, "/api/courses", `{"title":"x","role":"admin"}`, "10.0.0.1:1", nil), &destination); ok {
		t.Fatal("readJSON принял неизвестное поле")
	}
	if body := decodeError(t, rec); body.Code != ErrInvalidJSON {
		t.Fatalf("код %q, ожидался %q", body.Code, ErrInvalidJSON)
	}
}

func TestCSRFRejectionUsesItsOwnCode(t *testing.T) {
	// 403 при CSRF означает «подкрути токен», а не «нет прав»: проверка права и
	// проверка токена дают разные коды при одном статусе.
	h, _ := newFullEnv(t)
	handler := h.RequireCSRF(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("RequireCSRF пропустил запрос без токена")
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, request(http.MethodPost, "/api/courses", `{"title":"x"}`, "10.0.0.1:1", nil))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("ожидался 403, получено %d: %s", rec.Code, rec.Body.String())
	}
	if body := decodeError(t, rec); body.Code != ErrCSRF {
		t.Fatalf("код %q, ожидался %q", body.Code, ErrCSRF)
	}
}

func TestUnauthorizedFromMiddlewareKeepsCode(t *testing.T) {
	// RequireAuth пишет 401 сам, не доходя до обработчика: код обязан
	// появиться и там, иначе клиент не отличит истёкшую сессию от отказа права.
	h, _ := newFullEnv(t)
	handler := h.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, request(http.MethodGet, "/api/courses", "", "10.0.0.1:1", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("ожидался 401, получено %d: %s", rec.Code, rec.Body.String())
	}
	if body := decodeError(t, rec); body.Code != ErrUnauthorized {
		t.Fatalf("код %q, ожидался %q", body.Code, ErrUnauthorized)
	}
}

func TestMethodNotAllowedKeepsCode(t *testing.T) {
	rec := httptest.NewRecorder()
	methodNotAllowed(rec)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("ожидался 405, получено %d", rec.Code)
	}
	if body := decodeError(t, rec); body.Code != ErrMethod {
		t.Fatalf("код %q, ожидался %q", body.Code, ErrMethod)
	}
}
