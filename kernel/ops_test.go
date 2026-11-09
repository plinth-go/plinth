package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type typedNilReadyError struct{}

func (*typedNilReadyError) Error() string { panic("typed nil readiness error was dereferenced") }

func TestWriteJSONEncodesBeforeCommit(t *testing.T) {
	rec := httptest.NewRecorder()
	err := WriteJSON(rec, http.StatusOK, map[string]any{"value": math.NaN()})
	if err == nil {
		t.Fatal("unencodable JSON was accepted")
	}
	if rec.Header().Get("Content-Type") != "" || rec.Body.Len() != 0 {
		t.Fatalf("response was committed: headers=%#v body=%q", rec.Header(), rec.Body.String())
	}
}

func TestWriteJSONRejectsNilWriter(t *testing.T) {
	var writer *nilResponseWriter
	if err := WriteJSON(writer, http.StatusOK, map[string]string{"status": "ok"}); err == nil || !strings.Contains(err.Error(), "response writer is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestWriteJSONRejectsBodylessStatus(t *testing.T) {
	for _, status := range []int{http.StatusEarlyHints, http.StatusNoContent, http.StatusResetContent, http.StatusNotModified} {
		rec := httptest.NewRecorder()
		if err := WriteJSON(rec, status, map[string]string{"status": "invalid"}); err == nil {
			t.Fatalf("status %d was accepted", status)
		}
		if len(rec.Header()) != 0 || rec.Body.Len() != 0 {
			t.Fatalf("status %d committed a response: headers=%#v body=%q", status, rec.Header(), rec.Body.String())
		}
	}
}

func TestWriteJSONWritesEncodedBodyOnce(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := WriteJSON(rec, http.StatusCreated, map[string]string{"id": "abc"}); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusCreated || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("status = %d, headers = %#v", rec.Code, rec.Header())
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["id"] != "abc" {
		t.Fatalf("body = %#v", body)
	}
}

func TestWriteJSONRejectsShortWrite(t *testing.T) {
	writer := &shortSSEWriter{header: make(http.Header)}
	err := WriteJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("error = %v, want io.ErrShortWrite", err)
	}
}

func TestRegisterOpsDefaults(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := NewRouter(logger, "test", "1")
	router.RegisterOps(OpsConfig{
		Logger: logger,
		Checks: []ReadyCheck{
			{Name: "postgres", Check: func(context.Context) error { return nil }},
			{Name: "redis", Check: func(context.Context) error { return errors.New("connection refused") }},
		},
	})

	live := httptest.NewRecorder()
	router.ServeHTTP(live, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if live.Code != http.StatusOK {
		t.Fatalf("livez status = %d, body = %s", live.Code, live.Body.String())
	}
	if contentType := live.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("livez Content-Type = %q", contentType)
	}
	var liveBody map[string]string
	if err := json.NewDecoder(live.Body).Decode(&liveBody); err != nil {
		t.Fatal(err)
	}
	if liveBody["status"] != "ok" {
		t.Fatalf("livez body = %#v", liveBody)
	}

	ready := httptest.NewRecorder()
	router.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz status = %d, body = %s", ready.Code, ready.Body.String())
	}
	var readyBody struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}
	if err := json.NewDecoder(ready.Body).Decode(&readyBody); err != nil {
		t.Fatal(err)
	}
	if readyBody.Status != "unavailable" || readyBody.Checks["postgres"] != "ok" || readyBody.Checks["redis"] != "down" {
		t.Fatalf("readyz body = %#v", readyBody)
	}

	spec := httptest.NewRecorder()
	router.ServeHTTP(spec, httptest.NewRequest(http.MethodGet, "/openapi.json", nil))
	if spec.Code != http.StatusOK {
		t.Fatalf("openapi status = %d, body = %s", spec.Code, spec.Body.String())
	}
	if contentType := spec.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("openapi Content-Type = %q", contentType)
	}
	if !json.Valid(spec.Body.Bytes()) {
		t.Fatalf("openapi is not valid JSON: %.100s", spec.Body.String())
	}
	if got := spec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("openapi Cache-Control = %q", got)
	}
	document := decodeSpec(t, spec.Body.Bytes())
	for _, path := range []string{"/livez", "/readyz", "/openapi.json", "/docs/"} {
		if _, ok := document.Paths[path]; ok {
			t.Fatalf("operational path %s leaked into OpenAPI", path)
		}
	}

	docs := httptest.NewRecorder()
	router.ServeHTTP(docs, httptest.NewRequest(http.MethodGet, "/docs/", nil))
	if docs.Code != http.StatusOK || !strings.Contains(strings.ToLower(docs.Body.String()), "swagger") {
		t.Fatalf("docs status = %d, body = %.100s", docs.Code, docs.Body.String())
	}
	if got := docs.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("docs Cache-Control = %q", got)
	}
	if got := docs.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("docs X-Content-Type-Options = %q", got)
	}
}

func TestRegisterOpsCustomPathsAndHandlers(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	router.RegisterOps(OpsConfig{
		LivePath:    "/health",
		ReadyPath:   "/health/ready",
		OpenAPIPath: "/spec",
		DocsPath:    "/manual/",
		Live: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=3600")
			w.Header().Set("X-Content-Type-Options", "sniff")
			w.WriteHeader(http.StatusNoContent)
		},
		Ready: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ready"))
		},
		OpenAPI: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"openapi":"3.1.0"}`))
		},
		Docs: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("docs"))
		}),
	})

	live := httptest.NewRecorder()
	router.ServeHTTP(live, httptest.NewRequest(http.MethodGet, "/health", nil))
	if live.Code != http.StatusNoContent {
		t.Fatalf("custom livez status = %d", live.Code)
	}
	assertOpsSecurityHeaders(t, live)

	missing := httptest.NewRecorder()
	router.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("default livez path status = %d", missing.Code)
	}

	ready := httptest.NewRecorder()
	router.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if ready.Code != http.StatusOK || ready.Body.String() != "ready" {
		t.Fatalf("custom ready status = %d, body = %q", ready.Code, ready.Body.String())
	}
	assertOpsSecurityHeaders(t, ready)

	spec := httptest.NewRecorder()
	router.ServeHTTP(spec, httptest.NewRequest(http.MethodGet, "/spec", nil))
	if spec.Code != http.StatusOK || spec.Body.String() != `{"openapi":"3.1.0"}` {
		t.Fatalf("custom openapi status = %d, body = %q", spec.Code, spec.Body.String())
	}
	assertOpsSecurityHeaders(t, spec)

	docs := httptest.NewRecorder()
	router.ServeHTTP(docs, httptest.NewRequest(http.MethodGet, "/manual/", nil))
	if docs.Code != http.StatusOK || docs.Body.String() != "docs" {
		t.Fatalf("custom docs status = %d, body = %q", docs.Code, docs.Body.String())
	}
	assertOpsSecurityHeaders(t, docs)

	missingDocs := httptest.NewRecorder()
	router.ServeHTTP(missingDocs, httptest.NewRequest(http.MethodGet, "/docs/", nil))
	if missingDocs.Code != http.StatusNotFound {
		t.Fatalf("default docs path status = %d", missingDocs.Code)
	}
}

func TestRegisterOpsForcesSecurityHeadersForEmptyCustomHandler(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	router.RegisterOps(OpsConfig{
		Live: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=3600")
			w.Header().Set("X-Content-Type-Options", "sniff")
		},
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	assertOpsSecurityHeaders(t, rec)
}

func TestRegisterOpsPreservesSecurityHeadersOnPanic(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := NewRouter(logger, "test", "1")
	router.Use(Recover(logger))
	router.RegisterOps(OpsConfig{
		Live: func(http.ResponseWriter, *http.Request) {
			panic("ops boom")
		},
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	assertOpsSecurityHeaders(t, rec)
}

func assertOpsSecurityHeaders(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

func TestRegisterOpsDisabled(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	router.RegisterOps(OpsConfig{
		DisableLive:    true,
		DisableReady:   true,
		DisableOpenAPI: true,
	})

	for _, path := range []string{"/livez", "/readyz", "/openapi.json", "/docs/"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", path, rec.Code)
		}
	}
}

func TestRegisterOpsPreflightsPathsBeforeRegistration(t *testing.T) {
	t.Run("duplicate operational paths", func(t *testing.T) {
		router := NewRouter(nil, "test", "1")
		assertPanicsWith(t, "operational route GET /health conflicts", func() {
			router.RegisterOps(OpsConfig{LivePath: "/health", ReadyPath: "/health"})
		})
		if len(router.operationSnapshot()) != 0 {
			t.Fatal("failed RegisterOps left routes behind")
		}
		if router.frozen {
			t.Fatal("failed RegisterOps froze the router")
		}
	})

	t.Run("existing route conflict", func(t *testing.T) {
		router := NewRouter(nil, "test", "1")
		router.Raw("GET", "/health", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), Hidden())
		before := len(router.operationSnapshot())
		assertPanicsWith(t, "conflicts with existing route", func() {
			router.RegisterOps(OpsConfig{LivePath: "/health", DisableReady: true, DisableOpenAPI: true})
		})
		if got := len(router.operationSnapshot()); got != before {
			t.Fatalf("operations = %d after failed RegisterOps, want %d", got, before)
		}
	})

	t.Run("typed nil docs handler", func(t *testing.T) {
		router := NewRouter(nil, "test", "1")
		var docs *typedNilHandler
		assertPanicsWith(t, "operational docs handler is nil", func() {
			router.RegisterOps(OpsConfig{DisableLive: true, DisableReady: true, Docs: docs})
		})
		if len(router.operationSnapshot()) != 0 {
			t.Fatal("failed RegisterOps left routes behind")
		}
	})

	t.Run("overlapping operational paths", func(t *testing.T) {
		router := NewRouter(nil, "test", "1")
		assertPanicsWith(t, "conflicts", func() {
			router.RegisterOps(OpsConfig{
				LivePath:       "/items/{id}",
				ReadyPath:      "/items/{name}",
				DisableOpenAPI: true,
			})
		})
		if len(router.operationSnapshot()) != 0 {
			t.Fatal("failed RegisterOps left routes behind")
		}
		if router.frozen {
			t.Fatal("failed RegisterOps froze the router")
		}
	})

	t.Run("malformed operational path", func(t *testing.T) {
		router := NewRouter(nil, "test", "1")
		assertPanicsWith(t, "bad wildcard segment", func() {
			router.RegisterOps(OpsConfig{
				LivePath:       "/items/{",
				DisableReady:   true,
				DisableOpenAPI: true,
			})
		})
		if len(router.operationSnapshot()) != 0 {
			t.Fatal("failed RegisterOps left routes behind")
		}
		if router.frozen {
			t.Fatal("failed RegisterOps froze the router")
		}
	})

	t.Run("group failure remains mutable", func(t *testing.T) {
		router := NewRouter(nil, "test", "1")
		group := router.Group("/internal")
		assertPanicsWith(t, "bad wildcard segment", func() {
			group.RegisterOps(OpsConfig{
				LivePath:       "/items/{",
				DisableReady:   true,
				DisableOpenAPI: true,
			})
		})
		group.Tag("ops")
	})
}

func TestRegisterOpsOnAuthorizedGroup(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	router.RegisterOps(OpsConfig{
		DisableReady:   true,
		DisableOpenAPI: true,
	})

	requireOpsToken := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer ops" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	internal := router.Group("/internal", requireOpsToken)
	internal.RegisterOps(OpsConfig{
		DisableLive: true,
		Checks: []ReadyCheck{
			{Name: "postgres", Check: func(context.Context) error { return nil }},
		},
	})

	globalReady := httptest.NewRecorder()
	router.ServeHTTP(globalReady, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if globalReady.Code != http.StatusNotFound {
		t.Fatalf("global readyz status = %d, want 404", globalReady.Code)
	}

	unauthorized := httptest.NewRecorder()
	router.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/internal/readyz", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized readyz status = %d, want 401", unauthorized.Code)
	}

	ready := httptest.NewRecorder()
	readyRequest := httptest.NewRequest(http.MethodGet, "/internal/readyz", nil)
	readyRequest.Header.Set("Authorization", "Bearer ops")
	router.ServeHTTP(ready, readyRequest)
	if ready.Code != http.StatusOK {
		t.Fatalf("authorized readyz status = %d, body = %s", ready.Code, ready.Body.String())
	}

	spec := httptest.NewRecorder()
	specRequest := httptest.NewRequest(http.MethodGet, "/internal/openapi.json", nil)
	specRequest.Header.Set("Authorization", "Bearer ops")
	router.ServeHTTP(spec, specRequest)
	if spec.Code != http.StatusOK || !json.Valid(spec.Body.Bytes()) {
		t.Fatalf("authorized openapi status = %d, body = %.100s", spec.Code, spec.Body.String())
	}
	if got := spec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("authorized openapi Cache-Control = %q", got)
	}
	document := decodeSpec(t, spec.Body.Bytes())
	for _, path := range []string{"/livez", "/internal/readyz", "/internal/openapi.json", "/internal/docs/"} {
		if _, ok := document.Paths[path]; ok {
			t.Fatalf("operational path %s leaked into OpenAPI", path)
		}
	}

	docs := httptest.NewRecorder()
	docsRequest := httptest.NewRequest(http.MethodGet, "/internal/docs/", nil)
	docsRequest.Header.Set("Authorization", "Bearer ops")
	router.ServeHTTP(docs, docsRequest)
	if docs.Code != http.StatusOK || !strings.Contains(docs.Body.String(), "/internal/openapi.json") {
		t.Fatalf("authorized docs status = %d, body = %.120s", docs.Code, docs.Body.String())
	}
	if got := docs.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("authorized docs Cache-Control = %q", got)
	}

	disabled := httptest.NewRecorder()
	disabledRequest := httptest.NewRequest(http.MethodGet, "/internal/livez", nil)
	disabledRequest.Header.Set("Authorization", "Bearer ops")
	router.ServeHTTP(disabled, disabledRequest)
	if disabled.Code != http.StatusNotFound {
		t.Fatalf("disabled livez status = %d, want 404", disabled.Code)
	}
}

func TestReadyzRejectsDuplicateCheckNames(t *testing.T) {
	defer func() {
		recovered := recover()
		if recovered == nil || !strings.Contains(fmt.Sprint(recovered), "duplicate readiness check name") {
			t.Fatalf("panic = %v", recovered)
		}
	}()
	Readyz(ReadyConfig{Checks: []ReadyCheck{
		{Name: "postgres", Check: func(context.Context) error { return nil }},
		{Name: " postgres ", Check: func(context.Context) error { return nil }},
	}})
}

func TestReadyzNamesUnnamedChecksDeterministically(t *testing.T) {
	handler := Readyz(ReadyConfig{Checks: []ReadyCheck{
		{Check: func(context.Context) error { return nil }},
	}})
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Checks["check_1"] != "ok" {
		t.Fatalf("checks = %#v", body.Checks)
	}
}

func TestReadyzIsolatesTypedNilAndPanickingChecks(t *testing.T) {
	var typedNil *typedNilReadyError
	var healthyCalled bool
	handler := Readyz(ReadyConfig{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Checks: []ReadyCheck{
			{Name: "typed", Check: func(context.Context) error { return typedNil }},
			{Name: "panic", Check: func(context.Context) error { panic("check boom") }},
			{Name: "healthy", Check: func(context.Context) error {
				healthyCalled = true
				return nil
			}},
		},
	})
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !healthyCalled {
		t.Fatal("panicking check prevented later checks from running")
	}
	var body struct {
		Checks map[string]string `json:"checks"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Checks["typed"] != "down" || body.Checks["panic"] != "down" || body.Checks["healthy"] != "ok" {
		t.Fatalf("checks = %#v", body.Checks)
	}
}
