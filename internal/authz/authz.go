// Package authz отвечает на один вопрос: «можно ли роли выполнить это
// действие».
//
// Почему это отдельный пакет. До T1.1 право проверялось сравнением строки
// роли прямо в обработчиках — `user.Role == "admin"`,
// `hasRole([]string{"teacher", "admin"}, ...)`. таких мест набралось пятнадцать,
// и у каждого был свой текст отказа, свой список ролей и своя возможность
// ошибиться: добавление четвёртой роли означало бы найти все пятнадцать вслепую.
// Здесь решение живёт в одной таблице, а обработчик спрашивает
// `authz.Can(role, authz.CourseCreate)`.
//
// Граница ответственности (важно, чтобы не перепутать с проверкой владельца):
//
//   - authz — право *класса* («преподаватель в принципе может создавать
//     курсы»). Ответа достаточно, чтобы открыть маршрутизатор.
//   - обработчик/стор — право *на строку* («этот курс создан этим
//     пользователем»). Это проверка данных, она остаётся в обработчике и
//     хранит текст вида «you can only create assignments for your own courses».
//
// Права на «чужие» объекты специально вынесены в отдельные permission-ы с
// суффиксом `.any`: «админ может работать с чужими сдачами» — это право, а не
// частный случай сравнения роли.
package authz

import "sort"

// Role — роль пользователя. Значения совпадают со строками в колонке
// users.role: проверка роли в базе и в коде должна говорить одними словами.
type Role string

const (
	RoleStudent Role = "student"
	RoleTeacher Role = "teacher"
	RoleAdmin   Role = "admin"
)

// Permission — имя действия из реестра ниже. Строковый тип выбран намеренно:
// имя попадает в документацию, тесты и (в перспективе) ответ API, поэтому оно
// должно оставаться читаемым, а не порядковым номером.
type Permission string

const (
	// Аккаунты и администрирование.
	UserList       Permission = "user.list"        // посмотреть список пользователей
	UserRoleManage Permission = "user.role.manage" // назначить роль
	AdminPanelView Permission = "admin.panel.view" // открыть страницу /admin

	// Курсы и записи.
	CourseCreate       Permission = "course.create"            // создать курс
	CourseStudentsView Permission = "course.students.view"     // посмотреть записанных на курс
	CourseStudentsAny  Permission = "course.students.view.any" // ...на любом курсе, не только своём
	EnrollmentManage   Permission = "enrollment.manage"        // записаться/отписаться самому

	// Задания.
	AssignmentCreate    Permission = "assignment.create"     // создать задание на своём курсе
	AssignmentCreateAny Permission = "assignment.create.any" // ...на чужом курсе
	AssignmentView      Permission = "assignment.view"       // список заданий курса, в котором есть право
	AssignmentViewAny   Permission = "assignment.view.any"   // ...любого курса без записи

	// Сдачи работ.
	SubmissionView      Permission = "submission.view"       // посмотреть сдачи своего задания
	SubmissionViewAny   Permission = "submission.view.any"   // ...чужого задания
	SubmissionGrade     Permission = "submission.grade"      // оценить свою сдачу
	SubmissionGradeAny  Permission = "submission.grade.any"  // ...чужую
	SubmissionCreateAny Permission = "submission.create.any" // сдать работу без записи на курс

	// Витрины.
	ResourceCreate Permission = "resource.create" // добавить материал
	EventCreate    Permission = "event.create"    // добавить событие
	// EventSourceManage зарезервировано под T4.2 (источник событий из
	// Telegram/парсера): право настраивать внешний источник, а не постить руками.
	// Пока ни один маршрут его не запрашивает — таблица ниже фиксирует, кому оно
	// будет выдано, чтобы T4.2 не реша это на месте.
	EventSourceManage Permission = "event.source.manage"

	// Расписание.
	ScheduleView         Permission = "schedule.view"          // открыть ленту
	ScheduleScopeCreated Permission = "schedule.scope.created" // видеть свои курсы
	ScheduleScopeAll     Permission = "schedule.scope.all"     // видеть всё
)

// Spec — одна строка реестра: право, что оно значит и кому выдано.
type Spec struct {
	Name        Permission
	Description string
	Roles       []Role
}

// registry — единственная таблица прав.
//
// Читать её нужно сверху вниз: «student» в списке Roles означает, что право
// есть у студента, и только. Права, которых в списке нет, запрещены всем —
// включая администратора (см. Can). Поэтому «забыть» право в таблице нельзя:
// забытое право даёт 403, а не «разрешить всем».
var registry = []Spec{
	// Аккаунты и администрирование.
	{UserList, "список всех пользователей", []Role{RoleAdmin}},
	{UserRoleManage, "назначение ролей", []Role{RoleAdmin}},
	{AdminPanelView, "доступ к странице /admin", []Role{RoleAdmin}},

	// Курсы и записи.
	{CourseCreate, "создание курса", []Role{RoleTeacher, RoleAdmin}},
	{CourseStudentsView, "просмотр списка записанных на курс", []Role{RoleTeacher, RoleAdmin}},
	{CourseStudentsAny, "просмотр списка записанных на любом курсе", []Role{RoleAdmin}},
	{EnrollmentManage, "запись на курс и отмена записи", []Role{RoleStudent, RoleAdmin}},

	// Задания.
	{AssignmentCreate, "создание задания на своём курсе", []Role{RoleTeacher, RoleAdmin}},
	{AssignmentCreateAny, "создание задания на любом курсе", []Role{RoleAdmin}},
	{AssignmentView, "просмотр заданий курса, к которому есть доступ", []Role{RoleStudent, RoleTeacher, RoleAdmin}},
	{AssignmentViewAny, "просмотр заданий любого курса", []Role{RoleAdmin}},

	// Сдачи работ.
	{SubmissionView, "просмотр сдач своего задания", []Role{RoleTeacher, RoleAdmin}},
	{SubmissionViewAny, "просмотр сдач любого задания", []Role{RoleAdmin}},
	{SubmissionGrade, "оценка сдач своего задания", []Role{RoleTeacher, RoleAdmin}},
	{SubmissionGradeAny, "оценка сдач любого задания", []Role{RoleAdmin}},
	{SubmissionCreateAny, "сдача работы без записи на курс", []Role{RoleAdmin}},

	// Витрины.
	{ResourceCreate, "добавление материала", []Role{RoleTeacher, RoleAdmin}},
	{EventCreate, "добавление события", []Role{RoleTeacher, RoleAdmin}},
	{EventSourceManage, "управление внешним источником событий (T4.2)", []Role{RoleAdmin}},

	// Расписание.
	{ScheduleView, "просмотр ленты расписания", []Role{RoleStudent, RoleTeacher, RoleAdmin}},
	{ScheduleScopeCreated, "видеть в ленте свои курсы", []Role{RoleTeacher}},
	{ScheduleScopeAll, "видеть в ленте всё", []Role{RoleAdmin}},
}

// grants разворачивает registry в «право -> набор ролей» и заполняется один
// раз при старте процесса.
var grants = buildGrants()

func buildGrants() map[Permission]map[Role]bool {
	result := make(map[Permission]map[Role]bool, len(registry))

	for _, spec := range registry {
		roles := make(map[Role]bool, len(spec.Roles))
		for _, role := range spec.Roles {
			roles[role] = true
		}
		result[spec.Name] = roles
	}

	return result
}

// Can — главный вопрос пакета.
//
// Неизвестное право (например, опечатка в константе нового модуля) возвращает
// false: отказ по умолчанию безопаснее разрешения по умолчанию.
func Can(role Role, perm Permission) bool {
	return grants[perm][role]
}

// ValidRole — известна ли системе роль (сравнение регистронезависимое только
// для удобства вызывающего: нормализацию входной строки делает вызывающий).
func ValidRole(role Role) bool {
	for _, known := range Roles() {
		if known == role {
			return true
		}
	}
	return false
}

// Roles — все роли системы в алфавитном порядке. Порядок фиксирован, чтобы
// текст ошибки «role must be ...» и матричный тест не зависели от карты.
func Roles() []Role {
	return []Role{RoleAdmin, RoleStudent, RoleTeacher}
}

// Permissions — права роли в алфавитном порядке. Нужен тестам и, в
// перспективе, ответу /api/auth/me, чтобы фронтенд прятал недоступные кнопки по
// тому же списку, по которому сервер отказывает.
func Permissions(role Role) []Permission {
	result := make([]Permission, 0, len(registry))

	for _, spec := range registry {
		if grants[spec.Name][role] {
			result = append(result, spec.Name)
		}
	}

	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

// Registry — копия таблицы прав для документации и тестов. Внешний код получает
// её только для чтения: изменить grants извне нельзя.
func Registry() []Spec {
	out := make([]Spec, len(registry))
	for i, spec := range registry {
		out[i] = Spec{
			Name:        spec.Name,
			Description: spec.Description,
			Roles:       append([]Role(nil), spec.Roles...),
		}
	}
	return out
}

// ValidPermission — объявлено ли право в реестре.
//
// Нужна проверке целостности маршрутизации: маршрутизатор может сослаться на
// константу, которую потом переименовали, и тогда Can() начнёт отказывать всем.
// Тест с ValidPermission падает раньше, чем это происходит в проде.
func ValidPermission(perm Permission) bool {
	_, ok := grants[perm]
	return ok
}

// Scope — какой срез данных человек видит в лентах, где роль меняет не право,
// а запрос к базе.
type Scope string

const (
	ScopeEnrolled Scope = "enrolled" // студент: курсы, на которые он записан
	ScopeCreated  Scope = "created"  // преподаватель: курсы и задания, которые он создал
	ScopeAll      Scope = "all"      // администратор: все курсы
)

// ScheduleScope переводит роль в область видимости ленты расписания.
//
// Раньше в internal/store/schedule.go стоял switch по строке роли — то есть стор
// знал про имена ролей. Теперь он спрашивает authz и получает готовый Scope, а
// список прав остаётся в одном месте.
func ScheduleScope(role Role) (Scope, bool) {
	switch {
	case Can(role, ScheduleScopeAll):
		return ScopeAll, true
	case Can(role, ScheduleScopeCreated):
		return ScopeCreated, true
	case Can(role, ScheduleView):
		return ScopeEnrolled, true
	default:
		return "", false
	}
}
