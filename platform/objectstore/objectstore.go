// Package objectstore defines a small object-storage contract for application
// owned files. Implementations live in optional subpackages.
package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var ErrNotFound = errors.New("object not found")

// Object describes stored content.
type Object struct {
	Key         string
	Size        int64
	ContentType string
	ModifiedAt  time.Time
}

// PutOptions controls object metadata. ContentType is inferred from the key
// extension when empty.
type PutOptions struct {
	ContentType  string
	CacheControl string
	Metadata     map[string]string
}

// Store is the minimal object-storage surface used by applications. Put
// treats size as an exact byte count and implementations must reject a source
// that ends early or yields additional bytes.
type Store interface {
	Put(context.Context, string, io.Reader, int64, PutOptions) (Object, error)
	Open(context.Context, string) (io.ReadCloser, Object, error)
	Stat(context.Context, string) (Object, error)
	Delete(context.Context, string) error
	List(context.Context, string, int) ([]Object, error)
}

// NormalizeKey validates a portable object key. Keys are slash-separated,
// relative, non-empty, and cannot contain traversal segments or control bytes.
func NormalizeKey(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", errors.New("object key is required")
	}
	if len(key) > 1024 {
		return "", errors.New("object key must not exceed 1024 bytes")
	}
	if !utf8.ValidString(key) || strings.HasPrefix(key, "/") || strings.HasSuffix(key, "/") {
		return "", fmt.Errorf("object key %q must be a valid relative path", key)
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("object key %q contains an invalid segment", key)
		}
		if strings.Contains(segment, "\\") {
			return "", fmt.Errorf("object key %q must use forward slashes", key)
		}
		for _, char := range segment {
			if char == 0 || unicode.IsControl(char) {
				return "", fmt.Errorf("object key %q contains control characters", key)
			}
		}
	}
	return key, nil
}

// NormalizePrefix validates a list prefix. Empty is allowed and means all keys.
func NormalizePrefix(prefix string) (string, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return "", nil
	}
	if _, err := NormalizeKey(strings.TrimSuffix(prefix, "/")); err != nil {
		return "", fmt.Errorf("object prefix: %w", err)
	}
	return prefix, nil
}
