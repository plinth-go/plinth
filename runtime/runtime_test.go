package runtime

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordingComponent struct {
	name  string
	mu    *sync.Mutex
	order *[]string
	ready chan<- struct{}
}

type lifecycleComponent struct {
	name  string
	start func() error
	stop  func() error
}

type failingComponent struct {
	failed  chan struct{}
	err     error
	stopped chan struct{}
	once    sync.Once
}

type reentrantStreamCloser struct {
	http *HTTP
}

type typedNilHTTPHandler struct{}

func (*typedNilHTTPHandler) ServeHTTP(http.ResponseWriter, *http.Request) {
	panic("typed nil HTTP handler was used")
}

type typedNilStreamCloser struct{}

func (*typedNilStreamCloser) CloseStreams() {
	panic("typed nil stream closer was called")
}

type panicStreamCloser struct{}

func (panicStreamCloser) CloseStreams() {
	panic("stream closer boom")
}

type panicReporterComponent struct {
	failed   func() <-chan struct{}
	failure  func() error
	stopped  chan struct{}
	stopOnce sync.Once
}

func (*panicReporterComponent) Name() string { return "panic-reporter" }

func (*panicReporterComponent) Start(context.Context) error { return nil }

func (c *panicReporterComponent) Stop(context.Context) error {
	c.stopOnce.Do(func() { close(c.stopped) })
	return nil
}

func (c *panicReporterComponent) Failed() <-chan struct{} { return c.failed() }

func (c *panicReporterComponent) Failure() error { return c.failure() }

type panicNameComponent struct {
	failed  chan struct{}
	stopped chan struct{}
	once    sync.Once
}

type typedNilTestError struct{}

func (*typedNilTestError) Error() string { return "typed nil test error" }

func (*panicNameComponent) Name() string { panic("name boom") }

func (*panicNameComponent) Start(context.Context) error { return nil }

func (c *panicNameComponent) Stop(context.Context) error {
	c.once.Do(func() { close(c.stopped) })
	return nil
}

func (c *panicNameComponent) Failed() <-chan struct{} { return c.failed }

func (*panicNameComponent) Failure() error { return nil }

func requirePanicMessage(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		t.Helper()
		recovered := recover()
		if recovered == nil {
			t.Fatalf("panic was not raised, want %q", want)
		}
		message, ok := recovered.(string)
		if !ok {
			t.Fatalf("panic = %T %v, want string %q", recovered, recovered, want)
		}
		if !strings.Contains(message, want) {
			t.Fatalf("panic = %q, want %q", message, want)
		}
	}()
	fn()
}

func (c reentrantStreamCloser) CloseStreams() {
	_ = c.http.Failure()
	_ = c.http.Stop(context.Background())
}

func (c *failingComponent) Name() string { return "failing" }

func (c *failingComponent) Start(context.Context) error { return nil }

func (c *failingComponent) Stop(context.Context) error {
	c.once.Do(func() { close(c.stopped) })
	return nil
}

func (c *failingComponent) Failed() <-chan struct{} { return c.failed }

func (c *failingComponent) Failure() error { return c.err }

func (c lifecycleComponent) Name() string { return c.name }

func (c lifecycleComponent) Start(context.Context) error {
	if c.start == nil {
		return nil
	}
	return c.start()
}

func (c lifecycleComponent) Stop(context.Context) error {
	if c.stop == nil {
		return nil
	}
	return c.stop()
}

func (c recordingComponent) Name() string { return c.name }

func (c recordingComponent) Start(context.Context) error {
	c.mu.Lock()
	*c.order = append(*c.order, "start-"+c.name)
	c.mu.Unlock()
	if c.ready != nil {
		c.ready <- struct{}{}
	}
	return nil
}

func (c recordingComponent) Stop(context.Context) error {
	c.mu.Lock()
	*c.order = append(*c.order, "stop-"+c.name)
	c.mu.Unlock()
	return nil
}

func TestRuntimeStopsComponentsInReverseOrder(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	rt := New(logger, time.Second)
	var mu sync.Mutex
	var order []string
	ready := make(chan struct{}, 2)
	rt.Add(
		recordingComponent{name: "one", mu: &mu, order: &order, ready: ready},
		recordingComponent{name: "two", mu: &mu, order: &order, ready: ready},
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()
	<-ready
	<-ready
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime did not stop")
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"start-one", "start-two", "stop-two", "stop-one"}
	if len(order) != len(want) {
		t.Fatalf("order = %#v", order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %#v, want %#v", order, want)
		}
	}
}

func TestRuntimeDefaultsShutdownTimeout(t *testing.T) {
	rt := New(nil, 0)
	if rt.timeout != defaultShutdownTimeout {
		t.Fatalf("timeout = %s", rt.timeout)
	}
}

func TestRuntimeAddRejectsNilComponent(t *testing.T) {
	var typedNil *lifecycleComponent
	tests := []struct {
		name       string
		components []Component
		want       string
	}{
		{
			name:       "nil interface",
			components: []Component{nil},
			want:       "runtime: Add received nil component at index 0",
		},
		{
			name:       "typed nil pointer",
			components: []Component{typedNil},
			want:       "runtime: Add received nil component at index 0",
		},
		{
			name: "nil after valid component",
			components: []Component{
				lifecycleComponent{name: "one"},
				nil,
			},
			want: "runtime: Add received nil component at index 1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := New(nil, time.Second)
			requirePanicMessage(t, tt.want, func() {
				rt.Add(tt.components...)
			})
			if len(rt.components) != 0 {
				t.Fatalf("components = %d after rejected Add, want 0", len(rt.components))
			}
		})
	}
}

func TestRuntimeSkipsStartupWhenContextAlreadyCanceled(t *testing.T) {
	started := false
	component := lifecycleComponent{
		name: "one",
		start: func() error {
			started = true
			return nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rt := New(nil, time.Second)
	rt.Add(component)
	if err := rt.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if started {
		t.Fatal("component started after shutdown was already requested")
	}
}

func TestRuntimeStopsWhenCanceledDuringStartup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var starts []string
	var stops []string
	first := lifecycleComponent{
		name: "one",
		start: func() error {
			starts = append(starts, "one")
			cancel()
			return nil
		},
		stop: func() error {
			stops = append(stops, "one")
			return nil
		},
	}
	second := lifecycleComponent{
		name: "two",
		start: func() error {
			starts = append(starts, "two")
			return nil
		},
	}
	rt := New(nil, time.Second)
	rt.Add(first, second)
	if err := rt.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(starts) != 1 || starts[0] != "one" {
		t.Fatalf("starts = %#v", starts)
	}
	if len(stops) != 1 || stops[0] != "one" {
		t.Fatalf("stops = %#v", stops)
	}
}

func TestRuntimeJoinsStartAndStopErrors(t *testing.T) {
	startErr := errors.New("start failed")
	stopErr := errors.New("stop failed")
	first := lifecycleComponent{
		name: "one",
		stop: func() error { return stopErr },
	}
	second := lifecycleComponent{
		name:  "two",
		start: func() error { return startErr },
	}
	rt := New(nil, time.Second)
	rt.Add(first, second)
	err := rt.Run(context.Background())
	if !errors.Is(err, startErr) || !errors.Is(err, stopErr) {
		t.Fatalf("error = %v", err)
	}
}

func TestRuntimeContinuesStoppingAfterPanic(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ready := make(chan struct{})
	var stops []string
	first := lifecycleComponent{
		name: "one",
		stop: func() error {
			stops = append(stops, "one")
			return nil
		},
	}
	second := lifecycleComponent{
		name: "two",
		start: func() error {
			close(ready)
			return nil
		},
		stop: func() error {
			stops = append(stops, "two")
			panic("stop boom")
		},
	}

	rt := New(logger, time.Second)
	rt.Add(first, second)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()
	<-ready
	cancel()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "stop panicked") || !strings.Contains(err.Error(), "stop boom") {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime did not stop")
	}
	if len(stops) != 2 || stops[0] != "two" || stops[1] != "one" {
		t.Fatalf("stops = %#v", stops)
	}
}

func TestRuntimeRecoversComponentStartPanicAndStopsStarted(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var stops []string
	thirdStarted := false
	first := lifecycleComponent{
		name: "one",
		stop: func() error {
			stops = append(stops, "one")
			return nil
		},
	}
	second := lifecycleComponent{
		name: "two",
		start: func() error {
			panic("start boom")
		},
	}
	third := lifecycleComponent{
		name: "three",
		start: func() error {
			thirdStarted = true
			return nil
		},
	}

	rt := New(logger, time.Second)
	rt.Add(first, second, third)
	err := rt.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "start panicked") || !strings.Contains(err.Error(), "start boom") {
		t.Fatalf("error = %v", err)
	}
	if thirdStarted {
		t.Fatal("component after the panic was started")
	}
	if len(stops) != 1 || stops[0] != "one" {
		t.Fatalf("stops = %#v", stops)
	}
}

func TestRuntimeRecoversFailureReporterPanics(t *testing.T) {
	var typedNil *typedNilTestError
	tests := []struct {
		name    string
		failed  func() <-chan struct{}
		failure func() error
		want    string
	}{
		{
			name:    "failed callback",
			failed:  func() <-chan struct{} { panic("failed boom") },
			failure: func() error { return nil },
			want:    "failure reporter panicked: failed boom",
		},
		{
			name: "failure callback",
			failed: func() <-chan struct{} {
				ready := make(chan struct{})
				close(ready)
				return ready
			},
			failure: func() error { panic("failure boom") },
			want:    "failure reporter panicked: failure boom",
		},
		{
			name: "typed-nil failure",
			failed: func() <-chan struct{} {
				ready := make(chan struct{})
				close(ready)
				return ready
			},
			failure: func() error { return typedNil },
			want:    "stopped unexpectedly",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			component := &panicReporterComponent{
				failed:  tc.failed,
				failure: tc.failure,
				stopped: make(chan struct{}),
			}
			rt := New(slog.New(slog.NewTextHandler(io.Discard, nil)), time.Second)
			rt.Add(component)
			err := rt.Run(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Run error = %v, want %q", err, tc.want)
			}
			select {
			case <-component.stopped:
			default:
				t.Fatal("component was not stopped after reporter failure")
			}
		})
	}
}

func TestRuntimeRecoversPanickingComponentName(t *testing.T) {
	component := &panicNameComponent{
		failed:  make(chan struct{}),
		stopped: make(chan struct{}),
	}
	close(component.failed)
	rt := New(slog.New(slog.NewTextHandler(io.Discard, nil)), time.Second)
	rt.Add(component)
	err := rt.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "stopped unexpectedly") {
		t.Fatalf("Run error = %v", err)
	}
	select {
	case <-component.stopped:
	default:
		t.Fatal("component was not stopped after its Name panicked")
	}
}

func TestRuntimeStopsWhenComponentReportsFailure(t *testing.T) {
	failureErr := errors.New("component failed")
	component := &failingComponent{
		failed:  make(chan struct{}),
		err:     failureErr,
		stopped: make(chan struct{}),
	}
	rt := New(nil, time.Second)
	rt.Add(component)
	done := make(chan error, 1)
	go func() { done <- rt.Run(context.Background()) }()
	close(component.failed)

	select {
	case err := <-done:
		if !errors.Is(err, failureErr) {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime did not stop after component failure")
	}
	select {
	case <-component.stopped:
	default:
		t.Fatal("failed component was not stopped")
	}
}

func TestRuntimeRejectsConcurrentRun(t *testing.T) {
	started := make(chan struct{})
	component := lifecycleComponent{
		name: "one",
		start: func() error {
			close(started)
			return nil
		},
	}
	rt := New(nil, time.Second)
	rt.Add(component)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()
	<-started
	if err := rt.Run(context.Background()); err == nil {
		t.Fatal("concurrent Run was accepted")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestHTTPRejectsNilHandler(t *testing.T) {
	component := NewHTTP("127.0.0.1:0", nil, nil, nil)
	if err := component.Start(context.Background()); err == nil {
		t.Fatal("nil handler was accepted")
	}
}

func TestHTTPNameIsStable(t *testing.T) {
	if got := NewHTTP("127.0.0.1:0", http.NotFoundHandler(), nil, nil).Name(); got != "http" {
		t.Fatalf("Name() = %q", got)
	}
}

func TestHTTPRoutesServerErrorsThroughSlog(t *testing.T) {
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	component := NewHTTP("127.0.0.1:0", http.NotFoundHandler(), nil, logger)
	if err := component.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = component.Stop(context.Background())
	})

	component.srv.ErrorLog.Print("connection reset by peer")
	if !strings.Contains(logs.String(), "connection reset by peer") {
		t.Fatalf("server error was not logged: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "level=ERROR") {
		t.Fatalf("server error did not use ERROR level: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "component=http") {
		t.Fatalf("server error did not identify the HTTP component: %s", logs.String())
	}
}

func TestHTTPRejectsBlankAddressBeforeListening(t *testing.T) {
	component := NewHTTP(" \t ", http.NotFoundHandler(), nil, nil)
	if err := component.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "listen address") {
		t.Fatalf("Start error = %v", err)
	}
	if component.srv != nil || component.listener != nil {
		t.Fatal("rejected Start retained server state")
	}
}

func TestHTTPRejectsTypedNilHandler(t *testing.T) {
	var handler *typedNilHTTPHandler
	component := NewHTTP("127.0.0.1:0", handler, nil, nil)
	if err := component.Start(context.Background()); err == nil {
		t.Fatal("typed-nil handler was accepted")
	}
	if component.srv != nil || component.listener != nil {
		t.Fatal("rejected Start retained server state")
	}
}

func TestHTTPNormalizesTypedNilStreamCloser(t *testing.T) {
	var streams *typedNilStreamCloser
	component := NewHTTP("127.0.0.1:0", http.NotFoundHandler(), streams, nil)
	if component.streams != nil {
		t.Fatalf("streams = %#v, want nil", component.streams)
	}
	if err := component.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPStartHonorsCanceledContext(t *testing.T) {
	component := NewHTTP("127.0.0.1:0", http.NotFoundHandler(), nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := component.Start(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start error = %v, want context canceled", err)
	}
	if component.srv != nil || component.listener != nil {
		t.Fatal("canceled Start retained server state")
	}
}

func TestHTTPStopDoesNotHoldLockDuringCallbacks(t *testing.T) {
	component := NewHTTP("127.0.0.1:0", http.NotFoundHandler(), nil, nil)
	component.streams = reentrantStreamCloser{http: component}

	done := make(chan error, 1)
	go func() {
		done <- component.Stop(context.Background())
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Stop deadlocked while closing streams")
	}
}

func TestHTTPStopContinuesAfterStreamCloserPanic(t *testing.T) {
	component := NewHTTP("127.0.0.1:0", http.NotFoundHandler(), panicStreamCloser{}, nil)
	if err := component.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	component.mu.Lock()
	addr := component.listener.Addr().String()
	component.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := component.Stop(ctx)
	if err == nil || !strings.Contains(err.Error(), "close streams panicked") {
		t.Fatalf("Stop error = %v", err)
	}

	conn, dialErr := net.DialTimeout("tcp", addr, 100*time.Millisecond)
	if dialErr == nil {
		_ = conn.Close()
		t.Fatal("listener remained open after stream closer panic")
	}
}

func TestHTTPReportsUnexpectedServeFailure(t *testing.T) {
	component := NewHTTP("127.0.0.1:0", http.NotFoundHandler(), nil, nil)
	if err := component.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	component.mu.Lock()
	listener := component.listener
	component.mu.Unlock()
	if listener == nil {
		t.Fatal("listener was not recorded")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-component.Failed():
		if component.Failure() == nil {
			t.Fatal("failure cause is nil")
		}
	case <-time.After(time.Second):
		t.Fatal("unexpected Serve failure was not reported")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := component.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPRejectsRepeatedStart(t *testing.T) {
	component := NewHTTP("127.0.0.1:0", http.NotFoundHandler(), nil, nil)
	if err := component.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := component.Start(context.Background()); err == nil {
		t.Fatal("repeated start was accepted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := component.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPStopIsIdempotent(t *testing.T) {
	component := NewHTTP("127.0.0.1:0", http.NotFoundHandler(), nil, nil)
	if err := component.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := component.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := component.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPRejectsStartAfterStop(t *testing.T) {
	component := NewHTTP("127.0.0.1:0", http.NotFoundHandler(), nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := component.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := component.Start(context.Background()); err == nil {
		t.Fatal("start after stop was accepted")
	}
}

func TestHTTPConcurrentStartAllowsOneListener(t *testing.T) {
	component := NewHTTP("127.0.0.1:0", http.NotFoundHandler(), nil, nil)
	const callers = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	started := 0
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := component.Start(context.Background()); err == nil {
				mu.Lock()
				started++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if started != 1 {
		t.Fatalf("successful starts = %d", started)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := component.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}
