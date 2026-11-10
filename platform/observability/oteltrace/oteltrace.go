// Package oteltrace provides optional OpenTelemetry HTTP tracing without adding
// OpenTelemetry dependencies to the framework's core HTTP packages.
package oteltrace

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/plinth-go/plinth/platform/observability/internal/response"
	"github.com/plinth-go/plinth/utils"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// Config controls span naming and request filtering.
type Config struct {
	// TracerName selects the tracer. Empty selects "github.com/plinth-go/plinth/http".
	TracerName string
	// Propagator extracts incoming trace context. Empty selects the global
	// OpenTelemetry propagator.
	Propagator propagation.TextMapPropagator
	// Skip suppresses tracing for matching requests.
	Skip func(*http.Request) bool
}

// Tracer creates server spans for matching HTTP requests.
type Tracer struct {
	tracer     trace.Tracer
	propagator propagation.TextMapPropagator
	skip       func(*http.Request) bool
}

// New validates config and returns an HTTP middleware adapter.
func New(provider trace.TracerProvider, cfg Config) (*Tracer, error) {
	if provider == nil {
		return nil, errors.New("otel tracer provider is nil")
	}
	name := utils.IfEmptyElse(strings.TrimSpace(cfg.TracerName), "github.com/plinth-go/plinth/http")
	propagator := cfg.Propagator
	if propagator == nil {
		propagator = propagation.TraceContext{}
	}
	return &Tracer{tracer: provider.Tracer(name), propagator: propagator, skip: cfg.Skip}, nil
}

// Middleware starts a server span, attaches it to the request context, and
// records method, route, status, bytes, and handler errors.
func (t *Tracer) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if next == nil {
			http.Error(w, "observability middleware has no next handler", http.StatusInternalServerError)
			return
		}
		if t == nil || t.skip != nil && t.skip(r) {
			next.ServeHTTP(w, r)
			return
		}
		ctx := t.propagator.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := t.tracer.Start(ctx, spanName(r), trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()

		writer := &response.Writer{ResponseWriter: w}
		defer func() {
			status := writer.StatusCode()
			if recovered := recover(); recovered != nil {
				span.SetAttributes(
					attribute.String("http.request.method", requestMethod(r)),
					attribute.String("http.route", routeLabel(r)),
					attribute.Int("http.response.status_code", http.StatusInternalServerError),
				)
				span.RecordError(errorFromPanic(recovered))
				span.SetStatus(codes.Error, "handler panic")
				panic(recovered)
			}
			span.SetAttributes(
				attribute.String("http.request.method", requestMethod(r)),
				attribute.String("http.route", routeLabel(r)),
				attribute.Int("http.response.status_code", status),
				attribute.Int64("http.response.body.size", writer.Bytes),
			)
			if status >= http.StatusInternalServerError {
				span.SetStatus(codes.Error, http.StatusText(status))
			} else {
				span.SetStatus(codes.Unset, "")
			}
		}()
		next.ServeHTTP(writer, r.WithContext(ctx))
	})
}

func spanName(r *http.Request) string {
	method := requestMethod(r)
	route := routeLabel(r)
	if route == "unmatched" {
		return method
	}
	if fields := strings.Fields(route); len(fields) > 1 && strings.EqualFold(fields[0], method) {
		route = strings.Join(fields[1:], " ")
	}
	return method + " " + route
}

func requestMethod(r *http.Request) string {
	if r == nil || r.Method == "" {
		return "UNKNOWN"
	}
	return r.Method
}

func routeLabel(r *http.Request) string {
	if r == nil {
		return "unmatched"
	}
	if route := strings.TrimSpace(r.Pattern); route != "" {
		return route
	}
	return "unmatched"
}

func errorFromPanic(value any) error {
	if err, ok := value.(error); ok {
		return err
	}
	if value == nil {
		return errors.New("handler panic")
	}
	return fmt.Errorf("handler panic: %v", value)
}
