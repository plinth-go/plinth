// Package redis provides a go-redis session store for platform/session.
package redis

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/alexedwards/scs/v2"
	goredis "github.com/redis/go-redis/v9"
)

type Store struct {
	client goredis.UniversalClient
	prefix string
}

func NewStore(client goredis.UniversalClient, prefix string) (*Store, error) {
	if isNilRedis(client) {
		return nil, errors.New("session redis client is nil")
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return nil, errors.New("session redis prefix is required")
	}
	if strings.ContainsAny(prefix, "\r\n\x00") {
		return nil, errors.New("session redis prefix must not contain control characters")
	}
	return &Store{client: client, prefix: prefix}, nil
}

func (s *Store) Find(token string) ([]byte, bool, error) {
	return s.FindCtx(context.Background(), token)
}

func (s *Store) Commit(token string, value []byte, expiry time.Time) error {
	return s.CommitCtx(context.Background(), token, value, expiry)
}

func (s *Store) Delete(token string) error {
	return s.DeleteCtx(context.Background(), token)
}

func (s *Store) FindCtx(ctx context.Context, token string) ([]byte, bool, error) {
	if err := validateToken(token); err != nil {
		return nil, false, err
	}
	value, err := s.client.Get(ctx, s.prefix+token).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return value, true, nil
}

func (s *Store) CommitCtx(ctx context.Context, token string, value []byte, expiry time.Time) error {
	if err := validateToken(token); err != nil {
		return err
	}
	ttl := time.Until(expiry)
	if ttl <= 0 {
		return s.DeleteCtx(ctx, token)
	}
	return s.client.Set(ctx, s.prefix+token, value, ttl).Err()
}

func (s *Store) DeleteCtx(ctx context.Context, token string) error {
	if err := validateToken(token); err != nil {
		return err
	}
	return s.client.Del(ctx, s.prefix+token).Err()
}

func validateToken(token string) error {
	if strings.TrimSpace(token) == "" {
		return errors.New("session token is required")
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

var _ scs.CtxStore = (*Store)(nil)
