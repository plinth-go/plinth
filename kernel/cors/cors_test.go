package cors

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestActualRequestSetsAllowedHeaders(t *testing.T) {
	middleware, err := New(Config{
		AllowedOrigins:   []string{"https://app.example"},
		ExposedHeaders:   []string{"X-Request-ID"},
		AllowCredentials: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "/items", nil)
	request.Header.Set("Origin", "https://app.example")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, request)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example" {
		t.Fatalf("allow origin = %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("allow credentials = %q", got)
	}
	if got := rec.Header().Get("Access-Control-Expose-Headers"); got != "X-Request-ID" {
		t.Fatalf("exposed headers = %q", got)
	}
	if !strings.Contains(rec.Header().Get("Vary"), "Origin") {
		t.Fatalf("vary = %q", rec.Header().Get("Vary"))
	}
}

func TestDisallowedOriginDoesNotGetCORSHeaders(t *testing.T) {
	middleware, err := New(Config{AllowedOrigins: []string{"https://app.example"}})
	if err != nil {
		t.Fatal(err)
	}
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "/items", nil)
	request.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, request)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("status/origin = %d/%q", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestPreflight(t *testing.T) {
	middleware, err := New(Config{
		AllowedOrigins: []string{"https://app.example"},
		AllowedMethods: []string{http.MethodGet, http.MethodPost},
		AllowedHeaders: []string{"Content-Type", "X-CSRF-Token"},
		MaxAge:         10 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("preflight reached next handler")
	}))
	request := httptest.NewRequest(http.MethodOptions, "/items", nil)
	request.Header.Set("Origin", "https://app.example")
	request.Header.Set("Access-Control-Request-Method", "POST")
	request.Header.Set("Access-Control-Request-Headers", "content-type, x-csrf-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, request)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Access-Control-Allow-Methods") != http.MethodPost {
		t.Fatalf("allow methods = %q", rec.Header().Get("Access-Control-Allow-Methods"))
	}
	if rec.Header().Get("Access-Control-Allow-Headers") != "content-type, x-csrf-token" {
		t.Fatalf("allow headers = %q", rec.Header().Get("Access-Control-Allow-Headers"))
	}
	if rec.Header().Get("Access-Control-Max-Age") != "600" {
		t.Fatalf("max age = %q", rec.Header().Get("Access-Control-Max-Age"))
	}
}

func TestPreflightRejectsDisallowedMethodHeaderAndOrigin(t *testing.T) {
	middleware, err := New(Config{
		AllowedOrigins: []string{"https://app.example"},
		AllowedMethods: []string{http.MethodGet},
		AllowedHeaders: []string{"Content-Type"},
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		origin  string
		method  string
		headers string
	}{
		{name: "origin", origin: "https://evil.example", method: http.MethodGet},
		{name: "method", origin: "https://app.example", method: http.MethodPost},
		{name: "headers", origin: "https://app.example", method: http.MethodGet, headers: "X-Secret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodOptions, "/items", nil)
			request.Header.Set("Origin", tt.origin)
			request.Header.Set("Access-Control-Request-Method", tt.method)
			if tt.headers != "" {
				request.Header.Set("Access-Control-Request-Headers", tt.headers)
			}
			rec := httptest.NewRecorder()
			middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("denied preflight reached next handler")
			})).ServeHTTP(rec, request)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d", rec.Code)
			}
		})
	}
}

func TestConfigValidation(t *testing.T) {
	tests := []Config{
		{},
		{AllowedOrigins: []string{"*"}, AllowCredentials: true},
		{AllowedOrigins: []string{"https://app.example/path"}},
		{AllowedOrigins: []string{"https://app.example"}, AllowedMethods: []string{"GET\nPOST"}},
		{AllowedOrigins: []string{"https://app.example"}, AllowedMethods: []string{"*"}},
		{AllowedOrigins: []string{"https://app.example"}, AllowedHeaders: []string{"bad header"}},
		{AllowedOrigins: []string{"https://app.example"}, AllowedHeaders: []string{"*"}},
		{AllowedOrigins: []string{"https://app.example"}, MaxAge: -time.Second},
	}
	for _, cfg := range tests {
		if _, err := New(cfg); err == nil {
			t.Fatalf("accepted invalid config %#v", cfg)
		}
	}
	if _, err := New(Config{AllowedOrigins: []string{"https://APP.example/"}}); err != nil {
		t.Fatal(err)
	}
}

func TestNoOriginPassesThrough(t *testing.T) {
	middleware, err := New(Config{AllowedOrigins: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("status/origin = %d/%q", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
	}
}
