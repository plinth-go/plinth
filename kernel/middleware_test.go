package kernel

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type statusCapture struct {
	header http.Header
	codes  []int
	body   bytes.Buffer
}

func newStatusCapture() *statusCapture {
	return &statusCapture{header: make(http.Header)}
}

func (w *statusCapture) Header() http.Header { return w.header }

func (w *statusCapture) WriteHeader(code int) { w.codes = append(w.codes, code) }

func (w *statusCapture) Write(p []byte) (int, error) {
	if len(w.codes) == 0 {
		w.codes = append(w.codes, http.StatusOK)
	}
	return w.body.Write(p)
}

func TestAccessLogPreservesInformationalAndFinalStatus(t *testing.T) {
	for _, tc := range []struct {
		name       string
		final      int
		wantCodes  []int
		wantStatus string
	}{
		{name: "early hints then created", final: http.StatusCreated, wantCodes: []int{http.StatusEarlyHints, http.StatusCreated}, wantStatus: "status=201"},
		{name: "switching protocols is final", final: http.StatusSwitchingProtocols, wantCodes: []int{http.StatusSwitchingProtocols}, wantStatus: "status=101"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, nil))
			handler := AccessLog(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.final != http.StatusSwitchingProtocols {
					w.WriteHeader(http.StatusEarlyHints)
				}
				w.WriteHeader(tc.final)
				_, _ = w.Write([]byte("ok"))
			}))

			rec := newStatusCapture()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
			if len(rec.codes) != len(tc.wantCodes) {
				t.Fatalf("status codes = %v, want %v", rec.codes, tc.wantCodes)
			}
			for i := range tc.wantCodes {
				if rec.codes[i] != tc.wantCodes[i] {
					t.Fatalf("status codes = %v, want %v", rec.codes, tc.wantCodes)
				}
			}
			if !strings.Contains(logs.String(), tc.wantStatus) || !strings.Contains(logs.String(), "bytes=2") {
				t.Fatalf("logs = %q", logs.String())
			}
		})
	}
}

func TestRecoverWritesProblemAfterInformationalResponse(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := Recover(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusEarlyHints)
		panic("boom")
	}))

	rec := newStatusCapture()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if len(rec.codes) != 2 || rec.codes[0] != http.StatusEarlyHints || rec.codes[1] != http.StatusInternalServerError {
		t.Fatalf("status codes = %v", rec.codes)
	}
	if !strings.Contains(rec.body.String(), `"status":500`) {
		t.Fatalf("body = %q", rec.body.String())
	}
}

func TestRequestIDUsesOneSafeValueEverywhere(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		reuse  bool
	}{
		{name: "safe client value", values: []string{"req-01HZY:7.alpha/beta+gamma="}, reuse: true},
		{name: "missing", reuse: false},
		{name: "empty", values: []string{""}, reuse: false},
		{name: "too long", values: []string{strings.Repeat("a", maxRequestIDLength+1)}, reuse: false},
		{name: "leading space", values: []string{" request-id"}, reuse: false},
		{name: "trailing space", values: []string{"request-id "}, reuse: false},
		{name: "control character", values: []string{"request\nid"}, reuse: false},
		{name: "non ASCII", values: []string{"请求-id"}, reuse: false},
		{name: "duplicate values", values: []string{"first", "second"}, reuse: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var contextID string
			handler := RequestID()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				contextID = RequestIDFromContext(r.Context())
				WriteProblem(w, r, BadRequest("invalid"), nil)
			}))

			req := httptest.NewRequest(http.MethodGet, "/request-id", nil)
			for _, value := range tt.values {
				req.Header.Add("X-Request-ID", value)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			headerID := rec.Header().Get("X-Request-ID")
			if headerID == "" {
				t.Fatal("X-Request-ID response header is empty")
			}
			if contextID != headerID {
				t.Fatalf("context ID = %q, header ID = %q", contextID, headerID)
			}
			if tt.reuse && headerID != tt.values[0] {
				t.Fatalf("header ID = %q, want client value %q", headerID, tt.values[0])
			}
			if !tt.reuse && len(tt.values) > 0 && headerID == tt.values[0] {
				t.Fatalf("unsafe client ID %q was reused", tt.values[0])
			}

			var problem Problem
			if err := json.NewDecoder(rec.Body).Decode(&problem); err != nil {
				t.Fatal(err)
			}
			if problem.Instance != headerID {
				t.Fatalf("Problem instance = %q, header ID = %q", problem.Instance, headerID)
			}
		})
	}
}

func TestRequestIDPreventsLogInjection(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	var contextID string
	handler := RequestID()(AccessLog(logger)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		contextID = RequestIDFromContext(r.Context())
	})))

	req := httptest.NewRequest(http.MethodGet, "/log", nil)
	req.Header.Set("X-Request-ID", "forged request_id=attacker\nstatus=204")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if contextID == "" || rec.Header().Get("X-Request-ID") != contextID {
		t.Fatalf("context ID = %q, header ID = %q", contextID, rec.Header().Get("X-Request-ID"))
	}
	if strings.Contains(logs.String(), "attacker") {
		t.Fatalf("client request ID polluted logs: %q", logs.String())
	}
	if !strings.Contains(logs.String(), "request_id="+contextID) {
		t.Fatalf("logs do not contain generated request ID %q: %q", contextID, logs.String())
	}
}

func TestRequestIDGeneratorProducesUniqueSafeIDs(t *testing.T) {
	generator := newRequestIDGenerator()
	seen := make(map[string]struct{}, 1024)
	for range 1024 {
		id := generator.next()
		if !validRequestID(id) {
			t.Fatalf("generated unsafe request ID %q", id)
		}
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("duplicate request ID %q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestRequestIDNestedStateDoesNotOverwriteOuterRequest(t *testing.T) {
	outer := &requestContext{Context: context.Background(), requestID: "outer-id"}
	request := httptest.NewRequest(http.MethodGet, "/nested", nil).WithContext(outer)
	var innerID string
	handler := RequestID()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		innerID = RequestIDFromContext(r.Context())
	}))

	handler.ServeHTTP(httptest.NewRecorder(), request)

	if innerID == "" || innerID == outer.requestID {
		t.Fatalf("inner request ID = %q, outer = %q", innerID, outer.requestID)
	}
	if got := RequestIDFromContext(request.Context()); got != outer.requestID {
		t.Fatalf("outer request ID = %q, want %q", got, outer.requestID)
	}
}

func TestRecoverWritesProblemAndRestoresBaseHeaders(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	handler := RequestID()(Recover(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Output-Only", "leak")
		panic("boom")
	})))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Output-Only"); got != "" {
		t.Fatalf("X-Output-Only leaked: %q", got)
	}
	if got := rec.Header().Get("X-Request-ID"); got == "" {
		t.Fatal("base response header X-Request-ID was lost")
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/problem+json") {
		t.Fatalf("Content-Type = %q", got)
	}
	if !strings.Contains(logs.String(), "response_committed=false") {
		t.Fatalf("logs = %q", logs.String())
	}
}

func TestRecoverHeaderSnapshotHandlesMultipleValues(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header()["X-Base"] = []string{"one", "two"}
		for i := 0; i < inlineHeaderSnapshotLimit; i++ {
			w.Header().Set("X-Base-"+string(rune('A'+i)), "value")
		}
		Recover(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header()["X-Base"][0] = "leaked"
			w.Header().Set("X-Output-Only", "leak")
			panic("boom")
		})).ServeHTTP(w, r)
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Values("X-Base"); len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("X-Base = %#v", got)
	}
	if got := rec.Header().Get("X-Output-Only"); got != "" {
		t.Fatalf("X-Output-Only leaked: %q", got)
	}
}

func TestRecoverDoesNotAppendProblemAfterResponseStarted(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	handler := Recover(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("partial"))
		panic("boom")
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "partial" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if !strings.Contains(logs.String(), "response_committed=true") {
		t.Fatalf("logs = %q", logs.String())
	}
}

type unsupportedControlWriter struct {
	header http.Header
	codes  []int
	body   bytes.Buffer
}

func (w *unsupportedControlWriter) Header() http.Header { return w.header }

func (w *unsupportedControlWriter) WriteHeader(status int) { w.codes = append(w.codes, status) }

func (w *unsupportedControlWriter) Write(p []byte) (int, error) { return w.body.Write(p) }

func TestRecoverWritesProblemAfterUnsupportedFlushOrHijack(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(http.ResponseWriter)
	}{
		{name: "flush", call: func(w http.ResponseWriter) { w.(http.Flusher).Flush() }},
		{name: "hijack", call: func(w http.ResponseWriter) { _, _, _ = w.(http.Hijacker).Hijack() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			rec := &unsupportedControlWriter{header: make(http.Header)}
			handler := Recover(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				tc.call(w)
				panic("boom")
			}))

			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panic", nil))
			if len(rec.codes) != 1 || rec.codes[0] != http.StatusInternalServerError {
				t.Fatalf("codes = %v, body = %q", rec.codes, rec.body.String())
			}
			if !strings.Contains(rec.body.String(), `"status":500`) {
				t.Fatalf("body = %q", rec.body.String())
			}
		})
	}
}

type readerFromWriter struct {
	header http.Header
	codes  []int
	body   bytes.Buffer
	used   bool
}

func (w *readerFromWriter) Header() http.Header { return w.header }

func (w *readerFromWriter) WriteHeader(status int) { w.codes = append(w.codes, status) }

func (w *readerFromWriter) Write(p []byte) (int, error) { return w.body.Write(p) }

func (w *readerFromWriter) ReadFrom(src io.Reader) (int64, error) {
	w.used = true
	return io.Copy(&w.body, src)
}

func TestStatusWriterPreservesReaderFrom(t *testing.T) {
	underlying := &readerFromWriter{header: make(http.Header)}
	w := &statusWriter{ResponseWriter: underlying, status: http.StatusOK}
	n, err := w.ReadFrom(strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 || !underlying.used || underlying.body.String() != "hello" {
		t.Fatalf("n = %d, used = %v, body = %q", n, underlying.used, underlying.body.String())
	}
	if !w.wroteHeader || w.bytes != 5 {
		t.Fatalf("wroteHeader = %v, bytes = %d", w.wroteHeader, w.bytes)
	}
}

func TestOpsWriterPreservesReaderFromAndHeaders(t *testing.T) {
	underlying := &readerFromWriter{header: make(http.Header)}
	w := &opsResponseWriter{ResponseWriter: underlying}
	n, err := w.ReadFrom(strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 || !underlying.used || underlying.body.String() != "hello" {
		t.Fatalf("n = %d, used = %v, body = %q", n, underlying.used, underlying.body.String())
	}
	if got := underlying.header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := underlying.header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
	if len(underlying.codes) != 1 || underlying.codes[0] != http.StatusOK {
		t.Fatalf("codes = %v", underlying.codes)
	}
}

func TestAccessLogRecordsExplicitAndImplicitStatus(t *testing.T) {
	tests := []struct {
		name   string
		write  func(http.ResponseWriter)
		status string
	}{
		{
			name: "explicit",
			write: func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte("ok"))
			},
			status: "status=202",
		},
		{
			name: "implicit",
			write: func(w http.ResponseWriter) {
				_, _ = w.Write([]byte("ok"))
			},
			status: "status=200",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, nil))
			handler := AccessLog(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				tt.write(w)
			}))

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/log", nil))
			if !strings.Contains(logs.String(), tt.status) || !strings.Contains(logs.String(), "bytes=2") {
				t.Fatalf("logs = %q", logs.String())
			}
		})
	}
}

func TestAccessLogRecordsRecoveredPanic(t *testing.T) {
	for _, tc := range []struct {
		name            string
		chain           func(http.Handler, *slog.Logger) http.Handler
		wantBytes       func(final int) int
		wantAccessPanic bool
	}{
		{
			name: "access log outside recover",
			chain: func(next http.Handler, logger *slog.Logger) http.Handler {
				return AccessLog(logger)(Recover(logger)(next))
			},
			wantBytes: func(final int) int { return final },
		},
		{
			name: "access log inside recover",
			chain: func(next http.Handler, logger *slog.Logger) http.Handler {
				return Recover(logger)(AccessLog(logger)(next))
			},
			// The outer Recover writes the Problem after the inner access log
			// has logged and returned. That order can report status 500 but not
			// account for bytes written afterward.
			wantBytes:       func(int) int { return 0 },
			wantAccessPanic: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			handler := tc.chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				panic("boom")
			}), logger)

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panic", nil))
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
			}
			if rec.Body.Len() == 0 {
				t.Fatal("recovered panic did not write a Problem body")
			}
			entry := accessLogEntry(t, logs.Bytes())
			if got := int(entry["status"].(float64)); got != http.StatusInternalServerError {
				t.Fatalf("logged status = %d", got)
			}
			if got, want := int(entry["bytes"].(float64)), tc.wantBytes(rec.Body.Len()); got != want {
				t.Fatalf("logged bytes = %d, want %d (body bytes = %d)", got, want, rec.Body.Len())
			}
			gotPanic, hasPanic := entry["panic"]
			if tc.wantAccessPanic && gotPanic != "boom" {
				t.Fatalf("access log panic = %v", gotPanic)
			}
			if !tc.wantAccessPanic && hasPanic {
				t.Fatalf("access log unexpectedly contains panic = %v", gotPanic)
			}
			if !bytes.Contains(logs.Bytes(), []byte(`"panic":"boom"`)) {
				t.Fatalf("missing recovered panic in logs: %q", logs.String())
			}
		})
	}
}

func accessLogEntry(t *testing.T, logs []byte) map[string]any {
	t.Helper()
	for _, line := range bytes.Split(bytes.TrimSpace(logs), []byte("\n")) {
		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		if entry["msg"] == "http request" {
			return entry
		}
	}
	t.Fatalf("missing access log in %q", logs)
	return nil
}
