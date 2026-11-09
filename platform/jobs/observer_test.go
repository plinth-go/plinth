package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type observerContextKey struct{}

func TestWorkerObserverReceivesAttemptLifecycle(t *testing.T) {
	registry := NewRegistry()
	if err := Register(registry, "test.observed", func(ctx context.Context, payload struct {
		Value string `json:"value"`
	}) error {
		if ctx.Value(observerContextKey{}) != "observed" {
			return errors.New("observer context was not propagated")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	succeeded := false
	store := &workerTestStore{markSucceeded: func(context.Context, Lease) error {
		succeeded = true
		return nil
	}}
	started := false
	finished := false
	worker := newWorker(store, registry, nil, DefaultWorkerConfig(), WithWorkerObserver(Observer{
		AttemptStarted: func(ctx context.Context, job Job) context.Context {
			started = true
			if job.Kind != "test.observed" {
				t.Fatalf("job = %#v", job)
			}
			return context.WithValue(ctx, observerContextKey{}, "observed")
		},
		AttemptFinished: func(_ context.Context, job Job, duration time.Duration, err error) {
			finished = true
			if err != nil || duration <= 0 {
				t.Fatalf("finish = duration %s, error %v", duration, err)
			}
		},
	}))
	worker.jobCtx = context.Background()
	worker.runJob("worker-1", Job{
		ID: "job-1", Kind: "test.observed", Payload: json.RawMessage(`{"value":"ok"}`),
		Attempts: 1, MaxAttempts: 2,
	})
	if !started || !finished || !succeeded {
		t.Fatalf("started=%t finished=%t succeeded=%t", started, finished, succeeded)
	}
}

func TestWorkerObserverPanicDoesNotBreakHandler(t *testing.T) {
	registry := NewRegistry()
	if err := Register(registry, "test.safe", func(context.Context, struct{}) error { return nil }); err != nil {
		t.Fatal(err)
	}
	succeeded := false
	store := &workerTestStore{markSucceeded: func(context.Context, Lease) error {
		succeeded = true
		return nil
	}}
	worker := newWorker(store, registry, nil, DefaultWorkerConfig(), WithWorkerObserver(Observer{
		AttemptStarted: func(ctx context.Context, _ Job) context.Context {
			panic("observer boom")
		},
		AttemptFinished: func(context.Context, Job, time.Duration, error) {
			panic("observer finish boom")
		},
	}))
	worker.jobCtx = context.Background()
	worker.runJob("worker-1", Job{ID: "job-1", Kind: "test.safe", Payload: json.RawMessage(`{}`), Attempts: 1, MaxAttempts: 2})
	if !succeeded {
		t.Fatal("observer panic prevented successful acknowledgement")
	}
}

func TestSchedulerObserverReceivesDispatchResult(t *testing.T) {
	store := &schedulerTestStore{dispatch: func(context.Context, int) (int, error) {
		return 3, nil
	}}
	observed := false
	scheduler := newScheduler(store, nil, DefaultSchedulerConfig(), WithSchedulerObserver(Observer{
		SchedulesDispatched: func(_ context.Context, count int, duration time.Duration, err error) {
			observed = true
			if count != 3 || err != nil || duration <= 0 {
				t.Fatalf("dispatch = count %d, duration %s, error %v", count, duration, err)
			}
		},
	}))
	scheduler.dispatch(context.Background())
	if !observed {
		t.Fatal("scheduler observer was not called")
	}
}
