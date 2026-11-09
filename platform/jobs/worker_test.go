package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type workerTestStore struct {
	claim           func(context.Context, string) (*Job, error)
	markSucceeded   func(context.Context, Lease) error
	markFailed      func(context.Context, Lease, error, time.Time) error
	markInterrupted func(context.Context, Lease, error) error
	recoverStale    func(context.Context, time.Duration) error
}

func (s *workerTestStore) Claim(ctx context.Context, workerID string) (*Job, error) {
	if s.claim == nil {
		return nil, nil
	}
	return s.claim(ctx, workerID)
}

func (s *workerTestStore) MarkSucceeded(ctx context.Context, lease Lease) error {
	if s.markSucceeded == nil {
		return nil
	}
	return s.markSucceeded(ctx, lease)
}

func (s *workerTestStore) MarkFailed(ctx context.Context, lease Lease, cause error, retryAt time.Time) error {
	if s.markFailed == nil {
		return nil
	}
	return s.markFailed(ctx, lease, cause, retryAt)
}

func (s *workerTestStore) MarkInterrupted(ctx context.Context, lease Lease, cause error) error {
	if s.markInterrupted == nil {
		return nil
	}
	return s.markInterrupted(ctx, lease, cause)
}

func (s *workerTestStore) RecoverStale(ctx context.Context, staleAfter time.Duration) error {
	if s.recoverStale == nil {
		return nil
	}
	return s.recoverStale(ctx, staleAfter)
}

func TestWorkerReportsLoopPanic(t *testing.T) {
	store := &workerTestStore{
		claim: func(context.Context, string) (*Job, error) {
			panic("claim boom")
		},
	}
	worker := newWorker(store, NewRegistry(), nil, WorkerConfig{
		Concurrency:  1,
		PollInterval: time.Millisecond,
		JobTimeout:   time.Second,
		StaleAfter:   time.Minute,
		RetryBase:    time.Millisecond,
		RetryMax:     10 * time.Millisecond,
	})
	if err := worker.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-worker.Failed():
	case <-time.After(time.Second):
		t.Fatal("worker loop panic was not reported")
	}
	if err := worker.Failure(); err == nil || !strings.Contains(err.Error(), "claim boom") {
		t.Fatalf("failure = %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for worker.Ready() {
		if time.Now().After(deadline) {
			t.Fatal("worker stayed ready after its loop panicked")
		}
		time.Sleep(time.Millisecond)
	}
	if err := worker.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerRecoversStaleJobsPeriodically(t *testing.T) {
	var recoveries atomic.Int32
	store := &workerTestStore{
		recoverStale: func(context.Context, time.Duration) error {
			recoveries.Add(1)
			return nil
		},
	}
	worker := newWorker(store, NewRegistry(), nil, WorkerConfig{
		Concurrency:  1,
		PollInterval: time.Millisecond,
		JobTimeout:   time.Millisecond,
		StaleAfter:   20 * time.Millisecond,
		RetryBase:    time.Millisecond,
		RetryMax:     10 * time.Millisecond,
	})
	if err := worker.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := worker.Stop(stopCtx); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})

	deadline := time.Now().Add(2 * time.Second)
	for recoveries.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("stale recovery calls = %d, want at least 2", recoveries.Load())
		}
		time.Sleep(time.Millisecond)
	}
}

func TestWorkerReportsStaleRecoveryPanic(t *testing.T) {
	var calls atomic.Int32
	store := &workerTestStore{
		recoverStale: func(context.Context, time.Duration) error {
			if calls.Add(1) >= 2 {
				panic("recovery boom")
			}
			return nil
		},
	}
	worker := newWorker(store, NewRegistry(), nil, WorkerConfig{
		Concurrency:  1,
		PollInterval: time.Millisecond,
		JobTimeout:   time.Millisecond,
		StaleAfter:   20 * time.Millisecond,
		RetryBase:    time.Millisecond,
		RetryMax:     10 * time.Millisecond,
	})
	if err := worker.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = worker.Stop(stopCtx)
	})

	select {
	case <-worker.Failed():
	case <-time.After(2 * time.Second):
		t.Fatal("stale recovery panic was not reported")
	}
	if err := worker.Failure(); err == nil || !strings.Contains(err.Error(), "recovery boom") {
		t.Fatalf("failure = %v", err)
	}
}

func TestRecoveryIntervalIsBounded(t *testing.T) {
	tests := []struct {
		staleAfter time.Duration
		want       time.Duration
	}{
		{staleAfter: time.Millisecond, want: minRecoveryInterval},
		{staleAfter: 20 * time.Second, want: 10 * time.Second},
		{staleAfter: 10 * time.Minute, want: maxRecoveryInterval},
	}
	for _, tt := range tests {
		if got := recoveryInterval(tt.staleAfter); got != tt.want {
			t.Fatalf("recoveryInterval(%s) = %s, want %s", tt.staleAfter, got, tt.want)
		}
	}
}

func TestWorkerNilReceiverIsSafe(t *testing.T) {
	var worker *Worker
	if got := worker.Name(); got != "jobs-worker" {
		t.Fatalf("Name = %q", got)
	}
	if err := worker.Start(context.Background()); err == nil {
		t.Fatal("nil worker started")
	}
	if err := worker.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if worker.Ready() {
		t.Fatal("nil worker is ready")
	}
	if worker.InFlight() != 0 {
		t.Fatal("nil worker reports in-flight jobs")
	}
	if worker.Failed() != nil {
		t.Fatal("nil worker returned a failure channel")
	}
	if err := worker.Failure(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerInstanceIdentityIsUnique(t *testing.T) {
	first := newWorker(nil, NewRegistry(), nil, DefaultWorkerConfig())
	second := newWorker(nil, NewRegistry(), nil, DefaultWorkerConfig())
	if first.instanceID == "" || first.instanceID == second.instanceID {
		t.Fatalf("worker instance IDs = %q and %q", first.instanceID, second.instanceID)
	}
	if got, want := first.workerID(1), "jobs-worker-"+first.instanceID+"-1"; got != want {
		t.Fatalf("worker ID = %q, want %q", got, want)
	}
}

func TestWorkerRecordsTypedNilHandlerErrorAsFailure(t *testing.T) {
	var typedNil *typedNilJobError
	var recorded error
	store := &workerTestStore{
		markFailed: func(_ context.Context, _ Lease, cause error, _ time.Time) error {
			recorded = cause
			return nil
		},
	}
	registry := NewRegistry()
	if err := Register(registry, "typed.nil", func(context.Context, struct{}) error {
		return typedNil
	}); err != nil {
		t.Fatal(err)
	}
	worker := newWorker(store, registry, nil, WorkerConfig{
		Concurrency:  1,
		PollInterval: time.Millisecond,
		JobTimeout:   time.Second,
		StaleAfter:   time.Minute,
		RetryBase:    time.Millisecond,
		RetryMax:     10 * time.Millisecond,
	})
	worker.jobCtx = context.Background()
	worker.runJob("worker-1", Job{
		ID:          "job-1",
		Kind:        "typed.nil",
		Payload:     json.RawMessage(`{}`),
		Attempts:    1,
		MaxAttempts: 1,
	})
	if recorded == nil {
		t.Fatal("typed-nil handler error was treated as success")
	}
	if !strings.Contains(recorded.Error(), "typed-nil *jobs.typedNilJobError") {
		t.Fatalf("recorded error = %v", recorded)
	}
}

func TestRetryTerminalAckRetriesTransientErrors(t *testing.T) {
	var calls int
	err := retryTerminalAck(context.Background(), func(context.Context) error {
		calls++
		if calls < terminalAckAttempts {
			return errors.New("transient")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != terminalAckAttempts {
		t.Fatalf("ack attempts = %d, want %d", calls, terminalAckAttempts)
	}
}

func TestRetryTerminalAckStopsOnLeaseLost(t *testing.T) {
	var calls int
	err := retryTerminalAck(context.Background(), func(context.Context) error {
		calls++
		return ErrLeaseLost
	})
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("error = %v, want ErrLeaseLost", err)
	}
	if calls != 1 {
		t.Fatalf("ack attempts = %d, want 1", calls)
	}
}

func TestRetryTerminalAckReturnsLastErrorAfterExhaustion(t *testing.T) {
	var calls int
	last := errors.New("last")
	err := retryTerminalAck(context.Background(), func(context.Context) error {
		calls++
		if calls == terminalAckAttempts {
			return last
		}
		return errors.New("transient")
	})
	if !errors.Is(err, last) {
		t.Fatalf("error = %v, want %v", err, last)
	}
	if calls != terminalAckAttempts {
		t.Fatalf("ack attempts = %d, want %d", calls, terminalAckAttempts)
	}
}

func TestWorkerRetriesSuccessfulAck(t *testing.T) {
	var claimed atomic.Bool
	var ackCalls atomic.Int32
	var handlerCalls atomic.Int32

	store := &workerTestStore{
		claim: func(context.Context, string) (*Job, error) {
			if claimed.CompareAndSwap(false, true) {
				return &Job{
					ID:          "job-1",
					Kind:        "test.ok",
					Payload:     json.RawMessage(`{}`),
					Attempts:    1,
					MaxAttempts: 3,
				}, nil
			}
			return nil, nil
		},
		markSucceeded: func(context.Context, Lease) error {
			if ackCalls.Add(1) < terminalAckAttempts {
				return errors.New("ack temporarily unavailable")
			}
			return nil
		},
	}
	registry := NewRegistry()
	if err := Register(registry, "test.ok", func(context.Context, struct{}) error {
		handlerCalls.Add(1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	worker := newWorker(store, registry, logger, WorkerConfig{
		Concurrency:  1,
		PollInterval: time.Millisecond,
		JobTimeout:   time.Second,
		StaleAfter:   time.Minute,
		RetryBase:    time.Millisecond,
		RetryMax:     10 * time.Millisecond,
	})
	if err := worker.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = worker.Stop(stopCtx)
	})

	deadline := time.Now().Add(time.Second)
	for ackCalls.Load() < terminalAckAttempts {
		if time.Now().After(deadline) {
			t.Fatalf("ack attempts = %d, want %d", ackCalls.Load(), terminalAckAttempts)
		}
		time.Sleep(time.Millisecond)
	}
	if got := handlerCalls.Load(); got != 1 {
		t.Fatalf("handler calls = %d, want 1", got)
	}
}

func TestWorkerTerminalAckFailureLogIncludesContext(t *testing.T) {
	var logs strings.Builder
	store := &workerTestStore{
		markSucceeded: func(context.Context, Lease) error {
			return errors.New("ack unavailable")
		},
	}
	registry := NewRegistry()
	if err := Register(registry, "test.ack-log", func(context.Context, struct{}) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	worker := newWorker(store, registry, logger, WorkerConfig{
		Concurrency:  1,
		PollInterval: time.Millisecond,
		JobTimeout:   time.Second,
		StaleAfter:   time.Minute,
		RetryBase:    time.Millisecond,
		RetryMax:     10 * time.Millisecond,
	})
	worker.jobCtx = context.Background()
	worker.runJob("worker-log", Job{
		ID:          "job-log",
		Kind:        "test.ack-log",
		Payload:     json.RawMessage(`{}`),
		Attempts:    2,
		MaxAttempts: 5,
	})

	output := logs.String()
	for _, want := range []string{
		"mark job succeeded",
		"component=worker",
		"job_id=job-log",
		"kind=test.ack-log",
		"worker=worker-log",
		"attempt=2",
		"max_attempts=5",
		"ack unavailable",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("ack failure log missing %q: %s", want, output)
		}
	}
}

func TestWorkerTerminalAckExhaustionKeepsLoopReady(t *testing.T) {
	var claimed atomic.Bool
	var ackCalls atomic.Int32
	store := &workerTestStore{
		claim: func(context.Context, string) (*Job, error) {
			if claimed.CompareAndSwap(false, true) {
				return &Job{
					ID:          "job-ack-exhausted",
					Kind:        "test.ack-exhausted",
					Payload:     json.RawMessage(`{}`),
					Attempts:    1,
					MaxAttempts: 3,
				}, nil
			}
			return nil, nil
		},
		markSucceeded: func(context.Context, Lease) error {
			ackCalls.Add(1)
			return errors.New("ack unavailable")
		},
	}
	registry := NewRegistry()
	if err := Register(registry, "test.ack-exhausted", func(context.Context, struct{}) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	worker := newWorker(store, registry, slog.New(slog.NewTextHandler(io.Discard, nil)), WorkerConfig{
		Concurrency:  1,
		PollInterval: time.Millisecond,
		JobTimeout:   time.Second,
		StaleAfter:   time.Minute,
		RetryBase:    time.Millisecond,
		RetryMax:     10 * time.Millisecond,
	})
	if err := worker.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = worker.Stop(stopCtx)
	})

	deadline := time.Now().Add(time.Second)
	for ackCalls.Load() < terminalAckAttempts {
		if time.Now().After(deadline) {
			t.Fatalf("ack attempts = %d, want %d", ackCalls.Load(), terminalAckAttempts)
		}
		time.Sleep(time.Millisecond)
	}
	if !worker.Ready() {
		t.Fatal("worker became unready after an exhausted terminal ack")
	}
	if err := worker.Failure(); err != nil {
		t.Fatalf("worker failure = %v", err)
	}
	select {
	case <-worker.Failed():
		t.Fatal("terminal ack failure was reported as a fatal worker failure")
	default:
	}
}

func TestWorkerFinishClosesCapturedRunChannel(t *testing.T) {
	oldDone := make(chan struct{})
	newDone := make(chan struct{})
	worker := &Worker{done: oldDone}
	worker.wg.Add(1)
	finished := make(chan struct{})
	go func() {
		worker.finish(oldDone)
		close(finished)
	}()

	worker.mu.Lock()
	worker.done = newDone
	worker.mu.Unlock()
	worker.wg.Done()

	select {
	case <-oldDone:
	case <-time.After(time.Second):
		t.Fatal("old run completion channel was not closed")
	}
	select {
	case <-newDone:
		t.Fatal("old watcher closed the replacement run's completion channel")
	case <-finished:
	}
}
