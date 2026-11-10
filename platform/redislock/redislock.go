// Package redislock provides a small distributed lock backed by Redis. Locks
// are leases with a random token; release and refresh only succeed while the
// caller still owns that exact token.
package redislock

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/plinth-go/plinth/platform/internal/rediscompat"
	"github.com/plinth-go/plinth/utils"
	"github.com/redis/go-redis/v9"
)

var (
	// ErrLocked reports that another owner currently holds the lock.
	ErrLocked = errors.New("redis lock is held")
	// ErrLockLost reports that the lease expired or was released elsewhere.
	ErrLockLost = errors.New("redis lock lease was lost")
)

// Strategy selects how ownership is checked for release and refresh.
type Strategy string

const (
	// StrategyScript uses one Lua script per operation. It requires EVAL to be
	// available and is the default.
	StrategyScript Strategy = "script"
	// StrategyCommands uses WATCH/MULTI/EXEC. Use it when Lua is disabled by
	// Redis ACLs or the deployment forbids EVAL. It is slightly more expensive
	// but does not require scripting support.
	StrategyCommands Strategy = "commands"

	// MinimumRedisVersion is the oldest supported server. SET NX PX requires
	// Redis 2.6.12; both strategies rely on it.
	MinimumRedisVersion = "2.6.12"
)

const (
	releaseScript = `
		if redis.call("GET", KEYS[1]) == ARGV[1] then
			return redis.call("DEL", KEYS[1])
		end
		return 0
	`
	refreshScript = `
		if redis.call("GET", KEYS[1]) == ARGV[1] then
			return redis.call("PEXPIRE", KEYS[1], ARGV[2])
		end
		return 0
	`
)

// Locker acquires namespaced Redis lock leases.
type Locker struct {
	client   redis.UniversalClient
	prefix   string
	strategy Strategy
}

// Lock is one owned lease. It is safe to call Release and Refresh more than
// once; calls after ownership is lost return ErrLockLost.
type Lock struct {
	locker *Locker
	key    string
	token  string
}

// Config describes lock compatibility and key namespacing.
type Config struct {
	// Prefix is required and should be application-specific, for example
	// "shelf:lock:".
	Prefix string
	// Strategy defaults to StrategyScript. Set StrategyCommands when the Redis
	// deployment disables EVAL.
	Strategy Strategy
	// RedisVersion is optional but recommended. When set, New rejects versions
	// older than MinimumRedisVersion and versions that cannot satisfy the
	// selected strategy.
	RedisVersion string
}

// New creates a locker. Prefix is required and should be application-specific,
// for example "shelf:lock:".
func New(client redis.UniversalClient, cfg Config) (*Locker, error) {
	if isNilRedis(client) {
		return nil, errors.New("redis lock client is nil")
	}
	prefix := strings.TrimSpace(cfg.Prefix)
	if prefix == "" {
		return nil, errors.New("redis lock prefix is required")
	}
	if strings.ContainsAny(prefix, "\r\n\x00") {
		return nil, errors.New("redis lock prefix must not contain control characters")
	}
	strategy := utils.IfEmptyElse(cfg.Strategy, StrategyScript)
	if strategy != StrategyScript && strategy != StrategyCommands {
		return nil, fmt.Errorf("unsupported redis lock strategy %q", strategy)
	}
	if version := strings.TrimSpace(cfg.RedisVersion); version != "" {
		if err := validateRedisVersion(version, strategy); err != nil {
			return nil, err
		}
	}
	return &Locker{client: client, prefix: prefix, strategy: strategy}, nil
}

// Acquire attempts to obtain key once. A successful lease expires after ttl
// unless Refresh extends it.
func (l *Locker) Acquire(ctx context.Context, key string, ttl time.Duration) (*Lock, error) {
	if l == nil || isNilRedis(l.client) {
		return nil, errors.New("redis locker is nil")
	}
	key, err := l.redisKey(key)
	if err != nil {
		return nil, err
	}
	if ttl <= 0 {
		return nil, errors.New("redis lock TTL must be positive")
	}
	token, err := randomToken()
	if err != nil {
		return nil, fmt.Errorf("generate redis lock token: %w", err)
	}
	ok, err := l.client.SetNX(ctx, key, token, ttl).Result()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrLocked
	}
	return &Lock{locker: l, key: key, token: token}, nil
}

// AcquireWait retries until the lock is acquired, ctx ends, or wait elapses.
// A zero or negative wait means wait until ctx is done.
func (l *Locker) AcquireWait(ctx context.Context, key string, ttl, wait, retryInterval time.Duration) (*Lock, error) {
	if retryInterval <= 0 {
		return nil, errors.New("redis lock retry interval must be positive")
	}
	var timeout <-chan time.Time
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		timeout = timer.C
	}
	for {
		lock, err := l.Acquire(ctx, key, ttl)
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, ErrLocked) {
			return nil, err
		}
		timer := time.NewTimer(retryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timeout:
			timer.Stop()
			return nil, ErrLocked
		case <-timer.C:
		}
	}
}

// Release deletes the lease only while token ownership is intact.
func (l *Lock) Release(ctx context.Context) error {
	if l == nil || l.locker == nil {
		return errors.New("redis lock is nil")
	}
	return l.locker.updateOwned(ctx, l.key, l.token, false, 0)
}

// Refresh extends the lease only while token ownership is intact.
func (l *Lock) Refresh(ctx context.Context, ttl time.Duration) error {
	if l == nil || l.locker == nil {
		return errors.New("redis lock is nil")
	}
	if ttl <= 0 {
		return errors.New("redis lock TTL must be positive")
	}
	return l.locker.updateOwned(ctx, l.key, l.token, true, ttl)
}

// Key returns the fully namespaced Redis key for diagnostics.
func (l *Lock) Key() string {
	if l == nil {
		return ""
	}
	return l.key
}

func (l *Locker) redisKey(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", errors.New("redis lock key is required")
	}
	if !utf8.ValidString(key) {
		return "", errors.New("redis lock key must be valid UTF-8")
	}
	if strings.ContainsAny(key, "\r\n\x00") {
		return "", errors.New("redis lock key must not contain control characters")
	}
	return l.prefix + key, nil
}

func (l *Locker) updateOwned(ctx context.Context, key, token string, refresh bool, ttl time.Duration) error {
	if l.strategy == StrategyScript {
		var result int
		var err error
		if refresh {
			result, err = l.client.Eval(ctx, refreshScript, []string{key}, token, ttl.Milliseconds()).Int()
		} else {
			result, err = l.client.Eval(ctx, releaseScript, []string{key}, token).Int()
		}
		if err != nil {
			return err
		}
		if result == 0 {
			return ErrLockLost
		}
		return nil
	}

	for attempt := 0; attempt < 5; attempt++ {
		err := l.updateOwnedCommands(ctx, key, token, refresh, ttl)
		if errors.Is(err, redis.TxFailedErr) {
			continue
		}
		return err
	}
	return redis.TxFailedErr
}

func (l *Locker) updateOwnedCommands(ctx context.Context, key, token string, refresh bool, ttl time.Duration) error {
	return l.client.Watch(ctx, func(tx *redis.Tx) error {
		current, err := tx.Get(ctx, key).Result()
		if errors.Is(err, redis.Nil) {
			return ErrLockLost
		}
		if err != nil {
			return err
		}
		if current != token {
			return ErrLockLost
		}
		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			if refresh {
				pipe.PExpire(ctx, key, ttl)
			} else {
				pipe.Del(ctx, key)
			}
			return nil
		})
		return err
	}, key)
}

func validateRedisVersion(value string, strategy Strategy) error {
	version, err := rediscompat.Parse(value)
	if err != nil {
		return err
	}
	minimum, _ := rediscompat.Parse(MinimumRedisVersion)
	if rediscompat.Compare(version, minimum) < 0 {
		return fmt.Errorf("redis version %s is unsupported; minimum is %s (SET NX PX)", value, MinimumRedisVersion)
	}
	evalMinimum := rediscompat.Version{Major: 2, Minor: 6}
	if strategy == StrategyScript && rediscompat.Compare(version, evalMinimum) < 0 {
		return fmt.Errorf("redis version %s does not support EVAL; use StrategyCommands", value)
	}
	return nil
}

func randomToken() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
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
