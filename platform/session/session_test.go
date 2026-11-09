package session

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMiddlewareSavesAndLoadsSession(t *testing.T) {
	manager, err := New(NewMemoryStore(), Config{Logger: testLogger(), Secure: false})
	if err != nil {
		t.Fatal(err)
	}
	handler := manager.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/set" {
			manager.Put(r.Context(), "user", "alice")
		} else if got := manager.GetString(r.Context(), "user"); got != "alice" {
			t.Fatalf("session user = %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/set", nil))
	if first.Code != http.StatusNoContent {
		t.Fatalf("set status = %d", first.Code)
	}
	cookies := first.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "plinth_session" || !cookies[0].HttpOnly {
		t.Fatalf("cookies = %#v", cookies)
	}

	second := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/get", nil)
	request.AddCookie(cookies[0])
	handler.ServeHTTP(second, request)
	if second.Code != http.StatusNoContent {
		t.Fatalf("get status = %d body=%s", second.Code, second.Body.String())
	}
}

func TestDestroyClearsSession(t *testing.T) {
	manager, err := New(NewMemoryStore(), Config{Logger: testLogger()})
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	handler := manager.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			manager.Put(r.Context(), "user", "alice")
		} else {
			if err := manager.Destroy(r.Context()); err != nil {
				t.Fatal(err)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/", nil))
	cookie := first.Result().Cookies()[0]
	second := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(cookie)
	handler.ServeHTTP(second, request)
	deleted := second.Result().Cookies()
	if len(deleted) != 1 || deleted[0].MaxAge >= 0 {
		t.Fatalf("delete cookies = %#v", deleted)
	}
}

func TestConfigurationValidation(t *testing.T) {
	if _, err := New(nil, Config{}); err == nil {
		t.Fatal("nil store accepted")
	}
	store := NewMemoryStore()
	tests := []Config{
		{Lifetime: -time.Second},
		{IdleTimeout: -time.Second},
		{Lifetime: time.Minute, IdleTimeout: 2 * time.Minute},
		{Path: "relative"},
		{SameSite: http.SameSiteNoneMode},
		{CookieName: "bad cookie"},
		{Partitioned: true},
	}
	for _, cfg := range tests {
		if _, err := New(store, cfg); err == nil {
			t.Fatalf("accepted invalid config %#v", cfg)
		}
	}
	if _, err := New(store, Config{SameSite: http.SameSiteNoneMode, Secure: true}); err != nil {
		t.Fatal(err)
	}
	var typedNil *testStore
	if _, err := New(typedNil, Config{}); err == nil {
		t.Fatal("typed-nil store accepted")
	}
}

type testStore struct{}

func (*testStore) Delete(string) error { return nil }
func (*testStore) Find(string) ([]byte, bool, error) {
	return nil, false, nil
}
func (*testStore) Commit(string, []byte, time.Time) error { return nil }

func TestNilManagerFailsClosed(t *testing.T) {
	var manager *Manager
	rec := httptest.NewRecorder()
	manager.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("next handler ran")
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
