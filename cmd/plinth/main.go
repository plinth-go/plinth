package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	goruntime "runtime"

	"github.com/plinth-go/plinth/app"
	"github.com/plinth-go/plinth/internal/api"
	"github.com/plinth-go/plinth/internal/config"
	"github.com/plinth-go/plinth/kernel"
	"github.com/plinth-go/plinth/logging"
	"github.com/plinth-go/plinth/platform/jobs"
	"github.com/plinth-go/plinth/platform/jobs/migrations"
	"github.com/plinth-go/plinth/platform/migrate"
	"github.com/plinth-go/plinth/platform/pgdb"
	"github.com/plinth-go/plinth/platform/pgjobs"
	"github.com/plinth-go/plinth/platform/pgmigrate"
	"github.com/plinth-go/plinth/platform/redisclient"
	"github.com/plinth-go/plinth/runtime"
	"github.com/plinth-go/plinth/webui"
)

func main() {
	app.Main(app.Application{
		Name:        "plinth",
		Config:      &config.Config{},
		BuildRoutes: api.Routes,
		Serve:       run,
	})
}

func run(ctx context.Context) (err error) {
	cfg, err := config.Load(ctx)
	if err != nil {
		return err
	}
	console := logging.ConsoleStdout
	if !cfg.LogConsole {
		console = logging.ConsoleNone
	}
	logger, err := logging.New(logging.Config{
		Level:   cfg.LogLevel,
		Format:  cfg.LogFormat,
		Console: console,
		File:    cfg.LogFile,
	})
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := logger.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close logger: %w", closeErr))
		}
	}()
	postgres, err := pgdb.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	redis, err := redisclient.New(cfg.RedisURL)
	if err != nil {
		return err
	}
	static, err := webui.Handler(webui.Assets(), cfg.StaticDir)
	if err != nil {
		return err
	}

	store := pgjobs.NewStore(postgres.Pool())
	registry := jobs.NewRegistry()
	slogLogger := logger.Slog()
	registerSystemJobs(registry, slogLogger)
	worker := jobs.NewWorker(store, registry, slogLogger, cfg.Worker)
	scheduler := jobs.NewScheduler(store, slogLogger, cfg.Scheduler, jobs.WithRegistry(registry))
	streams := kernel.NewStreamRegistry(slogLogger)

	router, err := api.New(api.Dependencies{
		Logger:    slogLogger,
		Postgres:  postgres,
		Redis:     redis,
		Jobs:      store,
		Registry:  registry,
		Worker:    worker,
		Scheduler: scheduler,
		Streams:   streams,
		Static:    static,
		Ops:       cfg.Ops,
	})
	if err != nil {
		return err
	}

	httpConfig := runtime.DefaultHTTPConfig()
	httpConfig.TLSCertFile = cfg.TLSCertFile
	httpConfig.TLSKeyFile = cfg.TLSKeyFile
	httpConfig.EnableHTTP2 = cfg.HTTP2Enabled
	rt := runtime.New(slogLogger, cfg.ShutdownTimeout)
	rt.Add(
		postgres,
		redis,
		pgmigrate.New(cfg.DatabaseURL, cfg.AutoMigrate, slogLogger,
			migrate.Migration{Name: "plinth", FS: migrations.FS()},
		),
		worker,
		scheduler,
		runtime.NewHTTP(cfg.HTTPAddr, router, streams, slogLogger, runtime.WithHTTPConfig(httpConfig)),
	)

	slogLogger.Info("application starting", "http_addr", cfg.HTTPAddr, "go_version", goruntime.Version())
	if err := rt.Run(ctx); err != nil {
		return err
	}
	slogLogger.Info("application stopped")
	return nil
}

type echoJobPayload struct {
	Message string `json:"message"`
}

func registerSystemJobs(registry *jobs.Registry, logger *slog.Logger) {
	jobs.MustRegister(registry, "system.echo", func(_ context.Context, payload echoJobPayload) error {
		logger.Info("system echo job", "message", payload.Message)
		return nil
	})
}
