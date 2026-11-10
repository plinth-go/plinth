package pgjobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/plinth-go/plinth/platform/jobs"
	"github.com/plinth-go/plinth/platform/jobs/jobutil"
	"github.com/plinth-go/plinth/platform/jobs/migrations"
	"github.com/plinth-go/plinth/platform/migrate"
	"github.com/plinth-go/plinth/platform/pgdb"
	"github.com/plinth-go/plinth/platform/pgmigrate"
)

func TestIntegrationJobLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store := newIntegrationStore(t)

	job, err := store.Enqueue(ctx, "test.echo", map[string]string{"message": "hello"}, jobs.WithMaxAttempts(2))
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "pending" || job.Attempts != 0 || job.MaxAttempts != 2 {
		t.Fatalf("enqueued job = %#v", job)
	}

	claimed, err := store.Claim(ctx, "worker-1")
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.ID != job.ID || claimed.Status != "running" || claimed.Attempts != 1 {
		t.Fatalf("claimed job = %#v", claimed)
	}
	if next, err := store.Claim(ctx, "worker-2"); err != nil {
		t.Fatal(err)
	} else if next != nil {
		t.Fatalf("unexpected second claim: %#v", next)
	}

	if err := store.MarkSucceeded(ctx, claimed.Lease("worker-1")); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "succeeded" || got.LastError != nil {
		t.Fatalf("succeeded job = %#v", got)
	}
}

func TestIntegrationJobRetryInterruptAndStaleRecovery(t *testing.T) {
	t.Run("retry", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		store := newIntegrationStore(t)

		retryJob, err := store.Enqueue(ctx, "test.retry", map[string]string{"message": "hello"}, jobs.WithMaxAttempts(2))
		if err != nil {
			t.Fatal(err)
		}
		first, err := store.Claim(ctx, "worker-retry")
		if err != nil {
			t.Fatal(err)
		}
		if first == nil || first.ID != retryJob.ID {
			t.Fatalf("first claim = %#v, want %s", first, retryJob.ID)
		}
		if err := store.MarkFailed(ctx, first.Lease("worker-retry"), errors.New("boom"), time.Now()); err != nil {
			t.Fatal(err)
		}
		got, err := store.Get(ctx, retryJob.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != "pending" || got.LastError == nil || *got.LastError != "boom" {
			t.Fatalf("pending retry = %#v", got)
		}
		second, err := store.Claim(ctx, "worker-retry")
		if err != nil {
			t.Fatal(err)
		}
		if second == nil || second.ID != retryJob.ID || second.Attempts != 2 {
			t.Fatalf("second claim = %#v", second)
		}
		if err := store.MarkFailed(ctx, second.Lease("worker-retry"), errors.New("boom again"), time.Now()); err != nil {
			t.Fatal(err)
		}
		got, err = store.Get(ctx, retryJob.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != "failed" || got.LastError == nil || *got.LastError != "boom again" {
			t.Fatalf("failed retry = %#v", got)
		}
	})

	t.Run("interrupt", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		store := newIntegrationStore(t)

		interrupted, err := store.Enqueue(ctx, "test.interrupt", nil)
		if err != nil {
			t.Fatal(err)
		}
		claimed, err := store.Claim(ctx, "worker-interrupt")
		if err != nil {
			t.Fatal(err)
		}
		if claimed == nil || claimed.ID != interrupted.ID {
			t.Fatalf("interrupt claim = %#v", claimed)
		}
		if err := store.MarkInterrupted(ctx, claimed.Lease("worker-interrupt"), context.Canceled); err != nil {
			t.Fatal(err)
		}
		got, err := store.Get(ctx, interrupted.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != "pending" || got.Attempts != 0 {
			t.Fatalf("interrupted job = %#v", got)
		}
	})

	t.Run("stale recovery", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		store := newIntegrationStore(t)

		stale, err := store.Enqueue(ctx, "test.stale", nil)
		if err != nil {
			t.Fatal(err)
		}
		claimed, err := store.Claim(ctx, "worker-stale")
		if err != nil {
			t.Fatal(err)
		}
		if claimed == nil || claimed.ID != stale.ID {
			t.Fatalf("stale claim = %#v", claimed)
		}
		if _, err := store.pool.Exec(ctx, `UPDATE plinth_jobs SET locked_at = now() - interval '5 minutes' WHERE id = $1`, stale.ID); err != nil {
			t.Fatal(err)
		}
		if err := store.RecoverStale(ctx, time.Minute); err != nil {
			t.Fatal(err)
		}
		got, err := store.Get(ctx, stale.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != "pending" || got.LastError == nil || !strings.Contains(*got.LastError, "worker restarted") {
			t.Fatalf("recovered stale job = %#v", got)
		}
	})

	t.Run("stale recovery exhausts attempts", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		store := newIntegrationStore(t)

		stale, err := store.Enqueue(ctx, "test.stale.exhausted", nil, jobs.WithMaxAttempts(1))
		if err != nil {
			t.Fatal(err)
		}
		claimed, err := store.Claim(ctx, "worker-stale-exhausted")
		if err != nil {
			t.Fatal(err)
		}
		if claimed == nil || claimed.ID != stale.ID || claimed.Attempts != 1 {
			t.Fatalf("stale claim = %#v", claimed)
		}
		if _, err := store.pool.Exec(ctx, `UPDATE plinth_jobs SET locked_at = now() - interval '5 minutes' WHERE id = $1`, stale.ID); err != nil {
			t.Fatal(err)
		}
		if err := store.RecoverStale(ctx, time.Minute); err != nil {
			t.Fatal(err)
		}
		got, err := store.Get(ctx, stale.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != "failed" || got.Attempts != 1 || got.LastError == nil || !strings.Contains(*got.LastError, "worker restarted") {
			t.Fatalf("exhausted stale job = %#v", got)
		}
		next, err := store.Claim(ctx, "worker-after-exhaustion")
		if err != nil {
			t.Fatal(err)
		}
		if next != nil {
			t.Fatalf("exhausted job was claimed again: %#v", next)
		}
	})

	t.Run("lease fencing", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		store := newIntegrationStore(t)

		job, err := store.Enqueue(ctx, "test.fencing", nil)
		if err != nil {
			t.Fatal(err)
		}
		old, err := store.Claim(ctx, "worker-old")
		if err != nil {
			t.Fatal(err)
		}
		if old == nil || old.ID != job.ID {
			t.Fatalf("old claim = %#v", old)
		}
		if _, err := store.pool.Exec(ctx, `UPDATE plinth_jobs SET locked_at = now() - interval '5 minutes' WHERE id = $1`, job.ID); err != nil {
			t.Fatal(err)
		}
		if err := store.RecoverStale(ctx, time.Minute); err != nil {
			t.Fatal(err)
		}
		current, err := store.Claim(ctx, "worker-current")
		if err != nil {
			t.Fatal(err)
		}
		if current == nil || current.ID != job.ID || current.Attempts != 2 {
			t.Fatalf("current claim = %#v", current)
		}

		oldLease := old.Lease("worker-old")
		if err := store.MarkSucceeded(ctx, oldLease); !errors.Is(err, jobs.ErrLeaseLost) {
			t.Fatalf("old success error = %v", err)
		}
		if err := store.MarkFailed(ctx, oldLease, errors.New("late failure"), time.Now()); !errors.Is(err, jobs.ErrLeaseLost) {
			t.Fatalf("old failure error = %v", err)
		}
		if err := store.MarkInterrupted(ctx, oldLease, context.Canceled); !errors.Is(err, jobs.ErrLeaseLost) {
			t.Fatalf("old interrupt error = %v", err)
		}
		if err := store.MarkSucceeded(ctx, current.Lease("worker-current")); err != nil {
			t.Fatal(err)
		}
		got, err := store.Get(ctx, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != "succeeded" || got.Attempts != 2 {
			t.Fatalf("fenced job = %#v", got)
		}
	})
}

func TestIntegrationMarkSucceededReplayIsIdempotent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store := newIntegrationStore(t)

	job, err := store.Enqueue(ctx, "test.echo", map[string]string{"message": "hello"}, jobs.WithMaxAttempts(2))
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.Claim(ctx, "worker-1")
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.ID != job.ID {
		t.Fatalf("claimed job = %#v", claimed)
	}
	lease := claimed.Lease("worker-1")
	if err := store.MarkSucceeded(ctx, lease); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkSucceeded(ctx, lease); err != nil {
		t.Fatalf("replayed success acknowledgement = %v", err)
	}

	wrongAttempt := lease
	wrongAttempt.Attempt++
	if err := store.MarkSucceeded(ctx, wrongAttempt); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("wrong-attempt replay error = %v, want jobs.ErrLeaseLost", err)
	}
}

func TestIntegrationLeaseTokenSeparatesReusedAttempts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store := newIntegrationStore(t)

	job, err := store.Enqueue(ctx, "test.lease-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Claim(ctx, "worker-reused")
	if err != nil {
		t.Fatal(err)
	}
	if first == nil || first.ID != job.ID {
		t.Fatalf("first claim = %#v", first)
	}
	firstLease := first.Lease("worker-reused")
	if firstLease.Token == "" {
		t.Fatal("first claim did not issue a lease token")
	}
	if err := store.MarkInterrupted(ctx, firstLease, context.Canceled); err != nil {
		t.Fatal(err)
	}

	second, err := store.Claim(ctx, "worker-reused")
	if err != nil {
		t.Fatal(err)
	}
	if second == nil || second.ID != job.ID {
		t.Fatalf("second claim = %#v", second)
	}
	secondLease := second.Lease("worker-reused")
	if secondLease.Attempt != firstLease.Attempt {
		t.Fatalf("reused attempt = %d, want %d", secondLease.Attempt, firstLease.Attempt)
	}
	if secondLease.Token == firstLease.Token {
		t.Fatal("reused attempt received the previous lease token")
	}

	if err := store.MarkSucceeded(ctx, firstLease); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("stale success error = %v, want jobs.ErrLeaseLost", err)
	}
	if err := store.MarkInterrupted(ctx, firstLease, context.Canceled); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("stale interruption error = %v, want jobs.ErrLeaseLost", err)
	}
	if err := store.MarkSucceeded(ctx, secondLease); err != nil {
		t.Fatalf("current success acknowledgement = %v", err)
	}
}

func TestIntegrationTerminalAcknowledgementsReplayIsIdempotent(t *testing.T) {
	store := newIntegrationStore(t)

	t.Run("failed", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		job, err := store.Enqueue(ctx, "test.failed", nil, jobs.WithMaxAttempts(2))
		if err != nil {
			t.Fatal(err)
		}
		claimed, err := store.Claim(ctx, "worker-failed")
		if err != nil {
			t.Fatal(err)
		}
		lease := claimed.Lease("worker-failed")
		cause := errors.New("boom")
		retryAt := time.Now().Add(time.Second)
		if err := store.MarkFailed(ctx, lease, cause, retryAt); err != nil {
			t.Fatal(err)
		}
		if err := store.MarkFailed(ctx, lease, cause, retryAt); err != nil {
			t.Fatalf("replayed failure acknowledgement = %v", err)
		}
		wrongAttempt := lease
		wrongAttempt.Attempt++
		if err := store.MarkFailed(ctx, wrongAttempt, cause, retryAt); !errors.Is(err, jobs.ErrLeaseLost) {
			t.Fatalf("wrong-attempt failure replay error = %v, want jobs.ErrLeaseLost", err)
		}
		got, err := store.Get(ctx, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != "pending" || got.Attempts != 1 {
			t.Fatalf("job = %#v", got)
		}
	})

	t.Run("interrupted", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		job, err := store.Enqueue(ctx, "test.interrupted", nil)
		if err != nil {
			t.Fatal(err)
		}
		claimed, err := store.Claim(ctx, "worker-interrupted")
		if err != nil {
			t.Fatal(err)
		}
		lease := claimed.Lease("worker-interrupted")
		cause := context.Canceled
		if err := store.MarkInterrupted(ctx, lease, cause); err != nil {
			t.Fatal(err)
		}
		if err := store.MarkInterrupted(ctx, lease, cause); err != nil {
			t.Fatalf("replayed interruption acknowledgement = %v", err)
		}
		wrongAttempt := lease
		wrongAttempt.Attempt++
		if err := store.MarkInterrupted(ctx, wrongAttempt, cause); !errors.Is(err, jobs.ErrLeaseLost) {
			t.Fatalf("wrong-attempt interruption replay error = %v, want jobs.ErrLeaseLost", err)
		}
		got, err := store.Get(ctx, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != "pending" || got.Attempts != 0 {
			t.Fatalf("job = %#v", got)
		}
	})

	t.Run("truncated failure replay", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		store := newIntegrationStore(t)
		job, err := store.Enqueue(ctx, "test.large-failure", nil)
		if err != nil {
			t.Fatal(err)
		}
		claimed, err := store.Claim(ctx, "worker-large-failure")
		if err != nil {
			t.Fatal(err)
		}
		lease := claimed.Lease("worker-large-failure")
		cause := errors.New(strings.Repeat("错", jobutil.MaxErrorBytes))
		retryAt := time.Now().Add(time.Second)
		if err := store.MarkFailed(ctx, lease, cause, retryAt); err != nil {
			t.Fatal(err)
		}
		if err := store.MarkFailed(ctx, lease, cause, retryAt); err != nil {
			t.Fatalf("replayed truncated failure acknowledgement = %v", err)
		}
		got, err := store.Get(ctx, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.LastError == nil || len(*got.LastError) > jobutil.MaxErrorBytes || !strings.HasSuffix(*got.LastError, jobutil.TruncatedErrorSuffix) {
			t.Fatalf("stored last_error = %#v", got.LastError)
		}
	})

	t.Run("invalid UTF-8 failure", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		store := newIntegrationStore(t)
		job, err := store.Enqueue(ctx, "test.invalid-utf8-failure", nil)
		if err != nil {
			t.Fatal(err)
		}
		claimed, err := store.Claim(ctx, "worker-invalid-utf8")
		if err != nil {
			t.Fatal(err)
		}
		lease := claimed.Lease("worker-invalid-utf8")
		cause := errors.New(string([]byte{0xff, 0xfe, 'x'}))
		if err := store.MarkFailed(ctx, lease, cause, time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		got, err := store.Get(ctx, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.LastError == nil || !utf8.ValidString(*got.LastError) {
			t.Fatalf("stored last_error = %#v", got.LastError)
		}
	})
}

func TestIntegrationSchedulesAndWorker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store := newIntegrationStore(t)

	if err := store.UpsertSchedule(ctx, jobs.Schedule{
		Name:     "test-schedule",
		Kind:     "test.schedule",
		Payload:  map[string]int{"limit": 1},
		Interval: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE plinth_schedules SET next_run_at = now() WHERE name = 'test-schedule'`); err != nil {
		t.Fatal(err)
	}
	count, err := store.DispatchDueSchedules(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("dispatched schedules = %d", count)
	}
	if count, err := store.DispatchDueSchedules(ctx, 10); err != nil {
		t.Fatal(err)
	} else if count != 0 {
		t.Fatalf("second dispatch = %d", count)
	}
	if err := store.DisableSchedulesExcept(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE plinth_schedules SET next_run_at = now() WHERE name = 'test-schedule'`); err != nil {
		t.Fatal(err)
	}
	if count, err := store.DispatchDueSchedules(ctx, 10); err != nil {
		t.Fatal(err)
	} else if count != 0 {
		t.Fatalf("disabled dispatch = %d", count)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry := jobs.NewRegistry()
	received := make(chan string, 1)
	if err := jobs.Register(registry, "test.worker", func(_ context.Context, payload struct {
		Message string `json:"message"`
	}) error {
		received <- payload.Message
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	worker := jobs.NewWorker(store, registry, logger, jobs.WorkerConfig{
		Concurrency:  1,
		PollInterval: 10 * time.Millisecond,
		JobTimeout:   time.Second,
		StaleAfter:   time.Minute,
		RetryBase:    10 * time.Millisecond,
		RetryMax:     100 * time.Millisecond,
	})
	if err := worker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = worker.Stop(stopCtx)
	})

	job, err := store.Enqueue(ctx, "test.worker", map[string]string{"message": "work"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-received:
		if message != "work" {
			t.Fatalf("worker message = %q", message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not run job")
	}
	waitForJobStatus(ctx, t, store, job.ID, "succeeded")
}

func TestIntegrationNilPayloadRunsTypedZeroValueHandler(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store := newIntegrationStore(t)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry := jobs.NewRegistry()
	received := make(chan struct{}, 1)
	if err := jobs.Register(registry, "test.empty", func(context.Context, struct{}) error {
		received <- struct{}{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	worker := jobs.NewWorker(store, registry, logger, jobs.WorkerConfig{
		Concurrency:  1,
		PollInterval: 10 * time.Millisecond,
		JobTimeout:   time.Second,
		StaleAfter:   time.Minute,
		RetryBase:    10 * time.Millisecond,
		RetryMax:     100 * time.Millisecond,
	})
	if err := worker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = worker.Stop(stopCtx)
	})

	job, err := store.Enqueue(ctx, "test.empty", nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(job.Payload) != "{}" {
		t.Fatalf("payload = %s, want {}", job.Payload)
	}
	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not run empty-payload job")
	}
	waitForJobStatus(ctx, t, store, job.ID, "succeeded")
}

func TestIntegrationConcurrentScheduleDispatchIsExactlyOncePerTick(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store := newIntegrationStore(t)

	if err := store.UpsertSchedule(ctx, jobs.Schedule{
		Name:     "test-concurrent-schedule",
		Kind:     "test.concurrent-schedule",
		Interval: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}

	const (
		rounds      = 4
		dispatchers = 8
	)
	for round := 0; round < rounds; round++ {
		if _, err := store.pool.Exec(ctx, `UPDATE plinth_schedules SET next_run_at = now() WHERE name = 'test-concurrent-schedule'`); err != nil {
			t.Fatal(err)
		}

		start := make(chan struct{})
		results := make(chan int, dispatchers)
		errs := make(chan error, dispatchers)
		var wg sync.WaitGroup
		for i := 0; i < dispatchers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				count, err := store.DispatchDueSchedules(ctx, 10)
				results <- count
				errs <- err
			}()
		}
		close(start)
		wg.Wait()
		close(results)
		close(errs)

		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		dispatched := 0
		for count := range results {
			dispatched += count
		}
		if dispatched != 1 {
			t.Fatalf("round %d dispatched %d jobs, want 1", round, dispatched)
		}
	}

	var jobs int
	if err := store.pool.QueryRow(ctx, `
		SELECT count(*) FROM plinth_jobs WHERE kind = 'test.concurrent-schedule'
	`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != rounds {
		t.Fatalf("scheduled jobs = %d, want %d", jobs, rounds)
	}
}

func TestIntegrationReconcileSchedules(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store := newIntegrationStore(t)

	first := jobs.Schedule{Name: "first", Kind: "test.first", Payload: map[string]int{"limit": 1}, Interval: time.Minute}
	second := jobs.Schedule{Name: "second", Kind: "test.second", Payload: map[string]int{"limit": 2}, Interval: 2 * time.Minute}
	if err := store.ReconcileSchedules(ctx, []jobs.Schedule{first, second}); err != nil {
		t.Fatal(err)
	}
	var enabled int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM plinth_schedules WHERE enabled`).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled != 2 {
		t.Fatalf("enabled schedules = %d, want 2", enabled)
	}

	first.Interval = 3 * time.Minute
	if err := store.ReconcileSchedules(ctx, []jobs.Schedule{first}); err != nil {
		t.Fatal(err)
	}
	var firstEnabled, secondEnabled bool
	var firstInterval int
	if err := store.pool.QueryRow(ctx, `SELECT enabled, interval_seconds FROM plinth_schedules WHERE name = 'first'`).Scan(&firstEnabled, &firstInterval); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT enabled FROM plinth_schedules WHERE name = 'second'`).Scan(&secondEnabled); err != nil {
		t.Fatal(err)
	}
	if !firstEnabled || firstInterval != 180 {
		t.Fatalf("first schedule enabled=%v interval=%d", firstEnabled, firstInterval)
	}
	if secondEnabled {
		t.Fatal("undeclared second schedule remained enabled")
	}

	err := store.ReconcileSchedules(ctx, []jobs.Schedule{
		first,
		{Name: "broken", Kind: "test.broken", Payload: make(chan int), Interval: time.Minute},
	})
	if err == nil || !strings.Contains(err.Error(), "marshal schedule payload") {
		t.Fatalf("error = %v", err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT interval_seconds FROM plinth_schedules WHERE name = 'first'`).Scan(&firstInterval); err != nil {
		t.Fatal(err)
	}
	if firstInterval != 180 {
		t.Fatalf("first schedule changed after failed preflight: %d", firstInterval)
	}
}

func newIntegrationStore(t *testing.T) *Store {
	t.Helper()
	rawURL := strings.TrimSpace(os.Getenv("PLINTH_TEST_DATABASE_URL"))
	if rawURL == "" {
		t.Skip("PLINTH_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	schema := fmt.Sprintf("plinth_jobs_test_%d", time.Now().UnixNano())
	admin, err := pgx.Connect(ctx, rawURL)
	if err != nil {
		t.Fatal(err)
	}
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	urlWithSchema, err := databaseURLWithSearchPath(rawURL, schema)
	if err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	postgres, err := pgdb.New(ctx, urlWithSchema)
	if err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	if err := postgres.Start(ctx); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	migrator := pgmigrate.New(urlWithSchema, true, logger,
		migrate.Migration{Name: "jobs_test", FS: migrations.FS()},
	)
	if err := migrator.Start(ctx); err != nil {
		_ = postgres.Stop(context.Background())
		admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = postgres.Stop(context.Background())
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE")
		_ = admin.Close(context.Background())
	})
	return NewStore(postgres.Pool())
}

func databaseURLWithSearchPath(rawURL, schema string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func waitForJobStatus(ctx context.Context, t *testing.T, store *Store, id, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, err := store.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == want {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	job, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	t.Fatalf("job status = %q, want %q", job.Status, want)
}
