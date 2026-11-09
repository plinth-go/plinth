package redislimit

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

func TestStrategiesEnforceFixedWindow(t *testing.T) {
	for _, strategy := range []Strategy{StrategyScript, StrategyCommands} {
		t.Run(string(strategy), func(t *testing.T) {
			server := miniredis.RunT(t)
			client := goredis.NewClient(&goredis.Options{Addr: server.Addr()})
			t.Cleanup(func() { _ = client.Close() })
			limiter, err := New(client, Config{
				Prefix: "test:ratelimit:", Limit: 2, Window: time.Minute, Strategy: strategy,
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			first, err := limiter.Allow(ctx, "client")
			if err != nil || !first.Allowed || first.Remaining != 1 {
				t.Fatalf("first = %#v, %v", first, err)
			}
			second, err := limiter.Allow(ctx, "client")
			if err != nil || !second.Allowed || second.Remaining != 0 {
				t.Fatalf("second = %#v, %v", second, err)
			}
			third, err := limiter.Allow(ctx, "client")
			if err != nil || third.Allowed || third.RetryAfter <= 0 {
				t.Fatalf("third = %#v, %v", third, err)
			}
			server.FastForward(2 * time.Minute)
			next, err := limiter.Allow(ctx, "client")
			if err != nil || !next.Allowed || next.Remaining != 1 {
				t.Fatalf("next = %#v, %v", next, err)
			}
		})
	}
}

func TestValidation(t *testing.T) {
	if _, err := New(nil, Config{Prefix: "p:", Limit: 1, Window: time.Second}); err == nil {
		t.Fatal("nil client accepted")
	}
	server := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	tests := []Config{
		{Limit: 1, Window: time.Second},
		{Prefix: "p:", Window: time.Second},
		{Prefix: "p:", Limit: 1},
		{Prefix: "p:", Limit: 1, Window: time.Nanosecond},
		{Prefix: "p:", Limit: 1, Window: time.Second, Strategy: "magic"},
	}
	for _, cfg := range tests {
		if _, err := New(client, cfg); err == nil {
			t.Fatalf("accepted invalid config %#v", cfg)
		}
	}
	limiter, err := New(client, Config{Prefix: "p:", Limit: 1, Window: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := limiter.Allow(context.Background(), " "); err == nil {
		t.Fatal("empty key accepted")
	}
}

func TestScriptRepairsKeyWithoutExpiry(t *testing.T) {
	server := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	if err := client.Set(ctx, "test:ratelimit:client", 5, 0).Err(); err != nil {
		t.Fatal(err)
	}
	limiter, err := New(client, Config{
		Prefix: "test:ratelimit:", Limit: 10, Window: time.Minute, Strategy: StrategyScript,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := limiter.Allow(ctx, "client"); err != nil {
		t.Fatal(err)
	}
	ttl, err := client.PTTL(ctx, "test:ratelimit:client").Result()
	if err != nil || ttl <= 0 || ttl > time.Minute {
		t.Fatalf("ttl = %v, err = %v", ttl, err)
	}
}

func TestRedisVersionValidation(t *testing.T) {
	server := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	tests := []struct {
		name     string
		strategy Strategy
		version  string
		wantErr  string
	}{
		{name: "script minimum", strategy: StrategyScript, version: MinimumRedisVersion},
		{name: "commands minimum", strategy: StrategyCommands, version: MinimumRedisVersion},
		{name: "script redis seven", strategy: StrategyScript, version: "7.4.2"},
		{name: "commands redis seven", strategy: StrategyCommands, version: "7.4"},
		{name: "too old", strategy: StrategyCommands, version: "2.5.9", wantErr: "minimum is 2.6.0"},
		{name: "bad version", strategy: StrategyScript, version: "redis-7", wantErr: "major.minor"},
		{name: "too many segments", strategy: StrategyScript, version: "7.4.2.1", wantErr: "major.minor"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(client, Config{
				Prefix: "test:ratelimit:", Limit: 1, Window: time.Second,
				Strategy: test.strategy, RedisVersion: test.version,
			})
			if test.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}
