package config

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/plinth-go/plinth/app"
	"github.com/plinth-go/plinth/kernel"
	"github.com/plinth-go/plinth/platform/jobs"
)

// Config is the complete runtime configuration for the service.
// Keep configuration explicit: no Viper, no hidden precedence rules.
type Config struct {
	HTTPAddr     string `env:"HTTP_ADDR,default=:8080" envdoc:"HTTP listen address."`
	TLSCertFile  string `env:"TLS_CERT_FILE" envdoc:"PEM certificate file; requires TLS_KEY_FILE."`
	TLSKeyFile   string `env:"TLS_KEY_FILE" envdoc:"PEM private-key file; requires TLS_CERT_FILE."`
	HTTP2Enabled bool   `env:"HTTP2_ENABLED,default=false" envdoc:"Enable HTTP/2 when TLS is configured."`
	DatabaseURL  string `env:"DATABASE_URL,required" envdoc:"PostgreSQL connection URL."`
	RedisURL     string `env:"REDIS_URL,required" envdoc:"Redis connection URL."`
	AutoMigrate  bool   `env:"AUTO_MIGRATE,default=true" envdoc:"Apply configured migrations during startup."`
	// Background worker policy.
	Worker jobs.WorkerConfig `env:""`
	// Durable scheduler policy.
	Scheduler jobs.SchedulerConfig `env:""`
	// Standard operational endpoint switches.
	Ops             kernel.OpsSettings `env:""`
	ShutdownTimeout time.Duration      `env:"SHUTDOWN_TIMEOUT,default=10s" envdoc:"Deadline shared by component shutdowns." validate:"gt=0"`
	StaticDir       string             `env:"STATIC_DIR" envdoc:"Optional directory that replaces the embedded web assets."`
	LogLevel        string             `env:"LOG_LEVEL,default=info" envdoc:"Log level: debug, info, warn, or error."`
	LogFormat       string             `env:"LOG_FORMAT,default=json" envdoc:"Log encoding: json or text."`
	LogConsole      bool               `env:"LOG_CONSOLE,default=true" envdoc:"Write logs to stdout; disable only when a file is configured."`
	LogFile         string             `env:"LOG_FILE" envdoc:"Optional append-only log file."`
}

func Load(ctx context.Context) (Config, error) {
	var cfg Config
	if err := app.LoadEnv(ctx, &cfg); err != nil {
		return Config{}, fmt.Errorf("load config: %w", err)
	}
	if strings.TrimSpace(cfg.DatabaseURL) == "" {
		return Config{}, fmt.Errorf("DATABASE_URL must not be empty")
	}
	cfg.DatabaseURL = strings.TrimSpace(cfg.DatabaseURL)
	if strings.TrimSpace(cfg.RedisURL) == "" {
		return Config{}, fmt.Errorf("REDIS_URL must not be empty")
	}
	cfg.RedisURL = strings.TrimSpace(cfg.RedisURL)
	cfg.HTTPAddr = strings.TrimSpace(cfg.HTTPAddr)
	if cfg.HTTPAddr == "" {
		return Config{}, fmt.Errorf("HTTP_ADDR must not be empty")
	}
	cfg.TLSCertFile = strings.TrimSpace(cfg.TLSCertFile)
	cfg.TLSKeyFile = strings.TrimSpace(cfg.TLSKeyFile)
	if (cfg.TLSCertFile == "") != (cfg.TLSKeyFile == "") {
		return Config{}, fmt.Errorf("TLS_CERT_FILE and TLS_KEY_FILE must be configured together")
	}
	if cfg.HTTP2Enabled && cfg.TLSCertFile == "" {
		return Config{}, fmt.Errorf("HTTP2_ENABLED requires TLS_CERT_FILE and TLS_KEY_FILE")
	}
	cfg.LogLevel = strings.ToLower(strings.TrimSpace(cfg.LogLevel))
	switch cfg.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return Config{}, fmt.Errorf("LOG_LEVEL must be debug, info, warn, or error")
	}
	cfg.LogFormat = strings.ToLower(strings.TrimSpace(cfg.LogFormat))
	if cfg.LogFormat != "json" && cfg.LogFormat != "text" {
		return Config{}, fmt.Errorf("LOG_FORMAT must be json or text")
	}
	cfg.LogFile = strings.TrimSpace(cfg.LogFile)
	if !cfg.LogConsole && cfg.LogFile == "" {
		return Config{}, fmt.Errorf("LOG_CONSOLE=false requires LOG_FILE")
	}
	return cfg, nil
}
