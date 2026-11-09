package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
)

// SchedulerConfig controls due-schedule polling. Its env tags make it an
// embeddable configuration fragment.
type SchedulerConfig struct {
	// Interval is the delay between due-schedule scans.
	Interval time.Duration `env:"INTERVAL,default=1s" envdoc:"Delay between due-schedule scans." validate:"gt=0"`
	// Batch is the maximum number of due schedules dispatched per scan.
	Batch int `env:"BATCH,default=20" envdoc:"Maximum number of due schedules dispatched per scan." validate:"gte=1"`
}

// DefaultSchedulerConfig returns the defaults declared by the env tags.
func DefaultSchedulerConfig() SchedulerConfig {
	return SchedulerConfig{Interval: time.Second, Batch: 20}
}

// SchedulerOption configures optional scheduler integration checks.
type SchedulerOption func(*schedulerOptions)

type schedulerOptions struct {
	registry *Registry
	observer Observer
}

// WithRegistry validates every declared schedule against the worker registry
// before reconciliation. Scheduler-only producers that do not host handlers can
// omit it.
func WithRegistry(registry *Registry) SchedulerOption {
	if registry == nil {
		panic("jobs: scheduler registry is nil")
	}
	return func(options *schedulerOptions) {
		options.registry = registry
	}
}

// WithSchedulerObserver attaches backend-neutral telemetry hooks to scheduler
// scans. A zero Observer is a no-op. The observer must not block the loop.
func WithSchedulerObserver(observer Observer) SchedulerOption {
	return func(options *schedulerOptions) {
		options.observer = observer
	}
}

type Scheduler struct {
	store     SchedulerStore
	logger    *slog.Logger
	cfg       SchedulerConfig
	schedules []Schedule
	registry  *Registry
	observer  Observer

	lifecycleMu sync.Mutex
	mu          sync.Mutex
	cancel      context.CancelFunc
	done        chan struct{}
	wg          sync.WaitGroup

	running     atomic.Bool
	failed      chan struct{}
	failureMu   sync.Mutex
	failureErr  error
	failureOnce sync.Once
}

func NewScheduler(store SchedulerStore, logger *slog.Logger, cfg SchedulerConfig, opts ...SchedulerOption) *Scheduler {
	return newScheduler(store, logger, cfg, opts...)
}

func newScheduler(store SchedulerStore, logger *slog.Logger, cfg SchedulerConfig, opts ...SchedulerOption) *Scheduler {
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("component", "scheduler")
	defaults := DefaultSchedulerConfig()
	if cfg.Interval < 0 {
		panic("jobs: scheduler interval must not be negative")
	}
	if cfg.Interval == 0 {
		cfg.Interval = defaults.Interval
	}
	if cfg.Batch < 0 {
		panic("jobs: scheduler batch must not be negative")
	}
	if cfg.Batch == 0 {
		cfg.Batch = defaults.Batch
	}
	options := schedulerOptions{}
	for index, opt := range opts {
		if opt == nil {
			panic(fmt.Sprintf("jobs: scheduler option at index %d is nil", index))
		}
		opt(&options)
	}
	return &Scheduler{store: store, logger: logger, cfg: cfg, registry: options.registry, observer: options.observer, failed: make(chan struct{})}
}

func (s *Scheduler) Name() string { return "jobs-scheduler" }

// Declare registers a schedule that is reconciled into the configured store
// when the component starts. Declarations are code-owned; the store is the
// durable state.
func (s *Scheduler) Declare(schedule Schedule) error {
	if s == nil {
		return fmt.Errorf("job scheduler is nil")
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.running.Load() {
		return fmt.Errorf("cannot declare schedules after scheduler start")
	}
	schedule, err := normalizeSchedule(schedule)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.schedules {
		if s.schedules[i].Name == schedule.Name {
			s.schedules[i] = schedule
			return nil
		}
	}
	s.schedules = append(s.schedules, schedule)
	return nil
}

func (s *Scheduler) Start(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("job scheduler is nil")
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if isNilInterface(s.store) {
		return fmt.Errorf("scheduler requires a job store")
	}
	if err := s.failureCause(); err != nil {
		return fmt.Errorf("scheduler previously failed: %w", err)
	}
	if !s.running.CompareAndSwap(false, true) {
		return fmt.Errorf("scheduler already started")
	}

	s.mu.Lock()
	schedules := append([]Schedule(nil), s.schedules...)
	s.mu.Unlock()
	if err := s.validateSchedules(schedules); err != nil {
		s.running.Store(false)
		return err
	}
	reconcileCtx, reconcileCancel := context.WithTimeout(ctx, 10*time.Second)
	err := s.store.ReconcileSchedules(reconcileCtx, schedules)
	reconcileCancel()
	if err != nil {
		s.running.Store(false)
		return fmt.Errorf("reconcile schedules: %w", err)
	}

	loopCtx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.cancel = cancel
	s.done = make(chan struct{})
	done := s.done
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(done)
		defer s.running.Store(false)
		defer func() {
			if recovered := recover(); recovered != nil {
				s.reportFailure(fmt.Errorf("job scheduler panic: %v\n%s", recovered, debug.Stack()))
			}
		}()
		s.loop(loopCtx)
	}()

	s.logger.Info("job scheduler started", "schedules", len(schedules), "interval", s.cfg.Interval)
	return nil
}

func (s *Scheduler) validateSchedules(schedules []Schedule) error {
	for _, schedule := range schedules {
		payload, err := marshalPayload(schedule.Payload)
		if err != nil {
			return fmt.Errorf("declare schedule %q: marshal schedule payload: %w", schedule.Name, err)
		}
		if s.registry == nil {
			continue
		}
		if _, ok := s.registry.Handler(schedule.Kind); !ok {
			return fmt.Errorf("schedule %q uses unregistered job kind %q", schedule.Name, schedule.Kind)
		}
		if err := s.registry.ValidatePayload(schedule.Kind, payload); err != nil {
			return fmt.Errorf("schedule %q payload does not match job kind %q: %w", schedule.Name, schedule.Kind, err)
		}
	}
	return nil
}

func (s *Scheduler) Stop(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if !s.running.Load() {
		return nil
	}
	s.mu.Lock()
	cancel := s.cancel
	done := s.done
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("stop job scheduler: %w", ctx.Err())
	}
}

func (s *Scheduler) Ready() bool { return s != nil && s.running.Load() }

// Failed closes when the scheduler loop panics and the component cannot continue.
func (s *Scheduler) Failed() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.failed
}

// Failure returns the panic that stopped the scheduler loop.
func (s *Scheduler) Failure() error {
	if s == nil {
		return nil
	}
	return s.failureCause()
}

func (s *Scheduler) loop(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.Interval)
	defer ticker.Stop()
	for {
		s.dispatch(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Scheduler) reportFailure(err error) {
	s.failureOnce.Do(func() {
		s.failureMu.Lock()
		s.failureErr = err
		close(s.failed)
		s.failureMu.Unlock()

		s.mu.Lock()
		cancel := s.cancel
		s.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		s.logger.Error("job scheduler failed", "error", err)
	})
}

func (s *Scheduler) failureCause() error {
	s.failureMu.Lock()
	defer s.failureMu.Unlock()
	return s.failureErr
}

func (s *Scheduler) dispatch(ctx context.Context) {
	dispatchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	started := time.Now()
	count, err := s.store.DispatchDueSchedules(dispatchCtx, s.cfg.Batch)
	s.observer.dispatched(dispatchCtx, count, time.Since(started), err, s.logger)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Error("dispatch schedules", "error", err)
		}
		return
	}
	if count > 0 {
		s.logger.Info("schedules dispatched", "count", count)
	}
}
