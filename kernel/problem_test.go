package kernel

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteProblemNormalizesInvalidHTTPErrorStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteProblem(rec, httptest.NewRequest(http.MethodGet, "/broken", nil), &HTTPError{
		Status: 0,
		Title:  "Broken",
		Detail: "database password rejected",
	}, nil)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "password") {
		t.Fatalf("invalid error detail leaked: %s", rec.Body.String())
	}
	var problem Problem
	if err := json.NewDecoder(rec.Body).Decode(&problem); err != nil {
		t.Fatal(err)
	}
	if problem.Status != http.StatusInternalServerError || problem.Title != http.StatusText(http.StatusInternalServerError) {
		t.Fatalf("problem = %#v", problem)
	}
}

func TestWriteProblemPreservesValidHTTPErrorStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteProblem(rec, httptest.NewRequest(http.MethodGet, "/slow", nil), &HTTPError{
		Status: http.StatusTooManyRequests,
		Title:  http.StatusText(http.StatusTooManyRequests),
		Detail: "slow down",
	}, nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestWriteProblemHidesCustomServerErrorDetail(t *testing.T) {
	rec := httptest.NewRecorder()
	var logged string
	WriteProblem(rec, httptest.NewRequest(http.MethodGet, "/broken", nil), &HTTPError{
		Status: http.StatusServiceUnavailable,
		Title:  http.StatusText(http.StatusServiceUnavailable),
		Detail: "database password rejected",
		Err:    errors.New("connection string leaked"),
	}, func(_ string, args ...any) {
		for _, arg := range args {
			if value, ok := arg.(string); ok && value == "database password rejected" {
				logged = value
			}
		}
	})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "password") || strings.Contains(rec.Body.String(), "connection string") {
		t.Fatalf("server detail leaked: %s", rec.Body.String())
	}
	var problem Problem
	if err := json.NewDecoder(rec.Body).Decode(&problem); err != nil {
		t.Fatal(err)
	}
	if problem.Detail != "internal server error" {
		t.Fatalf("detail = %q", problem.Detail)
	}
	if logged != "database password rejected" {
		t.Fatalf("detail was not logged")
	}
}

func TestWriteProblemHidesCustomServerErrorTitle(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusServiceUnavailable, 599} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			rec := httptest.NewRecorder()
			WriteProblem(rec, httptest.NewRequest(http.MethodGet, "/broken", nil), &HTTPError{
				Status: status,
				Title:  "database password rejected",
				Detail: "connection string leaked",
			}, nil)

			if rec.Code != status {
				t.Fatalf("status = %d", rec.Code)
			}
			if strings.Contains(rec.Body.String(), "password") || strings.Contains(rec.Body.String(), "connection string") {
				t.Fatalf("server error content leaked: %s", rec.Body.String())
			}
			var problem Problem
			if err := json.NewDecoder(rec.Body).Decode(&problem); err != nil {
				t.Fatal(err)
			}
			wantTitle := http.StatusText(status)
			if wantTitle == "" {
				wantTitle = http.StatusText(http.StatusInternalServerError)
			}
			if problem.Title != wantTitle {
				t.Fatalf("title = %q, want %q", problem.Title, wantTitle)
			}
			if problem.Detail != "internal server error" {
				t.Fatalf("detail = %q", problem.Detail)
			}
		})
	}
}

func TestWriteProblemTreatsTypedNilErrorsAsInternal(t *testing.T) {
	var httpErr *HTTPError
	var validationErr *ValidationError
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "HTTP error", err: httpErr},
		{name: "wrapped HTTP error", err: fmt.Errorf("wrapped: %w", httpErr)},
		{name: "validation error", err: validationErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			WriteProblem(rec, httptest.NewRequest(http.MethodGet, "/broken", nil), tc.err, nil)
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d", rec.Code)
			}
			var problem Problem
			if err := json.NewDecoder(rec.Body).Decode(&problem); err != nil {
				t.Fatal(err)
			}
			if problem.Status != http.StatusInternalServerError || problem.Detail != "internal server error" {
				t.Fatalf("problem = %#v", problem)
			}
		})
	}
}

func TestTypedNilProblemErrorsAreNilReceiverSafe(t *testing.T) {
	var httpErr *HTTPError
	if got := httpErr.Error(); got == "" {
		t.Fatal("nil HTTPError returned an empty description")
	}
	if err := httpErr.Unwrap(); err != nil {
		t.Fatalf("nil HTTPError unwrap = %v", err)
	}

	var validationErr *ValidationError
	if got := validationErr.Error(); got == "" {
		t.Fatal("nil ValidationError returned an empty description")
	}
}

func TestWriteProblemAcceptsNilRequestAndWriter(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteProblem(rec, nil, BadRequest("invalid"), nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"detail":"invalid"`) {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}

	logged := false
	var writer *nilResponseWriter
	WriteProblem(writer, nil, BadRequest("invalid"), func(string, ...any) { logged = true })
	if !logged {
		t.Fatal("nil response writer was not reported")
	}
}

func TestWriteProblemAcceptsRequestWithoutURL(t *testing.T) {
	rec := httptest.NewRecorder()
	logged := false
	WriteProblem(rec, &http.Request{Method: http.MethodGet}, errors.New("boom"), func(string, ...any) {
		logged = true
	})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	if !logged {
		t.Fatal("request error was not logged")
	}
}

func TestWriteProblemLogsShortWrite(t *testing.T) {
	writer := &shortSSEWriter{header: make(http.Header)}
	logged := false
	WriteProblem(writer, httptest.NewRequest(http.MethodGet, "/broken", nil), BadRequest("invalid"), func(message string, _ ...any) {
		if message == "write problem response" {
			logged = true
		}
	})
	if !logged {
		t.Fatal("short problem write was not logged")
	}
}

func TestProblemConstructors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		status     int
		wantDetail string
	}{
		{name: "bad request", err: BadRequest("invalid request"), status: http.StatusBadRequest, wantDetail: "invalid request"},
		{name: "not found", err: NotFound("missing"), status: http.StatusNotFound, wantDetail: "missing"},
		{name: "conflict", err: Conflict("duplicate"), status: http.StatusConflict, wantDetail: "duplicate"},
		{name: "unprocessable", err: Unprocessable("bad input"), status: http.StatusUnprocessableEntity, wantDetail: "bad input"},
		{name: "too large", err: TooLarge("too big"), status: http.StatusRequestEntityTooLarge, wantDetail: "too big"},
		{name: "unsupported media type", err: UnsupportedMediaType("wrong type"), status: http.StatusUnsupportedMediaType, wantDetail: "wrong type"},
		{name: "internal", err: Internal(errors.New("database password rejected")), status: http.StatusInternalServerError, wantDetail: "internal server error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var httpErr *HTTPError
			if !errors.As(test.err, &httpErr) {
				t.Fatalf("error type = %T", test.err)
			}
			if httpErr.Status != test.status {
				t.Fatalf("HTTPError status = %d", httpErr.Status)
			}

			rec := httptest.NewRecorder()
			WriteProblem(rec, httptest.NewRequest(http.MethodGet, "/problem", nil), test.err, nil)
			if rec.Code != test.status {
				t.Fatalf("response status = %d", rec.Code)
			}
			var problem Problem
			if err := json.NewDecoder(rec.Body).Decode(&problem); err != nil {
				t.Fatal(err)
			}
			if problem.Status != test.status || problem.Title != http.StatusText(test.status) {
				t.Fatalf("problem = %#v", problem)
			}
			if problem.Detail != test.wantDetail {
				t.Fatalf("detail = %q, want %q", problem.Detail, test.wantDetail)
			}
			if test.status >= http.StatusInternalServerError && strings.Contains(rec.Body.String(), "password") {
				t.Fatalf("server error detail leaked: %s", rec.Body.String())
			}
		})
	}
}
