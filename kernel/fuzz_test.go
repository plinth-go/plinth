package kernel

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type fuzzJSONBody struct {
	Name   string            `json:"name"`
	Values []int             `json:"values,omitempty"`
	Nested map[string]string `json:"nested,omitempty"`
}

type fuzzJSONInput struct {
	Meta `route:"POST /fuzz/json"`
	Body fuzzJSONBody `body:",required"`
}

type fuzzJSONOutput struct {
	Accepted bool   `json:"accepted"`
	Name     string `json:"name"`
}

// FuzzStrictJSONBinding exercises the framework's strict JSON contract through
// the same router path used by applications. It checks response invariants; it
// intentionally does not re-test encoding/json correctness.
func FuzzStrictJSONBinding(f *testing.F) {
	router := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), "fuzz", "1")
	router.Handle(func(_ context.Context, in fuzzJSONInput) (fuzzJSONOutput, error) {
		return fuzzJSONOutput{Accepted: true, Name: in.Body.Name}, nil
	})

	for _, seed := range []string{
		`{"name":"ok"}`,
		`{"name":"ok","values":[1,2],"nested":{"key":"value"}}`,
		`null`,
		`{"name":null}`,
		`{"name":"ok","unknown":true}`,
		`{"name":"first"} {"name":"second"}`,
		`{"name":"quote: \" null"}`,
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		request := httptest.NewRequest(http.MethodPost, "/fuzz/json", strings.NewReader(string(body)))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)

		if response.Code < 100 || response.Code > 599 {
			t.Fatalf("invalid response status %d", response.Code)
		}
		if response.Code >= http.StatusOK && response.Code < http.StatusMultipleChoices && !json.Valid(response.Body.Bytes()) {
			t.Fatalf("successful response is not valid JSON: %q", response.Body.Bytes())
		}
	})
}

// FuzzRouterPath checks that arbitrary request paths reach net/http.ServeMux
// safely. Path matching, cleaning, and redirect behavior remain stdlib-owned.
func FuzzRouterPath(f *testing.F) {
	router := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), "fuzz", "1")
	router.Raw("GET", "/static", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}), Hidden())
	router.Raw("GET", "/items/{id}", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "item")
	}), Hidden())

	for _, seed := range []string{"/static", "/items/42", "/missing", "/../static", "/items/%2e%2e/%2f"} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, path string) {
		request := &http.Request{
			Method: http.MethodGet,
			URL:    &url.URL{Path: "/" + strings.TrimPrefix(path, "/")},
			Header: make(http.Header),
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)

		if response.Code < 100 || response.Code > 599 {
			t.Fatalf("invalid response status %d", response.Code)
		}
	})
}
