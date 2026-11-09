// Package redis provides a Redis-backed csrf.Store. It uses simple GET/SET/DEL
// operations and does not require Lua.
package redis

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/plinth-go/plinth/platform/csrf"
	goredis "github.com/redis/go-redis/v9"
)

type Store struct {
	client goredis.UniversalClient
	prefix string
}

func NewStore(client goredis.UniversalClient, prefix string) (*Store, error) {
	if isNilRedis(client) {
		return nil, errors.New("csrf redis client is nil")
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return nil, errors.New("csrf redis prefix is required")
	}
	if strings.ContainsAny(prefix, "\r\n\x00") {
		return nil, errors.New("csrf redis prefix must not contain control characters")
	}
	return &Store{client: client, prefix: prefix}, nil
}

func (s *Store) Put(ctx context.Context, id string, ttl time.Duration) error {
	if err := validate(id, ttl); err != nil {
		return err
	}
	return s.client.Set(ctx, s.prefix+id, "1", ttl).Err()
}

func (s *Store) Exists(ctx context.Context, id string) (bool, error) {
	if err := validate(id, time.Second); err != nil {
		return false, err
	}
	count, err := s.client.Exists(ctx, s.prefix+id).Result()
	return count > 0, err
}

func (s *Store) Delete(ctx context.Context, id string) error {
	if err := validate(id, time.Second); err != nil {
		return err
	}
	return s.client.Del(ctx, s.prefix+id).Err()
}

func validate(id string, ttl time.Duration) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("csrf token id is required")
	}
	if ttl <= 0 {
		return errors.New("csrf token TTL must be positive")
	}
	return nil
}

func isNilRedis(client goredis.UniversalClient) bool {
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

var _ csrf.Store = (*Store)(nil)
