package config

import (
	"context"
	"strings"
	"testing"

	"github.com/plinth-go/plinth/platform/jobs"
)

func TestLoad(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://plinth:plinth@localhost:5432/plinth")
	t.Setenv("REDIS_URL", "redis://localhost:6379/0")
	t.Setenv("WORKER_CONCURRENCY", "2")
	t.Setenv("LOG_FORMAT", "text")

	cfg, err := Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Worker.Concurrency != 2 {
		t.Fatalf("Worker.Concurrency = %d", cfg.Worker.Concurrency)
	}
	if cfg.LogFormat != "text" {
		t.Fatalf("LogFormat = %q", cfg.LogFormat)
	}
	if cfg.ShutdownTimeout <= 0 {
		t.Fatalf("ShutdownTimeout = %s", cfg.ShutdownTimeout)
	}
	if !cfg.Ops.LivenessEnabled || !cfg.Ops.ReadinessEnabled || !cfg.Ops.OpenAPIEnabled {
		t.Fatalf("Ops defaults = %#v", cfg.Ops)
	}
	if cfg.Ops.LivenessPath != "/livez" || cfg.Ops.ReadinessPath != "/readyz" || cfg.Ops.OpenAPIPath != "/openapi.json" || cfg.Ops.DocsPath != "/docs/" {
		t.Fatalf("Ops paths = %#v", cfg.Ops)
	}
	wantWorker := jobs.DefaultWorkerConfig()
	wantWorker.Concurrency = 2
	if cfg.Worker != wantWorker {
		t.Fatalf("Worker = %#v, want %#v", cfg.Worker, wantWorker)
	}
	if cfg.Scheduler != jobs.DefaultSchedulerConfig() {
		t.Fatalf("Scheduler defaults = %#v, want %#v", cfg.Scheduler, jobs.DefaultSchedulerConfig())
	}
}

func TestLoadRejectsInvalidOpsPath(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/plinth")
	t.Setenv("REDIS_URL", "redis://localhost:6379/0")
	t.Setenv("OPS_LIVENESS_PATH", "livez")

	_, err := Load(context.Background())
	if err == nil || !strings.Contains(err.Error(), "OPS_LIVENESS_PATH") || !strings.Contains(err.Error(), `validate:"startswith=/"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadRejectsInvalidRetryWindow(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/plinth")
	t.Setenv("REDIS_URL", "redis://localhost:6379/0")
	t.Setenv("WORKER_RETRY_BASE", "10s")
	t.Setenv("WORKER_RETRY_MAX", "1s")

	if _, err := Load(context.Background()); err == nil {
		t.Fatal("expected invalid retry window error")
	}
}

func TestLoadRejectsExplicitlyEmptyValues(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		value   string
		wantErr string
	}{
		{name: "database", key: "DATABASE_URL", value: " ", wantErr: "DATABASE_URL must not be empty"},
		{name: "redis", key: "REDIS_URL", value: " ", wantErr: "REDIS_URL must not be empty"},
		{name: "http address", key: "HTTP_ADDR", value: " ", wantErr: "HTTP_ADDR must not be empty"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://localhost/plinth")
			t.Setenv("REDIS_URL", "redis://localhost:6379/0")
			t.Setenv(test.key, test.value)
			if _, err := Load(context.Background()); err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestLoadNormalizesAndValidatesLogSettings(t *testing.T) {
	t.Setenv("DATABASE_URL", "  postgres://localhost/plinth  ")
	t.Setenv("REDIS_URL", "  redis://localhost:6379/0  ")
	t.Setenv("HTTP_ADDR", " :9090 ")
	t.Setenv("LOG_LEVEL", " DEBUG ")
	t.Setenv("LOG_FORMAT", " TEXT ")

	cfg, err := Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != ":9090" || cfg.LogLevel != "debug" || cfg.LogFormat != "text" {
		t.Fatalf("normalized config = %#v", cfg)
	}
	if cfg.DatabaseURL != "postgres://localhost/plinth" || cfg.RedisURL != "redis://localhost:6379/0" {
		t.Fatalf("endpoint URLs were not trimmed: %#v", cfg)
	}

	t.Setenv("LOG_LEVEL", "verbose")
	if _, err := Load(context.Background()); err == nil || !strings.Contains(err.Error(), "LOG_LEVEL") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadTLSAndFileLogging(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/plinth")
	t.Setenv("REDIS_URL", "redis://localhost:6379/0")
	t.Setenv("TLS_CERT_FILE", " /etc/tls/tls.crt ")
	t.Setenv("TLS_KEY_FILE", " /etc/tls/tls.key ")
	t.Setenv("HTTP2_ENABLED", "true")
	t.Setenv("LOG_CONSOLE", "false")
	t.Setenv("LOG_FILE", " /var/log/plinth.jsonl ")

	cfg, err := Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TLSCertFile != "/etc/tls/tls.crt" || cfg.TLSKeyFile != "/etc/tls/tls.key" || !cfg.HTTP2Enabled {
		t.Fatalf("TLS config = %#v", cfg)
	}
	if cfg.LogConsole || cfg.LogFile != "/var/log/plinth.jsonl" {
		t.Fatalf("log config = %#v", cfg)
	}
}

func TestLoadRejectsInvalidTLSAndLogOutput(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{
			name:    "certificate without key",
			env:     map[string]string{"TLS_CERT_FILE": "cert.pem"},
			wantErr: "configured together",
		},
		{
			name:    "HTTP2 without TLS",
			env:     map[string]string{"HTTP2_ENABLED": "true"},
			wantErr: "HTTP2_ENABLED requires",
		},
		{
			name:    "no log output",
			env:     map[string]string{"LOG_CONSOLE": "false"},
			wantErr: "LOG_CONSOLE=false requires",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://localhost/plinth")
			t.Setenv("REDIS_URL", "redis://localhost:6379/0")
			for key, value := range test.env {
				t.Setenv(key, value)
			}
			if _, err := Load(context.Background()); err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want %q", err, test.wantErr)
			}
		})
	}
}
