// Package utils provides small, deterministic helpers for application glue
// code. Helpers here must be pure, dependency-free, and have no policy beyond
// the arguments they receive. Business rules and infrastructure behavior belong
// in their respective packages.
package utils

// IfElse returns whenTrue when condition is true, otherwise whenFalse. All
// arguments are evaluated before the call, so use it only for values that are
// safe to compute eagerly.
func IfElse[T any](condition bool, whenTrue, whenFalse T) T {
	if condition {
		return whenTrue
	}
	return whenFalse
}

// IfNilElse returns value when it is non-nil, otherwise fallback. It accepts
// pointer types, including named pointer types. The fallback argument is
// evaluated before the call, so use it only for values that are safe to compute
// eagerly.
func IfNilElse[T any, P ~*T](value, fallback P) P {
	if value == nil {
		return fallback
	}
	return value
}

// IfEmptyElse returns value when it is not its type's zero value, otherwise
// fallback. The fallback argument is evaluated before the call, so use it only
// for values that are safe to compute eagerly.
func IfEmptyElse[T comparable](value, fallback T) T {
	var zero T
	if value == zero {
		return fallback
	}
	return value
}

// NonNilSlice returns values when it is non-nil, otherwise an empty slice. It
// preserves the original backing array.
func NonNilSlice[T any](values []T) []T {
	if values == nil {
		return []T{}
	}
	return values
}

// NormalizePage returns a bounded, zero-based pagination window. A non-positive
// limit uses defaultLimit; limits above maxLimit are capped; negative offsets
// become zero. Invalid default and maximum values are normalized to at least one,
// and defaultLimit never exceeds maxLimit.
func NormalizePage(limit, offset, defaultLimit, maxLimit int) (int, int) {
	if maxLimit < 1 {
		maxLimit = 1
	}
	if defaultLimit < 1 {
		defaultLimit = 1
	}
	if defaultLimit > maxLimit {
		defaultLimit = maxLimit
	}
	if limit < 1 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}
