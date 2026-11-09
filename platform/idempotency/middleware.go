package idempotency

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/plinth-go/plinth/kernel"
)

const (
	defaultHeaderName       = "Idempotency-Key"
	defaultTTL              = 24 * time.Hour
	defaultMaxRequestBytes  = 1 << 20
	defaultMaxResponseBytes = 4 << 20
)

// Config controls HTTP idempotency handling. Defaults are 24h TTL, 1 MiB
// request bodies, 4 MiB buffered responses, and POST/PATCH/DELETE methods.
// This middleware is intended for bounded, non-streaming endpoints.
type Config struct {
	HeaderName       string
	TTL              time.Duration
	MaxRequestBytes  int64
	MaxResponseBytes int64
	Methods          []string
	RequireKey       bool
}

// Middleware builds an HTTP middleware. Missing keys pass through unless
// RequireKey is set. A key reserves the operation before the handler runs;
// concurrent requests get 409, completed responses replay exactly once per
// key, and different request fingerprints are rejected.
func Middleware(store Store, cfg Config, logger *slog.Logger) (func(http.Handler) http.Handler, error) {
	if store == nil {
		return nil, errors.New("idempotency store is nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	cfg, err := normalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	methods := make(map[string]bool, len(cfg.Methods))
	for _, method := range cfg.Methods {
		methods[method] = true
	}
	logger = logger.With("component", "idempotency")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if next == nil {
				kernel.WriteProblem(w, r, kernel.Internal(errors.New("idempotency next handler is nil")), logger.Error)
				return
			}
			if !methods[r.Method] {
				next.ServeHTTP(w, r)
				return
			}
			key := strings.TrimSpace(r.Header.Get(cfg.HeaderName))
			if key == "" {
				if cfg.RequireKey {
					kernel.WriteProblem(w, r, kernel.BadRequest(cfg.HeaderName+" is required"), logger.Error)
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			if err := validateHTTPKey(key); err != nil {
				kernel.WriteProblem(w, r, kernel.BadRequest(err.Error()), logger.Error)
				return
			}
			body, err := readRequestBody(r, cfg.MaxRequestBytes)
			if err != nil {
				kernel.WriteProblem(w, r, err, logger.Error)
				return
			}
			fingerprint := requestFingerprint(r, body)
			pending := Record{
				Fingerprint: fingerprint,
				State:       statePending,
				CreatedAt:   time.Now().UTC(),
			}
			existing, acquired, err := store.Reserve(r.Context(), key, pending, cfg.TTL)
			if err != nil {
				kernel.WriteProblem(w, r, kernel.Internal(err), logger.Error)
				return
			}
			if !acquired {
				writeExisting(w, r, existing, fingerprint, cfg.HeaderName, logger)
				return
			}

			completed := false
			defer func() {
				if !completed {
					ctx := r.Context()
					if err := store.Abort(ctx, key, fingerprint); err != nil {
						logger.Warn("abort idempotency reservation", "error", err)
					}
				}
			}()

			capture := newCaptureWriter(cfg.MaxResponseBytes)
			next.ServeHTTP(capture, r)
			if capture.overflow {
				kernel.WriteProblem(w, r, kernel.Internal(errors.New("idempotent response exceeds configured limit")), logger.Error)
				return
			}
			if capture.status >= http.StatusInternalServerError {
				capture.writeTo(w)
				return
			}
			record := Record{
				Fingerprint: fingerprint,
				State:       stateComplete,
				Status:      capture.status,
				Header:      storedHeaders(capture.header),
				Body:        append([]byte(nil), capture.body.Bytes()...),
				CreatedAt:   pending.CreatedAt,
			}
			if err := store.Complete(r.Context(), key, record, cfg.TTL); err != nil {
				kernel.WriteProblem(w, r, kernel.Internal(err), logger.Error)
				return
			}
			completed = true
			capture.writeTo(w)
		})
	}, nil
}

func normalizeConfig(cfg Config) (Config, error) {
	cfg.HeaderName = strings.TrimSpace(cfg.HeaderName)
	if cfg.HeaderName == "" {
		cfg.HeaderName = defaultHeaderName
	}
	if cfg.TTL == 0 {
		cfg.TTL = defaultTTL
	}
	if cfg.TTL < 0 {
		return Config{}, errors.New("idempotency TTL must be positive")
	}
	if cfg.MaxRequestBytes == 0 {
		cfg.MaxRequestBytes = defaultMaxRequestBytes
	}
	if cfg.MaxRequestBytes < 0 {
		return Config{}, errors.New("idempotency max request bytes must be positive")
	}
	if cfg.MaxResponseBytes == 0 {
		cfg.MaxResponseBytes = defaultMaxResponseBytes
	}
	if cfg.MaxResponseBytes < 0 {
		return Config{}, errors.New("idempotency max response bytes must be positive")
	}
	if len(cfg.Methods) == 0 {
		cfg.Methods = []string{http.MethodPost, http.MethodPatch, http.MethodDelete}
	}
	for index, method := range cfg.Methods {
		method = strings.ToUpper(strings.TrimSpace(method))
		if method == "" {
			return Config{}, errors.New("idempotency method must not be empty")
		}
		cfg.Methods[index] = method
	}
	return cfg, nil
}

func validateHTTPKey(key string) error {
	if len(key) > 255 {
		return errors.New("idempotency key must not exceed 255 bytes")
	}
	for _, char := range key {
		if char < 0x21 || char > 0x7e {
			return errors.New("idempotency key must contain printable non-space ASCII")
		}
	}
	return nil
}

func readRequestBody(r *http.Request, limit int64) ([]byte, error) {
	if r.Body == nil || r.Body == http.NoBody {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, kernel.BadRequest("read request body: " + err.Error())
	}
	if int64(len(body)) > limit {
		return nil, kernel.TooLarge("request body exceeds idempotency limit")
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

func requestFingerprint(r *http.Request, body []byte) string {
	hash := sha256.New()
	_, _ = io.WriteString(hash, r.Method)
	_, _ = io.WriteString(hash, "\n")
	_, _ = io.WriteString(hash, r.URL.RequestURI())
	_, _ = io.WriteString(hash, "\n")
	_, _ = io.WriteString(hash, r.Header.Get("Content-Type"))
	_, _ = io.WriteString(hash, "\n")
	_, _ = hash.Write(body)
	return hex.EncodeToString(hash.Sum(nil))
}

func writeExisting(w http.ResponseWriter, r *http.Request, record Record, fingerprint, headerName string, logger *slog.Logger) {
	switch {
	case record.Fingerprint != fingerprint:
		kernel.WriteProblem(w, r, kernel.Conflict(headerName+" was already used for a different request"), logger.Error)
	case record.State == statePending:
		w.Header().Set("Retry-After", "1")
		kernel.WriteProblem(w, r, kernel.Conflict("request with this "+headerName+" is still in progress"), logger.Error)
	case record.State != stateComplete:
		kernel.WriteProblem(w, r, kernel.Internal(errors.New("idempotency record has invalid state")), logger.Error)
	default:
		for name, values := range storedHeaders(record.Header) {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		w.Header().Set("Idempotency-Replayed", "true")
		status := record.Status
		if status < 100 || status > 599 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = w.Write(record.Body)
	}
}

type captureWriter struct {
	header   http.Header
	status   int
	body     bytes.Buffer
	maxBytes int64
	overflow bool
}

func newCaptureWriter(maxBytes int64) *captureWriter {
	return &captureWriter{header: make(http.Header), maxBytes: maxBytes}
}

func (w *captureWriter) Header() http.Header { return w.header }

func (w *captureWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
}

func (w *captureWriter) Write(value []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if int64(w.body.Len()+len(value)) > w.maxBytes {
		w.overflow = true
		return 0, errors.New("idempotency response too large")
	}
	return w.body.Write(value)
}

func (w *captureWriter) writeTo(dst http.ResponseWriter) {
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	for name, values := range storedHeaders(w.header) {
		for _, value := range values {
			dst.Header().Add(name, value)
		}
	}
	dst.WriteHeader(status)
	_, _ = dst.Write(w.body.Bytes())
}

func storedHeaders(header http.Header) http.Header {
	cloned := header.Clone()
	for _, name := range []string{
		"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
		"Te", "Trailer", "Transfer-Encoding", "Upgrade", "Content-Length",
		"Idempotency-Replayed",
	} {
		cloned.Del(name)
	}
	if cloned == nil {
		cloned = make(http.Header)
	}
	return cloned
}
