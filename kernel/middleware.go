package kernel

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"sync"
	"time"
)

type requestIDKey struct{}

const maxRequestIDLength = 128

func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// RequestID reuses a client-supplied X-Request-ID only when it is unambiguous
// and safe to echo. Otherwise it generates a fresh ID. The final ID is shared by
// the response header, request context, Problem instance, and access log.
func RequestID() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			values := r.Header.Values("X-Request-ID")
			id := ""
			if len(values) == 1 && validRequestID(values[0]) {
				id = values[0]
			} else {
				id = newRequestID()
			}
			w.Header().Set("X-Request-ID", id)
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
		})
	}
}

func validRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLength {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] < '!' || id[i] > '~' {
			return false
		}
	}
	return true
}

// Recover isolates handler panics and writes a Problem when the response has
// not been committed. Place AccessLog outside Recover so the access log can
// observe the recovered response and its final byte count.
func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			state := acquireStatusWriter(w)
			baseHeaders := snapshotHeaders(state.Header())
			defer func() {
				if rec := recover(); rec != nil {
					logger.Error("panic recovered",
						"method", r.Method,
						"path", r.URL.Path,
						"panic", rec,
						"response_committed", state.wroteHeader,
						"stack", string(debug.Stack()),
					)
					if state.wroteHeader {
						releaseStatusWriter(state)
						return
					}
					noStore := state.Header().Get("Cache-Control") == "no-store"
					nosniff := state.Header().Get("X-Content-Type-Options") == "nosniff"
					baseHeaders.restore(state.Header())
					if noStore {
						state.Header().Set("Cache-Control", "no-store")
					}
					if nosniff {
						state.Header().Set("X-Content-Type-Options", "nosniff")
					}
					WriteProblem(state, r, Internal(nil), logger.Error)
				}
				releaseStatusWriter(state)
			}()
			next.ServeHTTP(state, r)
		})
	}
}

// AccessLog records the method, path, status, bytes written, and duration of a
// request. For an accurate status and byte count after a recovered panic, wrap
// Recover inside AccessLog; in the opposite order the inner access log cannot
// observe the response written by the outer recovery.
func AccessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := acquireStatusWriter(w)
			defer func() {
				status := sw.status
				recovered := recover()
				if recovered != nil && !sw.wroteHeader {
					status = http.StatusInternalServerError
				}
				ctx := r.Context()
				requestID := RequestIDFromContext(ctx)
				duration := time.Since(start)
				if recovered != nil {
					logger.LogAttrs(ctx, slog.LevelInfo, "http request",
						slog.String("request_id", requestID),
						slog.String("method", r.Method),
						slog.String("path", r.URL.Path),
						slog.Int("status", status),
						slog.Int("bytes", sw.bytes),
						slog.Duration("duration", duration),
						slog.String("panic", fmt.Sprint(recovered)),
					)
				} else {
					logger.LogAttrs(ctx, slog.LevelInfo, "http request",
						slog.String("request_id", requestID),
						slog.String("method", r.Method),
						slog.String("path", r.URL.Path),
						slog.Int("status", status),
						slog.Int("bytes", sw.bytes),
						slog.Duration("duration", duration),
					)
				}
				releaseStatusWriter(sw)
				if recovered != nil {
					panic(recovered)
				}
			}()
			next.ServeHTTP(sw, r)
		})
	}
}

func BodyLimit(maxBytes int64) func(http.Handler) http.Handler {
	if maxBytes < 0 {
		panic(fmt.Sprintf("kernel: BodyLimit must not be negative, got %d", maxBytes))
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isNilInterface(r.Body) && r.Body != http.NoBody {
				if r.ContentLength > maxBytes {
					WriteProblem(w, r, TooLarge(fmt.Sprintf("request body exceeds %d bytes", maxBytes)), nil)
					return
				}
				r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			}
			next.ServeHTTP(w, r)
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

var statusWriterPool = sync.Pool{New: func() any { return new(statusWriter) }}

func acquireStatusWriter(w http.ResponseWriter) *statusWriter {
	state := statusWriterPool.Get().(*statusWriter)
	state.ResponseWriter = w
	state.status = http.StatusOK
	state.bytes = 0
	state.wroteHeader = false
	return state
}

func releaseStatusWriter(state *statusWriter) {
	state.ResponseWriter = nil
	state.status = 0
	state.bytes = 0
	state.wroteHeader = false
	statusWriterPool.Put(state)
}

const inlineHeaderSnapshotLimit = 8

type headerSnapshotEntry struct {
	key   string
	value string
}

type headerSnapshot struct {
	entries  [inlineHeaderSnapshotLimit]headerSnapshotEntry
	count    int
	overflow http.Header
}

func snapshotHeaders(src http.Header) headerSnapshot {
	if len(src) > inlineHeaderSnapshotLimit {
		return headerSnapshot{overflow: src.Clone()}
	}
	var snapshot headerSnapshot
	for key, values := range src {
		if len(values) != 1 {
			return headerSnapshot{overflow: src.Clone()}
		}
		snapshot.entries[snapshot.count] = headerSnapshotEntry{key: key, value: values[0]}
		snapshot.count++
	}
	return snapshot
}

func (snapshot *headerSnapshot) restore(dst http.Header) {
	clear(dst)
	if snapshot.overflow != nil {
		resetHeaders(dst, snapshot.overflow)
		return
	}
	for i := 0; i < snapshot.count; i++ {
		entry := snapshot.entries[i]
		dst[entry.key] = []string{entry.value}
	}
}

func (w *statusWriter) WriteHeader(status int) {
	if status >= 100 && status <= 199 && status != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	w.wroteHeader = true
	n, err := w.ResponseWriter.Write(p)
	w.bytes += n
	return n, err
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) Flush() {
	_ = w.FlushError()
}

func (w *statusWriter) FlushError() error {
	err := http.NewResponseController(w.ResponseWriter).Flush()
	if err == nil || !errors.Is(err, http.ErrNotSupported) {
		w.wroteHeader = true
	}
	return err
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := responseHijacker(w.ResponseWriter)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	conn, rw, err := h.Hijack()
	if err == nil {
		w.wroteHeader = true
		w.status = http.StatusSwitchingProtocols
	}
	return conn, rw, err
}

func (w *statusWriter) ReadFrom(src io.Reader) (int64, error) {
	w.wroteHeader = true
	if rf, ok := responseReaderFrom(w.ResponseWriter); ok {
		n, err := rf.ReadFrom(src)
		w.bytes += int(n)
		return n, err
	}
	return io.Copy(struct{ io.Writer }{w}, src)
}

func responseFlusher(rw http.ResponseWriter) (func() error, bool) {
	for {
		switch writer := rw.(type) {
		case interface{ FlushError() error }:
			return writer.FlushError, true
		case http.Flusher:
			return func() error {
				writer.Flush()
				return nil
			}, true
		case interface{ Unwrap() http.ResponseWriter }:
			rw = writer.Unwrap()
		default:
			return nil, false
		}
	}
}

func responseHijacker(rw http.ResponseWriter) (http.Hijacker, bool) {
	for {
		switch writer := rw.(type) {
		case http.Hijacker:
			return writer, true
		case interface{ Unwrap() http.ResponseWriter }:
			rw = writer.Unwrap()
		default:
			return nil, false
		}
	}
}

func responseReaderFrom(rw http.ResponseWriter) (io.ReaderFrom, bool) {
	for {
		switch writer := rw.(type) {
		case io.ReaderFrom:
			return writer, true
		case interface{ Unwrap() http.ResponseWriter }:
			rw = writer.Unwrap()
		default:
			return nil, false
		}
	}
}

func newRequestID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return time.Now().UTC().Format("20060102150405.000000000")
	}
	return hex.EncodeToString(b[:])
}
