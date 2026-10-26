package authz_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/NumerR/SOS/internal/authz"
)

// expected — эталонная матрица «роль × пермиссия».
//
// Она написана руками, а не вычислена из authz.Registry(): иначе тест
// проверял бы сам себя и молча узаконил бы любую правку таблицы. Здесь
// намеренно дублирование: меняя право в authz, нужно осознанно поправить и
// этот список — ровно так же, как при смене контракта API правится тест
// контракта.
var expected = map[authz.Role][]authz.Permission{
	authz.RoleStudent: {
		authz.AssignmentView,
		authz.EnrollmentManage,
		authz.ScheduleView,
	},
	authz.RoleTeacher: {
		authz.AssignmentCreate,
		authz.AssignmentView,
		authz.CourseCreate,
		authz.CourseStudentsView,
		authz.EventCreate,
		authz.ResourceCreate,
		authz.ScheduleScopeCreated,
		authz.ScheduleView,
		authz.SubmissionGrade,
		authz.SubmissionView,
	},
	authz.RoleAdmin: {
		authz.AdminPanelView,
		authz.AssignmentCreate,
		authz.AssignmentCreateAny,
		authz.AssignmentView,
		authz.AssignmentViewAny,
		authz.AuditView,
		authz.CourseCreate,
		authz.CourseStudentsAny,
		authz.CourseStudentsView,
		authz.EnrollmentManage,
		authz.EventCreate,
		authz.EventSourceManage,
		authz.ResourceCreate,
		authz.ScheduleScopeAll,
		authz.ScheduleSourceManage,
		authz.ScheduleView,
		authz.SubmissionCreateAny,
		authz.SubmissionGrade,
		authz.SubmissionGradeAny,
		authz.SubmissionView,
		authz.SubmissionViewAny,
		authz.UserList,
		authz.UserRoleManage,
	},
}

// TestRolePermissionMatrix — таблица из брифа: «роль × пермиссия».
//
// Покрывает оба направления: каждое право из эталона обязано быть выдано
// (Can == true) и любое другое обязано быть отказано (Can == false). Второй
// блок ловит «выдал право по ошибке», первый — «потерял право при рефакторинге».
func TestRolePermissionMatrix(t *testing.T) {
	all := authz.Registry()

	for _, role := range authz.Roles() {
		want := map[authz.Permission]bool{}
		for _, perm := range expected[role] {
			want[perm] = true
		}

		for _, spec := range all {
			got := authz.Can(role, spec.Name)

			if got != want[spec.Name] {
				t.Errorf("роль %s, право %s: Can=%v, ожидалось %v", role, spec.Name, got, want[spec.Name])
			}
		}

		// Permissions() обязан совпадать с тем же набором: на него опирается
		// документация и будущий ответ API.
		listed := authz.Permissions(role)
		if len(listed) != len(want) {
			t.Errorf("роль %s: Permissions() вернула %d прав, ожидалось %d", role, len(listed), len(want))
		}

		for _, perm := range listed {
			if !want[perm] {
				t.Errorf("роль %s: Permissions() вернула %s, которого нет в эталоне", role, perm)
			}
		}

		if !sort.SliceIsSorted(listed, func(i, j int) bool { return listed[i] < listed[j] }) {
			t.Errorf("роль %s: Permissions() не отсортирована — текст документации зависит от порядка", role)
		}
	}
}

// TestUnknownPermissionIsDenied — отказ по умолчанию.
//
// Это единственный тест, который защищает от опечатки в новой константе
// модуля: право, которого нет в реестре, не должно «случайно» открыться
// администратору.
func TestUnknownPermissionIsDenied(t *testing.T) {
	unknown := authz.Permission("definitely.not.registered")

	for _, role := range authz.Roles() {
		if authz.Can(role, unknown) {
			t.Errorf("роль %s получила неизвестное право %s", role, unknown)
		}
	}

	// Пустое право — тоже отказ, а не «пропустить проверку».
	for _, role := range authz.Roles() {
		if authz.Can(role, "") {
			t.Errorf("роль %s получила пустое право", role)
		}
	}
}

func TestValidRole(t *testing.T) {
	for _, role := range authz.Roles() {
		if !authz.ValidRole(role) {
			t.Errorf("роль %s не прошла ValidRole, хотя объявлена в Roles()", role)
		}
	}

	for _, bad := range []authz.Role{"", "Student", "superadmin", "teacher " /* пробел */, "moderator"} {
		if authz.ValidRole(bad) {
			t.Errorf("роль %q признана валидной, хотя её нет в системе", bad)
		}
	}
}

// TestRegistryIntegrity — реестр сам на себе не противоречит.
func TestRegistryIntegrity(t *testing.T) {
	seen := map[authz.Permission]bool{}

	for _, spec := range authz.Registry() {
		if spec.Name == "" {
			t.Fatal("в реестре есть право с пустым именем")
		}
		if seen[spec.Name] {
			t.Errorf("право %s объявлено в реестре дважды", spec.Name)
		}
		seen[spec.Name] = true

		if strings.TrimSpace(spec.Description) == "" {
			t.Errorf("у права %s пустое описание — его нельзя будет объяснить в документации", spec.Name)
		}
		if len(spec.Roles) == 0 {
			t.Errorf("у права %s ни одной роли: его никто не сможет получить", spec.Name)
		}

		for _, role := range spec.Roles {
			if !authz.ValidRole(role) {
				t.Errorf("право %s выдано неизвестной роли %q", spec.Name, role)
			}
		}
	}

	// Права, на которые опирается маршрутизация (internal/modules) и проверки
	// владельца внутри обработчиков (*.any). Если кто-то переименует константу,
	// не поправив реестр, этот тест упадёт раньше, чем «забытое» право начнёт
	// отвечать 403 всем подряд.
	for _, perm := range []authz.Permission{
		authz.AdminPanelView,
		authz.AssignmentCreate,
		authz.AssignmentCreateAny,
		authz.AssignmentViewAny,
		authz.AuditView,
		authz.CourseCreate,
		authz.CourseStudentsAny,
		authz.CourseStudentsView,
		authz.EnrollmentManage,
		authz.EventCreate,
		authz.ResourceCreate,
		authz.ScheduleView,
		authz.SubmissionCreateAny,
		authz.SubmissionGrade,
		authz.SubmissionGradeAny,
		authz.SubmissionView,
		authz.SubmissionViewAny,
		authz.UserList,
		authz.UserRoleManage,
	} {
		if !authz.ValidPermission(perm) {
			t.Errorf("право %s используется в маршрутизации, но не объявлено в реестре", perm)
		}
	}
}

// TestRegistryCopyIsIndependent — Registry() отдаёт копию.
//
// Если бы вызывающий мог изменить возвращённый срез ролей, любой пакет получил
// бы возможность переписать права без правки authz.
func TestRegistryCopyIsIndependent(t *testing.T) {
	specs := authz.Registry()
	if len(specs) == 0 {
		t.Fatal("Registry() вернула пустой список")
	}

	specs[0].Roles[0] = authz.Role("hacked")

	if again := authz.Registry(); again[0].Roles[0] == authz.Role("hacked") {
		t.Error("изменение копии повлияло на реестр — инкапсуляция сломана")
	}
}

// TestScheduleScope — переход «роль -> область видимости ленты».
func TestScheduleScope(t *testing.T) {
	cases := []struct {
		role authz.Role
		want authz.Scope
		ok   bool
	}{
		{authz.RoleStudent, authz.ScopeEnrolled, true},
		{authz.RoleTeacher, authz.ScopeCreated, true},
		{authz.RoleAdmin, authz.ScopeAll, true},
		{authz.Role("guest"), "", false},
	}

	for _, tc := range cases {
		got, ok := authz.ScheduleScope(tc.role)
		if ok != tc.ok {
			t.Errorf("роль %s: ok=%v, ожидалось %v", tc.role, ok, tc.ok)
		}
		if got != tc.want {
			t.Errorf("роль %s: scope=%q, ожидалось %q", tc.role, got, tc.want)
		}
	}
}
