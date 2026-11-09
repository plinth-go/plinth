package jobs

import (
	"context"
	"log/slog"
	"time"
)

// Observer is an optional, backend-neutral hook for job and scheduler
// telemetry. Every field is nil-safe. Callbacks run on the worker or scheduler
// goroutine and must return quickly; expensive exporters should enqueue work.
type Observer struct {
	// AttemptStarted runs immediately before a registered handler. The returned
	// context, when non-nil, is passed to the handler and AttemptFinished.
	AttemptStarted func(context.Context, Job) context.Context
	// AttemptFinished runs after the handler returns or panics.
	AttemptFinished func(context.Context, Job, time.Duration, error)
	// SchedulesDispatched runs once per scheduler scan, including failed scans.
	SchedulesDispatched func(context.Context, int, time.Duration, error)
}

func (o Observer) start(ctx context.Context, job Job, logger *slog.Logger) context.Context {
	if o.AttemptStarted == nil {
		return ctx
	}
	var observed context.Context
	func() {
		defer recoverObserver(logger, "job attempt observer start")
		observed = o.AttemptStarted(ctx, job)
	}()
	if observed == nil {
		return ctx
	}
	return observed
}

func (o Observer) finish(ctx context.Context, job Job, duration time.Duration, err error, logger *slog.Logger) {
	if o.AttemptFinished == nil {
		return
	}
	defer recoverObserver(logger, "job attempt observer finish")
	o.AttemptFinished(ctx, job, duration, err)
}

func (o Observer) dispatched(ctx context.Context, count int, duration time.Duration, err error, logger *slog.Logger) {
	if o.SchedulesDispatched == nil {
		return
	}
	defer recoverObserver(logger, "scheduler observer")
	o.SchedulesDispatched(ctx, count, duration, err)
}

func recoverObserver(logger *slog.Logger, operation string) {
	if recovered := recover(); recovered != nil {
		if logger == nil {
			logger = slog.Default()
		}
		logger.Error("observer panicked", "operation", operation, "panic", recovered)
	}
}
