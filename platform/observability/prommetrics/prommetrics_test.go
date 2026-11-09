package prommetrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	prom "github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestMiddlewareRecordsBoundedRouteLabels(t *testing.T) {
	registry := prom.NewRegistry()
	metrics, err := New(registry, Config{})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /items/{id}", metrics.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("ok"))
	})))

	request := httptest.NewRequest(http.MethodGet, "/items/42?debug=1", nil)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d", recorder.Code)
	}

	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	requests := metricFamily(t, families, "plinth_http_requests_total")
	if len(requests.Metric) != 1 {
		t.Fatalf("request metric count = %d", len(requests.Metric))
	}
	counter := requests.Metric[0]
	if got := counter.GetCounter().GetValue(); got != 1 {
		t.Fatalf("request count = %v", got)
	}
	labels := labelValues(counter)
	if labels["method"] != http.MethodGet || labels["route"] != "GET /items/{id}" || labels["status"] != "201" {
		t.Fatalf("request labels = %#v", labels)
	}
	if strings.Contains(labels["route"], "42") {
		t.Fatalf("route label contains request data: %q", labels["route"])
	}

	durations := metricFamily(t, families, "plinth_http_request_duration_seconds")
	if len(durations.Metric) != 1 || durations.Metric[0].GetHistogram().GetSampleCount() != 1 {
		t.Fatalf("duration metric = %#v", durations.Metric)
	}
	inFlight := metricFamily(t, families, "plinth_http_requests_in_flight")
	if got := inFlight.Metric[0].GetGauge().GetValue(); got != 0 {
		t.Fatalf("in-flight gauge = %v", got)
	}
}

func TestMiddlewareSkipAndValidation(t *testing.T) {
	if _, err := New(nil, Config{}); err == nil {
		t.Fatal("nil registerer was accepted")
	}
	registry := prom.NewRegistry()
	if _, err := New(registry, Config{Namespace: "bad-name"}); err == nil {
		t.Fatal("invalid namespace was accepted")
	}
	if _, err := New(registry, Config{DurationBuckets: []float64{0}}); err == nil {
		t.Fatal("invalid bucket was accepted")
	}

	metrics, err := New(registry, Config{Skip: func(*http.Request) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /", metrics.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if len(family.Metric) != 0 {
			t.Fatalf("skipped request recorded %s", family.GetName())
		}
	}

	if _, err := New(registry, Config{}); err == nil {
		t.Fatal("duplicate registration was accepted")
	}
}

func metricFamily(t *testing.T, families []*dto.MetricFamily, name string) *dto.MetricFamily {
	t.Helper()
	for _, family := range families {
		if family.GetName() == name {
			return family
		}
	}
	t.Fatalf("metric family %q not found", name)
	return nil
}

func labelValues(metric *dto.Metric) map[string]string {
	values := make(map[string]string, len(metric.Label))
	for _, label := range metric.Label {
		values[label.GetName()] = label.GetValue()
	}
	return values
}
