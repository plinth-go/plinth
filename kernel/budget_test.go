package kernel

import (
	"context"
	"log/slog"
	"net/http"
	"testing"
)

func assertAllocationBudget(t *testing.T, name string, limit float64, run func()) {
	t.Helper()
	allocations := testing.AllocsPerRun(200, run)
	if allocations > limit {
		t.Errorf("%s uses %.1f allocs/op, budget %.1f", name, allocations, limit)
	}
}

func skipAllocationBudgetUnderRace(t *testing.T) {
	t.Helper()
	if raceEnabled {
		t.Skip("allocation counts are not meaningful under the race detector")
	}
}

func TestHTTPAllocationBudgets(t *testing.T) {
	skipAllocationBudgetUnderRace(t)
	router := benchmarkRouter(t)
	writer := newBenchmarkWriter()
	getRequest := benchmarkRequest(http.MethodGet, "/items/42?q=alpha", nil)
	assertAllocationBudget(t, "typed GET", 7, func() {
		writer.reset()
		router.ServeHTTP(writer, getRequest)
	})

	noQueryRequest := benchmarkRequest(http.MethodGet, "/items/42", nil)
	assertAllocationBudget(t, "typed GET without query", 7, func() {
		writer.reset()
		router.ServeHTTP(writer, noQueryRequest)
	})

	staticRequest := benchmarkRequest(http.MethodGet, "/static", nil)
	assertAllocationBudget(t, "typed static GET", 5, func() {
		writer.reset()
		router.ServeHTTP(writer, staticRequest)
	})

	headerRequest := benchmarkRequest(http.MethodGet, "/bound/42?q=alpha", nil)
	headerRequest.Header.Set("X-Tenant", "acme")
	assertAllocationBudget(t, "typed GET with header", 7, func() {
		writer.reset()
		router.ServeHTTP(writer, headerRequest)
	})

	postBody := []byte(`{"name":"alpha","count":2}`)
	body := newBenchmarkBody(postBody)
	postRequest := persistentJSONRequest(http.MethodPost, "/items", body)
	assertAllocationBudget(t, "typed JSON POST", 13, func() {
		writer.reset()
		body.Reset(postBody)
		router.ServeHTTP(writer, postRequest)
	})

	invalidBody := []byte(`{"name":"","count":0}`)
	invalid := newBenchmarkBody(invalidBody)
	invalidRequest := persistentJSONRequest(http.MethodPost, "/items", invalid)
	assertAllocationBudget(t, "validation failure", 34, func() {
		writer.reset()
		invalid.Reset(invalidBody)
		router.ServeHTTP(writer, invalidRequest)
	})

	encodeFailureRequest := benchmarkRequest(http.MethodGet, "/encode-failure", nil)
	assertAllocationBudget(t, "JSON encode failure", 15, func() {
		writer.reset()
		router.ServeHTTP(writer, encodeFailureRequest)
	})
}

func TestHTTPMiddlewareAllocationBudget(t *testing.T) {
	skipAllocationBudgetUnderRace(t)
	logger := slog.New(slog.NewTextHandler(discardWriter{}, nil))
	router := NewRouter(logger, "budget", "v1")
	router.Use(RequestID(), AccessLog(logger), Recover(logger), BodyLimit(1<<20))
	router.Handle(func(_ context.Context, in benchmarkGetInput) (benchmarkGetOutput, error) {
		return benchmarkGetOutput{ID: in.ID, Q: in.Q}, nil
	})
	request := benchmarkRequest(http.MethodGet, "/items/42?q=alpha", nil)
	writer := newBenchmarkWriter()

	assertAllocationBudget(t, "middleware chain", 16, func() {
		writer.reset()
		router.ServeHTTP(writer, request)
	})
}

func TestHTTPBodyLimitAllocationBudget(t *testing.T) {
	skipAllocationBudgetUnderRace(t)
	logger := slog.New(slog.NewTextHandler(discardWriter{}, nil))
	router := NewRouter(logger, "budget", "v1")
	router.Use(BodyLimit(8))
	router.Handle(func(_ context.Context, in benchmarkPostInput) (benchmarkPostOutput, error) {
		return benchmarkPostOutput{ID: 1, Name: in.Body.Name}, nil
	})
	payload := []byte(`{"name":"alpha","count":2}`)
	writer := newBenchmarkWriter()

	body := newBenchmarkBody(payload)
	declared := persistentJSONRequest(http.MethodPost, "/items", body)
	assertAllocationBudget(t, "declared body-limit rejection", 15, func() {
		writer.reset()
		body.Reset(payload)
		router.ServeHTTP(writer, declared)
	})

	streamedBody := newBenchmarkBody(payload)
	streamed := persistentJSONRequest(http.MethodPost, "/items", streamedBody)
	streamed.ContentLength = -1
	assertAllocationBudget(t, "streamed body-limit rejection", 20, func() {
		writer.reset()
		streamedBody.Reset(payload)
		router.ServeHTTP(writer, streamed)
	})
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
