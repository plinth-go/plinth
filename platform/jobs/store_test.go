package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/plinth-go/plinth/internal/jsonx"
)

type typedNilJobError struct{}

func (*typedNilJobError) Error() string { panic("typed nil job error was dereferenced") }

type duplicateKeyPayload struct{}

const (
	testJobID      = "018f0000-0000-7000-8000-000000000000"
	testLeaseToken = "018f0000-0000-7000-8000-000000000001"
)

func (duplicateKeyPayload) MarshalJSON() ([]byte, error) {
	return []byte(`{"name":"first","name":"second"}`), nil
}

func TestMarshalPayloadNormalizesNullToEmptyObject(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}
	var typedNil *payload
	var nilMap map[string]string

	tests := []struct {
		name    string
		payload any
		want    string
	}{
		{name: "nil interface", payload: nil, want: `{}`},
		{name: "typed nil pointer", payload: typedNil, want: `{}`},
		{name: "nil map", payload: nilMap, want: `{}`},
		{name: "raw null", payload: json.RawMessage("null"), want: `{}`},
		{name: "whitespace raw null", payload: json.RawMessage("  null  "), want: `{}`},
		{name: "value", payload: payload{Name: "work"}, want: `{"name":"work"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := marshalPayload(tt.payload)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Fatalf("payload = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestMarshalPayloadRejectsUnsupportedValues(t *testing.T) {
	if _, err := marshalPayload(make(chan int)); err == nil {
		t.Fatal("unsupported payload was accepted")
	}
}

func TestMarshalPayloadRejectsValuesTypedHandlersCannotDecode(t *testing.T) {
	type nullablePayload struct {
		Value *string `json:"value"`
	}
	if _, err := marshalPayload(nullablePayload{}); !errors.Is(err, jsonx.ErrNull) {
		t.Fatalf("nested null error = %v", err)
	}
	if _, err := marshalPayload(duplicateKeyPayload{}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate member error = %v", err)
	}
}

func TestErrorMessageIsBoundedAndUTF8Safe(t *testing.T) {
	if got := errorMessage(errors.New("boom")); got != "boom" {
		t.Fatalf("short message = %q", got)
	}
	if got := errorMessage(errors.New(string([]byte{0xff, 0xfe, 'x'}))); !utf8.ValidString(got) {
		t.Fatalf("short invalid UTF-8 message = %q", got)
	}
	message := strings.Repeat("错", maxJobErrorBytes)
	got := errorMessage(errors.New(message))
	if len(got) > maxJobErrorBytes {
		t.Fatalf("truncated message has %d bytes, want at most %d", len(got), maxJobErrorBytes)
	}
	if !strings.HasSuffix(got, truncatedJobErrorSuffix) {
		t.Fatalf("truncated message = %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatal("truncated message is not valid UTF-8")
	}
	invalid := strings.Repeat("\xff", maxJobErrorBytes*2)
	got = errorMessage(errors.New(invalid))
	if len(got) > maxJobErrorBytes || !utf8.ValidString(got) || !strings.HasSuffix(got, truncatedJobErrorSuffix) {
		t.Fatalf("invalid UTF-8 message = %q", got)
	}
}

func TestStoreEnqueuePreflightsPayloadBeforeDatabase(t *testing.T) {
	type nullablePayload struct {
		Value *string `json:"value"`
	}
	store := NewStore(nil)
	_, err := store.Enqueue(context.Background(), "test.kind", nullablePayload{})
	if !errors.Is(err, jsonx.ErrNull) {
		t.Fatalf("error = %v, want jsonx.ErrNull", err)
	}
	if errors.Is(err, ErrStoreUnavailable) {
		t.Fatal("invalid payload was hidden by store unavailability")
	}
}

func TestStoreEnqueueRejectsMaxAttemptsOutsidePostgresIntegerRange(t *testing.T) {
	store := NewStore(nil)
	_, err := store.Enqueue(context.Background(), "test.kind", nil, WithMaxAttempts(math.MaxInt32+1))
	if err == nil || !strings.Contains(err.Error(), "must not exceed") {
		t.Fatalf("error = %v", err)
	}
	if errors.Is(err, ErrStoreUnavailable) {
		t.Fatal("invalid max attempts was hidden by store unavailability")
	}
}

func TestStoreRejectsInvalidIdentifiersBeforeDatabase(t *testing.T) {
	ctx := context.Background()
	store := NewStore(nil)
	if _, err := store.Get(ctx, "not-a-uuid"); !errors.Is(err, ErrInvalidJobID) {
		t.Fatalf("Get error = %v, want ErrInvalidJobID", err)
	}
	if _, err := store.Claim(ctx, "   "); err == nil || !strings.Contains(err.Error(), "worker ID is required") {
		t.Fatalf("Claim error = %v", err)
	}

	invalidLeases := []Lease{
		{JobID: "not-a-uuid", WorkerID: "worker-1", Attempt: 1},
		{JobID: testJobID, WorkerID: "   ", Attempt: 1},
		{JobID: testJobID, WorkerID: "worker-1", Attempt: 0},
		{JobID: testJobID, WorkerID: "worker-1", Attempt: 1},
		{JobID: testJobID, WorkerID: "worker-1", Attempt: 1, Token: "not-a-uuid"},
	}
	for _, lease := range invalidLeases {
		if err := store.MarkSucceeded(ctx, lease); !errors.Is(err, ErrInvalidLease) {
			t.Fatalf("MarkSucceeded(%#v) error = %v, want ErrInvalidLease", lease, err)
		}
		if err := store.MarkFailed(ctx, lease, errors.New("boom"), time.Now()); !errors.Is(err, ErrInvalidLease) {
			t.Fatalf("MarkFailed(%#v) error = %v, want ErrInvalidLease", lease, err)
		}
		if err := store.MarkInterrupted(ctx, lease, context.Canceled); !errors.Is(err, ErrInvalidLease) {
			t.Fatalf("MarkInterrupted(%#v) error = %v, want ErrInvalidLease", lease, err)
		}
	}

	validLease := Lease{JobID: testJobID, WorkerID: "worker-1", Attempt: 1, Token: testLeaseToken}
	if err := store.MarkFailed(ctx, validLease, errors.New("boom"), time.Time{}); err == nil || !strings.Contains(err.Error(), "retry time") {
		t.Fatalf("zero retry time error = %v", err)
	}
}

func TestStoreCanonicalizesIdentifiers(t *testing.T) {
	id, err := normalizeJobID("  {F81D4FAE-7DEC-11D0-A765-00A0C91E6BF6}  ")
	if err != nil {
		t.Fatal(err)
	}
	if id != "f81d4fae-7dec-11d0-a765-00a0c91e6bf6" {
		t.Fatalf("job ID = %q", id)
	}
	workerID, err := normalizeWorkerID("  worker-1  ")
	if err != nil {
		t.Fatal(err)
	}
	if workerID != "worker-1" {
		t.Fatalf("worker ID = %q", workerID)
	}
	lease, err := normalizeLease(Lease{
		JobID: " " + id + " ", WorkerID: " " + workerID + " ", Attempt: 2,
		Token: " {" + testLeaseToken + "} ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if lease.JobID != id || lease.WorkerID != workerID || lease.Attempt != 2 || lease.Token != testLeaseToken {
		t.Fatalf("lease = %#v", lease)
	}
}

func TestNormalizeScheduleNamesCanonicalizesAndRejectsAmbiguity(t *testing.T) {
	names, err := normalizeScheduleNames([]string{" first ", "second"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, ",") != "first,second" {
		t.Fatalf("names = %#v", names)
	}
	if _, err := normalizeScheduleNames([]string{"first", " first "}); err == nil || !strings.Contains(err.Error(), "repeated") {
		t.Fatalf("duplicate error = %v", err)
	}
	if _, err := normalizeScheduleNames([]string{"  "}); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty error = %v", err)
	}
}

func TestIdentifiersRejectInvalidUTF8ControlAndOversize(t *testing.T) {
	invalidUTF8 := string([]byte{0xff})
	tooLong := strings.Repeat("x", maxIdentifierBytes+1)
	tests := []struct {
		name string
		call func() error
		want string
	}{
		{
			name: "job kind invalid UTF-8",
			call: func() error { _, err := normalizeJobKind(invalidUTF8); return err },
			want: "valid UTF-8",
		},
		{
			name: "job kind control character",
			call: func() error { _, err := normalizeJobKind("task\nkind"); return err },
			want: "printable",
		},
		{
			name: "job kind too long",
			call: func() error { _, err := normalizeJobKind(tooLong); return err },
			want: "must not exceed",
		},
		{
			name: "worker ID invalid UTF-8",
			call: func() error { _, err := normalizeWorkerID(invalidUTF8); return err },
			want: "valid UTF-8",
		},
		{
			name: "schedule name control character",
			call: func() error {
				return validateIdentifier("daily\nreport", "schedule name")
			},
			want: "printable",
		},
		{
			name: "schedule kind too long",
			call: func() error {
				_, err := normalizeSchedule(Schedule{Name: "valid", Kind: tooLong, Interval: time.Minute})
				return err
			},
			want: "must not exceed",
		},
		{
			name: "disabled schedule name too long",
			call: func() error { _, err := normalizeScheduleNames([]string{tooLong}); return err },
			want: "must not exceed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestStorePreflightsScheduleNamesBeforeDatabase(t *testing.T) {
	store := NewStore(nil)
	err := store.DisableSchedulesExcept(context.Background(), []string{" "})
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("error = %v", err)
	}
	if errors.Is(err, ErrStoreUnavailable) {
		t.Fatal("invalid schedule names were hidden by store unavailability")
	}
}

func TestStoreRecoverStaleRejectsNonPositiveAge(t *testing.T) {
	store := &Store{}
	for _, staleAfter := range []time.Duration{0, -time.Second} {
		if err := store.RecoverStale(context.Background(), staleAfter); err == nil {
			t.Fatalf("stale duration %s was accepted", staleAfter)
		}
	}
}

func TestStoreRejectsUnavailablePool(t *testing.T) {
	ctx := context.Background()
	lease := Lease{JobID: testJobID, WorkerID: "worker-1", Attempt: 1, Token: testLeaseToken}
	checks := []struct {
		name string
		call func(*Store) error
	}{
		{name: "enqueue", call: func(store *Store) error {
			_, err := store.Enqueue(ctx, "test.kind", struct{}{})
			return err
		}},
		{name: "get", call: func(store *Store) error {
			_, err := store.Get(ctx, testJobID)
			return err
		}},
		{name: "list failed", call: func(store *Store) error {
			_, err := store.ListFailed(ctx, 10, 0)
			return err
		}},
		{name: "retry failed", call: func(store *Store) error {
			_, err := store.RetryFailed(ctx, testJobID)
			return err
		}},
		{name: "delete failed", call: func(store *Store) error {
			return store.DeleteFailed(ctx, testJobID)
		}},
		{name: "claim", call: func(store *Store) error {
			_, err := store.Claim(ctx, "worker-1")
			return err
		}},
		{name: "mark succeeded", call: func(store *Store) error {
			return store.MarkSucceeded(ctx, lease)
		}},
		{name: "mark failed", call: func(store *Store) error {
			return store.MarkFailed(ctx, lease, errors.New("boom"), time.Now())
		}},
		{name: "mark interrupted", call: func(store *Store) error {
			return store.MarkInterrupted(ctx, lease, context.Canceled)
		}},
		{name: "recover stale", call: func(store *Store) error {
			return store.RecoverStale(ctx, time.Minute)
		}},
		{name: "dispatch due schedules", call: func(store *Store) error {
			_, err := store.DispatchDueSchedules(ctx, 1)
			return err
		}},
		{name: "upsert schedule", call: func(store *Store) error {
			return store.UpsertSchedule(ctx, Schedule{
				Name: "test", Kind: "test.kind", Interval: time.Minute,
			})
		}},
		{name: "reconcile schedules", call: func(store *Store) error {
			return store.ReconcileSchedules(ctx, nil)
		}},
		{name: "disable schedules", call: func(store *Store) error {
			return store.DisableSchedulesExcept(ctx, nil)
		}},
		{name: "ping", call: func(store *Store) error {
			return store.Ping(ctx)
		}},
	}

	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			for _, store := range []*Store{nil, {}} {
				if err := tc.call(store); !errors.Is(err, ErrStoreUnavailable) {
					t.Fatalf("error = %v, want ErrStoreUnavailable", err)
				}
			}
		})
	}
}

func TestListFailedRejectsUnboundedPagination(t *testing.T) {
	store := NewStore(nil)
	for _, test := range []struct {
		limit  int
		offset int
		want   string
	}{
		{limit: 0, want: "limit"},
		{limit: 101, want: "limit"},
		{limit: 10, offset: -1, want: "offset"},
	} {
		_, err := store.ListFailed(context.Background(), test.limit, test.offset)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("ListFailed(%d, %d) error = %v", test.limit, test.offset, err)
		}
		if errors.Is(err, ErrStoreUnavailable) {
			t.Fatalf("ListFailed(%d, %d) reached store availability check", test.limit, test.offset)
		}
	}
}

func TestStoreTerminalCausesAcceptTypedNilErrors(t *testing.T) {
	var cause *typedNilJobError
	lease := Lease{JobID: testJobID, WorkerID: "worker-1", Attempt: 1, Token: testLeaseToken}
	store := &Store{}
	if err := store.MarkFailed(context.Background(), lease, cause, time.Now()); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("MarkFailed error = %v", err)
	}
	if err := store.MarkInterrupted(context.Background(), lease, cause); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("MarkInterrupted error = %v", err)
	}
}
