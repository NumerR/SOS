package platform

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/NumerR/SOS/internal/authz"
)

// Registry — накапливающий реестр маршрутов одного набора модулей.
//
// Ошибки не возвращаются из Add, а накапливаются: модуль, допустивший
// нестыковку, не должен обрывать сборку остальных, а Mount обязан показать
// все проблемы разом. Так отладка декларации остаётся одной правкой, а не
// «исправил одно — узнал про следующее».
type Registry struct {
	routes   []Route
	problems []string
}

// NewRegistry создаёт пустой реестр.
func NewRegistry() *Registry {
	return &Registry{}
}

// Add заявляет маршрут.
//
// Валидация здесь, а не в Mount, потому что текст ошибки должен указать
// порядок добавления — проще найти по индексу, когда он известен сразу.
func (reg *Registry) Add(route Route) {
	index := len(reg.routes)

	switch {
	case route.Path == "":
		reg.problems = append(reg.problems, fmt.Sprintf("маршрут #%d: пустой Path", index))
	case route.Path[0] != '/':
		reg.problems = append(reg.problems, fmt.Sprintf("маршрут #%d %q: Path обязан начинаться с «/»", index, route.Path))
	}

	if route.Method != "" && strings.TrimSpace(route.Method) != route.Method {
		reg.problems = append(reg.problems, fmt.Sprintf("маршрут %s %q: в Method не должно быть пробелов", route.Method, route.Path))
	}
	if route.Handler == nil {
		reg.problems = append(reg.problems, fmt.Sprintf("маршрут %s %q: nil Handler", route.Method, route.Path))
	}

	if route.Public && route.Perm != "" {
		reg.problems = append(reg.problems, fmt.Sprintf("маршрут %s %q: Public=true вместе с Perm=%q противоречивы", route.Method, route.Path, route.Perm))
	}

	// Право обязано быть в реестре authz. Иначе опечатка в константе дала бы
	// молчаливый 403 для всех ролей (см. authz.Can), что выглядит как «модуль
	// сломался», а не как «модуль неправильно объявлен».
	if route.Perm != "" && !authz.ValidPermission(route.Perm) {
		reg.problems = append(reg.problems, fmt.Sprintf("маршрут %s %q: право %q не объявлено в authz.Registry()", route.Method, route.Path, route.Perm))
	}

	// Метод нормализуем в верхней регистре: http.ServeMux сравнивает методы
	// строго, а «get» в декларации читается так же, как GET.
	route.Method = strings.ToUpper(route.Method)
	reg.routes = append(reg.routes, route)
}

// Routes — заявленные маршруты в порядке добавления. Нужен тестам и выгрузке
// документации (какие права на каких путях).
func (reg *Registry) Routes() []Route {
	out := make([]Route, len(reg.routes))
	copy(out, reg.routes)
	return out
}

// Problems — ошибки декларации. Пустой срез означает, что реестр собран корректно.
func (reg *Registry) Problems() []string {
	out := make([]string, len(reg.problems))
	copy(out, reg.problems)
	return out
}

// Mount собирает маршруты реестра в http.ServeMux.
//
// Возвращает ошибку, а не паникует, потому что вызов идёт на старте процесса:
// main.go обязан закрыть базу и выйти с понятным сообщением вместо стека в логе.
func Mount(mux *http.ServeMux, reg *Registry, mw Middleware) error {
	if mux == nil {
		return errors.New("platform: nil mux")
	}
	if reg == nil {
		return errors.New("platform: nil registry")
	}

	if len(reg.problems) > 0 {
		return fmt.Errorf("platform: реестр маршрутов некорректен (%d шт):\n  - %s",
			len(reg.problems), strings.Join(reg.problems, "\n  - "))
	}

	if mw.Error == nil {
		mw.Error = writeDefaultError
	}

	// Один путь может состоять из нескольких методов (GET листает, POST
	// создаёт). ServeMux не позволяет зарегистрировать один паттерн дважды,
	// поэтому группируем по пути и ставим один диспетчер по методу.
	byPath := map[string][]Route{}
	order := make([]string, 0, len(reg.routes))

	for _, route := range reg.routes {
		if _, seen := byPath[route.Path]; !seen {
			order = append(order, route.Path)
		}
		byPath[route.Path] = append(byPath[route.Path], route)
	}

	if err := checkMiddleware(byPath, mw); err != nil {
		return err
	}

	for _, path := range order {
		routes := byPath[path]

		// Конфликт одинаковых методов на одном пути ловим здесь: иначе
		// ServeMux запаниковал бы на первом же дубликате, а второй вариант
		// (метод + catch-all) тихо перекрывал бы первый.
		byMethod := make(map[string]Route, len(routes))
		for _, route := range routes {
			key := route.Method
			if key == "" {
				key = catchAllMethod
			}
			if previous, ok := byMethod[key]; ok {
				return fmt.Errorf("platform: маршрут %s %q объявлен дважды (второй — %q)",
					route.Method, route.Path, previous.Method)
			}
			byMethod[key] = route
		}

		// Любой метод, не перечисленный в декларации, обязан вернуть 405, а не
		// 404: у пути есть обработчик, просто для этого метода его нет.
		// Обёртки навешиваются на каждый метод отдельно — право и CSRF у GET и
		// POST разные.
		dispatched := make(map[string]http.Handler, len(byMethod))
		for method, route := range byMethod {
			dispatched[method] = mw.apply(route, route.Handler)
		}

		mux.Handle(path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handler, ok := dispatched[r.Method]
			if !ok {
				handler, ok = dispatched[catchAllMethod]
			}
			if !ok {
				mw.Error(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			handler.ServeHTTP(w, r)
		}))
	}

	return nil
}

const catchAllMethod = "*"

// checkMiddleware сообщает только о недостающих обёртках, которые реально
// используются: тестовый реестр из публичных маршрутов не обязан предоставлять
// Auth.
func checkMiddleware(byPath map[string][]Route, mw Middleware) error {
	missing := make(map[string]bool)

	for _, routes := range byPath {
		for _, route := range routes {
			switch {
			case route.needsPermissionMiddleware() && mw.Permission == nil:
				missing["Permission"] = true
			case route.needsAuthMiddleware() && mw.Auth == nil:
				missing["Auth"] = true
			}
			if route.CSRF && mw.CSRF == nil {
				missing["CSRF"] = true
			}
		}
	}

	if len(missing) == 0 {
		return nil
	}

	names := make([]string, 0, len(missing))
	for name := range missing {
		names = append(names, name)
	}
	sort.Strings(names)

	return fmt.Errorf("platform: Middleware не заполнен: %s", strings.Join(names, ", "))
}

// writeDefaultError — формат ошибки по умолчанию: тот же JSON, что отдают
// обработчики ({"error": "..."}), чтобы клиент не встречал два разных контракта
// в зависимости от того, кто отказал — маршрутизатор или тело обработчика
// (см. docs/02 §4 о переходе на code/message).
func writeDefaultError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	// Encoder, а не ручная склейка строк: сообщение может содержать кавычку
	// или перевод строки, а JSON обязан оставаться валидным.
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
