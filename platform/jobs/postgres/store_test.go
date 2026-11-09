package postgres

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/plinth-go/plinth/internal/jsonx"
	"github.com/plinth-go/plinth/platform/jobs"
)

type typedNilJobError struct{}

func (*typedNilJobError) Error() string { panic("typed nil job error was dereferenced") }

const (
	testJobID      = "018f0000-0000-7000-8000-000000000000"
	testLeaseToken = "018f0000-0000-7000-8000-000000000001"
)

func TestStoreEnqueuePreflightsPayloadBeforeDatabase(t *testing.T) {
	type nullablePayload struct {
		Value *string `json:"value"`
	}
	store := NewStore(nil)
	_, err := store.Enqueue(context.Background(), "test.kind", nullablePayload{})
	if !errors.Is(err, jsonx.ErrNull) {
		t.Fatalf("error = %v, want jsonx.ErrNull", err)
	}
	if errors.Is(err, jobs.ErrStoreUnavailable) {
		t.Fatal("invalid payload was hidden by store unavailability")
	}
}

func TestStoreEnqueueRejectsInvalidMaxAttemptsBeforeDatabase(t *testing.T) {
	store := NewStore(nil)
	_, err := store.Enqueue(context.Background(), "test.kind", nil, jobs.WithMaxAttempts(math.MaxInt32+1))
	if err == nil || !strings.Contains(err.Error(), "must not exceed") {
		t.Fatalf("error = %v", err)
	}
	if errors.Is(err, jobs.ErrStoreUnavailable) {
		t.Fatal("invalid max attempts was hidden by store unavailability")
	}
}

func TestStoreRejectsInvalidIdentifiersBeforeDatabase(t *testing.T) {
	ctx := context.Background()
	store := NewStore(nil)
	if _, err := store.Get(ctx, "not-a-uuid"); !errors.Is(err, jobs.ErrInvalidJobID) {
		t.Fatalf("Get error = %v, want ErrInvalidJobID", err)
	}
	if _, err := store.Claim(ctx, "   "); err == nil || !strings.Contains(err.Error(), "worker ID is required") {
		t.Fatalf("Claim error = %v", err)
	}

	invalidLeases := []jobs.Lease{
		{JobID: "not-a-uuid", WorkerID: "worker-1", Attempt: 1},
		{JobID: testJobID, WorkerID: "   ", Attempt: 1},
		{JobID: testJobID, WorkerID: "worker-1", Attempt: 0},
		{JobID: testJobID, WorkerID: "worker-1", Attempt: 1},
		{JobID: testJobID, WorkerID: "worker-1", Attempt: 1, Token: "not-a-uuid"},
	}
	for _, lease := range invalidLeases {
		if err := store.MarkSucceeded(ctx, lease); !errors.Is(err, jobs.ErrInvalidLease) {
			t.Fatalf("MarkSucceeded(%#v) error = %v, want ErrInvalidLease", lease, err)
		}
		if err := store.MarkFailed(ctx, lease, errors.New("boom"), time.Now()); !errors.Is(err, jobs.ErrInvalidLease) {
			t.Fatalf("MarkFailed(%#v) error = %v, want ErrInvalidLease", lease, err)
		}
		if err := store.MarkInterrupted(ctx, lease, context.Canceled); !errors.Is(err, jobs.ErrInvalidLease) {
			t.Fatalf("MarkInterrupted(%#v) error = %v, want ErrInvalidLease", lease, err)
		}
	}

	validLease := jobs.Lease{JobID: testJobID, WorkerID: "worker-1", Attempt: 1, Token: testLeaseToken}
	if err := store.MarkFailed(ctx, validLease, errors.New("boom"), time.Time{}); err == nil || !strings.Contains(err.Error(), "retry time") {
		t.Fatalf("zero retry time error = %v", err)
	}
}

func TestStorePreflightsScheduleNamesBeforeDatabase(t *testing.T) {
	store := NewStore(nil)
	err := store.DisableSchedulesExcept(context.Background(), []string{" "})
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("error = %v", err)
	}
	if errors.Is(err, jobs.ErrStoreUnavailable) {
		t.Fatal("invalid schedule names were hidden by store unavailability")
	}
}

func TestStoreRecoverStaleRejectsNonPositiveAge(t *testing.T) {
	for _, store := range []*Store{nil, {}} {
		for _, staleAfter := range []time.Duration{0, -time.Second} {
			if err := store.RecoverStale(context.Background(), staleAfter); err == nil {
				t.Fatalf("stale duration %s was accepted", staleAfter)
			}
		}
	}
}

func TestStoreRejectsUnavailablePool(t *testing.T) {
	ctx := context.Background()
	lease := jobs.Lease{JobID: testJobID, WorkerID: "worker-1", Attempt: 1, Token: testLeaseToken}
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
			return store.UpsertSchedule(ctx, jobs.Schedule{
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
				if err := tc.call(store); !errors.Is(err, jobs.ErrStoreUnavailable) {
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
		if errors.Is(err, jobs.ErrStoreUnavailable) {
			t.Fatalf("ListFailed(%d, %d) reached store availability check", test.limit, test.offset)
		}
	}
}

func TestStoreTerminalCausesAcceptTypedNilErrors(t *testing.T) {
	var cause *typedNilJobError
	lease := jobs.Lease{JobID: testJobID, WorkerID: "worker-1", Attempt: 1, Token: testLeaseToken}
	store := &Store{}
	if err := store.MarkFailed(context.Background(), lease, cause, time.Now()); !errors.Is(err, jobs.ErrStoreUnavailable) {
		t.Fatalf("MarkFailed error = %v", err)
	}
	if err := store.MarkInterrupted(context.Background(), lease, cause); !errors.Is(err, jobs.ErrStoreUnavailable) {
		t.Fatalf("MarkInterrupted error = %v", err)
	}
}

func TestStoreUpsertScheduleValidatesBeforeDatabase(t *testing.T) {
	store := NewStore(nil)
	err := store.UpsertSchedule(context.Background(), jobs.Schedule{
		Name:     "test",
		Kind:     "test.kind",
		Interval: 1500 * time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "whole number of seconds") {
		t.Fatalf("error = %v", err)
	}
}

func TestStoreReconcileSchedulesPreflightsBeforeDatabase(t *testing.T) {
	store := NewStore(nil)
	err := store.ReconcileSchedules(context.Background(), []jobs.Schedule{
		{Name: "valid", Kind: "test.valid", Payload: map[string]string{"ok": "yes"}, Interval: time.Minute},
		{Name: "invalid", Kind: "test.invalid", Payload: make(chan int), Interval: time.Minute},
	})
	if err == nil || !strings.Contains(err.Error(), "marshal schedule payload") {
		t.Fatalf("error = %v", err)
	}
}

func TestStoreRejectsInvalidEnqueueAndDispatchLimits(t *testing.T) {
	store := NewStore(nil)
	if _, err := store.Enqueue(context.Background(), "test.kind", nil, jobs.WithMaxAttempts(0)); err == nil || !strings.Contains(err.Error(), "at least 1") {
		t.Fatalf("enqueue error = %v", err)
	}
	if _, err := store.Enqueue(context.Background(), "test.kind", nil, nil); err == nil || !strings.Contains(err.Error(), "option at index 0 is nil") {
		t.Fatalf("nil option error = %v", err)
	}
	if _, err := store.Enqueue(context.Background(), "   ", nil); err == nil || !strings.Contains(err.Error(), "kind is empty") {
		t.Fatalf("blank kind error = %v", err)
	}
	if _, err := store.DispatchDueSchedules(context.Background(), 0); err == nil || !strings.Contains(err.Error(), "at least 1") {
		t.Fatalf("dispatch error = %v", err)
	}
}

func TestStoreReconcileSchedulesRejectsDuplicateNamesBeforeDatabase(t *testing.T) {
	store := NewStore(nil)
	err := store.ReconcileSchedules(context.Background(), []jobs.Schedule{
		{Name: "duplicate", Kind: "test.first", Interval: time.Minute},
		{Name: "  duplicate  ", Kind: " test.second ", Interval: time.Minute},
	})
	if err == nil || !strings.Contains(err.Error(), "declared more than once") {
		t.Fatalf("error = %v", err)
	}
}

func TestStoreRejectsPostgresIntervalOverflow(t *testing.T) {
	_, err := normalizeSchedule(jobs.Schedule{
		Name: "test", Kind: "test.kind", Interval: time.Duration(math.MaxInt32+1) * time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "Postgres integer-seconds limit") {
		t.Fatalf("error = %v", err)
	}
}
