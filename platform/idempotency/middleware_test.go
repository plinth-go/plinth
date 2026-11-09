package idempotency

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestMiddlewareReplaysCompletedResponse(t *testing.T) {
	var calls atomic.Int32
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("X-Business", "yes")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	handler := mustMiddleware(t, NewMemoryStore(), Config{}, next)

	first := request(handler, `{"value":1}`)
	if first.Code != http.StatusCreated || first.Body.String() != `{"ok":true}` || first.Header().Get("X-Business") != "yes" {
		t.Fatalf("first response = %d %q %#v", first.Code, first.Body.String(), first.Header())
	}
	second := request(handler, `{"value":1}`)
	if second.Code != http.StatusCreated || second.Body.String() != `{"ok":true}` {
		t.Fatalf("second response = %d %q", second.Code, second.Body.String())
	}
	if second.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay header = %#v", second.Header())
	}
	if calls.Load() != 1 {
		t.Fatalf("handler calls = %d", calls.Load())
	}
}

func TestMiddlewareRejectsDifferentFingerprint(t *testing.T) {
	handler := mustMiddleware(t, NewMemoryStore(), Config{}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	if got := request(handler, `{"value":1}`); got.Code != http.StatusNoContent {
		t.Fatalf("first status = %d", got.Code)
	}
	got := request(handler, `{"value":2}`)
	if got.Code != http.StatusConflict {
		t.Fatalf("second status = %d, body = %q", got.Code, got.Body.String())
	}
}

func TestMiddlewareRequiresKey(t *testing.T) {
	handler := mustMiddleware(t, NewMemoryStore(), Config{RequireKey: true}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest(http.MethodPost, "/items", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestMiddlewareRejectsOversizedRequest(t *testing.T) {
	handler := mustMiddleware(t, NewMemoryStore(), Config{MaxRequestBytes: 4}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest(http.MethodPost, "/items", strings.NewReader("12345"))
	req.Header.Set(defaultHeaderName, "key-1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestMiddlewareDoesNotCacheServerErrors(t *testing.T) {
	var calls atomic.Int32
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "temporary", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("recovered"))
	})
	handler := mustMiddleware(t, NewMemoryStore(), Config{}, next)
	if got := request(handler, `{}`); got.Code != http.StatusServiceUnavailable {
		t.Fatalf("first status = %d", got.Code)
	}
	if got := request(handler, `{}`); got.Code != http.StatusOK || got.Body.String() != "recovered" {
		t.Fatalf("second status = %d body = %q", got.Code, got.Body.String())
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestMiddlewarePendingReservation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte("done"))
	})
	handler := mustMiddleware(t, NewMemoryStore(), Config{}, next)
	go func() { _ = request(handler, `{}`) }()
	<-started
	pending := request(handler, `{}`)
	if pending.Code != http.StatusConflict {
		t.Fatalf("pending status = %d", pending.Code)
	}
	close(release)
}

func TestConfigValidation(t *testing.T) {
	store := NewMemoryStore()
	for _, test := range []struct {
		name string
		cfg  Config
		want string
	}{
		{name: "negative TTL", cfg: Config{TTL: -time.Second}, want: "TTL"},
		{name: "negative request", cfg: Config{MaxRequestBytes: -1}, want: "request bytes"},
		{name: "negative response", cfg: Config{MaxResponseBytes: -1}, want: "response bytes"},
		{name: "empty method", cfg: Config{Methods: []string{" "}}, want: "method"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Middleware(store, test.cfg, nil); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestRedisStoreLifecycle(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store, err := NewRedisStore(client, "test:idem:")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pending := Record{Fingerprint: "fp", State: statePending, CreatedAt: time.Now()}
	existing, acquired, err := store.Reserve(ctx, "key", pending, time.Minute)
	if err != nil || !acquired || existing.Fingerprint != "fp" {
		t.Fatalf("reserve = %#v %t %v", existing, acquired, err)
	}
	again, acquired, err := store.Reserve(ctx, "key", pending, time.Minute)
	if err != nil || acquired || again.State != statePending {
		t.Fatalf("second reserve = %#v %t %v", again, acquired, err)
	}
	completed := pending
	completed.State = stateComplete
	completed.Status = http.StatusOK
	completed.Body = []byte("ok")
	if err := store.Complete(ctx, "key", completed, time.Minute); err != nil {
		t.Fatal(err)
	}
	_, acquired, err = store.Reserve(ctx, "key", pending, time.Minute)
	if err != nil || acquired {
		t.Fatalf("completed reserve = %t %v", acquired, err)
	}
	if err := store.Abort(ctx, "missing", "fp"); err != nil {
		t.Fatal(err)
	}
}

func mustMiddleware(t *testing.T, store Store, cfg Config, next http.Handler) http.Handler {
	t.Helper()
	middleware, err := Middleware(store, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return middleware(next)
}

func request(handler http.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/items", bytes.NewBufferString(body))
	req.Header.Set(defaultHeaderName, "key-1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestMemoryStoreExpiry(t *testing.T) {
	store := NewMemoryStore()
	pending := Record{Fingerprint: "fp", State: statePending, CreatedAt: time.Now()}
	_, acquired, err := store.Reserve(context.Background(), "key", pending, -time.Nanosecond)
	if err == nil || acquired {
		t.Fatalf("negative TTL accepted: acquired=%t err=%v", acquired, err)
	}
	if !errors.Is(err, errors.New("idempotency TTL must be positive")) && !strings.Contains(err.Error(), "TTL") {
		t.Fatalf("error = %v", err)
	}
}
