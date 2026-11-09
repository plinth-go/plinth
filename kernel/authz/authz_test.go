package authz

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/plinth-go/plinth/kernel"
)

type protectedInput struct {
	kernel.Meta `route:"GET /protected" authz:"links:read,links:write"`
}

func TestMiddlewareAllowsDeclaredPermissions(t *testing.T) {
	var called bool
	middleware, err := New(PermissionSourceFunc(func(context.Context) ([]string, error) {
		return []string{"links:read", "links:write", "unused"}, nil
	}), Config{Logger: testLogger()})
	if err != nil {
		t.Fatal(err)
	}
	router := kernel.NewRouter(testLogger(), "test", "1")
	router.Use(middleware)
	router.Handle(func(context.Context, protectedInput) (struct{}, error) {
		called = true
		return struct{}{}, nil
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/protected", nil))
	if rec.Code != http.StatusNoContent || !called {
		t.Fatalf("status/called = %d/%t, body = %s", rec.Code, called, rec.Body.String())
	}
}

func TestMiddlewareDeniesMissingPermissions(t *testing.T) {
	var called bool
	middleware, err := New(PermissionSourceFunc(func(context.Context) ([]string, error) {
		return []string{"links:read"}, nil
	}), Config{Logger: testLogger()})
	if err != nil {
		t.Fatal(err)
	}
	router := kernel.NewRouter(testLogger(), "test", "1")
	router.Use(middleware)
	router.Handle(func(context.Context, protectedInput) (struct{}, error) {
		called = true
		return struct{}{}, nil
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/protected", nil))
	if rec.Code != http.StatusForbidden || called {
		t.Fatalf("status/called = %d/%t, body = %s", rec.Code, called, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "access denied") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestMiddlewareWildcardAndNoRequirements(t *testing.T) {
	t.Run("wildcard", func(t *testing.T) {
		middleware, err := New(PermissionSourceFunc(func(context.Context) ([]string, error) {
			return []string{"*"}, nil
		}), Config{Logger: testLogger()})
		if err != nil {
			t.Fatal(err)
		}
		router := kernel.NewRouter(testLogger(), "test", "1")
		router.Use(middleware)
		router.Handle(func(context.Context, protectedInput) (struct{}, error) { return struct{}{}, nil })
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/protected", nil))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d", rec.Code)
		}
	})

	t.Run("no requirements", func(t *testing.T) {
		calls := 0
		middleware, err := New(PermissionSourceFunc(func(context.Context) ([]string, error) {
			calls++
			return nil, errors.New("must not be called")
		}), Config{Logger: testLogger()})
		if err != nil {
			t.Fatal(err)
		}
		router := kernel.NewRouter(testLogger(), "test", "1")
		router.Use(middleware)
		router.Raw("GET", "/public", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}), kernel.Hidden())

		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/public", nil))
		if rec.Code != http.StatusNoContent || calls != 0 {
			t.Fatalf("status/calls = %d/%d", rec.Code, calls)
		}
	})
}

func TestMiddlewarePolicyFailureAndMissingMetadata(t *testing.T) {
	t.Run("policy error", func(t *testing.T) {
		middleware, err := New(PermissionSourceFunc(func(context.Context) ([]string, error) {
			return nil, errors.New("policy unavailable")
		}), Config{Logger: testLogger()})
		if err != nil {
			t.Fatal(err)
		}
		router := kernel.NewRouter(testLogger(), "test", "1")
		router.Use(middleware)
		router.Handle(func(context.Context, protectedInput) (struct{}, error) { return struct{}{}, nil })
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/protected", nil))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d", rec.Code)
		}
	})

	t.Run("missing metadata", func(t *testing.T) {
		middleware, err := New(PermissionSourceFunc(func(context.Context) ([]string, error) {
			return nil, nil
		}), Config{Logger: testLogger()})
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Fatal("next handler was called")
		})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d", rec.Code)
		}
	})
}

func TestNewValidationAndRawPermissions(t *testing.T) {
	if _, err := New(nil, Config{}); err == nil {
		t.Fatal("nil source was accepted")
	}
	var typedNil *permissionSource
	if _, err := New(typedNil, Config{}); err == nil {
		t.Fatal("typed-nil source was accepted")
	}

	middleware, err := New(PermissionSourceFunc(func(context.Context) ([]string, error) {
		return []string{"admin"}, nil
	}), Config{Logger: testLogger()})
	if err != nil {
		t.Fatal(err)
	}
	router := kernel.NewRouter(testLogger(), "test", "1")
	router.Use(middleware)
	router.Raw("POST", "/admin", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), kernel.RequirePermissions("admin"), kernel.Hidden())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

type permissionSource struct{}

func (*permissionSource) Permissions(context.Context) ([]string, error) { return nil, nil }

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
