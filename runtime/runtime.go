package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/plinth-go/plinth/utils"
)

// Component is a long-running part of the service: HTTP, workers, schedulers, pools.
// Start must return after the component has started, not when it exits.
type Component interface {
	Name() string
	Start(context.Context) error
	Stop(context.Context) error
}

// FailureReporter is an optional component contract for failures that happen
// asynchronously after Start returns. Failed must close exactly once, and
// Failure must return the cause recorded before the close.
type FailureReporter interface {
	Failed() <-chan struct{}
	Failure() error
}

type Runtime struct {
	logger     *slog.Logger
	timeout    time.Duration
	components []Component

	mu      sync.Mutex
	running bool
	ran     bool
}

const defaultShutdownTimeout = 10 * time.Second

func New(logger *slog.Logger, timeout time.Duration) *Runtime {
	logger = utils.IfNilElse(logger, slog.Default())
	if timeout <= 0 {
		timeout = defaultShutdownTimeout
	}
	return &Runtime{logger: logger, timeout: timeout}
}

// Add registers components to start in order. It must be called before Run.
// Nil interfaces and typed-nil components are rejected immediately.
func (r *Runtime) Add(components ...Component) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running || r.ran {
		panic("runtime: Add must be called before Run")
	}
	for index, component := range components {
		if isNilComponent(component) {
			panic(fmt.Sprintf("runtime: Add received nil component at index %d", index))
		}
	}
	r.components = append(r.components, components...)
}

func isNilComponent(component Component) bool {
	return isNilInterface(component)
}

func componentName(component Component) (name string) {
	defer func() {
		if recover() != nil {
			name = fmt.Sprintf("%T", component)
		}
	}()
	return component.Name()
}

func (r *Runtime) Run(ctx context.Context) error {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return errors.New("runtime is already running")
	}
	if r.ran {
		r.mu.Unlock()
		return errors.New("runtime has already run")
	}
	r.running = true
	components := append([]Component(nil), r.components...)
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.running = false
		r.ran = true
		r.mu.Unlock()
	}()

	if ctx.Err() != nil {
		r.logger.Info("shutdown requested before startup")
		return nil
	}

	started := make([]Component, 0, len(components))
	for _, component := range components {
		name := componentName(component)
		r.logger.Info("starting component", "component", name)
		if err := r.startComponent(ctx, component); err != nil {
			if ctx.Err() == nil {
				r.logger.Error("component start failed", "component", name, "error", err)
			}
			stopErr := r.stop(started)
			if ctx.Err() != nil {
				return stopErr
			}
			return errors.Join(err, stopErr)
		}
		started = append(started, component)
		if ctx.Err() != nil {
			r.logger.Info("shutdown requested during startup")
			return r.stop(started)
		}
	}

	watchCtx, stopWatching := context.WithCancel(ctx)
	defer stopWatching()
	failures := make(chan componentFailure, len(started))
	for _, component := range started {
		reporter, ok := component.(FailureReporter)
		if !ok {
			continue
		}
		go watchComponent(watchCtx, component, reporter, r.logger, failures)
	}

	select {
	case <-ctx.Done():
		r.logger.Info("shutdown requested")
		return r.stop(started)
	case failure := <-failures:
		r.logger.Error("component failed", "component", failure.component, "error", failure.err)
		return errors.Join(failure.err, r.stop(started))
	}
}

func (r *Runtime) startComponent(ctx context.Context, component Component) (err error) {
	name := componentName(component)
	defer func() {
		if recovered := recover(); recovered != nil {
			r.logger.Error("component start panicked",
				"component", name,
				"panic", recovered,
				"stack", string(debug.Stack()),
			)
			err = fmt.Errorf("component %s start panicked: %v", name, recovered)
		}
	}()
	return component.Start(ctx)
}

type componentFailure struct {
	component string
	err       error
}

func watchComponent(ctx context.Context, component Component, reporter FailureReporter, logger *slog.Logger, failures chan<- componentFailure) {
	name := componentName(component)
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.Error("component failure reporter panicked",
				"component", name,
				"panic", recovered,
				"stack", string(debug.Stack()),
			)
			failures <- componentFailure{
				component: name,
				err:       fmt.Errorf("component %s failure reporter panicked: %v", name, recovered),
			}
		}
	}()
	select {
	case <-reporter.Failed():
		err := reporter.Failure()
		if isNilInterface(err) {
			err = fmt.Errorf("component %s stopped unexpectedly", name)
		}
		failures <- componentFailure{component: name, err: err}
	case <-ctx.Done():
	}
}

func (r *Runtime) stop(started []Component) error {
	stopCtx, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()

	var errs []error
	for i := len(started) - 1; i >= 0; i-- {
		component := started[i]
		name := componentName(component)
		r.logger.Info("stopping component", "component", name)
		if err := r.stopComponent(stopCtx, component); err != nil {
			r.logger.Error("component stop failed", "component", name, "error", err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (r *Runtime) stopComponent(ctx context.Context, component Component) (err error) {
	name := componentName(component)
	defer func() {
		if recovered := recover(); recovered != nil {
			r.logger.Error("component stop panicked",
				"component", name,
				"panic", recovered,
				"stack", string(debug.Stack()),
			)
			err = fmt.Errorf("component %s stop panicked: %v", name, recovered)
		}
	}()
	return component.Stop(ctx)
}
