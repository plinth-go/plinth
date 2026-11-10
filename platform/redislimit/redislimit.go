// Package redislimit provides a Redis-backed fixed-window limiter. Lua is the
// default; use StrategyCommands when EVAL is disabled by ACLs or deployment
// policy.
package redislimit

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/plinth-go/plinth/platform/internal/rediscompat"
	"github.com/plinth-go/plinth/platform/ratelimit"
	"github.com/plinth-go/plinth/utils"
	"github.com/redis/go-redis/v9"
)

type Strategy string

const (
	StrategyScript   Strategy = "script"
	StrategyCommands Strategy = "commands"

	// MinimumRedisVersion is the oldest supported server. Both strategies rely
	// on PTTL/PEXPIRE; the script strategy also requires EVAL.
	MinimumRedisVersion = "2.6.0"
)

const allowScript = `
local count = redis.call("INCR", KEYS[1])
local ttl = redis.call("PTTL", KEYS[1])
if count == 1 or ttl < 0 then
	redis.call("PEXPIRE", KEYS[1], ARGV[1])
	ttl = tonumber(ARGV[1])
end
return {count, ttl}
`

type Config struct {
	Prefix       string
	Limit        int
	Window       time.Duration
	Strategy     Strategy
	RedisVersion string
}

type Limiter struct {
	client   redis.UniversalClient
	prefix   string
	limit    int
	window   time.Duration
	strategy Strategy
}

func New(client redis.UniversalClient, cfg Config) (*Limiter, error) {
	if isNilRedis(client) {
		return nil, errors.New("rate limit redis client is nil")
	}
	prefix := strings.TrimSpace(cfg.Prefix)
	if prefix == "" {
		return nil, errors.New("rate limit redis prefix is required")
	}
	if strings.ContainsAny(prefix, "\r\n\x00") {
		return nil, errors.New("rate limit redis prefix must not contain control characters")
	}
	if cfg.Limit <= 0 {
		return nil, errors.New("rate limit must be positive")
	}
	if cfg.Window <= 0 {
		return nil, errors.New("rate limit window must be positive")
	}
	if cfg.Window < time.Millisecond {
		return nil, errors.New("rate limit window must be at least 1ms for Redis")
	}
	strategy := utils.IfEmptyElse(cfg.Strategy, StrategyScript)
	if strategy != StrategyScript && strategy != StrategyCommands {
		return nil, fmt.Errorf("unsupported rate limit strategy %q", strategy)
	}
	if version := strings.TrimSpace(cfg.RedisVersion); version != "" {
		if err := validateRedisVersion(version, strategy); err != nil {
			return nil, err
		}
	}
	return &Limiter{
		client: client, prefix: prefix, limit: cfg.Limit, window: cfg.Window, strategy: strategy,
	}, nil
}

func validateRedisVersion(value string, strategy Strategy) error {
	version, err := rediscompat.Parse(value)
	if err != nil {
		return err
	}
	minimum, _ := rediscompat.Parse(MinimumRedisVersion)
	if rediscompat.Compare(version, minimum) < 0 {
		return fmt.Errorf("redis version %s is unsupported; minimum is %s (PTTL/PEXPIRE)", value, MinimumRedisVersion)
	}
	evalMinimum := rediscompat.Version{Major: 2, Minor: 6}
	if strategy == StrategyScript && rediscompat.Compare(version, evalMinimum) < 0 {
		return fmt.Errorf("redis version %s does not support EVAL; use StrategyCommands", value)
	}
	return nil
}

func (l *Limiter) Allow(ctx context.Context, key string) (ratelimit.Result, error) {
	if l == nil || isNilRedis(l.client) {
		return ratelimit.Result{}, errors.New("rate limiter is nil")
	}
	if err := ratelimit.ValidateKey(key); err != nil {
		return ratelimit.Result{}, err
	}
	key = strings.TrimSpace(key)
	redisKey := l.prefix + key
	if l.strategy == StrategyScript {
		return l.allowScript(ctx, redisKey)
	}
	return l.allowCommands(ctx, redisKey)
}

func (l *Limiter) allowScript(ctx context.Context, key string) (ratelimit.Result, error) {
	values, err := l.client.Eval(ctx, allowScript, []string{key}, l.window.Milliseconds()).Slice()
	if err != nil {
		return ratelimit.Result{}, err
	}
	if len(values) != 2 {
		return ratelimit.Result{}, errors.New("rate limit script returned an invalid result")
	}
	count, err := integer(values[0])
	if err != nil {
		return ratelimit.Result{}, err
	}
	ttl, err := integer(values[1])
	if err != nil {
		return ratelimit.Result{}, err
	}
	if ttl < 0 {
		ttl = l.window.Milliseconds()
	}
	return l.result(count, time.Duration(ttl)*time.Millisecond), nil
}

func (l *Limiter) allowCommands(ctx context.Context, key string) (ratelimit.Result, error) {
	for attempt := 0; attempt < 3; attempt++ {
		if _, err := l.client.SetNX(ctx, key, 0, l.window).Result(); err != nil {
			return ratelimit.Result{}, err
		}
		count, err := l.client.Incr(ctx, key).Result()
		if err != nil {
			return ratelimit.Result{}, err
		}
		ttl, err := l.client.PTTL(ctx, key).Result()
		if err != nil {
			return ratelimit.Result{}, err
		}
		if ttl >= 0 {
			return l.result(count, ttl), nil
		}
		if ttl == -2 {
			continue
		}
		if err := l.client.PExpire(ctx, key, l.window).Err(); err != nil {
			return ratelimit.Result{}, err
		}
		ttl, err = l.client.PTTL(ctx, key).Result()
		if err != nil {
			return ratelimit.Result{}, err
		}
		if ttl >= 0 {
			return l.result(count, ttl), nil
		}
	}
	return ratelimit.Result{}, errors.New("rate limit window could not be established")
}

func (l *Limiter) result(count int64, ttl time.Duration) ratelimit.Result {
	if ttl <= 0 || ttl > l.window {
		ttl = l.window
	}
	reset := time.Now().Add(ttl)
	if count > int64(l.limit) {
		return ratelimit.Result{Allowed: false, Limit: l.limit, Remaining: 0, ResetAt: reset, RetryAfter: ttl}
	}
	return ratelimit.Result{Allowed: true, Limit: l.limit, Remaining: l.limit - int(count), ResetAt: reset}
}

func integer(value any) (int64, error) {
	switch value := value.(type) {
	case int64:
		return value, nil
	case int:
		return int64(value), nil
	case string:
		var parsed int64
		if _, err := fmt.Sscan(value, &parsed); err != nil {
			return 0, err
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("rate limit result has type %T", value)
	}
}

func isNilRedis(client redis.UniversalClient) bool {
	if client == nil {
		return true
	}
	value := reflect.ValueOf(client)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
