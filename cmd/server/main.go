package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/NumerR/SOS/internal/database"
	"github.com/NumerR/SOS/internal/handlers"
	"github.com/NumerR/SOS/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func main() {
	cwd, _ := os.Getwd()
	log.Printf("working directory: %s", cwd)

	if _, err := os.Stat(filepath.Join("web", "index.html")); err != nil {
		log.Printf("WARNING: web/index.html not found in %s — run `go run ./cmd/server` from the repository root", cwd)
	}

	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = filepath.Join("data", "app.db")
	}

	db, err := database.Open(dbPath)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	if err := database.Migrate(db); err != nil {
		log.Fatalf("failed to migrate database: %v", err)
	}

	userStore := store.NewUserStore(db)
	sessionStore := store.NewSessionStore(db)
	courseStore := store.NewCourseStore(db)
	enrollmentStore := store.NewEnrollmentStore(db)
	assignmentStore := store.NewAssignmentStore(db)
	submissionStore := store.NewSubmissionStore(db)
	scheduleStore := store.NewScheduleStore(db)
	resourceStore := store.NewResourceStore(db)
	eventStore := store.NewEventStore(db)

	seedAdmin(userStore)

	h := handlers.New(handlers.Deps{
		Users:       userStore,
		Sessions:    sessionStore,
		Courses:     courseStore,
		Enrollments: enrollmentStore,
		Assignments: assignmentStore,
		Submissions: submissionStore,
		Schedule:    scheduleStore,
		Resources:   resourceStore,
		Events:      eventStore,
		Limits:      handlers.NewLimiters(),
	})

	mux := http.NewServeMux()

	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// HTML-страницы.
	mux.HandleFunc("/", h.Index)
	mux.HandleFunc("/login", h.LoginPage)
	mux.HandleFunc("/register", h.RegisterPage)
	mux.HandleFunc("/dashboard", h.DashboardPage)
	mux.HandleFunc("/admin", h.AdminPage)
	mux.HandleFunc("/schedule", h.SchedulePage)

	// API авторизации (публичные). Оба эндпоинта ограничены по IP: login — от
	// перебора паролей, register — от накрутки мусорных аккаунтов. Внутри Login
	// есть второй уровень — бакет неудачных попыток по идентификатору учётки.
	// CSRF на них не проверяется: с SameSite=Lax межсайтовый POST сюда не доходит
	// (known limitation, см. docs/01-STATE-REPORT.md).
	mux.Handle("/api/auth/register", h.RateLimit(h.Limits.Register, "register", handlers.LimitByIP)(http.HandlerFunc(h.Register)))
	mux.Handle("/api/auth/login", h.RateLimit(h.Limits.Login, "login", handlers.LimitByIP)(http.HandlerFunc(h.Login)))
	mux.HandleFunc("/api/auth/logout", h.Logout)
	mux.Handle("/api/auth/me", h.RequireAuth(http.HandlerFunc(h.Me))) // только GET -> без RequireCSRF

	// Курсы: GET + POST -> RequireCSRF пропустит GET, проверит POST.
	mux.Handle("/api/courses", h.RequireAuth(h.RequireCSRF(http.HandlerFunc(h.CoursesAPI))))

	// Записи на курсы.
	mux.Handle("/api/enrollments/mine", h.RequireAuth(http.HandlerFunc(h.MyEnrollments)))                     // GET-only
	mux.Handle("/api/enrollments", h.RequireAuth(h.RequireCSRF(http.HandlerFunc(h.Enroll))))                  // POST
	mux.Handle("/api/enrollments/cancel", h.RequireAuth(h.RequireCSRF(http.HandlerFunc(h.CancelEnrollment)))) // POST
	mux.Handle("/api/enrollments/course", h.RequireAuth(http.HandlerFunc(h.CourseStudents)))                  // GET-only

	// Админ-контур: список пользователей и выдача ролей.
	// Единственный способ получить роль teacher или admin.
	mux.Handle("/api/admin/users", h.RequireRole("admin")(http.HandlerFunc(h.AdminUsers)))
	mux.Handle("/api/admin/users/role", h.RequireRole("admin")(h.RequireCSRF(http.HandlerFunc(h.AdminSetRole))))

	// Задания и сдачи работ (смешанные GET+POST -> оборачиваем целиком).
	mux.Handle("/api/assignments", h.RequireAuth(h.RequireCSRF(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			h.ListAssignments(w, r)
		case http.MethodPost:
			h.CreateAssignment(w, r)
		default:
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	}))))

	mux.Handle("/api/submissions", h.RequireAuth(h.RequireCSRF(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			h.ListSubmissionsForAssignment(w, r)
		case http.MethodPost:
			h.SubmitWork(w, r)
		default:
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	}))))

	mux.Handle("/api/submissions/mine", h.RequireAuth(http.HandlerFunc(h.MySubmissions)))                   // GET-only
	mux.Handle("/api/submissions/grade", h.RequireAuth(h.RequireCSRF(http.HandlerFunc(h.GradeSubmission)))) // POST

	// Лента расписания (GET-only).
	mux.Handle("/api/schedule", h.RequireAuth(http.HandlerFunc(h.ScheduleFeed)))

	// Ресурсы и события (смешанные GET+POST).
	mux.Handle("/api/resources", h.RequireAuth(h.RequireCSRF(http.HandlerFunc(h.ResourcesAPI))))
	mux.Handle("/api/events", h.RequireAuth(h.RequireCSRF(http.HandlerFunc(h.EventsAPI))))

	// Статика.
	mux.Handle("/css/", http.StripPrefix("/css/", http.FileServer(http.Dir(filepath.Join("web", "css")))))
	mux.Handle("/js/", http.StripPrefix("/js/", http.FileServer(http.Dir(filepath.Join("web", "js")))))

	addr := ":8080"
	log.Printf("Server started on http://localhost%s", addr)

	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

// seedAdmin создаёт учётную запись администратора из переменных окружения.
func seedAdmin(users *store.UserStore) {
	username := os.Getenv("ADMIN_USERNAME")
	password := os.Getenv("ADMIN_PASSWORD")

	if username == "" {
		log.Printf("admin seed skipped: ADMIN_USERNAME is not set")
		return
	}
	if password == "" {
		log.Printf("admin seed skipped: ADMIN_USERNAME is set but ADMIN_PASSWORD is empty")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		log.Fatalf("failed to hash admin password: %v", err)
	}

	email := os.Getenv("ADMIN_EMAIL")
	if email == "" {
		email = username + "@local.admin"
	}
	fullName := os.Getenv("ADMIN_FULL_NAME")
	if fullName == "" {
		fullName = "Administrator"
	}

	ctx := context.Background()
	_, created, err := users.Ensure(ctx, username, email, fullName, string(hash), "admin")
	if err != nil {
		log.Fatalf("failed to seed admin: %v", err)
	}
	if created {
		log.Printf("admin account created: %s", username)
	} else {
		log.Printf("admin account already exists: %s", username)
	}
}
