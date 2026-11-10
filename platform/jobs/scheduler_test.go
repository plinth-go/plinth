package jobs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSchedulerNilReceiverIsSafe(t *testing.T) {
	var scheduler *Scheduler
	if got := scheduler.Name(); got != "jobs-scheduler" {
		t.Fatalf("Name = %q", got)
	}
	if err := scheduler.Declare(Schedule{}); err == nil {
		t.Fatal("nil scheduler accepted a declaration")
	}
	if err := scheduler.Start(context.Background()); err == nil {
		t.Fatal("nil scheduler started")
	}
	if err := scheduler.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if scheduler.Ready() {
		t.Fatal("nil scheduler is ready")
	}
	if scheduler.Failed() != nil {
		t.Fatal("nil scheduler returned a failure channel")
	}
	if err := scheduler.Failure(); err != nil {
		t.Fatal(err)
	}
}

func TestSchedulerDeclarationValidatesIntervals(t *testing.T) {
	for _, tc := range []struct {
		name     string
		interval time.Duration
		want     string
	}{
		{name: "below minimum", interval: 500 * time.Millisecond, want: "at least one second"},
		{name: "fractional second", interval: 1500 * time.Millisecond, want: "whole number of seconds"},
		{name: "one second", interval: time.Second},
		{name: "whole minutes", interval: 5 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheduler := NewScheduler(nil, nil, SchedulerConfig{})
			err := scheduler.Declare(Schedule{Name: "test", Kind: "test.kind", Interval: tc.interval})
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestSchedulerConfigRejectsNegativeValues(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  SchedulerConfig
		want string
	}{
		{name: "interval", cfg: SchedulerConfig{Interval: -time.Second}, want: "interval must not be negative"},
		{name: "batch", cfg: SchedulerConfig{Batch: -1}, want: "batch must not be negative"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertJobsPanicsWith(t, tc.want, func() {
				NewScheduler(nil, nil, tc.cfg)
			})
		})
	}
}

func TestScheduleIdentifiersAreCanonicalized(t *testing.T) {
	normalized, err := normalizeSchedule(Schedule{
		Name:     "  test.schedule  ",
		Kind:     "  test.kind  ",
		Interval: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Name != "test.schedule" || normalized.Kind != "test.kind" {
		t.Fatalf("schedule = %#v", normalized)
	}

	scheduler := NewScheduler(nil, nil, SchedulerConfig{})
	if err := scheduler.Declare(Schedule{Name: " test.schedule ", Kind: " first.kind ", Interval: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Declare(Schedule{Name: "test.schedule", Kind: "second.kind", Interval: time.Minute}); err != nil {
		t.Fatal(err)
	}
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	if len(scheduler.schedules) != 1 || scheduler.schedules[0].Kind != "second.kind" {
		t.Fatalf("schedules = %#v", scheduler.schedules)
	}
}

type schedulerTestStore struct {
	reconcile func(context.Context, []Schedule) error
	dispatch  func(context.Context, int) (int, error)
}

func (s *schedulerTestStore) ReconcileSchedules(ctx context.Context, schedules []Schedule) error {
	if s.reconcile == nil {
		return nil
	}
	return s.reconcile(ctx, schedules)
}

func (s *schedulerTestStore) DispatchDueSchedules(ctx context.Context, limit int) (int, error) {
	if s.dispatch == nil {
		return 0, nil
	}
	return s.dispatch(ctx, limit)
}

func TestSchedulerStartPreflightsAllSchedulePayloads(t *testing.T) {
	reconciles := 0
	store := &schedulerTestStore{
		reconcile: func(context.Context, []Schedule) error {
			reconciles++
			return nil
		},
	}
	scheduler := newScheduler(store, nil, SchedulerConfig{})
	if err := scheduler.Declare(Schedule{Name: "valid", Kind: "test.valid", Payload: map[string]string{"ok": "yes"}, Interval: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Declare(Schedule{Name: "invalid", Kind: "test.invalid", Payload: make(chan int), Interval: time.Minute}); err != nil {
		t.Fatal(err)
	}

	err := scheduler.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "marshal schedule payload") {
		t.Fatalf("Start error = %v", err)
	}
	if reconciles != 0 {
		t.Fatalf("reconciled schedules %d times before payload preflight completed", reconciles)
	}
	if scheduler.Ready() {
		t.Fatal("scheduler remained ready after failed start")
	}
}

func TestSchedulerRegistryOptionRejectsNil(t *testing.T) {
	assertJobsPanicsWith(t, "scheduler registry is nil", func() {
		WithRegistry(nil)
	})
}

func TestSchedulerWithRegistryRejectsUnknownKindBeforeReconcile(t *testing.T) {
	reconciles := 0
	store := &schedulerTestStore{
		reconcile: func(context.Context, []Schedule) error {
			reconciles++
			return nil
		},
	}
	scheduler := newScheduler(store, nil, SchedulerConfig{}, WithRegistry(NewRegistry()))
	if err := scheduler.Declare(Schedule{Name: "unknown", Kind: "test.unknown", Interval: time.Minute}); err != nil {
		t.Fatal(err)
	}

	err := scheduler.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unregistered job kind") {
		t.Fatalf("Start error = %v", err)
	}
	if reconciles != 0 {
		t.Fatalf("reconciled schedules %d times before kind preflight completed", reconciles)
	}
	if scheduler.Ready() {
		t.Fatal("scheduler remained ready after unknown kind")
	}
}

func TestSchedulerWithRegistryRejectsPayloadMismatchBeforeReconcile(t *testing.T) {
	registry := NewRegistry()
	if err := Register(registry, "test.kind", func(context.Context, struct {
		Message string `json:"message"`
	}) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	reconciles := 0
	store := &schedulerTestStore{
		reconcile: func(context.Context, []Schedule) error {
			reconciles++
			return nil
		},
	}
	scheduler := newScheduler(store, nil, SchedulerConfig{}, WithRegistry(registry))
	if err := scheduler.Declare(Schedule{
		Name:     "mismatch",
		Kind:     "test.kind",
		Payload:  map[string]string{"unknown": "field"},
		Interval: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}

	err := scheduler.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "payload does not match job kind") {
		t.Fatalf("Start error = %v", err)
	}
	if reconciles != 0 {
		t.Fatalf("reconciled schedules %d times before payload preflight completed", reconciles)
	}
	if scheduler.Ready() {
		t.Fatal("scheduler remained ready after payload mismatch")
	}
}

func TestSchedulerWithRegistryAcceptsMatchingPayload(t *testing.T) {
	registry := NewRegistry()
	if err := Register(registry, "test.kind", func(context.Context, struct {
		Message string `json:"message"`
	}) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var reconciled []Schedule
	store := &schedulerTestStore{
		reconcile: func(_ context.Context, schedules []Schedule) error {
			reconciled = append([]Schedule(nil), schedules...)
			return nil
		},
	}
	scheduler := newScheduler(store, nil, SchedulerConfig{Interval: time.Hour}, WithRegistry(registry))
	if err := scheduler.Declare(Schedule{
		Name:     "valid",
		Kind:     "test.kind",
		Payload:  map[string]string{"message": "ok"},
		Interval: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}

	if err := scheduler.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(reconciled) != 1 || reconciled[0].Name != "valid" {
		t.Fatalf("reconciled schedules = %#v", reconciled)
	}
}

func TestSchedulerReadyTracksLoopExitAfterStopTimeout(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	store := &schedulerTestStore{
		dispatch: func(context.Context, int) (int, error) {
			select {
			case started <- struct{}{}:
			default:
			}
			<-release
			return 0, nil
		},
	}
	scheduler := newScheduler(store, nil, SchedulerConfig{Interval: time.Hour})
	if err := scheduler.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not start dispatching")
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := scheduler.Stop(stopCtx); err == nil {
		t.Fatal("Stop succeeded while dispatch was still blocked")
	}
	if !scheduler.Ready() {
		t.Fatal("scheduler became unready before its loop exited")
	}

	close(release)
	deadline := time.Now().Add(time.Second)
	for scheduler.Ready() {
		if time.Now().After(deadline) {
			t.Fatal("scheduler stayed ready after its loop exited")
		}
		time.Sleep(time.Millisecond)
	}
	if err := scheduler.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSchedulerContinuesAfterDispatchError(t *testing.T) {
	calls := make(chan struct{}, 4)
	attempt := 0
	store := &schedulerTestStore{
		dispatch: func(context.Context, int) (int, error) {
			attempt++
			calls <- struct{}{}
			if attempt == 1 {
				return 0, errors.New("ambiguous commit response")
			}
			return 0, nil
		},
	}
	scheduler := newScheduler(store, nil, SchedulerConfig{Interval: time.Millisecond})
	if err := scheduler.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = scheduler.Stop(stopCtx)
	})

	deadline := time.After(time.Second)
	for dispatched := 0; dispatched < 2; {
		select {
		case <-calls:
			dispatched++
		case <-deadline:
			t.Fatal("scheduler did not retry after a dispatch error")
		}
	}
	if !scheduler.Ready() {
		t.Fatal("scheduler became unready after an ordinary dispatch error")
	}
	if err := scheduler.Failure(); err != nil {
		t.Fatalf("scheduler failure = %v", err)
	}
	select {
	case <-scheduler.Failed():
		t.Fatal("dispatch error was reported as a fatal scheduler failure")
	default:
	}
}

func TestSchedulerReportsLoopPanic(t *testing.T) {
	store := &schedulerTestStore{
		dispatch: func(context.Context, int) (int, error) {
			panic("dispatch boom")
		},
	}
	scheduler := newScheduler(store, nil, SchedulerConfig{Interval: time.Hour})
	if err := scheduler.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-scheduler.Failed():
	case <-time.After(time.Second):
		t.Fatal("scheduler loop panic was not reported")
	}
	if err := scheduler.Failure(); err == nil || !strings.Contains(err.Error(), "dispatch boom") {
		t.Fatalf("failure = %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for scheduler.Ready() {
		if time.Now().After(deadline) {
			t.Fatal("scheduler stayed ready after its loop panicked")
		}
		time.Sleep(time.Millisecond)
	}
	if err := scheduler.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}
