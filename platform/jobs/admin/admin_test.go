package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/plinth-go/plinth/platform/jobs"
)

type fakeStore struct {
	list   func(context.Context, int, int) ([]jobs.Job, error)
	retry  func(context.Context, string) (jobs.Job, error)
	delete func(context.Context, string) error
}

func (s fakeStore) ListFailed(ctx context.Context, limit, offset int) ([]jobs.Job, error) {
	return s.list(ctx, limit, offset)
}

func (s fakeStore) RetryFailed(ctx context.Context, id string) (jobs.Job, error) {
	return s.retry(ctx, id)
}

func (s fakeStore) DeleteFailed(ctx context.Context, id string) error {
	return s.delete(ctx, id)
}

func TestFailedJobRoutes(t *testing.T) {
	const id = "018f0000-0000-7000-8000-000000000000"
	store := fakeStore{
		list: func(_ context.Context, limit, offset int) ([]jobs.Job, error) {
			if limit != 25 || offset != 5 {
				t.Fatalf("pagination = %d, %d", limit, offset)
			}
			return nil, nil
		},
		retry: func(_ context.Context, got string) (jobs.Job, error) {
			if got != id {
				t.Fatalf("retry id = %q", got)
			}
			return jobs.Job{ID: id, Status: "pending"}, nil
		},
		delete: func(_ context.Context, got string) error {
			if got != id {
				t.Fatalf("delete id = %q", got)
			}
			return nil
		},
	}
	handler := NewHandler(store, nil)

	list := httptest.NewRecorder()
	handler.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/jobs/failed?limit=25&offset=5", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"jobs":[]`) {
		t.Fatalf("list = %d %q", list.Code, list.Body.String())
	}
	if list.Header().Get("Cache-Control") != "no-store" || list.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("security headers = %#v", list.Header())
	}

	retry := httptest.NewRecorder()
	handler.ServeHTTP(retry, httptest.NewRequest(http.MethodPost, "/jobs/"+id+"/retry", nil))
	if retry.Code != http.StatusOK {
		t.Fatalf("retry = %d %q", retry.Code, retry.Body.String())
	}
	var job jobs.Job
	if err := json.Unmarshal(retry.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if job.ID != id || job.Status != "pending" {
		t.Fatalf("retried job = %#v", job)
	}

	del := httptest.NewRecorder()
	handler.ServeHTTP(del, httptest.NewRequest(http.MethodDelete, "/jobs/"+id, nil))
	if del.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %q", del.Code, del.Body.String())
	}
}

func TestFailedJobErrors(t *testing.T) {
	tests := []struct {
		name   string
		store  fakeStore
		method string
		target string
		want   int
	}{
		{
			name: "invalid pagination", store: fakeStore{},
			method: http.MethodGet, target: "/jobs/failed?limit=101", want: http.StatusBadRequest,
		},
		{
			name: "not found", store: fakeStore{retry: func(context.Context, string) (jobs.Job, error) {
				return jobs.Job{}, jobs.ErrNotFound
			}},
			method: http.MethodPost, target: "/jobs/missing/retry", want: http.StatusNotFound,
		},
		{
			name: "wrong state", store: fakeStore{delete: func(context.Context, string) error {
				return jobs.ErrNotFailed
			}},
			method: http.MethodDelete, target: "/jobs/running", want: http.StatusConflict,
		},
		{
			name: "invalid id", store: fakeStore{retry: func(context.Context, string) (jobs.Job, error) {
				return jobs.Job{}, jobs.ErrInvalidJobID
			}},
			method: http.MethodPost, target: "/jobs/bad/retry", want: http.StatusBadRequest,
		},
		{
			name: "internal", store: fakeStore{list: func(context.Context, int, int) ([]jobs.Job, error) {
				return nil, errors.New("database unavailable")
			}},
			method: http.MethodGet, target: "/jobs/failed", want: http.StatusInternalServerError,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			NewHandler(test.store, nil).ServeHTTP(rec, httptest.NewRequest(test.method, test.target, nil))
			if rec.Code != test.want {
				t.Fatalf("status = %d, want %d; body = %q", rec.Code, test.want, rec.Body.String())
			}
		})
	}
}

func TestMethodNotAllowedUsesMuxContract(t *testing.T) {
	store := fakeStore{
		list:   func(context.Context, int, int) ([]jobs.Job, error) { return nil, nil },
		retry:  func(context.Context, string) (jobs.Job, error) { return jobs.Job{}, nil },
		delete: func(context.Context, string) error { return nil },
	}
	rec := httptest.NewRecorder()
	NewHandler(store, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/jobs/failed", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", rec.Code)
	}
}
