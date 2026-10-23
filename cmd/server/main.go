package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/NumerR/SOS/internal/database"
	"github.com/NumerR/SOS/internal/handlers"
	"github.com/NumerR/SOS/internal/store"
	"golang.org/x/crypto/bcrypt"
)

const sessionCleanupInterval = time.Hour

// Таймауты HTTP-сервера.
//
// Без них один медленный (или злой) клиент удерживает за собой горутина и
// сокет сколько угодно: «Slowloris» — это ровно про отсутствие
// ReadHeaderTimeout. Значения подобраны под наш трафик: ответы здесь маленькие
// и быстрые, а 30 секунд на запись — это уже космос для JSON-эндпоинта.
const (
	serverReadTimeout       = 10 * time.Second
	serverReadHeaderTimeout = 5 * time.Second
	serverWriteTimeout      = 30 * time.Second
	serverIdleTimeout       = 90 * time.Second

	// 64 КиБ на заголовок: кука сессии, токен CSRF и обычный набор — это
	// пара сотен байт. Ограничение по голове, а не по телу (тело режет
	// readJSON), иначе кто-то присылает 10 МиБ в X-Bogus.
	maxHeaderBytes = 1 << 16

	// Graceful shutdown: столько даём недособранным запросам доработать.
	// Больше — это уже не «дожечь активные запросы», а «ждать зависшие».
	shutdownTimeout = 10 * time.Second

	// Проверка базы в health-эндпоинте не должна превращать дешёвый GET в
	// зависание: не ответила за секунду — значит health говорит правду.
	healthPingTimeout = time.Second

	// Сколько ждём фонового сборщика сессий, прежде чем закрыть базу.
	cleanupStopTimeout = 5 * time.Second
)

func main() {
	slog.SetDefault(newLogger())

	cwd, _ := os.Getwd()
	slog.Info("запуск сервера", slog.String("workdir", cwd))

	if _, err := os.Stat(filepath.Join("web", "index.html")); err != nil {
		slog.Warn("веб-каталог не найден",
			slog.String("workdir", cwd),
			slog.String("hint", "запускайте `go run ./cmd/server` из корня репозитория"),
		)
	}

	// ctx живёт столько, сколько процесс. SIGINT/SIGTERM останавливают и
	// фоновые goroutine, и сервер: без этого graceful shutdown превращается в
	// «остановили HTTP, а сборщик сессий работает доубитым».
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Доверять ли X-Forwarded-For / X-Forwarded-Proto. По умолчанию нет:
	// заголовок подделывается клиентом, и «доверять всегда» означало бы обход
	// всех лимитов одной строчкой в curl.
	if v := os.Getenv("TRUST_PROXY"); v == "1" || strings.EqualFold(v, "true") {
		handlers.SetTrustProxy(true)
		slog.Warn("TRUST_PROXY включён: X-Forwarded-For и X-Forwarded-Proto принимаются как есть",
			slog.String("check", "убедитесь, что прямой доступ к порту закрыт"),
		)
	}

	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = filepath.Join("data", "app.db")
	}

	db, err := database.Open(dbPath)
	if err != nil {
		slog.Error("не удалось открыть базу", slog.Any("error", err))
		os.Exit(1)
	}
	defer db.Close()

	if err := database.Migrate(db); err != nil {
		slog.Error("миграции не применились", slog.Any("error", err))
		os.Exit(1)
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

	// Сборщик протухших сессий. Первый проход — сразу, чтобы после рестарта
	// база не осталась с мусором; дальше раз в sessionCleanupInterval.
	// cleanupStopped закрывается, когда goroutine вышел, — это даёт дождаться
	// его до db.Close(), а не гадать на глаз.
	cleanupStopped := make(chan struct{})
	go func() {
		defer close(cleanupStopped)
		cleanSessions(ctx, sessionStore)
	}()

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

	// Живость. Отвечает не «да», а «да, и база отвечает» — иначе мониторинг
	// молча считает мёртвый сервер рабочим.
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")

		pingCtx, cancel := context.WithTimeout(r.Context(), healthPingTimeout)
		defer cancel()

		if err := db.PingContext(pingCtx); err != nil {
			slog.Error("health: база не ответила", slog.Any("error", err))
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"error","db":"error"}`))
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","db":"ok"}`))
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
	mux.Handle("/api/auth/register", h.RateLimit(h.Limits.Register, "register", handlers.ClientIP)(http.HandlerFunc(h.Register)))
	mux.Handle("/api/auth/login", h.RateLimit(h.Limits.Login, "login", handlers.ClientIP)(http.HandlerFunc(h.Login)))
	mux.HandleFunc("/api/auth/logout", h.Logout)

	// «Выйти везде» и смена пароля меняют состояние — RequireAuth + RequireCSRF.
	// Смена пароля дополнительно ограничена по IP: текущий пароль внутри
	// открытой сессии тоже можно перебирать.
	mux.Handle("/api/auth/logout-all", h.RequireAuth(h.RequireCSRF(http.HandlerFunc(h.LogoutAll))))
	mux.Handle(
		"/api/auth/change-password",
		h.RequireAuth(h.RequireCSRF(h.RateLimit(h.Limits.Password, "password", handlers.ClientIP)(http.HandlerFunc(h.ChangePassword)))),
	)

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

	// Порядок обёрт: логирование снаружи, иначе оно не увидит итоговый статус
	// обработчика; дедлайн БД внутри него, чтобы в лог попал и request id.
	root := handlers.LogRequests(handlers.WithDBDeadline(mux))

	addr := envOr("ADDR", ":8080")

	server := &http.Server{
		Addr:           addr,
		Handler:        root,
		ReadTimeout:    serverReadTimeout,
		WriteTimeout:   serverWriteTimeout,
		IdleTimeout:    serverIdleTimeout,
		MaxHeaderBytes: maxHeaderBytes,
		// ReadHeaderTimeout отдельно: заголовки должны приходить быстрее тела.
		// Именно тело медленного клиента тут и не режет, а заголовок — то, чем
		// живой сервер отличается от вешалки для сокетов.
		ReadHeaderTimeout: serverReadHeaderTimeout,
	}

	serverErr := make(chan error, 1)

	go func() {
		slog.Info("сервер запущен", slog.String("addr", addr), slog.String("health", "/api/health"))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	select {
	case err := <-serverErr:
		if err != nil {
			slog.Error("сервер завершился с ошибкой", slog.Any("error", err))
			stop()
			<-cleanupStopped
			os.Exit(1)
		}
		slog.Info("сервер остановлен")
	case <-ctx.Done():
		slog.Info("получен сигнал остановки, завершаем активные запросы",
			slog.String("reason", ctx.Err().Error()),
		)

		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			// Активный запрос не доождался — закрываем принудительно, иначе
			// процесс висит намертво и оркестратор убивает его по SIGKILL.
			slog.Error("graceful shutdown не завершился, закрываем принудительно", slog.Any("error", err))
			if err := server.Close(); err != nil {
				slog.Error("не удалось закрыть listener", slog.Any("error", err))
			}
		}

		// Собираем фоновый процесс до закрытия базы: горутина, пишущая в
		// уже закрытый *sql.DB, — это гонка, которую не поймает ни один тест.
		select {
		case <-cleanupStopped:
		case <-time.After(cleanupStopTimeout):
			slog.Warn("сборщик сессий не остановился за отведённое время")
		}

		slog.Info("сервер остановлен корректно")
	}
}

// cleanSessions периодически удаляет протухшие сессии.
//
// Почему это нужно: Sessions.Get фильтрует по expires_at, поэтому просроченная
// кука уже не даёт доступ — но строка в таблице остаётся навсегда, и через год
// это тысячи мёртвых записей, которые никто не удалял.
//
// ctx останавливает цикл; ошибок fatal тут нет намеренно — сбой одной чистки
// не должен ронять сервер, который продолжает отдавать API.
func cleanSessions(ctx context.Context, sessions *store.SessionStore) {
	// Первая чистка — сразу, чтобы после рестарта база не осталась с мусором.
	runSessionCleanup(ctx, sessions)

	ticker := time.NewTicker(sessionCleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("сборщик сессий остановлен", slog.String("reason", ctx.Err().Error()))
			return
		case <-ticker.C:
			runSessionCleanup(ctx, sessions)
		}
	}
}

func runSessionCleanup(ctx context.Context, sessions *store.SessionStore) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	deleted, err := sessions.DeleteExpired(ctx)
	if err != nil {
		// На остановке контекст отменяется штатно — это не авария.
		if ctx.Err() != nil {
			return
		}
		slog.Error("session cleanup: не удалось удалить протухшие сессии", slog.Any("error", err))
		return
	}

	// Пишем только когда было что удалять: пустой час не должен плодить строки
	// в логе.
	if deleted > 0 {
		active, _ := sessions.CountActive(ctx)
		slog.Info("session cleanup",
			slog.Int64("deleted", deleted),
			slog.Int64("active_left", active),
		)
	}
}

// newLogger собирает JSON-логгер: разворачивание предполагает, что строки
// читает не только человек, но и сборщик логов. Уровень — из LOG_LEVEL
// (debug|info|warn|error), по умолчанию info.
func newLogger() *slog.Logger {
	level := slog.LevelInfo

	switch strings.ToLower(os.Getenv("LOG_LEVEL")) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// seedAdmin создаёт учётную запись администратора из переменных окружения.
func seedAdmin(users *store.UserStore) {
	username := os.Getenv("ADMIN_USERNAME")
	password := os.Getenv("ADMIN_PASSWORD")

	if username == "" {
		slog.Info("админ не создан: ADMIN_USERNAME не задан")
		return
	}
	if password == "" {
		slog.Warn("админ не создан: ADMIN_USERNAME задан, ADMIN_PASSWORD пуст")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		slog.Error("не удалось захэшировать пароль админа", slog.Any("error", err))
		os.Exit(1)
	}

	email := os.Getenv("ADMIN_EMAIL")
	if email == "" {
		email = username + "@local.admin"
	}
	fullName := os.Getenv("ADMIN_FULL_NAME")
	if fullName == "" {
		fullName = "Administrator"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, created, err := users.Ensure(ctx, username, email, fullName, string(hash), "admin")
	if err != nil {
		slog.Error("сид админа не выполнен", slog.Any("error", err))
		os.Exit(1)
	}

	if created {
		slog.Info("учётная запись админа создана", slog.String("username", username))
		return
	}
	slog.Info("учётная запись админа уже существует", slog.String("username", username))
}
