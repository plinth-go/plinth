package kernel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

var ErrStreamsClosed = errors.New("kernel: stream registry is closed")

const sseWriteTimeout = 10 * time.Second

// StreamRegistry owns the cancellation hooks for long-lived responses. The
// HTTP component closes it before http.Server.Shutdown so SSE/WS handlers can
// return instead of keeping shutdown blocked until its timeout.
type StreamRegistry struct {
	mu      sync.Mutex
	next    uint64
	closed  bool
	closers map[uint64]func()
	logger  *slog.Logger
}

func NewStreamRegistry(loggers ...*slog.Logger) *StreamRegistry {
	logger := slog.Default()
	if len(loggers) > 0 && loggers[0] != nil {
		logger = loggers[0]
	}
	return &StreamRegistry{closers: map[uint64]func(){}, logger: logger}
}

func (r *StreamRegistry) Add(closeFn func()) (remove func(), ok bool) {
	if r == nil {
		return func() {}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return func() {}, false
	}
	if closeFn == nil {
		return func() {}, true
	}
	if r.closers == nil {
		r.closers = map[uint64]func(){}
	}
	r.next++
	id := r.next
	r.closers[id] = closeFn
	return func() {
		r.mu.Lock()
		delete(r.closers, id)
		r.mu.Unlock()
	}, true
}

func (r *StreamRegistry) CloseStreams() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	closers := make([]func(), 0, len(r.closers))
	for _, closeFn := range r.closers {
		closers = append(closers, closeFn)
	}
	clear(r.closers)
	r.mu.Unlock()

	for _, closeFn := range closers {
		r.closeStream(closeFn)
	}
}

func (r *StreamRegistry) closeStream(closeFn func()) {
	defer func() {
		if recovered := recover(); recovered != nil {
			logger := r.logger
			if logger == nil {
				logger = slog.Default()
			}
			logger.Error("stream closer panicked",
				"panic", recovered,
				"stack", string(debug.Stack()),
			)
		}
	}()
	closeFn()
}

func (r *StreamRegistry) Closed() bool {
	if r == nil {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// SSE writes server-sent events and bounds each network write when the response
// writer supports write deadlines.
type SSE struct {
	w          http.ResponseWriter
	controller *http.ResponseController
}

func (r *StreamRegistry) ServeSSE(w http.ResponseWriter, req *http.Request, fn func(context.Context, *SSE) error) error {
	if isNilInterface(w) {
		return errors.New("kernel: SSE response writer is nil")
	}
	if isNilInterface(req) {
		return errors.New("kernel: SSE request is nil")
	}
	if fn == nil {
		http.Error(w, "SSE callback is required", http.StatusInternalServerError)
		return errors.New("kernel: SSE callback is nil")
	}
	if r == nil {
		http.Error(w, "server is shutting down", http.StatusServiceUnavailable)
		return ErrStreamsClosed
	}
	if _, ok := responseFlusher(w); !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return errors.New("kernel: response writer does not support flushing")
	}

	ctx, cancel := context.WithCancel(req.Context())
	remove, ok := r.Add(cancel)
	if !ok {
		cancel()
		http.Error(w, "server is shutting down", http.StatusServiceUnavailable)
		return ErrStreamsClosed
	}
	defer func() {
		remove()
		cancel()
	}()

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	controller := http.NewResponseController(w)
	sse := &SSE{w: w, controller: controller}
	if err := sse.withWriteDeadline(func() error {
		if err := controller.Flush(); err != nil {
			return fmt.Errorf("flush SSE headers: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}

	return fn(ctx, sse)
}

// Event writes one complete event in a single bounded write.
func (s *SSE) Event(name string, value any) error {
	if s == nil {
		return errors.New("kernel: SSE stream is nil")
	}
	var event bytes.Buffer
	if name != "" {
		fmt.Fprintf(&event, "event: %s\n", sanitizeSSELine(name))
	}
	var data []byte
	switch value := value.(type) {
	case string:
		data = []byte(value)
	case []byte:
		data = value
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("encode SSE data: %w", err)
		}
		data = encoded
	}
	for _, line := range sseDataLines(data) {
		fmt.Fprintf(&event, "data: %s\n", sanitizeSSELine(line))
	}
	event.WriteByte('\n')
	return s.write(event.Bytes())
}

// Comment writes one complete comment block in a single bounded write.
func (s *SSE) Comment(text string) error {
	if s == nil {
		return errors.New("kernel: SSE stream is nil")
	}
	var comment bytes.Buffer
	for _, line := range sseTextLines(text) {
		fmt.Fprintf(&comment, ": %s\n", sanitizeSSELine(line))
	}
	comment.WriteByte('\n')
	return s.write(comment.Bytes())
}

func (s *SSE) write(payload []byte) error {
	if s == nil || isNilInterface(s.w) || s.controller == nil {
		return errors.New("kernel: SSE stream is not initialized")
	}
	return s.withWriteDeadline(func() error {
		n, err := s.w.Write(payload)
		if err == nil && n != len(payload) {
			err = io.ErrShortWrite
		}
		if err != nil {
			return err
		}
		if err := s.controller.Flush(); err != nil {
			return fmt.Errorf("flush SSE event: %w", err)
		}
		return nil
	})
}

func (s *SSE) withWriteDeadline(fn func() error) (err error) {
	if err := s.controller.SetWriteDeadline(time.Now().Add(sseWriteTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return fmt.Errorf("set SSE write deadline: %w", err)
	}
	defer func() {
		if clearErr := s.controller.SetWriteDeadline(time.Time{}); clearErr != nil && !errors.Is(clearErr, http.ErrNotSupported) && err == nil {
			err = fmt.Errorf("clear SSE write deadline: %w", clearErr)
		}
	}()
	return fn()
}

func sanitizeSSELine(value string) string {
	return strings.NewReplacer("\r", "", "\n", " ").Replace(value)
}

func sseDataLines(data []byte) []string {
	return sseTextLines(string(data))
}

func sseTextLines(value string) []string {
	normalized := strings.ReplaceAll(value, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	return strings.Split(normalized, "\n")
}
