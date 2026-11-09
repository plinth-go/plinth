// Package prometheus provides optional HTTP metrics backed by the Prometheus
// client library. It is deliberately separate from the core HTTP packages so
// applications that do not scrape Prometheus do not link the client.
package prometheus

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/plinth-go/plinth/platform/observability/internal/response"
	prom "github.com/prometheus/client_golang/prometheus"
)

const (
	defaultNamespace = "plinth"
	defaultSubsystem = "http"
)

// Config controls metric names, buckets, and request filtering.
type Config struct {
	// Namespace is the metric namespace. Empty selects "plinth".
	Namespace string
	// Subsystem is the metric subsystem. Empty selects "http".
	Subsystem string
	// DurationBuckets overrides the default histogram buckets in seconds.
	DurationBuckets []float64
	// Skip suppresses metrics for matching requests. It is evaluated before
	// the handler runs and should be cheap and side-effect free.
	Skip func(*http.Request) bool
}

// Metrics owns the collectors registered for one HTTP middleware chain.
type Metrics struct {
	requests  *prom.CounterVec
	durations *prom.HistogramVec
	inFlight  *prom.GaugeVec
	skip      func(*http.Request) bool
}

// New validates config, registers collectors, and returns an HTTP middleware.
// Registration errors are returned unchanged so applications can detect a
// duplicate registry entry instead of panicking during startup.
func New(registerer prom.Registerer, cfg Config) (*Metrics, error) {
	if registerer == nil {
		return nil, errors.New("prometheus registerer is nil")
	}
	namespace := strings.TrimSpace(cfg.Namespace)
	if namespace == "" {
		namespace = defaultNamespace
	}
	subsystem := strings.TrimSpace(cfg.Subsystem)
	if subsystem == "" {
		subsystem = defaultSubsystem
	}
	if !validMetricName(namespace) {
		return nil, fmt.Errorf("prometheus namespace %q is invalid", namespace)
	}
	if !validMetricName(subsystem) {
		return nil, fmt.Errorf("prometheus subsystem %q is invalid", subsystem)
	}
	buckets := cfg.DurationBuckets
	if len(buckets) == 0 {
		buckets = prom.DefBuckets
	} else {
		buckets = append([]float64(nil), buckets...)
		for _, bucket := range buckets {
			if bucket <= 0 {
				return nil, errors.New("prometheus duration buckets must be positive")
			}
		}
	}

	metrics := &Metrics{
		requests: prom.NewCounterVec(prom.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "requests_total",
			Help:      "Total HTTP requests observed after routing.",
		}, []string{"method", "route", "status"}),
		durations: prom.NewHistogramVec(prom.HistogramOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "request_duration_seconds",
			Help:      "HTTP request duration in seconds, labelled by route.",
			Buckets:   buckets,
		}, []string{"method", "route", "status"}),
		inFlight: prom.NewGaugeVec(prom.GaugeOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "requests_in_flight",
			Help:      "HTTP requests currently executing.",
		}, []string{"method", "route"}),
		skip: cfg.Skip,
	}

	registered := make([]prom.Collector, 0, 3)
	for _, collector := range []prom.Collector{metrics.requests, metrics.durations, metrics.inFlight} {
		if err := registerer.Register(collector); err != nil {
			for _, previous := range registered {
				registerer.Unregister(previous)
			}
			return nil, err
		}
		registered = append(registered, collector)
	}
	return metrics, nil
}

// Middleware records request count, duration, status, and in-flight requests.
// Route labels use net/http's matched pattern, never an arbitrary request path,
// to keep cardinality bounded.
func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if next == nil {
			http.Error(w, "observability middleware has no next handler", http.StatusInternalServerError)
			return
		}
		if m == nil || m.skip != nil && m.skip(r) {
			next.ServeHTTP(w, r)
			return
		}
		method := requestMethod(r)
		route := routeLabel(r)
		m.inFlight.WithLabelValues(method, route).Inc()
		started := time.Now()
		writer := &response.Writer{ResponseWriter: w}
		defer func() {
			m.inFlight.WithLabelValues(method, route).Dec()
			m.requests.WithLabelValues(method, route, statusLabel(writer.StatusCode())).Inc()
			m.durations.WithLabelValues(method, route, statusLabel(writer.StatusCode())).Observe(time.Since(started).Seconds())
			if recovered := recover(); recovered != nil {
				panic(recovered)
			}
		}()
		next.ServeHTTP(writer, r)
	})
}

func requestMethod(r *http.Request) string {
	if r == nil || r.Method == "" {
		return "UNKNOWN"
	}
	return r.Method
}

// routeLabel returns the matched ServeMux pattern when available. Handlers
// using this middleware with a custom router may leave Pattern empty; that is
// still safer than recording every arbitrary path.
func routeLabel(r *http.Request) string {
	if r == nil {
		return "unmatched"
	}
	if route := strings.TrimSpace(r.Pattern); route != "" {
		return route
	}
	return "unmatched"
}

func statusLabel(status int) string {
	if status < 100 || status > 599 {
		return "500"
	}
	return fmt.Sprintf("%d", status)
}

func validMetricName(value string) bool {
	if value == "" {
		return false
	}
	for index, char := range value {
		if char == '_' || char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || index > 0 && char >= '0' && char <= '9' {
			continue
		}
		return false
	}
	return true
}
