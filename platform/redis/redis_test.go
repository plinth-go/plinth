package redis

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestStopIsIdempotent(t *testing.T) {
	component, err := New("redis://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = component.Stop(context.Background()) })
	if err := component.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := component.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := component.Start(context.Background()); err == nil {
		t.Fatal("redis started after stop")
	}
	if err := component.Ping(context.Background()); err == nil {
		t.Fatal("redis ping succeeded after stop")
	}
}

func TestNilReceiverIsSafe(t *testing.T) {
	var component *Redis
	if got := component.Name(); got != "redis" {
		t.Fatalf("Name = %q", got)
	}
	if err := component.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := component.Start(context.Background()); err == nil {
		t.Fatal("nil redis started")
	}
	if err := component.Ping(context.Background()); err == nil {
		t.Fatal("nil redis pinged")
	}
	if component.Client() != nil {
		t.Fatal("nil redis returned a client")
	}
}

func TestRejectsBlankURLAndTrimsWhitespace(t *testing.T) {
	if _, err := New(" \t "); err == nil || !strings.Contains(err.Error(), "REDIS_URL") {
		t.Fatalf("error = %v", err)
	}
	component, err := New("  redis://127.0.0.1:1  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = component.Stop(context.Background()) })
	if component.Client() == nil {
		t.Fatal("trimmed URL did not create a client")
	}
}

func TestIntegrationLifecycle(t *testing.T) {
	redisURL := strings.TrimSpace(os.Getenv("PLINTH_TEST_REDIS_URL"))
	if redisURL == "" {
		t.Skip("PLINTH_TEST_REDIS_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	component, err := New(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = component.Stop(context.Background()) })

	if err := component.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := component.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	key := fmt.Sprintf("plinth:test:%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = component.Client().Del(context.Background(), key).Err() })
	if err := component.Client().Set(ctx, key, "ok", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	value, err := component.Client().Get(ctx, key).Result()
	if err != nil {
		t.Fatal(err)
	}
	if value != "ok" {
		t.Fatalf("redis value = %q", value)
	}
}
