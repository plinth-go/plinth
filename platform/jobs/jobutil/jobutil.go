// Package jobutil contains backend-neutral validation, normalization, and
// payload encoding shared by the jobs package and database adapters.
package jobutil

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	uuid "github.com/google/uuid"
	"github.com/plinth-go/plinth/internal/jsonx"
)

var (
	ErrNotFound         = errors.New("job not found")
	ErrNotFailed        = errors.New("job is not in the failed state")
	ErrInvalidJobID     = errors.New("job ID is invalid")
	ErrInvalidLease     = errors.New("job lease is invalid")
	ErrLeaseLost        = errors.New("job lease lost")
	ErrStoreUnavailable = errors.New("job store is unavailable")
)

const MaxIdentifierBytes = 255

const MaxErrorBytes = 4096

const TruncatedErrorSuffix = "\n[truncated]"

// Lease is the normalized representation of a claimed job attempt. The jobs
// package converts it to its public Lease type at the adapter boundary.
type Lease struct {
	JobID    string
	WorkerID string
	Attempt  int
	Token    string
}

func ValidateIdentifier(value, label string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", label)
	}
	if len(value) > MaxIdentifierBytes {
		return fmt.Errorf("%s must not exceed %d bytes", label, MaxIdentifierBytes)
	}
	for _, char := range value {
		if !unicode.IsPrint(char) {
			return fmt.Errorf("%s must contain only printable characters", label)
		}
	}
	return nil
}

func NormalizeJobKind(kind string) (string, error) {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return "", fmt.Errorf("job kind is empty")
	}
	if err := ValidateIdentifier(kind, "job kind"); err != nil {
		return "", err
	}
	return kind, nil
}

func NormalizeJobID(id string) (string, error) {
	id = strings.TrimSpace(id)
	parsed, err := uuid.Parse(id)
	if err != nil {
		return "", fmt.Errorf("%w: %q", ErrInvalidJobID, id)
	}
	return parsed.String(), nil
}

func NormalizeWorkerID(workerID string) (string, error) {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" {
		return "", fmt.Errorf("job worker ID is required")
	}
	if err := ValidateIdentifier(workerID, "job worker ID"); err != nil {
		return "", err
	}
	return workerID, nil
}

func NormalizeLease(jobID, workerID string, attempt int, token string) (Lease, error) {
	normalizedID, err := NormalizeJobID(jobID)
	if err != nil {
		return Lease{}, fmt.Errorf("%w: %w", ErrInvalidLease, err)
	}
	normalizedWorker, err := NormalizeWorkerID(workerID)
	if err != nil {
		return Lease{}, fmt.Errorf("%w: %w", ErrInvalidLease, err)
	}
	if attempt < 1 {
		return Lease{}, fmt.Errorf("%w: attempt must be at least 1", ErrInvalidLease)
	}
	parsedToken, err := uuid.Parse(strings.TrimSpace(token))
	if err != nil {
		return Lease{}, fmt.Errorf("%w: lease token is invalid", ErrInvalidLease)
	}
	return Lease{
		JobID:    normalizedID,
		WorkerID: normalizedWorker,
		Attempt:  attempt,
		Token:    parsedToken.String(),
	}, nil
}

func NormalizeSchedule(name, kind string, interval time.Duration) (string, string, error) {
	name = strings.TrimSpace(name)
	kind = strings.TrimSpace(kind)
	if name == "" || kind == "" {
		return "", "", fmt.Errorf("schedule name and kind are required")
	}
	if err := ValidateIdentifier(name, "schedule name"); err != nil {
		return "", "", err
	}
	if err := ValidateIdentifier(kind, "schedule kind"); err != nil {
		return "", "", err
	}
	if interval < time.Second {
		return "", "", fmt.Errorf("schedule %q interval must be at least one second", name)
	}
	if interval%time.Second != 0 {
		return "", "", fmt.Errorf("schedule %q interval must be a whole number of seconds", name)
	}
	return name, kind, nil
}

func NormalizeScheduleNames(names []string) ([]string, error) {
	normalized := make([]string, 0, len(names))
	seen := make(map[string]bool, len(names))
	for index, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("schedule name at index %d is empty", index)
		}
		if err := ValidateIdentifier(name, fmt.Sprintf("schedule name at index %d", index)); err != nil {
			return nil, err
		}
		if seen[name] {
			return nil, fmt.Errorf("schedule name %q is repeated at index %d", name, index)
		}
		seen[name] = true
		normalized = append(normalized, name)
	}
	return normalized, nil
}

// MarshalPayload encodes a job or schedule payload. A nil top-level payload
// means the empty object, matching the table defaults and the strict decoder
// used by typed handlers. Nested nulls and ambiguous duplicate names are
// rejected before persistence because typed handlers could never decode them.
func MarshalPayload(payload any) ([]byte, error) {
	if payload == nil || isNilInterface(payload) {
		return []byte("{}"), nil
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(bytes.TrimSpace(body), []byte("null")) {
		return []byte("{}"), nil
	}
	var value json.RawMessage
	if err := jsonx.DecodeOne(bytes.NewReader(body), &value); err != nil {
		return nil, fmt.Errorf("validate job payload JSON: %w", err)
	}
	return body, nil
}

// ErrorMessage returns a deterministic UTF-8-safe message capped at
// MaxErrorBytes. Typed-nil errors produce an empty message.
func ErrorMessage(err error) string {
	if isNilInterface(err) {
		return ""
	}
	return TruncateErrorMessage(err.Error())
}

func TruncateErrorMessage(message string) string {
	tooLong := len(message) > MaxErrorBytes
	if tooLong {
		limit := MaxErrorBytes + utf8.UTFMax
		if limit > len(message) {
			limit = len(message)
		}
		message = message[:limit]
	}
	message = strings.ToValidUTF8(message, "\uFFFD")
	if !tooLong && len(message) <= MaxErrorBytes {
		return message
	}
	limit := MaxErrorBytes - len(TruncatedErrorSuffix)
	if len(message) <= limit {
		return message + TruncatedErrorSuffix
	}
	for limit > 0 && !utf8.RuneStart(message[limit]) {
		limit--
	}
	return message[:limit] + TruncatedErrorSuffix
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
