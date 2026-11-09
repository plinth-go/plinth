package csrf

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSafeRequestIssuesTokenAndStateChangingRequestValidatesIt(t *testing.T) {
	store := NewMemoryStore()
	middleware, err := New(Config{Store: store, Logger: testLogger(), Secure: false})
	if err != nil {
		t.Fatal(err)
	}
	var issued string
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issued = TokenFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/", nil))
	if first.Code != http.StatusNoContent || issued == "" {
		t.Fatalf("safe request = %d token=%q", first.Code, issued)
	}
	cookie := first.Result().Cookies()[0]
	if cookie.Name != defaultCookieName || cookie.Value != issued || cookie.Secure || cookie.HttpOnly {
		t.Fatalf("cookie = %#v", cookie)
	}

	validRequest := httptest.NewRequest(http.MethodPost, "/", nil)
	validRequest.AddCookie(cookie)
	validRequest.Header.Set(defaultHeaderName, issued)
	valid := httptest.NewRecorder()
	handler.ServeHTTP(valid, validRequest)
	if valid.Code != http.StatusNoContent {
		t.Fatalf("valid request = %d body=%s", valid.Code, valid.Body.String())
	}

	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.Header.Del(defaultHeaderName) },
		func(r *http.Request) { r.Header.Set(defaultHeaderName, "wrong") },
		func(r *http.Request) { r.Header.Set(defaultHeaderName, issued); r.Header.Del("Cookie") },
	} {
		request := httptest.NewRequest(http.MethodPost, "/", nil)
		request.AddCookie(cookie)
		request.Header.Set(defaultHeaderName, issued)
		mutate(request)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, request)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("invalid request = %d body=%s", rec.Code, rec.Body.String())
		}
	}
}

func TestStoreAndConfigurationFailures(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("nil store accepted")
	}
	var typedNil *MemoryStore
	if _, err := New(Config{Store: typedNil}); err == nil {
		t.Fatal("typed-nil store accepted")
	}
	store := NewMemoryStore()
	tests := []Config{
		{Store: store, Path: "relative"},
		{Store: store, TTL: -time.Second},
		{Store: store, SameSite: http.SameSiteNoneMode},
		{Store: store, Methods: []string{""}},
		{Store: store, CookieName: "bad cookie"},
	}
	for _, cfg := range tests {
		if _, err := New(cfg); err == nil {
			t.Fatalf("accepted invalid config %#v", cfg)
		}
	}

	failing := failingStore{}
	middleware, err := New(Config{Store: failing, Logger: testLogger()})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("next handler ran")
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("store failure status = %d", rec.Code)
	}
}

func TestMalformedCookieIsReissuedOnSafeRequestAndRejectedOnUnsafeRequest(t *testing.T) {
	store := NewMemoryStore()
	middleware, err := New(Config{Store: store, Logger: testLogger(), Secure: false})
	if err != nil {
		t.Fatal(err)
	}
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if TokenFromContext(r.Context()) == "malformed" {
			t.Fatal("malformed token reached handler")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{Name: defaultCookieName, Value: "malformed"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, request)
	cookies := rec.Result().Cookies()
	if rec.Code != http.StatusNoContent || len(cookies) != 1 || !validIssuedToken(cookies[0].Value) {
		t.Fatalf("status/cookies = %d/%#v", rec.Code, cookies)
	}

	request = httptest.NewRequest(http.MethodPost, "/", nil)
	request.AddCookie(&http.Cookie{Name: defaultCookieName, Value: "malformed"})
	request.Header.Set(defaultHeaderName, "malformed")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, request)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestMemoryStoreExpiry(t *testing.T) {
	store := NewMemoryStore()
	now := time.Unix(100, 0)
	store.now = func() time.Time { return now }
	if err := store.Put(context.Background(), "id", time.Minute); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.Exists(context.Background(), "id"); err != nil || !ok {
		t.Fatalf("exists = %t, %v", ok, err)
	}
	now = now.Add(time.Minute)
	if ok, err := store.Exists(context.Background(), "id"); err != nil || ok {
		t.Fatalf("expired exists = %t, %v", ok, err)
	}
}

func TestMemoryStoreIsBounded(t *testing.T) {
	store := NewMemoryStore()
	store.maxEntries = 2
	now := time.Unix(100, 0)
	store.now = func() time.Time { return now }
	ctx := context.Background()
	for _, id := range []string{"a", "b"} {
		if err := store.Put(ctx, id, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Put(ctx, "c", time.Minute); err == nil {
		t.Fatal("capacity overflow was accepted")
	}
	now = now.Add(2 * time.Minute)
	if err := store.Put(ctx, "c", time.Minute); err != nil {
		t.Fatalf("expired entries were not pruned: %v", err)
	}
}

func TestCustomTokenExtractor(t *testing.T) {
	middleware, err := New(Config{
		Store: NewMemoryStore(), Logger: testLogger(), Secure: false,
		Token: func(r *http.Request) string { return r.Header.Get("X-Form-Token") },
	})
	if err != nil {
		t.Fatal(err)
	}
	var issued string
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issued = TokenFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/", nil))
	cookie := first.Result().Cookies()[0]
	if issued == "" || cookie.Value != issued {
		t.Fatalf("issued token = %q, cookie = %q", issued, cookie.Value)
	}
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.AddCookie(cookie)
	request.Header.Set("X-Form-Token", cookie.Value)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, request)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

type failingStore struct{}

func (failingStore) Put(context.Context, string, time.Duration) error {
	return errors.New("unavailable")
}
func (failingStore) Exists(context.Context, string) (bool, error) {
	return false, errors.New("unavailable")
}
func (failingStore) Delete(context.Context, string) error { return errors.New("unavailable") }

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
