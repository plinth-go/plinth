// Package redis provides the optional go-redis lifecycle component. Import it
// only when the application needs Redis.
package redis

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

type Redis struct {
	mu      sync.Mutex
	client  *goredis.Client
	started bool
	closed  bool
}

// New creates a Redis lifecycle component from a redis:// URL.
func New(redisURL string) (*Redis, error) {
	redisURL = strings.TrimSpace(redisURL)
	if redisURL == "" {
		return nil, errors.New("REDIS_URL must not be empty")
	}
	opt, err := goredis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse REDIS_URL: %w", err)
	}
	return &Redis{client: goredis.NewClient(opt)}, nil
}

func (r *Redis) Name() string { return "redis" }

func (r *Redis) Start(ctx context.Context) error {
	if r == nil {
		return errors.New("redis component is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.client == nil {
		return errors.New("redis client is nil")
	}
	if r.closed {
		return errors.New("redis component already stopped")
	}
	if r.started {
		return errors.New("redis component already started")
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := r.client.Ping(pingCtx).Err(); err != nil {
		_ = r.client.Close()
		r.closed = true
		return fmt.Errorf("ping redis: %w", err)
	}
	r.started = true
	return nil
}

func (r *Redis) Stop(context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	if r.client == nil {
		return nil
	}
	return r.client.Close()
}

func (r *Redis) Client() *goredis.Client {
	if r == nil {
		return nil
	}
	return r.client
}

func (r *Redis) Ping(ctx context.Context) error {
	if r == nil {
		return errors.New("redis component is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.client == nil {
		return errors.New("redis client is nil")
	}
	if r.closed {
		return errors.New("redis component is stopped")
	}
	return r.client.Ping(ctx).Err()
}
