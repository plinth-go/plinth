package redislock

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestLocker(t *testing.T) *Locker {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	locker, err := New(client, Config{Prefix: "test:lock:"})
	if err != nil {
		t.Fatal(err)
	}
	return locker
}

func TestAcquireReleaseAndRefresh(t *testing.T) {
	ctx := context.Background()
	locker := newTestLocker(t)
	lock, err := locker.Acquire(ctx, "resource", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if lock.Key() != "test:lock:resource" {
		t.Fatalf("key = %q", lock.Key())
	}
	if _, err := locker.Acquire(ctx, "resource", time.Minute); !errors.Is(err, ErrLocked) {
		t.Fatalf("second acquire = %v", err)
	}
	if err := lock.Refresh(ctx, 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(ctx); err != nil {
		t.Fatal(err)
	}
	next, err := locker.Acquire(ctx, "resource", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(ctx); !errors.Is(err, ErrLockLost) {
		t.Fatalf("stale release = %v", err)
	}
	if err := next.Release(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestExpiredLeaseCannotBeReleased(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	locker, err := New(client, Config{Prefix: "test:lock:"})
	if err != nil {
		t.Fatal(err)
	}
	lock, err := locker.Acquire(context.Background(), "resource", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	server.FastForward(2 * time.Second)
	if err := lock.Refresh(context.Background(), time.Minute); !errors.Is(err, ErrLockLost) {
		t.Fatalf("refresh = %v", err)
	}
	if err := lock.Release(context.Background()); !errors.Is(err, ErrLockLost) {
		t.Fatalf("release = %v", err)
	}
}

func TestAcquireWaitHonorsContext(t *testing.T) {
	locker := newTestLocker(t)
	first, err := locker.Acquire(context.Background(), "resource", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = locker.AcquireWait(ctx, "resource", time.Minute, time.Second, time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
}

func TestValidation(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	if _, err := New(nil, Config{Prefix: "prefix:"}); err == nil {
		t.Fatal("nil client was accepted")
	}
	if _, err := New(client, Config{Prefix: " "}); err == nil {
		t.Fatal("empty prefix was accepted")
	}
	locker, err := New(client, Config{Prefix: "prefix:"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := locker.Acquire(context.Background(), " ", time.Minute); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("empty key error = %v", err)
	}
	if _, err := locker.Acquire(context.Background(), "key", 0); err == nil {
		t.Fatal("zero TTL was accepted")
	}
	if _, err := locker.AcquireWait(context.Background(), "key", time.Minute, time.Second, 0); err == nil {
		t.Fatal("zero retry interval was accepted")
	}
}

func TestStrategyAndVersionValidation(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	for _, test := range []struct {
		name string
		cfg  Config
		want string
	}{
		{name: "unknown strategy", cfg: Config{Prefix: "p:", Strategy: "magic"}, want: "unsupported redis lock strategy"},
		{name: "old version", cfg: Config{Prefix: "p:", RedisVersion: "2.6.11"}, want: "minimum is 2.6.12"},
		{name: "bad version", cfg: Config{Prefix: "p:", RedisVersion: "redis-7"}, want: "major.minor"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(client, test.cfg); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}

	if _, err := New(client, Config{Prefix: "p:", Strategy: StrategyCommands, RedisVersion: "7.2.1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(client, Config{Prefix: "p:", RedisVersion: MinimumRedisVersion}); err != nil {
		t.Fatal(err)
	}
}

func TestRedisVersionStrategyMatrix(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	tests := []struct {
		name     string
		strategy Strategy
		version  string
		wantErr  string
	}{
		{name: "script minimum", strategy: StrategyScript, version: "2.6.12"},
		{name: "commands minimum", strategy: StrategyCommands, version: "2.6.12"},
		{name: "script older patch", strategy: StrategyScript, version: "2.6.11", wantErr: "minimum is 2.6.12"},
		{name: "commands older patch", strategy: StrategyCommands, version: "2.6.11", wantErr: "minimum is 2.6.12"},
		{name: "script old minor", strategy: StrategyScript, version: "2.5.0", wantErr: "minimum is 2.6.12"},
		{name: "commands old minor", strategy: StrategyCommands, version: "2.5.0", wantErr: "minimum is 2.6.12"},
		{name: "script redis 4", strategy: StrategyScript, version: "4.0"},
		{name: "commands redis 4", strategy: StrategyCommands, version: "4.0"},
		{name: "script redis 7", strategy: StrategyScript, version: "7.4.2"},
		{name: "commands redis 7", strategy: StrategyCommands, version: "7.4.2"},
		{name: "script with too many segments", strategy: StrategyScript, version: "7.4.2.1", wantErr: "major.minor[.patch]"},
		{name: "commands with negative segment", strategy: StrategyCommands, version: "7.-1", wantErr: "invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(client, Config{Prefix: "test:lock:", Strategy: test.strategy, RedisVersion: test.version})
			if test.wantErr == "" && err != nil {
				t.Fatalf("error = %v", err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestCommandStrategy(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	locker, err := New(client, Config{Prefix: "test:lock:", Strategy: StrategyCommands, RedisVersion: "6.2"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	lock, err := locker.Acquire(ctx, "resource", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Refresh(ctx, 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(ctx); !errors.Is(err, ErrLockLost) {
		t.Fatalf("second release = %v", err)
	}
}
