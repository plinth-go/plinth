package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
	"uuid"
)

const (
	terminalAckAttempts  = 3
	terminalAckRetryBase = 100 * time.Millisecond
	minRecoveryInterval  = 100 * time.Millisecond
	maxRecoveryInterval  = time.Minute
)

// WorkerConfig controls job claiming, execution, stale recovery, and retry
// backoff. Its env tags make it an embeddable configuration fragment.
type WorkerConfig struct {
	// Concurrency is the number of jobs claimed concurrently.
	Concurrency int `env:"CONCURRENCY,default=4" envdoc:"Number of jobs claimed concurrently." validate:"gte=1"`
	// PollInterval is the delay between empty queue polls.
	PollInterval time.Duration `env:"POLL_INTERVAL,default=500ms" envdoc:"Delay between empty queue polls." validate:"gt=0"`
	// JobTimeout bounds one job attempt.
	JobTimeout time.Duration `env:"TIMEOUT,default=30s" envdoc:"Maximum duration of one job attempt." validate:"gt=0"`
	// StaleAfter returns abandoned running jobs to the queue.
	StaleAfter time.Duration `env:"STALE_AFTER,default=2m" envdoc:"Age after which an abandoned running job is returned to the queue." validate:"gtfield=JobTimeout"`
	// RetryBase is the initial retry delay.
	RetryBase time.Duration `env:"RETRY_BASE,default=1s" envdoc:"Initial retry delay." validate:"gt=0"`
	// RetryMax is the maximum retry delay.
	RetryMax time.Duration `env:"RETRY_MAX,default=1m" envdoc:"Maximum retry delay." validate:"gtefield=RetryBase"`
}

// DefaultWorkerConfig returns the defaults declared by the env tags.
func DefaultWorkerConfig() WorkerConfig {
	return WorkerConfig{
		Concurrency:  4,
		PollInterval: 500 * time.Millisecond,
		JobTimeout:   30 * time.Second,
		StaleAfter:   2 * time.Minute,
		RetryBase:    time.Second,
		RetryMax:     time.Minute,
	}
}

type Worker struct {
	store      WorkerStore
	registry   *Registry
	logger     *slog.Logger
	cfg        WorkerConfig
	instanceID string
	observer   Observer

	lifecycleMu sync.Mutex
	mu          sync.Mutex
	claimCtx    context.Context
	claim       context.CancelFunc
	jobCtx      context.Context
	job         context.CancelFunc
	done        chan struct{}
	wg          sync.WaitGroup

	running     atomic.Bool
	inFlight    atomic.Int64
	failed      chan struct{}
	failureMu   sync.Mutex
	failureErr  error
	failureOnce sync.Once
}

// WorkerOption configures optional worker integration hooks.
type WorkerOption func(*workerOptions)

type workerOptions struct {
	observer Observer
}

// WithWorkerObserver attaches backend-neutral telemetry hooks to a worker.
// A zero Observer is a no-op. The observer must not block the worker loop.
func WithWorkerObserver(observer Observer) WorkerOption {
	return func(options *workerOptions) {
		options.observer = observer
	}
}

func NewWorker(store WorkerStore, registry *Registry, logger *slog.Logger, cfg WorkerConfig, opts ...WorkerOption) *Worker {
	return newWorker(store, registry, logger, cfg, opts...)
}

func newWorker(store WorkerStore, registry *Registry, logger *slog.Logger, cfg WorkerConfig, opts ...WorkerOption) *Worker {
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("component", "worker")
	cfg = normalizeWorkerConfig(cfg)
	options := workerOptions{}
	for index, opt := range opts {
		if opt == nil {
			panic(fmt.Sprintf("jobs: worker option at index %d is nil", index))
		}
		opt(&options)
	}
	return &Worker{
		store: store, registry: registry, logger: logger, cfg: cfg,
		instanceID: uuid.New().String(), observer: options.observer, failed: make(chan struct{}),
	}
}

func (w *Worker) Name() string { return "jobs-worker" }

func (w *Worker) Start(ctx context.Context) error {
	if w == nil {
		return fmt.Errorf("job worker is nil")
	}
	w.lifecycleMu.Lock()
	defer w.lifecycleMu.Unlock()
	if isNilInterface(w.store) || w.registry == nil {
		return fmt.Errorf("job worker requires store and registry")
	}
	if err := w.failureCause(); err != nil {
		return fmt.Errorf("job worker previously failed: %w", err)
	}
	if !w.running.CompareAndSwap(false, true) {
		return fmt.Errorf("job worker already started")
	}

	recoverCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := w.store.RecoverStale(recoverCtx, w.cfg.StaleAfter); err != nil {
		w.running.Store(false)
		return fmt.Errorf("recover stale jobs: %w", err)
	}

	w.mu.Lock()
	w.claimCtx, w.claim = context.WithCancel(context.Background())
	w.jobCtx, w.job = context.WithCancel(context.Background())
	done := make(chan struct{})
	w.done = done
	w.mu.Unlock()

	for i := 0; i < w.cfg.Concurrency; i++ {
		workerID := w.workerID(i + 1)
		w.wg.Add(1)
		go w.loop(workerID)
	}
	w.wg.Add(1)
	go w.recoverLoop()
	go w.finish(done)

	w.logger.Info("job worker started", "concurrency", w.cfg.Concurrency)
	return nil
}

func (w *Worker) workerID(index int) string {
	return fmt.Sprintf("%s-%s-%d", w.Name(), w.instanceID, index)
}

// Stop first stops claiming new work, then lets in-flight jobs finish. If the
// caller's deadline expires, job contexts are cancelled and the worker drains.
func (w *Worker) Stop(ctx context.Context) error {
	if w == nil {
		return nil
	}
	w.lifecycleMu.Lock()
	defer w.lifecycleMu.Unlock()
	if !w.running.Load() {
		return nil
	}

	w.mu.Lock()
	claimCancel := w.claim
	jobCancel := w.job
	done := w.done
	w.mu.Unlock()
	if claimCancel != nil {
		claimCancel()
	}

	select {
	case <-done:
		return nil
	case <-ctx.Done():
	}

	if jobCancel != nil {
		jobCancel()
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		w.logger.Warn("job worker did not drain after cancellation")
	}
	return fmt.Errorf("stop job worker: %w", ctx.Err())
}

func (w *Worker) Ready() bool { return w != nil && w.running.Load() }

func (w *Worker) InFlight() int64 {
	if w == nil {
		return 0
	}
	return w.inFlight.Load()
}

// Failed closes when a worker loop panics and the component cannot continue.
func (w *Worker) Failed() <-chan struct{} {
	if w == nil {
		return nil
	}
	return w.failed
}

// Failure returns the panic that stopped a worker loop.
func (w *Worker) Failure() error {
	if w == nil {
		return nil
	}
	return w.failureCause()
}

func (w *Worker) loop(workerID string) {
	defer w.wg.Done()
	defer func() {
		if recovered := recover(); recovered != nil {
			w.reportFailure(fmt.Errorf("job worker %s panic: %v\n%s", workerID, recovered, debug.Stack()))
		}
	}()
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()

	for {
		if w.claimCtx.Err() != nil {
			return
		}
		for w.claimCtx.Err() == nil {
			job, err := w.store.Claim(w.claimCtx, workerID)
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					w.logger.Error("claim job", "worker", workerID, "error", err)
				}
				break
			}
			if job == nil {
				break
			}
			w.runJob(workerID, *job)
		}

		select {
		case <-w.claimCtx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) recoverLoop() {
	defer w.wg.Done()
	defer func() {
		if recovered := recover(); recovered != nil {
			w.reportFailure(fmt.Errorf("job worker stale recovery panic: %v\n%s", recovered, debug.Stack()))
		}
	}()

	ticker := time.NewTicker(recoveryInterval(w.cfg.StaleAfter))
	defer ticker.Stop()
	for {
		select {
		case <-w.claimCtx.Done():
			return
		case <-ticker.C:
		}
		recoverCtx, cancel := context.WithTimeout(w.claimCtx, 10*time.Second)
		err := w.store.RecoverStale(recoverCtx, w.cfg.StaleAfter)
		cancel()
		if err != nil && w.claimCtx.Err() == nil {
			w.logger.Error("recover stale jobs", "error", err)
		}
	}
}

func recoveryInterval(staleAfter time.Duration) time.Duration {
	interval := staleAfter / 2
	if interval < minRecoveryInterval {
		return minRecoveryInterval
	}
	if interval > maxRecoveryInterval {
		return maxRecoveryInterval
	}
	return interval
}

func (w *Worker) reportFailure(err error) {
	w.failureOnce.Do(func() {
		w.failureMu.Lock()
		w.failureErr = err
		close(w.failed)
		w.failureMu.Unlock()

		w.mu.Lock()
		cancel := w.claim
		w.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		w.logger.Error("job worker failed", "error", err)
	})
}

func (w *Worker) failureCause() error {
	w.failureMu.Lock()
	defer w.failureMu.Unlock()
	return w.failureErr
}

func (w *Worker) finish(done chan struct{}) {
	w.wg.Wait()
	w.running.Store(false)
	w.mu.Lock()
	close(done)
	w.mu.Unlock()
}

func (w *Worker) runJob(workerID string, job Job) {
	w.inFlight.Add(1)
	defer w.inFlight.Add(-1)
	lease := job.Lease(workerID)

	handler, ok := w.registry.Handler(job.Kind)
	if !ok {
		err := fmt.Errorf("no handler registered for job kind %q", job.Kind)
		w.fail(lease, job, err)
		return
	}

	started := time.Now()
	err := w.invoke(w.jobCtx, handler, job)
	if err != nil && isNilError(err) {
		err = fmt.Errorf("job handler returned a typed-nil %T error", err)
	}
	if err == nil {
		ackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ackErr := retryTerminalAck(ackCtx, func(ctx context.Context) error {
			return w.store.MarkSucceeded(ctx, lease)
		})
		if ackErr != nil {
			if errors.Is(ackErr, ErrLeaseLost) {
				w.logger.Warn("job lease lost before success",
					"job_id", job.ID, "kind", job.Kind, "worker", workerID,
					"attempt", lease.Attempt, "max_attempts", job.MaxAttempts)
				return
			}
			w.logger.Error("mark job succeeded",
				"job_id", job.ID, "kind", job.Kind, "worker", workerID,
				"attempt", lease.Attempt, "max_attempts", job.MaxAttempts, "error", ackErr)
			return
		}
		w.logger.Info("job succeeded", "job_id", job.ID, "kind", job.Kind,
			"worker", workerID, "attempt", job.Attempts, "duration", time.Since(started))
		return
	}

	if errors.Is(w.jobCtx.Err(), context.Canceled) {
		ackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ackErr := retryTerminalAck(ackCtx, func(ctx context.Context) error {
			return w.store.MarkInterrupted(ctx, lease, err)
		})
		if ackErr != nil {
			if errors.Is(ackErr, ErrLeaseLost) {
				w.logger.Warn("job lease lost before interruption",
					"job_id", job.ID, "kind", job.Kind, "worker", workerID,
					"attempt", lease.Attempt, "max_attempts", job.MaxAttempts)
				return
			}
			w.logger.Error("release interrupted job",
				"job_id", job.ID, "kind", job.Kind, "worker", workerID,
				"attempt", lease.Attempt, "max_attempts", job.MaxAttempts, "error", ackErr)
		}
		return
	}
	w.fail(lease, job, err)
}

func (w *Worker) invoke(parent context.Context, handler Handler, job Job) (err error) {
	ctx, cancel := context.WithTimeout(parent, w.cfg.JobTimeout)
	defer cancel()
	ctx = w.observer.start(ctx, job, w.logger)
	started := time.Now()
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("job panic: %v\n%s", recovered, debug.Stack())
		}
		if err != nil && isNilError(err) {
			err = fmt.Errorf("job handler returned a typed-nil %T error", err)
		}
		w.observer.finish(ctx, job, time.Since(started), err, w.logger)
	}()
	return handler(ctx, job.Payload)
}

func (w *Worker) fail(lease Lease, job Job, cause error) {
	delay := retryDelay(w.cfg, job.Attempts)
	retryAt := time.Now().Add(delay)
	ackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := retryTerminalAck(ackCtx, func(ctx context.Context) error {
		return w.store.MarkFailed(ctx, lease, cause, retryAt)
	}); err != nil {
		if errors.Is(err, ErrLeaseLost) {
			w.logger.Warn("job lease lost before failure",
				"job_id", job.ID, "kind", job.Kind, "worker", lease.WorkerID,
				"attempt", lease.Attempt, "max_attempts", job.MaxAttempts)
			return
		}
		w.logger.Error("mark job failed",
			"job_id", job.ID, "kind", job.Kind, "worker", lease.WorkerID,
			"attempt", lease.Attempt, "max_attempts", job.MaxAttempts, "error", err)
		return
	}

	level := slog.LevelWarn
	if job.Attempts >= job.MaxAttempts {
		level = slog.LevelError
	}
	w.logger.Log(ackCtx, level, "job failed",
		"job_id", job.ID,
		"kind", job.Kind,
		"worker", lease.WorkerID,
		"attempt", job.Attempts,
		"max_attempts", job.MaxAttempts,
		"retry_in", delay,
		"error", errorMessage(cause),
	)
}

// retryTerminalAck retries an idempotent, lease-fenced terminal transition.
// ErrLeaseLost is final: another attempt owns the job, so retrying cannot repair
// this worker's lease. Every attempt uses the same acknowledgement context.
func retryTerminalAck(ctx context.Context, ack func(context.Context) error) error {
	var lastErr error
	for attempt := 0; attempt < terminalAckAttempts; attempt++ {
		if attempt > 0 {
			delay := time.Duration(attempt) * terminalAckRetryBase
			select {
			case <-ctx.Done():
				return lastErr
			case <-time.After(delay):
			}
		}
		lastErr = ack(ctx)
		if lastErr == nil || errors.Is(lastErr, ErrLeaseLost) {
			return lastErr
		}
	}
	return lastErr
}

func retryDelay(cfg WorkerConfig, attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	delay := cfg.RetryBase
	for attempt := 1; attempt < attempts; attempt++ {
		if delay >= cfg.RetryMax || delay > cfg.RetryMax/2 {
			return cfg.RetryMax
		}
		delay *= 2
	}
	if delay > cfg.RetryMax {
		return cfg.RetryMax
	}
	return delay
}

func normalizeWorkerConfig(cfg WorkerConfig) WorkerConfig {
	cfg, err := resolveWorkerConfig(cfg)
	if err != nil {
		panic("jobs: " + err.Error())
	}
	return cfg
}

func resolveWorkerConfig(cfg WorkerConfig) (WorkerConfig, error) {
	defaults := DefaultWorkerConfig()
	if cfg.Concurrency < 0 {
		return WorkerConfig{}, fmt.Errorf("worker concurrency must not be negative")
	}
	if cfg.Concurrency == 0 {
		cfg.Concurrency = defaults.Concurrency
	}
	if cfg.PollInterval < 0 {
		return WorkerConfig{}, fmt.Errorf("worker poll interval must not be negative")
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = defaults.PollInterval
	}
	if cfg.JobTimeout < 0 {
		return WorkerConfig{}, fmt.Errorf("worker job timeout must not be negative")
	}
	if cfg.JobTimeout == 0 {
		cfg.JobTimeout = defaults.JobTimeout
	}
	if cfg.StaleAfter < 0 {
		return WorkerConfig{}, fmt.Errorf("worker stale after must not be negative")
	}
	if cfg.StaleAfter == 0 {
		cfg.StaleAfter = defaults.StaleAfter
	}
	if cfg.StaleAfter <= cfg.JobTimeout {
		return WorkerConfig{}, fmt.Errorf("worker stale after (%s) must be greater than job timeout (%s)", cfg.StaleAfter, cfg.JobTimeout)
	}
	if cfg.RetryBase < 0 {
		return WorkerConfig{}, fmt.Errorf("worker retry base must not be negative")
	}
	if cfg.RetryBase == 0 {
		cfg.RetryBase = defaults.RetryBase
	}
	if cfg.RetryMax < 0 {
		return WorkerConfig{}, fmt.Errorf("worker retry max must not be negative")
	}
	if cfg.RetryMax == 0 {
		cfg.RetryMax = defaults.RetryMax
	}
	if cfg.RetryMax < cfg.RetryBase {
		return WorkerConfig{}, fmt.Errorf("worker retry max (%s) must not be less than retry base (%s)", cfg.RetryMax, cfg.RetryBase)
	}
	return cfg, nil
}
