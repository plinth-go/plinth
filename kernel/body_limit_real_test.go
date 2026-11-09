package kernel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestBodyLimitOnRealServer(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	router.Use(BodyLimit(8))
	var routeCalls atomic.Int64
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			routeCalls.Add(1)
			next.ServeHTTP(w, r)
		})
	})
	testJSON(router, "POST", "/items", func(_ context.Context, in testInput) (testOutput, error) {
		return testOutput{Slug: in.Body.Slug, Count: in.Body.Count}, nil
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	tests := []struct {
		name       string
		body       func() io.Reader
		wantCalls  int64
		wantDetail string
	}{
		{
			name:       "declared length rejected before handler",
			body:       func() io.Reader { return strings.NewReader(`{"title":"far too large","count":1}`) },
			wantCalls:  0,
			wantDetail: "exceeds 8 bytes",
		},
		{
			name: "chunked length rejected while reading",
			body: func() io.Reader {
				return unknownLengthBody{Reader: strings.NewReader(`{"title":"far too large","count":1}`)}
			},
			wantCalls:  1,
			wantDetail: "exceeds 8 bytes",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			routeCalls.Store(0)
			request, err := http.NewRequest(http.MethodPost, server.URL+"/items", test.body())
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Content-Type", "application/json")
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusRequestEntityTooLarge {
				data, _ := io.ReadAll(response.Body)
				t.Fatalf("status = %d, body = %s", response.StatusCode, data)
			}
			if calls := routeCalls.Load(); calls != test.wantCalls {
				t.Fatalf("route calls = %d, want %d", calls, test.wantCalls)
			}
			if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/problem+json") {
				t.Fatalf("Content-Type = %q", contentType)
			}
			var problem Problem
			if err := json.NewDecoder(response.Body).Decode(&problem); err != nil {
				t.Fatal(err)
			}
			if problem.Status != http.StatusRequestEntityTooLarge || !strings.Contains(problem.Detail, test.wantDetail) {
				t.Fatalf("problem = %#v", problem)
			}
			if strings.Contains(problem.Detail, "request validation failed") {
				t.Fatalf("body limit was reported as validation: %#v", problem)
			}
		})
	}
}

type unknownLengthBody struct {
	io.Reader
}
