package oteltrace

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestMiddlewareCreatesServerSpan(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	tracer, err := New(provider, Config{})
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /items/{id}", tracer.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("ok"))
	})))
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/items/42", nil))

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("span count = %d", len(spans))
	}
	span := spans[0]
	if span.Name != "GET /items/{id}" {
		t.Fatalf("span name = %q", span.Name)
	}
	attributes := spanAttributes(span)
	if attributes["http.request.method"] != http.MethodGet || attributes["http.route"] != "GET /items/{id}" {
		t.Fatalf("attributes = %#v", attributes)
	}
	if attributes["http.response.status_code"] != int64(http.StatusCreated) {
		t.Fatalf("status attribute = %#v", attributes["http.response.status_code"])
	}
	if span.Status.Code != codes.Unset {
		t.Fatalf("span status = %v", span.Status)
	}
}

func TestMiddlewareSkipValidationAndPanic(t *testing.T) {
	if _, err := New(nil, Config{}); err == nil {
		t.Fatal("nil provider was accepted")
	}
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	skipped, err := New(provider, Config{Skip: func(*http.Request) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /skip", skipped.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})))
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/skip", nil))
	if len(exporter.GetSpans()) != 0 {
		t.Fatal("skipped request created a span")
	}

	tracer, err := New(provider, Config{})
	if err != nil {
		t.Fatal(err)
	}
	panicMux := http.NewServeMux()
	panicMux.Handle("GET /panic", tracer.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})))
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("panic was swallowed")
			}
		}()
		panicMux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/panic", nil))
	}()
	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("panic span count = %d", len(spans))
	}
	if spans[0].Status.Code != codes.Error {
		t.Fatalf("panic span status = %v", spans[0].Status)
	}
}

func spanAttributes(span tracetest.SpanStub) map[string]any {
	values := make(map[string]any, len(span.Attributes))
	for _, attribute := range span.Attributes {
		values[string(attribute.Key)] = attribute.Value.AsInterface()
	}
	return values
}
