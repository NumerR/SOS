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
	auditStore := store.NewAuditStore(db)

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
		Audit:       auditStore,
		Limits:      handlers.NewLimiters(),
	})

	// Модули платформы: список в cmd/server/modules.go.
	//
	// Миграции модулей применяются до сборки маршрутизатора и после базовой
	// схемы: не применились — значит сервер не стартует вовсе, а не работает с
	// половиной таблиц.
	modules := moduleList(h)

	if err := applyModuleMigrations(ctx, db, modules); err != nil {
		slog.Error("миграции модулей не применились", slog.Any("error", err))
		stop()
		<-cleanupStopped
		os.Exit(1)
	}

	// LOG_LEVEL=debug отдаёт полный список путей и прав без чтения кода.
	routeTable(modules)

	mux, err := newRouter(h, db, modules)
	if err != nil {
		// Дубликат маршрута или право, которого нет в реестре authz, — ошибка
		// сборки, а не рантайм: продолжать нет смысла, половина API
		// всё равно была бы недоступна.
		slog.Error("маршруты не собраны", slog.Any("error", err))
		stop()
		<-cleanupStopped
		os.Exit(1)
	}

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
