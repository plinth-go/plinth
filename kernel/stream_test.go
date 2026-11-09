package kernel

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type failingSSEValue struct{}

func (failingSSEValue) MarshalJSON() ([]byte, error) {
	return nil, errors.New("sse marshal failure")
}

type sseDeadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (r *sseDeadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	r.deadlines = append(r.deadlines, deadline)
	return nil
}

func TestServeSSEAndShutdownCancellation(t *testing.T) {
	streams := NewStreamRegistry()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/events", nil)

	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- streams.ServeSSE(rec, req, func(ctx context.Context, sse *SSE) error {
			if err := sse.Event("tick", "hello"); err != nil {
				return err
			}
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
	}()

	<-started
	streams.CloseStreams()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ServeSSE error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SSE callback did not stop after CloseStreams")
	}

	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("Content-Type = %q", got)
	}
	if !strings.Contains(rec.Body.String(), "event: tick") || !strings.Contains(rec.Body.String(), "data: hello") {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
}

func TestServeSSERejectsNilCallbackBeforeWriting(t *testing.T) {
	streams := NewStreamRegistry()
	rec := httptest.NewRecorder()
	if err := streams.ServeSSE(rec, httptest.NewRequest(http.MethodGet, "/events", nil), nil); err == nil {
		t.Fatal("nil SSE callback was accepted")
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Header().Get("Content-Type") == "text/event-stream; charset=utf-8" {
		t.Fatal("nil SSE callback committed the stream")
	}
}

func TestServeSSERejectsNilInputsBeforeWriting(t *testing.T) {
	streams := NewStreamRegistry()
	var writer *nilResponseWriter
	if err := streams.ServeSSE(writer, httptest.NewRequest(http.MethodGet, "/events", nil), nil); err == nil {
		t.Fatal("typed-nil response writer was accepted")
	}
	if err := streams.ServeSSE(httptest.NewRecorder(), nil, nil); err == nil {
		t.Fatal("nil request was accepted")
	}
}

func TestSSEMethodsRejectNilReceivers(t *testing.T) {
	var sse *SSE
	if err := sse.Event("event", "data"); err == nil {
		t.Fatal("nil SSE accepted an event")
	}
	if err := sse.Comment("comment"); err == nil {
		t.Fatal("nil SSE accepted a comment")
	}
	if err := sse.write([]byte("data")); err == nil {
		t.Fatal("nil SSE accepted a write")
	}
}

func TestSSECommentIsOneSanitizedBlock(t *testing.T) {
	streams := NewStreamRegistry()
	rec := httptest.NewRecorder()
	err := streams.ServeSSE(rec, httptest.NewRequest(http.MethodGet, "/events", nil), func(_ context.Context, sse *SSE) error {
		return sse.Comment("ready\r\n: injected\rnext")
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := rec.Body.String(), ": ready\n: : injected\n: next\n\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestSSEEventBoundsWritesWithDeadline(t *testing.T) {
	streams := NewStreamRegistry()
	rec := &sseDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	req := httptest.NewRequest(http.MethodGet, "/events", nil)

	err := streams.ServeSSE(rec, req, func(_ context.Context, sse *SSE) error {
		return sse.Event("tick", "hello")
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.deadlines) != 4 {
		t.Fatalf("deadline calls = %d, want 4", len(rec.deadlines))
	}
	if rec.deadlines[0].IsZero() || !rec.deadlines[1].IsZero() || rec.deadlines[2].IsZero() || !rec.deadlines[3].IsZero() {
		t.Fatalf("deadlines = %#v", rec.deadlines)
	}
	if !strings.Contains(rec.Body.String(), "event: tick") || !strings.Contains(rec.Body.String(), "data: hello") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

type sseClearDeadlineFailureWriter struct {
	*httptest.ResponseRecorder
	clears int
	failOn int
}

func (w *sseClearDeadlineFailureWriter) SetWriteDeadline(deadline time.Time) error {
	if deadline.IsZero() {
		w.clears++
		if w.clears == w.failOn {
			return errors.New("deadline clear failure")
		}
	}
	return nil
}

func TestServeSSEReturnsInitialDeadlineClearFailure(t *testing.T) {
	streams := NewStreamRegistry()
	rec := &sseClearDeadlineFailureWriter{ResponseRecorder: httptest.NewRecorder(), failOn: 1}
	called := false

	err := streams.ServeSSE(rec, httptest.NewRequest(http.MethodGet, "/events", nil), func(context.Context, *SSE) error {
		called = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "clear SSE write deadline: deadline clear failure") {
		t.Fatalf("error = %v", err)
	}
	if called {
		t.Fatal("callback ran after the initial flush deadline could not be cleared")
	}
}

func TestSSEEventReturnsDeadlineClearFailure(t *testing.T) {
	streams := NewStreamRegistry()
	rec := &sseClearDeadlineFailureWriter{ResponseRecorder: httptest.NewRecorder(), failOn: 2}

	err := streams.ServeSSE(rec, httptest.NewRequest(http.MethodGet, "/events", nil), func(_ context.Context, sse *SSE) error {
		return sse.Event("tick", "hello")
	})
	if err == nil || !strings.Contains(err.Error(), "clear SSE write deadline: deadline clear failure") {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(rec.Body.String(), "event: tick") || !strings.Contains(rec.Body.String(), "data: hello") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestClosedStreamRegistryRejectsNilCloser(t *testing.T) {
	streams := NewStreamRegistry()
	streams.CloseStreams()
	if _, ok := streams.Add(nil); ok {
		t.Fatal("closed registry accepted a stream")
	}
}

func TestNilStreamRegistryIsClosed(t *testing.T) {
	var streams *StreamRegistry
	if !streams.Closed() {
		t.Fatal("nil stream registry reported open")
	}
	if _, ok := streams.Add(nil); ok {
		t.Fatal("nil stream registry accepted a stream")
	}
	streams.CloseStreams()

	rec := httptest.NewRecorder()
	err := streams.ServeSSE(rec, httptest.NewRequest(http.MethodGet, "/events", nil), func(context.Context, *SSE) error {
		return nil
	})
	if !errors.Is(err, ErrStreamsClosed) {
		t.Fatalf("ServeSSE error = %v, want ErrStreamsClosed", err)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got == "text/event-stream; charset=utf-8" {
		t.Fatal("nil registry committed an event stream")
	}
}

func TestStreamRegistryZeroValue(t *testing.T) {
	var streams StreamRegistry
	closed := 0
	if _, ok := streams.Add(func() { closed++ }); !ok {
		t.Fatal("zero-value registry rejected a stream")
	}
	if streams.Closed() {
		t.Fatal("zero-value registry reported closed before shutdown")
	}
	streams.CloseStreams()
	if closed != 1 {
		t.Fatalf("closed = %d, want 1", closed)
	}
	if !streams.Closed() {
		t.Fatal("zero-value registry did not close")
	}
}

func TestZeroValueStreamRegistryIsolatesPanickingCloser(t *testing.T) {
	oldDefault := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(oldDefault) })

	var streams StreamRegistry
	streams.Add(func() { panic("closer boom") })
	streams.CloseStreams()
	if !streams.Closed() {
		t.Fatal("registery did not close after closer panic")
	}
}

func TestCloseStreamsContinuesAfterCloserPanic(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	streams := NewStreamRegistry(logger)
	closed := 0
	streams.Add(func() { closed++ })
	streams.Add(func() { panic("closer boom") })
	streams.Add(func() { closed++ })

	streams.CloseStreams()
	if closed != 2 {
		t.Fatalf("closed = %d, want 2", closed)
	}
	if !streams.Closed() {
		t.Fatal("registry did not close")
	}
	if !strings.Contains(logs.String(), "stream closer panicked") || !strings.Contains(logs.String(), "closer boom") {
		t.Fatalf("logs = %q", logs.String())
	}
}

func TestSSEEventNormalizesCarriageReturns(t *testing.T) {
	streams := NewStreamRegistry()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/events", nil)

	err := streams.ServeSSE(rec, req, func(_ context.Context, sse *SSE) error {
		return sse.Event("tick", "line1\r\nevent: injected\rdata: hidden")
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "event: tick\ndata: line1\ndata: event: injected\ndata: data: hidden\n\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %q, want %q", rec.Body.String(), want)
	}
	eventLines := 0
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if strings.HasPrefix(line, "event:") {
			eventLines++
		}
	}
	if eventLines != 1 {
		t.Fatalf("SSE field injection produced %d event lines: %q", eventLines, rec.Body.String())
	}
}

func TestSSEEventMarshalFailureWritesNoPartialEvent(t *testing.T) {
	streams := NewStreamRegistry()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/events", nil)

	err := streams.ServeSSE(rec, req, func(_ context.Context, sse *SSE) error {
		return sse.Event("tick", failingSSEValue{})
	})
	if err == nil || !strings.Contains(err.Error(), "sse marshal failure") {
		t.Fatalf("error = %v", err)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("partial SSE event written: %q", rec.Body.String())
	}
}

type shortSSEWriter struct {
	header http.Header
}

func (w *shortSSEWriter) Header() http.Header { return w.header }

func (w *shortSSEWriter) WriteHeader(int) {}

func (w *shortSSEWriter) Write(p []byte) (int, error) {
	return len(p) - 1, nil
}

func (w *shortSSEWriter) Flush() {}

func TestSSEEventRejectsShortWrite(t *testing.T) {
	streams := NewStreamRegistry()
	w := &shortSSEWriter{header: make(http.Header)}
	req := httptest.NewRequest(http.MethodGet, "/events", nil)

	err := streams.ServeSSE(w, req, func(_ context.Context, sse *SSE) error {
		return sse.Event("tick", "hello")
	})
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("error = %v, want io.ErrShortWrite", err)
	}
}

type failingFlushWriter struct {
	header  http.Header
	flushes int
	failOn  int
	body    strings.Builder
}

func (w *failingFlushWriter) Header() http.Header { return w.header }

func (w *failingFlushWriter) WriteHeader(int) {}

func (w *failingFlushWriter) Write(p []byte) (int, error) {
	return w.body.Write(p)
}

func (w *failingFlushWriter) Flush() {}

func (w *failingFlushWriter) FlushError() error {
	w.flushes++
	if w.flushes == w.failOn {
		return errors.New("client disconnected")
	}
	return nil
}

func TestSSEEventReturnsFlushFailure(t *testing.T) {
	streams := NewStreamRegistry()
	w := &failingFlushWriter{header: make(http.Header), failOn: 2}
	req := httptest.NewRequest(http.MethodGet, "/events", nil)

	err := streams.ServeSSE(w, req, func(_ context.Context, sse *SSE) error {
		return sse.Event("tick", "hello")
	})
	if err == nil || !strings.Contains(err.Error(), "flush SSE event: client disconnected") {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(w.body.String(), "event: tick") || !strings.Contains(w.body.String(), "data: hello") {
		t.Fatalf("body = %q", w.body.String())
	}
}

func TestServeSSEReturnsInitialFlushFailure(t *testing.T) {
	streams := NewStreamRegistry()
	w := &failingFlushWriter{header: make(http.Header), failOn: 1}
	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	called := false

	err := streams.ServeSSE(w, req, func(context.Context, *SSE) error {
		called = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "flush SSE headers: client disconnected") {
		t.Fatalf("error = %v", err)
	}
	if called {
		t.Fatal("callback ran after the response stream failed to flush")
	}
}

func TestSSEFlushFailurePropagatesThroughAccessLog(t *testing.T) {
	streams := NewStreamRegistry()
	w := &failingFlushWriter{header: make(http.Header), failOn: 2}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	var serveErr error
	handler := AccessLog(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveErr = streams.ServeSSE(w, r, func(_ context.Context, sse *SSE) error {
			return sse.Event("tick", "hello")
		})
	}))

	handler.ServeHTTP(w, req)
	if serveErr == nil || !strings.Contains(serveErr.Error(), "flush SSE event: client disconnected") {
		t.Fatalf("error = %v", serveErr)
	}
}

func TestOpsWriterRejectsUnsupportedControlBeforeCommit(t *testing.T) {
	rec := &unsupportedControlWriter{header: make(http.Header)}
	w := &opsResponseWriter{ResponseWriter: rec}
	if _, _, err := w.Hijack(); !errors.Is(err, http.ErrNotSupported) {
		t.Fatalf("hijack error = %v", err)
	}
	if err := w.FlushError(); !errors.Is(err, http.ErrNotSupported) {
		t.Fatalf("flush error = %v", err)
	}
	if len(rec.codes) != 0 {
		t.Fatalf("unsupported control committed status %v", rec.codes)
	}
}

type failingHijackWriter struct {
	header http.Header
	codes  []int
	body   bytes.Buffer
}

func (w *failingHijackWriter) Header() http.Header { return w.header }

func (w *failingHijackWriter) WriteHeader(status int) { w.codes = append(w.codes, status) }

func (w *failingHijackWriter) Write(p []byte) (int, error) { return w.body.Write(p) }

func (w *failingHijackWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, errors.New("hijack failed")
}

func TestOpsWriterHijackFailureDoesNotCommit(t *testing.T) {
	rec := &failingHijackWriter{header: make(http.Header)}
	w := &opsResponseWriter{ResponseWriter: rec}
	if _, _, err := w.Hijack(); err == nil || !strings.Contains(err.Error(), "hijack failed") {
		t.Fatalf("hijack error = %v", err)
	}
	if w.wroteHeader || len(rec.codes) != 0 {
		t.Fatalf("hijack failure committed response: wroteHeader=%v codes=%v", w.wroteHeader, rec.codes)
	}
}

func TestRecoverWritesProblemAfterOpsHijackFailure(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	rec := &failingHijackWriter{header: make(http.Header)}
	handler := Recover(logger)(noStore(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _, _ = w.(http.Hijacker).Hijack()
		panic("boom")
	})))

	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ops", nil))
	if len(rec.codes) != 1 || rec.codes[0] != http.StatusInternalServerError {
		t.Fatalf("codes = %v, body = %q", rec.codes, rec.body.String())
	}
	if !strings.Contains(rec.body.String(), `"status":500`) {
		t.Fatalf("body = %q", rec.body.String())
	}
	if rec.header.Get("Cache-Control") != "no-store" || rec.header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("policy headers = %#v", rec.header)
	}
}

func TestWriteCSVAttachment(t *testing.T) {
	rec := httptest.NewRecorder()
	err := WriteCSV(rec, "items.csv", []string{"id", "name"}, func(yield func([]string) error) error {
		return yield([]string{"1", "alpha"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/csv") {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, "items.csv") {
		t.Fatalf("Content-Disposition = %q", got)
	}
	if rec.Body.String() != "id,name\n1,alpha\n" {
		t.Fatalf("body = %q", rec.Body.String())
	}
}
