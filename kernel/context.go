package kernel

import "context"

type contextKeyID struct{}

type ContextKey[T any] struct {
	name string
	id   *contextKeyID
}

func NewContextKey[T any](name string) ContextKey[T] {
	return ContextKey[T]{name: name, id: &contextKeyID{}}
}

func (k ContextKey[T]) Name() string { return k.name }

func (k ContextKey[T]) With(ctx context.Context, value T) context.Context {
	if k.id == nil {
		panic("kernel: uninitialized context key")
	}
	return context.WithValue(ctx, k, value)
}

func (k ContextKey[T]) Get(ctx context.Context) (T, bool) {
	var zero T
	if k.id == nil {
		return zero, false
	}
	value, ok := ctx.Value(k).(T)
	return value, ok
}
