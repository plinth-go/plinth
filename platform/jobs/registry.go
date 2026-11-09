package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/plinth-go/plinth/internal/jsonx"
)

type Handler func(context.Context, json.RawMessage) error

type registration struct {
	handler  Handler
	validate func(json.RawMessage) error
}

type Registry struct {
	mu       sync.RWMutex
	handlers map[string]registration
}

func NewRegistry() *Registry {
	return &Registry{handlers: map[string]registration{}}
}

// Register binds a typed payload to a job kind. This is the only place where
// the framework touches payload decoding; business handlers never see JSON.
func Register[T any](r *Registry, kind string, fn func(context.Context, T) error) error {
	if r == nil {
		return errors.New("job registry is nil")
	}
	kind, err := normalizeJobKind(kind)
	if err != nil {
		return err
	}
	if fn == nil {
		return fmt.Errorf("job handler for %q is nil", kind)
	}
	payloadType := reflect.TypeFor[T]()
	if err := jsonx.ValidateType(payloadType); err != nil {
		return fmt.Errorf("job payload type for %q: %w", kind, err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.handlers == nil {
		r.handlers = make(map[string]registration)
	}
	if _, exists := r.handlers[kind]; exists {
		return fmt.Errorf("job kind %q already registered", kind)
	}
	decode := func(payload json.RawMessage) (T, error) {
		var value T
		if err := jsonx.DecodeOne(bytes.NewReader(payload), &value); err != nil {
			return value, fmt.Errorf("decode %s payload: %w", kind, err)
		}
		return value, nil
	}
	r.handlers[kind] = registration{
		handler: func(ctx context.Context, payload json.RawMessage) error {
			value, err := decode(payload)
			if err != nil {
				return err
			}
			return fn(ctx, value)
		},
		validate: func(payload json.RawMessage) error {
			_, err := decode(payload)
			return err
		},
	}
	return nil
}

func MustRegister[T any](r *Registry, kind string, fn func(context.Context, T) error) {
	if err := Register(r, kind, fn); err != nil {
		panic(err)
	}
}

func (r *Registry) Handler(kind string) (Handler, bool) {
	if r == nil {
		return nil, false
	}
	kind = strings.TrimSpace(kind)
	r.mu.RLock()
	defer r.mu.RUnlock()
	registration, ok := r.handlers[kind]
	return registration.handler, ok
}

// ValidatePayload decodes a payload with the registered handler's exact type
// and strict JSON policy without invoking the handler. Nil and JSON null are
// normalized to the empty object, matching job persistence.
func (r *Registry) ValidatePayload(kind string, payload json.RawMessage) error {
	if r == nil {
		return errors.New("job registry is nil")
	}
	kind = strings.TrimSpace(kind)
	r.mu.RLock()
	registration, ok := r.handlers[kind]
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("job kind %q is not registered", kind)
	}
	normalized, err := marshalPayload(payload)
	if err != nil {
		return fmt.Errorf("encode job payload: %w", err)
	}
	return registration.validate(normalized)
}

func (r *Registry) Kinds() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	kinds := make([]string, 0, len(r.handlers))
	for kind := range r.handlers {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	return kinds
}

func normalizeJobKind(kind string) (string, error) {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return "", fmt.Errorf("job kind is empty")
	}
	if err := validateIdentifier(kind, "job kind"); err != nil {
		return "", err
	}
	return kind, nil
}
