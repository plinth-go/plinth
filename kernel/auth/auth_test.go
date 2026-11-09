package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type verifierFunc func(context.Context, string) (Principal, error)

func (f verifierFunc) Verify(ctx context.Context, token string) (Principal, error) {
	return f(ctx, token)
}

func TestMiddlewareAttachesPrincipal(t *testing.T) {
	verifier := verifierFunc(func(_ context.Context, token string) (Principal, error) {
		if token != "good" {
			return Principal{}, errors.New("bad token")
		}
		return Principal{Subject: "user-1", Issuer: "test"}, nil
	})
	middleware, err := New(verifier, Config{Realm: "test"}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := FromContext(r.Context())
		if !ok || principal.Subject != "user-1" || principal.Issuer != "test" {
			t.Fatalf("principal = %#v, %t", principal, ok)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer good")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestMiddlewareRejectsMissingAndInvalidCredentials(t *testing.T) {
	verifier := verifierFunc(func(context.Context, string) (Principal, error) {
		return Principal{}, errors.New("invalid")
	})
	middleware, err := New(verifier, Config{}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	handler := middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("next handler was called")
	}))
	for _, headers := range [][]string{
		nil,
		{"Bearer"},
		{"Basic abc"},
		{"Bearer bad"},
		{"Bearer one", "Bearer two"},
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		for _, value := range headers {
			req.Header.Add("Authorization", value)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("headers %#v status = %d", headers, rec.Code)
		}
		if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer realm="api"` {
			t.Fatalf("challenge = %q", got)
		}
	}
}

func TestOptionalAllowsMissingHeaderButNotBadToken(t *testing.T) {
	verifier := verifierFunc(func(context.Context, string) (Principal, error) {
		return Principal{}, errors.New("invalid")
	})
	middleware, err := New(verifier, Config{Optional: true}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("missing header status = %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer bad")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad token status = %d", rec.Code)
	}
}

func TestConfigValidation(t *testing.T) {
	verifier := verifierFunc(func(context.Context, string) (Principal, error) { return Principal{}, nil })
	if _, err := New(nil, Config{}, testLogger()); err == nil {
		t.Fatal("nil verifier accepted")
	}
	if _, err := New(verifier, Config{Realm: `bad"realm`}, testLogger()); err == nil || !strings.Contains(err.Error(), "realm") {
		t.Fatalf("realm error = %v", err)
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
