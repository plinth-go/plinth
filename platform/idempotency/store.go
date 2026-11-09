package idempotency

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/redis/go-redis/v9"
)

const (
	statePending  = "pending"
	stateComplete = "complete"
)

var (
	// ErrReservationLost reports that the pending reservation expired or was
	// replaced before completion.
	ErrReservationLost = errors.New("idempotency reservation was lost")
)

// Record is the durable state associated with one idempotency key.
type Record struct {
	Fingerprint string      `json:"fingerprint"`
	State       string      `json:"state"`
	Status      int         `json:"status,omitempty"`
	Header      http.Header `json:"header,omitempty"`
	Body        []byte      `json:"body,omitempty"`
	CreatedAt   time.Time   `json:"created_at"`
}

// Store atomically reserves, completes, and aborts idempotency records.
// Reserve returns the existing record and acquired=false when the key is
// already present.
type Store interface {
	Reserve(context.Context, string, Record, time.Duration) (Record, bool, error)
	Complete(context.Context, string, Record, time.Duration) error
	Abort(context.Context, string, string) error
}

// MemoryStore is a process-local Store for one instance or tests. Use
// RedisStore for coordination across processes.
type MemoryStore struct {
	mu      sync.Mutex
	records map[string]memoryRecord
}

type memoryRecord struct {
	record    Record
	expiresAt time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{records: make(map[string]memoryRecord)}
}

func (s *MemoryStore) Reserve(_ context.Context, key string, pending Record, ttl time.Duration) (Record, bool, error) {
	if err := validateStoreArgs(key, ttl); err != nil {
		return Record{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire(key, time.Now())
	if existing, ok := s.records[key]; ok {
		return cloneRecord(existing.record), false, nil
	}
	s.records[key] = memoryRecord{record: cloneRecord(pending), expiresAt: time.Now().Add(ttl)}
	return cloneRecord(pending), true, nil
}

func (s *MemoryStore) Complete(_ context.Context, key string, completed Record, ttl time.Duration) error {
	if err := validateStoreArgs(key, ttl); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire(key, time.Now())
	existing, ok := s.records[key]
	if !ok || existing.record.State != statePending || existing.record.Fingerprint != completed.Fingerprint {
		return ErrReservationLost
	}
	s.records[key] = memoryRecord{record: cloneRecord(completed), expiresAt: time.Now().Add(ttl)}
	return nil
}

func (s *MemoryStore) Abort(_ context.Context, key, fingerprint string) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("idempotency key is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire(key, time.Now())
	existing, ok := s.records[key]
	if ok && existing.record.State == statePending && existing.record.Fingerprint == fingerprint {
		delete(s.records, key)
	}
	return nil
}

func (s *MemoryStore) expire(key string, now time.Time) {
	if existing, ok := s.records[key]; ok && !existing.expiresAt.After(now) {
		delete(s.records, key)
	}
}

// RedisStore stores JSON records under one application-owned key prefix. It
// uses WATCH/MULTI/EXEC rather than Lua for broad Redis-version and ACL
// compatibility.
type RedisStore struct {
	client redis.UniversalClient
	prefix string
}

func NewRedisStore(client redis.UniversalClient, prefix string) (*RedisStore, error) {
	if isNilRedis(client) {
		return nil, errors.New("idempotency redis client is nil")
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return nil, errors.New("idempotency redis prefix is required")
	}
	if strings.ContainsAny(prefix, "\r\n\x00") {
		return nil, errors.New("idempotency redis prefix must not contain control characters")
	}
	return &RedisStore{client: client, prefix: prefix}, nil
}

func (s *RedisStore) Reserve(ctx context.Context, key string, pending Record, ttl time.Duration) (Record, bool, error) {
	if err := validateStoreArgs(key, ttl); err != nil {
		return Record{}, false, err
	}
	redisKey := s.prefix + key
	encoded, err := json.Marshal(pending)
	if err != nil {
		return Record{}, false, err
	}
	for attempt := 0; attempt < 5; attempt++ {
		var existing Record
		acquired := false
		err := s.client.Watch(ctx, func(tx *redis.Tx) error {
			value, err := tx.Get(ctx, redisKey).Bytes()
			if err == nil {
				if err := json.Unmarshal(value, &existing); err != nil {
					return fmt.Errorf("decode idempotency record: %w", err)
				}
				return nil
			}
			if !errors.Is(err, redis.Nil) {
				return err
			}
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Set(ctx, redisKey, encoded, ttl)
				return nil
			})
			if err == nil {
				acquired = true
			}
			return err
		}, redisKey)
		if errors.Is(err, redis.TxFailedErr) {
			continue
		}
		if err != nil {
			return Record{}, false, err
		}
		if acquired {
			return cloneRecord(pending), true, nil
		}
		return cloneRecord(existing), false, nil
	}
	return Record{}, false, redis.TxFailedErr
}

func (s *RedisStore) Complete(ctx context.Context, key string, completed Record, ttl time.Duration) error {
	if err := validateStoreArgs(key, ttl); err != nil {
		return err
	}
	redisKey := s.prefix + key
	encoded, err := json.Marshal(completed)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 5; attempt++ {
		err := s.client.Watch(ctx, func(tx *redis.Tx) error {
			existing, err := readRecord(ctx, tx, redisKey)
			if err != nil {
				return err
			}
			if existing.State != statePending || existing.Fingerprint != completed.Fingerprint {
				return ErrReservationLost
			}
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Set(ctx, redisKey, encoded, ttl)
				return nil
			})
			return err
		}, redisKey)
		if errors.Is(err, redis.TxFailedErr) {
			continue
		}
		return err
	}
	return redis.TxFailedErr
}

func (s *RedisStore) Abort(ctx context.Context, key, fingerprint string) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("idempotency key is required")
	}
	redisKey := s.prefix + key
	for attempt := 0; attempt < 5; attempt++ {
		err := s.client.Watch(ctx, func(tx *redis.Tx) error {
			existing, err := readRecord(ctx, tx, redisKey)
			if errors.Is(err, redis.Nil) {
				return nil
			}
			if err != nil {
				return err
			}
			if existing.State != statePending || existing.Fingerprint != fingerprint {
				return nil
			}
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Del(ctx, redisKey)
				return nil
			})
			return err
		}, redisKey)
		if errors.Is(err, redis.TxFailedErr) {
			continue
		}
		return err
	}
	return redis.TxFailedErr
}

func readRecord(ctx context.Context, tx *redis.Tx, key string) (Record, error) {
	value, err := tx.Get(ctx, key).Bytes()
	if err != nil {
		return Record{}, err
	}
	var record Record
	if err := json.Unmarshal(value, &record); err != nil {
		return Record{}, fmt.Errorf("decode idempotency record: %w", err)
	}
	return record, nil
}

func validateStoreArgs(key string, ttl time.Duration) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("idempotency key is required")
	}
	if !utf8.ValidString(key) {
		return errors.New("idempotency key must be valid UTF-8")
	}
	if ttl <= 0 {
		return errors.New("idempotency TTL must be positive")
	}
	return nil
}

func cloneRecord(record Record) Record {
	record.Header = record.Header.Clone()
	record.Body = append([]byte(nil), record.Body...)
	return record
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
