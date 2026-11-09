package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRegistryDecodesTypedPayload(t *testing.T) {
	type payload struct {
		Message string `json:"message"`
	}
	registry := NewRegistry()
	var received string
	if err := Register(registry, "test.echo", func(_ context.Context, value payload) error {
		received = value.Message
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	handler, ok := registry.Handler("test.echo")
	if !ok {
		t.Fatal("handler not found")
	}
	if err := handler(context.Background(), json.RawMessage(`{"message":"hello"}`)); err != nil {
		t.Fatal(err)
	}
	if received != "hello" {
		t.Fatalf("received = %q", received)
	}
	if err := handler(context.Background(), json.RawMessage(`{"message":"hello","extra":true}`)); err == nil {
		t.Fatal("unknown payload field was accepted")
	}
	if err := handler(context.Background(), json.RawMessage(`{"message":"hello"} {"message":"again"}`)); err == nil {
		t.Fatal("trailing JSON value was accepted")
	}
	if err := handler(context.Background(), json.RawMessage(`{"message":"hello","message":"again"}`)); err == nil {
		t.Fatal("duplicate payload field was accepted")
	}
	if err := handler(context.Background(), json.RawMessage(`null`)); err == nil {
		t.Fatal("null payload was accepted")
	}
	if err := handler(context.Background(), json.RawMessage(`{"message":null}`)); err == nil {
		t.Fatal("nested null payload was accepted")
	}
	if err := Register(registry, "test.echo", func(context.Context, payload) error { return nil }); err == nil {
		t.Fatal("duplicate registration succeeded")
	}
	if err := Register[payload](nil, "test.echo", func(context.Context, payload) error { return nil }); err == nil {
		t.Fatal("nil registry was accepted")
	}
}

func TestRegistryKindsAreSorted(t *testing.T) {
	registry := NewRegistry()
	for _, kind := range []string{"z.last", "a.first", "m.middle"} {
		if err := Register(registry, kind, func(context.Context, struct{}) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Join(registry.Kinds(), ","); got != "a.first,m.middle,z.last" {
		t.Fatalf("kinds = %q", got)
	}
}

func TestRegistryCanonicalizesJobKinds(t *testing.T) {
	registry := NewRegistry()
	if err := Register(registry, "  test.echo  ", func(context.Context, struct{}) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Handler("test.echo"); !ok {
		t.Fatal("canonical kind was not registered")
	}
	if _, ok := registry.Handler("  test.echo  "); !ok {
		t.Fatal("query kind was not canonicalized")
	}
	if got := strings.Join(registry.Kinds(), ","); got != "test.echo" {
		t.Fatalf("kinds = %q", got)
	}
	if err := Register(registry, "test.echo", func(context.Context, struct{}) error { return nil }); err == nil {
		t.Fatal("canonical duplicate kind was accepted")
	}
}

func TestRegistryZeroValueCanRegister(t *testing.T) {
	var registry Registry
	if err := Register(&registry, "test.zero", func(context.Context, struct{}) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Handler("test.zero"); !ok {
		t.Fatal("handler registered on zero-value registry was not found")
	}
	if got := strings.Join(registry.Kinds(), ","); got != "test.zero" {
		t.Fatalf("kinds = %q", got)
	}
}

func TestNilRegistryQueriesAreEmpty(t *testing.T) {
	var registry *Registry
	if _, ok := registry.Handler("test.missing"); ok {
		t.Fatal("nil registry returned a handler")
	}
	if kinds := registry.Kinds(); kinds != nil {
		t.Fatalf("kinds = %#v, want nil", kinds)
	}
}

func TestRegistryValidatePayloadUsesHandlerTypeWithoutExecuting(t *testing.T) {
	type payload struct {
		Message string `json:"message"`
	}
	registry := NewRegistry()
	var called bool
	if err := Register(registry, "test.echo", func(context.Context, payload) error {
		called = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	for _, valid := range []json.RawMessage{
		nil,
		json.RawMessage(`null`),
		json.RawMessage(`{}`),
		json.RawMessage(`{"message":"hello"}`),
	} {
		if err := registry.ValidatePayload("test.echo", valid); err != nil {
			t.Fatalf("ValidatePayload(%s) error = %v", valid, err)
		}
	}
	for _, invalid := range []json.RawMessage{
		json.RawMessage(`{"message":"hello","extra":true}`),
		json.RawMessage(`{"message":null}`),
		json.RawMessage(`{"message":"hello"} {"message":"again"}`),
		json.RawMessage(`{"message":"hello","message":"again"}`),
		json.RawMessage(`{`),
	} {
		if err := registry.ValidatePayload("test.echo", invalid); err == nil {
			t.Fatalf("ValidatePayload(%s) was accepted", invalid)
		}
	}
	if err := registry.ValidatePayload("missing.kind", nil); err == nil {
		t.Fatal("unknown kind was accepted")
	}
	if err := (*Registry)(nil).ValidatePayload("test.echo", nil); err == nil {
		t.Fatal("nil registry was accepted")
	}
	if called {
		t.Fatal("ValidatePayload executed the handler")
	}
}

func TestRegistryRejectsUndecodablePayloadTypesAtRegistration(t *testing.T) {
	registry := NewRegistry()
	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "channel",
			call: func() error {
				return Register(registry, "test.channel", func(context.Context, chan int) error { return nil })
			},
		},
		{
			name: "field",
			call: func() error {
				type payload struct {
					Work chan int
				}
				return Register(registry, "test.field", func(context.Context, payload) error { return nil })
			},
		},
		{
			name: "function",
			call: func() error {
				return Register(registry, "test.function", func(context.Context, func()) error { return nil })
			},
		},
	}
	for _, tt := range tests {
		err := tt.call()
		if err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("%s registration error = %v", tt.name, err)
		}
	}
	if kinds := registry.Kinds(); len(kinds) != 0 {
		t.Fatalf("rejected registrations left kinds behind: %#v", kinds)
	}
}

func TestRetryDelayIsCapped(t *testing.T) {
	cfg := normalizeWorkerConfig(WorkerConfig{RetryBase: time.Second, RetryMax: 5 * time.Second})
	if got := retryDelay(cfg, 1); got != time.Second {
		t.Fatalf("attempt 1 delay = %s", got)
	}
	if got := retryDelay(cfg, 3); got != 4*time.Second {
		t.Fatalf("attempt 3 delay = %s", got)
	}
	if got := retryDelay(cfg, 8); got != 5*time.Second {
		t.Fatalf("capped delay = %s", got)
	}
	if got := retryDelay(cfg, 1_000_000); got != 5*time.Second {
		t.Fatalf("huge attempt delay = %s", got)
	}
}

func TestRetryMaxCannotBeBelowRetryBase(t *testing.T) {
	assertJobsPanicsWith(t, "must not be less than retry base", func() {
		normalizeWorkerConfig(WorkerConfig{RetryBase: 2 * time.Minute})
	})
}

func TestStaleAfterMustExceedJobTimeout(t *testing.T) {
	assertJobsPanicsWith(t, "must be greater than job timeout", func() {
		normalizeWorkerConfig(WorkerConfig{
			JobTimeout: 2 * time.Minute,
			StaleAfter: time.Minute,
		})
	})
}

func TestWorkerConfigRejectsNegativeValues(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  WorkerConfig
		want string
	}{
		{name: "concurrency", cfg: WorkerConfig{Concurrency: -1}, want: "concurrency must not be negative"},
		{name: "poll interval", cfg: WorkerConfig{PollInterval: -time.Second}, want: "poll interval must not be negative"},
		{name: "job timeout", cfg: WorkerConfig{JobTimeout: -time.Second}, want: "job timeout must not be negative"},
		{name: "stale after", cfg: WorkerConfig{StaleAfter: -time.Second}, want: "stale after must not be negative"},
		{name: "retry base", cfg: WorkerConfig{RetryBase: -time.Second}, want: "retry base must not be negative"},
		{name: "retry max", cfg: WorkerConfig{RetryMax: -time.Second}, want: "retry max must not be negative"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertJobsPanicsWith(t, tc.want, func() {
				NewWorker(nil, nil, nil, tc.cfg)
			})
		})
	}
}

func TestZeroWorkerConfigUsesDefaults(t *testing.T) {
	cfg := normalizeWorkerConfig(WorkerConfig{})
	defaults := DefaultWorkerConfig()
	if cfg != defaults {
		t.Fatalf("config = %#v, defaults = %#v", cfg, defaults)
	}
}

func assertJobsPanicsWith(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatalf("expected panic containing %q", want)
		}
		if got := fmt.Sprint(recovered); !strings.Contains(got, want) {
			t.Fatalf("panic = %q, want substring %q", got, want)
		}
	}()
	fn()
}
