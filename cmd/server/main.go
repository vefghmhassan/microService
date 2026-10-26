package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/compress"
	"github.com/gofiber/fiber/v2/middleware/recover"

	microserver "microserver"
	"microserver/internal"
	"microserver/internal/bot"
	"microserver/internal/config"
	"microserver/internal/controllers"
	"microserver/internal/database"
	"microserver/internal/jobs"
	"microserver/internal/middleware"
	"microserver/internal/services/orchestrator"
	"microserver/internal/services/secret"
	"microserver/internal/services/settings"
	"microserver/internal/services/sshx"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if err := secret.Init(cfg.MasterKey); err != nil {
		log.Fatalf("secret: %v", err)
	}
	db, err := database.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	if err := database.Migrate(db); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	if err := database.SeedAdmin(db, cfg.AdminUsername, cfg.AdminPassword); err != nil {
		log.Fatalf("seed: %v", err)
	}
	st, err := settings.New(db)
	if err != nil {
		log.Fatalf("settings: %v", err)
	}
	key, err := sshx.LoadOrCreateKey(cfg.DataDir)
	if err != nil {
		log.Fatalf("ssh key: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	engine := jobs.NewEngine(db, 6)
	orch := orchestrator.New(db, st, key, engine)
	orch.Register()

	tgBot := &bot.Manager{DB: db, Settings: st, Engine: engine}
	if err := tgBot.Start(ctx); err != nil {
		log.Printf("telegram: %v", err)
	}
	orch.Notify = tgBot.Notify
	engine.OnFinish(tgBot.OnJobFinish)
	st.OnChange(func(changed []string) {
		for _, k := range changed {
			if strings.HasPrefix(k, "telegram.") {
				if err := tgBot.Start(ctx); err != nil {
					log.Printf("telegram: %v", err)
				}
				return
			}
		}
	})

	if err := engine.Start(ctx); err != nil {
		log.Fatalf("jobs: %v", err)
	}
	go scheduleChecks(ctx, engine, st)
	go scheduleStats(ctx, orch, st)

	app := fiber.New(fiber.Config{
		AppName:      "microServer",
		BodyLimit:    8 << 20,
		ReadTimeout:  60 * time.Second,
		ErrorHandler: errorHandler,
	})
	app.Use(recover.New())
	app.Use(compress.New(compress.Config{
		Next: func(c *fiber.Ctx) bool { return c.Path() == "/api/v1/events" },
	}))
	a := &controllers.App{
		DB: db, Cfg: cfg, Settings: st, Engine: engine, Orch: orch,
		Auth:         &middleware.Auth{Secret: cfg.JWTSecret, TTL: 7 * 24 * time.Hour},
		TestTelegram: tgBot.Test,
	}
	internal.Register(app, a, microserver.WebFS())

	go func() {
		<-ctx.Done()
		_ = app.ShutdownWithTimeout(5 * time.Second)
	}()
	log.Printf("microServer listening on :%s (SSH public key: %s)", cfg.Port, key.PublicKey)
	if err := app.Listen(":" + cfg.Port); err != nil {
		log.Fatal(err)
	}
}

// scheduleChecks re-checks every live server from Iran on the configured
// interval, so filtering is noticed without anyone pressing a button.
func scheduleChecks(ctx context.Context, e *jobs.Engine, st *settings.Store) {
	last := time.Now()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			every := st.Int(settings.CheckIntervalMin)
			if every <= 0 || time.Since(last) < time.Duration(every)*time.Minute {
				continue
			}
			last = time.Now()
			if _, err := e.Enqueue(orchestrator.KindCheckAll, nil, struct{}{}, "schedule"); err != nil {
				log.Printf("schedule: %v", err)
			}
		}
	}
}

// scheduleStats reads traffic and online users from every panel on the
// configured interval and enforces the traffic cap.
func scheduleStats(ctx context.Context, o *orchestrator.Orchestrator, st *settings.Store) {
	last := time.Time{}
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			every := st.Int(settings.StatsIntervalMin)
			if every <= 0 || time.Since(last) < time.Duration(every)*time.Minute {
				continue
			}
			last = time.Now()
			o.CollectAll(ctx)
		}
	}
}

func errorHandler(c *fiber.Ctx, err error) error {
	code := fiber.StatusInternalServerError
	var fe *fiber.Error
	if errors.As(err, &fe) {
		code = fe.Code
	}
	if code >= 500 {
		log.Printf("%s %s: %v", c.Method(), c.Path(), err)
	}
	return c.Status(code).JSON(fiber.Map{"error": err.Error()})
}
