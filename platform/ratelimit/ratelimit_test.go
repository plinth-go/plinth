package ratelimit

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/plinth-go/plinth/kernel"
)

func TestMemoryLimiterFixedWindow(t *testing.T) {
	limiter, err := NewMemory(2, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	limiter.now = func() time.Time { return now }
	ctx := context.Background()
	first, err := limiter.Allow(ctx, "key")
	if err != nil || !first.Allowed || first.Remaining != 1 {
		t.Fatalf("first = %#v, %v", first, err)
	}
	second, err := limiter.Allow(ctx, "key")
	if err != nil || !second.Allowed || second.Remaining != 0 {
		t.Fatalf("second = %#v, %v", second, err)
	}
	third, err := limiter.Allow(ctx, "key")
	if err != nil || third.Allowed || third.RetryAfter != time.Minute {
		t.Fatalf("third = %#v, %v", third, err)
	}
	now = now.Add(time.Minute)
	next, err := limiter.Allow(ctx, "key")
	if err != nil || !next.Allowed || next.Remaining != 1 {
		t.Fatalf("next = %#v, %v", next, err)
	}
}

func TestMiddlewareAllowsAndDenies(t *testing.T) {
	limiter, err := NewMemory(1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	middleware, err := New(Config{Limiter: limiter, Logger: testLogger()})
	if err != nil {
		t.Fatal(err)
	}
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/", nil))
	if first.Code != http.StatusNoContent || first.Header().Get("RateLimit-Remaining") != "0" {
		t.Fatalf("first = %d %q", first.Code, first.Header().Get("RateLimit-Remaining"))
	}
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/", nil))
	if second.Code != http.StatusTooManyRequests || second.Header().Get("Retry-After") == "" {
		t.Fatalf("second = %d retry=%q body=%s", second.Code, second.Header().Get("Retry-After"), second.Body.String())
	}
}

func TestMiddlewareFailClosedAndSkip(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("nil limiter was accepted")
	}
	var typedNil *MemoryLimiter
	if _, err := New(Config{Limiter: typedNil}); err == nil {
		t.Fatal("typed-nil limiter was accepted")
	}
	failing, err := New(Config{
		Limiter: limiterFunc(func(context.Context, string) (Result, error) { return Result{}, errors.New("redis down") }),
		Logger:  testLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	failing(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("next handler ran")
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}

	skipped, err := New(Config{
		Limiter: limiterFunc(func(context.Context, string) (Result, error) { return Result{}, errors.New("must not run") }),
		Skip:    func(*http.Request) bool { return true },
		Logger:  testLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	skipped(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("skipped status = %d", rec.Code)
	}
}

func TestDefaultKeyUsesOperationAndIgnoresForwardedFor(t *testing.T) {
	var key string
	middleware, err := New(Config{
		Limiter: limiterFunc(func(_ context.Context, value string) (Result, error) {
			key = value
			return Result{Allowed: true, Limit: 1, Remaining: 0, ResetAt: time.Now().Add(time.Minute)}, nil
		}),
		Logger: testLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	router := kernel.NewRouter(testLogger(), "test", "v1")
	router.Use(middleware)
	router.Handle(func(context.Context, defaultKeyInput) (struct{}, error) { return struct{}{}, nil })
	request := httptest.NewRequest(http.MethodGet, "/key", nil)
	request.RemoteAddr = "203.0.113.9:4321"
	request.Header.Set("X-Forwarded-For", "198.51.100.7")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, request)
	if rec.Code != http.StatusNoContent || key != "get--key|203.0.113.9" {
		t.Fatalf("status/key = %d/%q", rec.Code, key)
	}
}

type defaultKeyInput struct {
	kernel.Meta `route:"GET /key"`
}

func TestMemoryValidation(t *testing.T) {
	if _, err := NewMemory(0, time.Minute); err == nil {
		t.Fatal("zero limit accepted")
	}
	if _, err := NewMemory(1, 0); err == nil {
		t.Fatal("zero window accepted")
	}
	limiter, err := NewMemory(1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := limiter.Allow(context.Background(), ""); err == nil {
		t.Fatal("empty key accepted")
	}
	if err := ValidateKey("x\nforged"); err == nil {
		t.Fatal("control character accepted")
	}
	if err := ValidateKey(strings.Repeat("x", 513)); err == nil {
		t.Fatal("oversized key accepted")
	}
}

func TestMemoryLimiterPrunesAndBoundsEntries(t *testing.T) {
	limiter, err := NewMemory(1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	limiter.maxEntries = 2
	now := time.Unix(1000, 0)
	limiter.now = func() time.Time { return now }
	ctx := context.Background()
	for _, key := range []string{"a", "b"} {
		if _, err := limiter.Allow(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := limiter.Allow(ctx, "c"); err == nil {
		t.Fatal("capacity overflow was accepted")
	}
	now = now.Add(2 * time.Minute)
	if _, err := limiter.Allow(ctx, "c"); err != nil {
		t.Fatalf("expired entries were not pruned: %v", err)
	}
}

type limiterFunc func(context.Context, string) (Result, error)

func (f limiterFunc) Allow(ctx context.Context, key string) (Result, error) { return f(ctx, key) }

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
