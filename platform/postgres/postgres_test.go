package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/plinth-go/plinth/platform/internal/pgtest"
)

func TestStopIsIdempotent(t *testing.T) {
	postgres, err := New(context.Background(), "postgres://user:pass@127.0.0.1:1/db?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = postgres.Stop(context.Background()) })
	if err := postgres.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Start(context.Background()); err == nil {
		t.Fatal("postgres started after stop")
	}
	if err := postgres.Ping(context.Background()); err == nil {
		t.Fatal("postgres ping succeeded after stop")
	}
}

func TestNilReceiverIsSafe(t *testing.T) {
	var postgres *Postgres
	if got := postgres.Name(); got != "postgres" {
		t.Fatalf("Name = %q", got)
	}
	if err := postgres.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Start(context.Background()); err == nil {
		t.Fatal("nil postgres started")
	}
	if err := postgres.Ping(context.Background()); err == nil {
		t.Fatal("nil postgres pinged")
	}
	if postgres.Pool() != nil {
		t.Fatal("nil postgres returned a pool")
	}
}

func TestRejectsBlankDatabaseURL(t *testing.T) {
	if _, err := New(context.Background(), " \t "); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("error = %v", err)
	}
	postgres, err := New(context.Background(), "  postgres://user:pass@127.0.0.1:1/db?sslmode=disable  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = postgres.Stop(context.Background()) })
	if postgres.Pool() == nil {
		t.Fatal("trimmed URL did not create a pool")
	}
}

func TestIntegrationLifecycle(t *testing.T) {
	databaseURL := pgtest.RequireEnv(t, "PLINTH_TEST_DATABASE_URL")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	postgres, err := New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = postgres.Stop(context.Background()) })

	if err := postgres.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if postgres.Pool() == nil {
		t.Fatal("postgres pool is nil")
	}
}
