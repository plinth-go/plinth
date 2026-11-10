// Package ratelimit provides a small fixed-window limiter contract and an
// HTTP middleware. Its memory implementation is process-local; the optional
// platform/redislimit subpackage coordinates across instances.
package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/plinth-go/plinth/kernel"
	"github.com/plinth-go/plinth/utils"
)

const defaultMemoryEntries = 100_000

var errMemoryCapacity = errors.New("rate limiter memory capacity exhausted")

// Result describes one limiter decision. ResetAt is the end of the current
// fixed window; RetryAfter is zero when the request is allowed.
type Result struct {
	Allowed    bool
	Limit      int
	Remaining  int
	ResetAt    time.Time
	RetryAfter time.Duration
}

// Limiter decides whether key may perform one operation.
type Limiter interface {
	Allow(context.Context, string) (Result, error)
}

// MemoryLimiter is a process-local fixed-window limiter. Use Redis for
// coordination across replicas.
type MemoryLimiter struct {
	mu         sync.Mutex
	limit      int
	window     time.Duration
	entries    map[string]memoryEntry
	maxEntries int
	lastSweep  time.Time
	now        func() time.Time
}

type memoryEntry struct {
	count int
	reset time.Time
}

func NewMemory(limit int, window time.Duration) (*MemoryLimiter, error) {
	if limit <= 0 {
		return nil, errors.New("rate limit must be positive")
	}
	if window <= 0 {
		return nil, errors.New("rate limit window must be positive")
	}
	return &MemoryLimiter{
		limit: limit, window: window, entries: make(map[string]memoryEntry),
		maxEntries: defaultMemoryEntries, now: time.Now,
	}, nil
}

func (l *MemoryLimiter) Allow(ctx context.Context, key string) (Result, error) {
	if l == nil {
		return Result{}, errors.New("rate limiter is nil")
	}
	if err := ValidateKey(key); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if !now.Before(l.lastSweep.Add(l.window)) {
		l.sweepExpired(now)
		l.lastSweep = now
	}
	entry, ok := l.entries[key]
	if !ok && len(l.entries) >= l.maxEntries {
		return Result{}, errMemoryCapacity
	}
	if !ok || !now.Before(entry.reset) {
		entry = memoryEntry{reset: now.Add(l.window)}
	}
	entry.count++
	l.entries[key] = entry
	if entry.count > l.limit {
		return Result{
			Allowed: false, Limit: l.limit, Remaining: 0,
			ResetAt: entry.reset, RetryAfter: entry.reset.Sub(now),
		}, nil
	}
	return Result{
		Allowed: true, Limit: l.limit, Remaining: l.limit - entry.count, ResetAt: entry.reset,
	}, nil
}

func (l *MemoryLimiter) sweepExpired(now time.Time) {
	for key, entry := range l.entries {
		if !now.Before(entry.reset) {
			delete(l.entries, key)
		}
	}
}

// Config controls HTTP integration.
type Config struct {
	Limiter Limiter
	Logger  *slog.Logger
	// Key defaults to the matched operation ID plus the direct RemoteAddr host.
	// Applications behind a trusted proxy should provide their own key function.
	Key  func(*http.Request) (string, error)
	Skip func(*http.Request) bool
}

// New builds rate-limit middleware. A limiter error fails closed with a 500
// Problem rather than silently allowing unlimited traffic.
func New(cfg Config) (func(http.Handler) http.Handler, error) {
	if isNilLimiter(cfg.Limiter) {
		return nil, errors.New("rate limit limiter is nil")
	}
	logger := utils.IfNilElse(cfg.Logger, slog.Default())
	keyFunc := cfg.Key
	if keyFunc == nil {
		keyFunc = DefaultKey
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if cfg.Skip != nil && cfg.Skip(r) {
				next.ServeHTTP(w, r)
				return
			}
			key, err := keyFunc(r)
			if err != nil {
				logger.Error("rate limit key failed", "method", r.Method, "path", r.URL.Path, "error", err)
				kernel.WriteProblem(w, r, kernel.Internal(err), logger.Error)
				return
			}
			if err := ValidateKey(key); err != nil {
				logger.Error("rate limit key is invalid", "method", r.Method, "path", r.URL.Path, "error", err)
				kernel.WriteProblem(w, r, kernel.Internal(err), logger.Error)
				return
			}
			result, err := cfg.Limiter.Allow(r.Context(), key)
			if err != nil {
				logger.Error("rate limit check failed", "method", r.Method, "path", r.URL.Path, "error", err)
				kernel.WriteProblem(w, r, kernel.Internal(err), logger.Error)
				return
			}
			setHeaders(w.Header(), result)
			if !result.Allowed {
				kernel.WriteProblem(w, r, kernel.TooManyRequests("rate limit exceeded"), logger.Error)
				return
			}
			next.ServeHTTP(w, r)
		})
	}, nil
}

func isNilLimiter(limiter Limiter) bool {
	if limiter == nil {
		return true
	}
	value := reflect.ValueOf(limiter)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// DefaultKey limits each matched operation and direct client address
// independently. It deliberately does not trust X-Forwarded-For.
func DefaultKey(r *http.Request) (string, error) {
	scope := "unmatched"
	if operation, ok := kernel.OperationFromContext(r.Context()); ok && operation.OperationID != "" {
		scope = operation.OperationID
	}
	host := strings.TrimSpace(r.RemoteAddr)
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	host = utils.IfEmptyElse(host, "unknown")
	return scope + "|" + host, nil
}

func setHeaders(header http.Header, result Result) {
	header.Set("RateLimit-Limit", strconv.Itoa(result.Limit))
	header.Set("RateLimit-Remaining", strconv.Itoa(result.Remaining))
	reset := result.ResetAt
	if reset.IsZero() {
		return
	}
	seconds := int64(time.Until(reset).Seconds() + 0.999)
	if seconds < 0 {
		seconds = 0
	}
	header.Set("RateLimit-Reset", strconv.FormatInt(seconds, 10))
	if result.RetryAfter > 0 {
		header.Set("Retry-After", strconv.FormatInt(int64(result.RetryAfter.Seconds()+0.999), 10))
	}
}

// ValidateKey applies the limits shared by the built-in memory and Redis
// limiters. Implementations may apply stricter application-specific rules.
func ValidateKey(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("rate limit key is required")
	}
	if !utf8.ValidString(key) {
		return errors.New("rate limit key must be valid UTF-8")
	}
	if len(key) > 512 {
		return fmt.Errorf("rate limit key must not exceed 512 bytes")
	}
	for index := 0; index < len(key); index++ {
		if key[index] < 0x20 || key[index] == 0x7f {
			return errors.New("rate limit key must not contain control characters")
		}
	}
	return nil
}
