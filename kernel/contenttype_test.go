package kernel

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type testResponder struct {
	Meta        `produces:"text/plain"`
	ContentType string
	Status      int
	Body        string
}

type nilResponderOutput struct {
	Meta `produces:"text/plain"`
}

func (*nilResponderOutput) ResponseHeaders() http.Header {
	panic("nil responder headers must not be called")
}

func (*nilResponderOutput) Respond(http.ResponseWriter, *http.Request) error {
	panic("nil responder must not be called")
}

type nilHeadererOutput struct {
	Value string `json:"value"`
}

func (*nilHeadererOutput) ResponseHeaders() http.Header {
	panic("nil headerer must not be called")
}

func (r testResponder) Respond(w http.ResponseWriter, _ *http.Request) error {
	if r.ContentType != "" {
		w.Header().Set("Content-Type", r.ContentType)
	}
	if r.Status != 0 {
		w.WriteHeader(r.Status)
	}
	_, err := w.Write([]byte(r.Body))
	return err
}

type multiResponder struct {
	Meta `produces:"application/json,text/csv"`
}

type hijackWriter struct {
	header   http.Header
	hijacked bool
}

func (w *hijackWriter) Header() http.Header { return w.header }

func (w *hijackWriter) WriteHeader(int) {}

func (w *hijackWriter) Write(p []byte) (int, error) { return len(p), nil }

func (w *hijackWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.hijacked = true
	return nil, nil, nil
}

func TestTypedResponderGuardPreservesReaderFrom(t *testing.T) {
	underlying := &readerFromWriter{header: make(http.Header)}
	guard := &contentTypeGuard{
		w:        underlying,
		req:      httptest.NewRequest(http.MethodGet, "/file", nil),
		produces: compileProduces([]string{"text/plain"}),
	}
	n, err := guard.ReadFrom(io.LimitReader(strings.NewReader("hello"), 5))
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 || !underlying.used || underlying.body.String() != "hello" {
		t.Fatalf("n = %d, used = %v, body = %q", n, underlying.used, underlying.body.String())
	}
	if got := underlying.Header().Get("Content-Type"); got != "text/plain" {
		t.Fatalf("Content-Type = %q", got)
	}
}

func TestTypedResponderGuardRejectsHijack(t *testing.T) {
	underlying := &hijackWriter{header: make(http.Header)}
	guard := &contentTypeGuard{w: underlying}
	if _, _, err := http.NewResponseController(guard).Hijack(); !errors.Is(err, http.ErrNotSupported) {
		t.Fatalf("error = %v", err)
	}
	if underlying.hijacked {
		t.Fatal("typed Responder guard forwarded Hijack")
	}
}

func TestTypedResponderGuardPropagatesFlushError(t *testing.T) {
	underlying := &failingFlushWriter{header: make(http.Header), failOn: 1}
	guard := &contentTypeGuard{
		w:        underlying,
		req:      httptest.NewRequest(http.MethodGet, "/stream", nil),
		produces: compileProduces([]string{"text/plain"}),
	}
	err := guard.FlushError()
	if err == nil || !strings.Contains(err.Error(), "client disconnected") {
		t.Fatalf("error = %v", err)
	}
	if got := underlying.Header().Get("Content-Type"); got != "text/plain" {
		t.Fatalf("Content-Type = %q", got)
	}
}

func TestNilTypedResponderWritesProblem(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	testJSON(router, "GET", "/nil", func(context.Context, struct{}) (*nilResponderOutput, error) {
		return nil, nil
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nil", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/problem+json") {
		t.Fatalf("Content-Type = %q", got)
	}
	var problem Problem
	if err := json.NewDecoder(rec.Body).Decode(&problem); err != nil {
		t.Fatal(err)
	}
	if problem.Status != http.StatusInternalServerError || problem.Detail != "internal server error" {
		t.Fatalf("problem = %#v", problem)
	}
}

func TestNilHeadererDoesNotPanic(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	testJSON(router, "GET", "/nil-header", func(context.Context, struct{}) (*nilHeadererOutput, error) {
		return nil, nil
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nil-header", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "null\n" {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
}

func (multiResponder) Respond(w http.ResponseWriter, _ *http.Request) error {
	_, err := w.Write([]byte("ambiguous"))
	return err
}

type headerOverrideOutput struct {
	Value string `json:"value"`
}

func (headerOverrideOutput) ResponseHeaders() http.Header {
	return http.Header{"Content-Type": {"text/plain"}}
}

type invalidHeadersOutput struct {
	Value string `json:"value"`
}

func (invalidHeadersOutput) ResponseHeaders() http.Header {
	return http.Header{"X-Test": {"bad\r\nInjected: true"}}
}

type framingHeadersOutput struct {
	Value string `json:"value"`
}

func (framingHeadersOutput) ResponseHeaders() http.Header {
	return http.Header{"Content-Length": {"999"}}
}

type hopByHopHeadersOutput struct {
	Value string `json:"value"`
}

func (hopByHopHeadersOutput) ResponseHeaders() http.Header {
	return http.Header{"Connection": {"close"}}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestJSONContentTypeCannotBeOverriddenByHeaderer(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	testJSON(router, "GET", "/value", func(context.Context, struct{}) (headerOverrideOutput, error) {
		return headerOverrideOutput{Value: "ok"}, nil
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/value", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	var value headerOverrideOutput
	if err := json.NewDecoder(rec.Body).Decode(&value); err != nil {
		t.Fatal(err)
	}
	if value.Value != "ok" {
		t.Fatalf("value = %#v", value)
	}
}

func TestInvalidHeadererWritesProblemBeforeCommittingHeaders(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	testJSON(router, "GET", "/value", func(context.Context, struct{}) (invalidHeadersOutput, error) {
		return invalidHeadersOutput{Value: "ok"}, nil
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/value", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Test") != "" || rec.Header().Get("X-Injected") != "" {
		t.Fatalf("invalid headers leaked: %#v", rec.Header())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/problem+json") {
		t.Fatalf("Content-Type = %q", got)
	}
}

func TestHeadererCannotControlResponseFraming(t *testing.T) {
	router := NewRouter(discardLogger(), "test", "1")
	testJSON(router, "GET", "/value", func(context.Context, struct{}) (framingHeadersOutput, error) {
		return framingHeadersOutput{Value: "ok"}, nil
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/value", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Length"); got != "" {
		t.Fatalf("Content-Length leaked: %q", got)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/problem+json") {
		t.Fatalf("Content-Type = %q", got)
	}
}

func TestHeadererCannotControlHopByHopHeaders(t *testing.T) {
	router := NewRouter(discardLogger(), "test", "1")
	testJSON(router, "GET", "/value", func(context.Context, struct{}) (hopByHopHeadersOutput, error) {
		return hopByHopHeadersOutput{Value: "ok"}, nil
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/value", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Connection"); got != "" {
		t.Fatalf("Connection leaked: %q", got)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/problem+json") {
		t.Fatalf("Content-Type = %q", got)
	}
}

func TestTypedJSONLogsShortWrite(t *testing.T) {
	var logs strings.Builder
	router := NewRouter(slog.New(slog.NewTextHandler(&logs, nil)), "test", "1")
	testJSON(router, "GET", "/value", func(context.Context, struct{}) (headerOverrideOutput, error) {
		return headerOverrideOutput{Value: "ok"}, nil
	})

	writer := &shortSSEWriter{header: make(http.Header)}
	router.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/value", nil))
	if !strings.Contains(logs.String(), "write JSON response") {
		t.Fatalf("short write was not logged: %s", logs.String())
	}
}

type unencodableOutput struct {
	Headers Headers `json:"-"`
	Value   float64 `json:"value"`
}

func (o unencodableOutput) ResponseHeaders() http.Header { return http.Header(o.Headers) }

func TestJSONEncodingFailureReturnsProblemWithoutLeakingHeaders(t *testing.T) {
	router := NewRouter(discardLogger(), "test", "1")
	testJSON(router, "GET", "/unencodable", func(context.Context, struct{}) (unencodableOutput, error) {
		return unencodableOutput{
			Headers: Headers{"X-Output-Only": {"leak"}},
			Value:   math.NaN(),
		}, nil
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unencodable", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Output-Only"); got != "" {
		t.Fatalf("X-Output-Only leaked: %q", got)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/problem+json") {
		t.Fatalf("Content-Type = %q", got)
	}
}

func TestResponderDefaultsSingleDeclaredContentType(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	testJSON(router, "GET", "/text", func(context.Context, struct{}) (testResponder, error) {
		return testResponder{Body: "ok"}, nil
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/text", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/plain" {
		t.Fatalf("Content-Type = %q", got)
	}
}

func TestResponderRejectsUndeclaredContentType(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	testJSON(router, "GET", "/text", func(context.Context, struct{}) (testResponder, error) {
		return testResponder{ContentType: "application/json", Body: `{}`}, nil
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/text", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/problem+json") {
		t.Fatalf("Content-Type = %q", got)
	}
}

func TestResponderRejectsNonOKStatusAtRuntime(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	testJSON(router, "GET", "/created", func(context.Context, struct{}) (testResponder, error) {
		return testResponder{ContentType: "text/plain", Status: http.StatusCreated, Body: "created"}, nil
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/created", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
}

func TestResponderRequiresContentTypeWhenProducesIsAmbiguous(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	testJSON(router, "GET", "/ambiguous", func(context.Context, struct{}) (multiResponder, error) {
		return multiResponder{}, nil
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ambiguous", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
}

type leakyResponder struct {
	Meta `produces:"text/plain"`
}

func (leakyResponder) ResponseHeaders() http.Header {
	return http.Header{
		"Content-Disposition": {"attachment; filename=leak.csv"},
		"X-Output-Only":       {"leak"},
	}
}

func (leakyResponder) Respond(w http.ResponseWriter, _ *http.Request) error {
	w.Header().Set("Content-Type", "application/json")
	_, err := w.Write([]byte(`{}`))
	return err
}

func TestResponderContractViolationRestoresBaseHeaders(t *testing.T) {
	router := NewRouter(discardLogger(), "test", "1")
	router.Use(RequestID())
	testJSON(router, "GET", "/leak", func(context.Context, struct{}) (leakyResponder, error) {
		return leakyResponder{}, nil
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/leak", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Disposition"); got != "" {
		t.Fatalf("Content-Disposition leaked: %q", got)
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
}

type failingResponder struct {
	Meta `produces:"text/plain"`
}

func (failingResponder) ResponseHeaders() http.Header {
	return http.Header{"X-Output-Only": {"leak"}}
}

func (failingResponder) Respond(http.ResponseWriter, *http.Request) error {
	return errors.New("responder failed before writing")
}

func TestResponderErrorBeforeWritingRestoresBaseHeaders(t *testing.T) {
	router := NewRouter(discardLogger(), "test", "1")
	router.Use(RequestID())
	testJSON(router, "GET", "/fail", func(context.Context, struct{}) (failingResponder, error) {
		return failingResponder{}, nil
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fail", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Output-Only"); got != "" {
		t.Fatalf("X-Output-Only leaked: %q", got)
	}
	if got := rec.Header().Get("X-Request-ID"); got == "" {
		t.Fatal("base response header X-Request-ID was lost")
	}
}

type noContentOutput struct {
	Meta    `status:"204"`
	Headers Headers `json:"-"`
}

func (o noContentOutput) ResponseHeaders() http.Header { return http.Header(o.Headers) }

func TestNoContentSkipsBodyAndAppliesHeaders(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	testJSON(router, "DELETE", "/items/{id}", func(context.Context, struct{}) (noContentOutput, error) {
		return noContentOutput{Headers: Headers{"X-Test-Output": {"deleted"}}}, nil
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/items/1", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if got := rec.Header().Get("X-Test-Output"); got != "deleted" {
		t.Fatalf("X-Test-Output = %q", got)
	}

	operation := decodeSpec(t, router.OpenAPIJSON()).Paths["/items/{id}"]["delete"]
	response, ok := operation.Responses["204"]
	if !ok {
		t.Fatalf("responses = %#v", operation.Responses)
	}
	if response.Content != nil {
		t.Fatalf("204 content = %#v", response.Content)
	}
}

type notModifiedOutput struct {
	Meta    `status:"304"`
	Headers Headers `json:"-"`
	Value   string  `json:"value"`
}

func (o notModifiedOutput) ResponseHeaders() http.Header { return http.Header(o.Headers) }

func TestNotModifiedSkipsBodyAndOpenAPIContent(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	testJSON(router, "GET", "/cached", func(context.Context, struct{}) (notModifiedOutput, error) {
		return notModifiedOutput{
			Headers: Headers{"ETag": {`"v1"`}},
			Value:   "must not be written",
		}, nil
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/cached", nil))
	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if got := rec.Header().Get("ETag"); got != `"v1"` {
		t.Fatalf("ETag = %q", got)
	}

	operation := decodeSpec(t, router.OpenAPIJSON()).Paths["/cached"]["get"]
	response, ok := operation.Responses["304"]
	if !ok {
		t.Fatalf("responses = %#v", operation.Responses)
	}
	if response.Content != nil {
		t.Fatalf("304 content = %#v", response.Content)
	}
}

type resetContentOutput struct {
	Meta  `status:"205"`
	Value string `json:"value"`
}

func TestResetContentSkipsBodyAndOpenAPIContent(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	testJSON(router, "POST", "/reset", func(context.Context, struct{}) (resetContentOutput, error) {
		return resetContentOutput{Value: "must not be written"}, nil
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/reset", nil))
	if rec.Code != http.StatusResetContent {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body = %q", rec.Body.String())
	}

	operation := decodeSpec(t, router.OpenAPIJSON()).Paths["/reset"]["post"]
	response, ok := operation.Responses["205"]
	if !ok {
		t.Fatalf("responses = %#v", operation.Responses)
	}
	if response.Content != nil {
		t.Fatalf("205 content = %#v", response.Content)
	}
}

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlineSet bool
}

func (r *deadlineRecorder) SetWriteDeadline(time.Time) error {
	r.deadlineSet = true
	return nil
}

type controllerResponder struct {
	Meta `produces:"text/plain"`
}

func (controllerResponder) Respond(w http.ResponseWriter, _ *http.Request) error {
	controller := http.NewResponseController(w)
	if err := controller.SetWriteDeadline(time.Now().Add(time.Minute)); err != nil {
		return err
	}
	if _, err := w.Write([]byte("ok")); err != nil {
		return err
	}
	return controller.Flush()
}

func TestResponderCanUseResponseControllerThroughUnwrap(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	testJSON(router, "GET", "/stream", func(context.Context, struct{}) (controllerResponder, error) {
		return controllerResponder{}, nil
	})

	rec := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stream", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if !rec.deadlineSet {
		t.Fatal("ResponseController did not reach the wrapped SetWriteDeadline")
	}
	if !rec.Flushed {
		t.Fatal("ResponseController did not reach the wrapped Flush")
	}
}

func TestMediaTypeMatches(t *testing.T) {
	for _, tc := range []struct {
		actual   string
		declared string
		want     bool
	}{
		{actual: "text/csv; charset=utf-8", declared: "text/csv", want: true},
		{actual: "application/json", declared: "application/*", want: true},
		{actual: "application/json", declared: "*/*", want: true},
		{actual: "text/plain", declared: "application/json", want: false},
		{actual: "not a media type", declared: "text/plain", want: false},
	} {
		if got := mediaTypeMatches(tc.actual, tc.declared); got != tc.want {
			t.Fatalf("mediaTypeMatches(%q, %q) = %v, want %v", tc.actual, tc.declared, got, tc.want)
		}
	}
}

func TestResponderWildcardProducesRequiresConcreteContentType(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := NewRouter(logger, "test", "1")
	testJSON(router, "GET", "/any", func(context.Context, struct{}) (wildcardResponder, error) {
		return wildcardResponder{}, nil
	})
	testJSON(router, "GET", "/any-typed", func(context.Context, struct{}) (wildcardResponder, error) {
		return wildcardResponder{contentType: "application/pdf"}, nil
	})
	testJSON(router, "GET", "/application", func(context.Context, struct{}) (applicationWildcardResponder, error) {
		return applicationWildcardResponder{}, nil
	})
	testJSON(router, "GET", "/application-typed", func(context.Context, struct{}) (applicationWildcardResponder, error) {
		return applicationWildcardResponder{contentType: "application/json"}, nil
	})
	if err := router.Validate(); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		path       string
		wantStatus int
		wantType   string
	}{
		{path: "/any", wantStatus: http.StatusInternalServerError},
		{path: "/any-typed", wantStatus: http.StatusOK, wantType: "application/pdf"},
		{path: "/application", wantStatus: http.StatusInternalServerError},
		{path: "/application-typed", wantStatus: http.StatusOK, wantType: "application/json"},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, test.path, nil))
			if rec.Code != test.wantStatus {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if test.wantType != "" {
				if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, test.wantType) {
					t.Fatalf("Content-Type = %q, want %q", got, test.wantType)
				}
			}
		})
	}

	spec := decodeSpec(t, router.OpenAPIJSON())
	if _, ok := spec.Paths["/any"]["get"].Responses["200"].Content["*/*"]; !ok {
		t.Fatalf("wildcard response content = %#v", spec.Paths["/any"]["get"].Responses["200"].Content)
	}
	if _, ok := spec.Paths["/application"]["get"].Responses["200"].Content["application/*"]; !ok {
		t.Fatalf("application wildcard content = %#v", spec.Paths["/application"]["get"].Responses["200"].Content)
	}
}

type wildcardResponder struct {
	Meta        `produces:"*/*"`
	contentType string
}

func (o wildcardResponder) Respond(w http.ResponseWriter, _ *http.Request) error {
	if o.contentType != "" {
		w.Header().Set("Content-Type", o.contentType)
	}
	_, err := w.Write([]byte("ok"))
	return err
}

type applicationWildcardResponder struct {
	Meta        `produces:"application/*"`
	contentType string
}

func (o applicationWildcardResponder) Respond(w http.ResponseWriter, _ *http.Request) error {
	if o.contentType != "" {
		w.Header().Set("Content-Type", o.contentType)
	}
	_, err := w.Write([]byte("ok"))
	return err
}
