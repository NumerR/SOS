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

	seedAdmin(userStore)

	h := handlers.New(userStore, sessionStore, courseStore, enrollmentStore)

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

	// API авторизации.
	mux.HandleFunc("/api/auth/register", h.Register)
	mux.HandleFunc("/api/auth/login", h.Login)
	mux.HandleFunc("/api/auth/logout", h.Logout)
	mux.Handle("/api/auth/me", h.RequireAuth(http.HandlerFunc(h.Me)))

	// Курсы.
	mux.Handle("/api/courses", h.RequireAuth(http.HandlerFunc(h.CoursesAPI)))

	// Записи на курсы (роли проверяются внутри обработчиков).
	mux.Handle("/api/enrollments/mine", h.RequireAuth(http.HandlerFunc(h.MyEnrollments)))
	mux.Handle("/api/enrollments", h.RequireAuth(http.HandlerFunc(h.Enroll)))
	mux.Handle("/api/enrollments/cancel", h.RequireAuth(http.HandlerFunc(h.CancelEnrollment)))
	mux.Handle("/api/enrollments/course", h.RequireAuth(http.HandlerFunc(h.CourseStudents)))

	// Админ-контур.
	mux.Handle("/api/admin/users", h.RequireRole("admin")(http.HandlerFunc(h.AdminUsers)))

	// Статика.
	mux.Handle("/css/", http.StripPrefix("/css/", http.FileServer(http.Dir(filepath.Join("web", "css")))))
	mux.Handle("/js/", http.StripPrefix("/js/", http.FileServer(http.Dir(filepath.Join("web", "js")))))

	addr := ":8080"
	log.Printf("Server started on http://localhost%s", addr)

	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

// seedAdmin создаёт учётную запись администратора из переменных окружения,
// если они заданы и такого пользователя ещё нет. Публичная регистрация
// роль admin не выдаёт никогда — только здесь.
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
