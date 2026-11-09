package kernel

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

type benchmarkWriter struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func newBenchmarkWriter() *benchmarkWriter {
	return &benchmarkWriter{header: make(http.Header)}
}

func (w *benchmarkWriter) Header() http.Header { return w.header }

func (w *benchmarkWriter) WriteHeader(status int) { w.status = status }

func (w *benchmarkWriter) Write(body []byte) (int, error) { return w.body.Write(body) }

func (w *benchmarkWriter) reset() {
	w.status = 0
	w.body.Reset()
	clear(w.header)
}

type benchmarkGetInput struct {
	Meta `route:"GET /items/{id}"`
	ID   int    `path:"id"`
	Q    string `query:"q"`
}

type benchmarkGetOutput struct {
	ID int    `json:"id"`
	Q  string `json:"q"`
}

type benchmarkBoundInput struct {
	Meta   `route:"GET /bound/{id}"`
	ID     int    `path:"id"`
	Q      string `query:"q"`
	Tenant string `header:"X-Tenant"`
}

type benchmarkStaticInput struct {
	Meta `route:"GET /static"`
}

type benchmarkEncodeFailureInput struct {
	Meta `route:"GET /encode-failure"`
}

type benchmarkEncodeFailure struct{}

var errBenchmarkEncodeFailure = errors.New("benchmark encode failure")

func (benchmarkEncodeFailure) MarshalJSON() ([]byte, error) {
	return nil, errBenchmarkEncodeFailure
}

type benchmarkPostInput struct {
	Meta `route:"POST /items"`
	Body benchmarkPostBody `body:",required"`
}

type benchmarkPostBody struct {
	Name  string `json:"name" validate:"required,min=2"`
	Count int    `json:"count" validate:"min=1"`
}

type benchmarkPostOutput struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func benchmarkRouter(tb testing.TB) http.Handler {
	tb.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := NewRouter(logger, "benchmark", "v1")
	router.Handle(func(_ context.Context, in benchmarkGetInput) (benchmarkGetOutput, error) {
		return benchmarkGetOutput{ID: in.ID, Q: in.Q}, nil
	})
	router.Handle(func(_ context.Context, in benchmarkBoundInput) (benchmarkGetOutput, error) {
		return benchmarkGetOutput{ID: in.ID, Q: in.Q}, nil
	})
	router.Handle(func(_ context.Context, _ benchmarkStaticInput) (benchmarkGetOutput, error) {
		return benchmarkGetOutput{ID: 1, Q: "static"}, nil
	})
	router.Handle(func(_ context.Context, _ benchmarkEncodeFailureInput) (benchmarkEncodeFailure, error) {
		return benchmarkEncodeFailure{}, nil
	})
	router.Handle(func(_ context.Context, in benchmarkPostInput) (benchmarkPostOutput, error) {
		return benchmarkPostOutput{ID: 1, Name: in.Body.Name}, nil
	})
	router.Raw("GET", "/raw", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	}), Hidden())
	return router
}

func benchmarkRequest(method, target string, body []byte) *http.Request {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request := httptest.NewRequest(method, target, reader)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

type benchmarkBody struct {
	*bytes.Reader
}

func newBenchmarkBody(body []byte) *benchmarkBody {
	return &benchmarkBody{Reader: bytes.NewReader(body)}
}

func (b *benchmarkBody) Close() error { return nil }

func (b *benchmarkBody) Reset(body []byte) {
	b.Reader.Reset(body)
}

func persistentJSONRequest(method, target string, body *benchmarkBody) *http.Request {
	parsed, err := url.Parse(target)
	if err != nil {
		panic(err)
	}
	contentLength := int64(0)
	if body != nil {
		contentLength = int64(body.Len())
	}
	return &http.Request{
		Method:        method,
		URL:           parsed,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": {"application/json"}},
		Body:          body,
		ContentLength: contentLength,
		Host:          parsed.Host,
	}
}

func BenchmarkHTTP(b *testing.B) {
	router := benchmarkRouter(b)
	getRequest := benchmarkRequest(http.MethodGet, "/items/42?q=alpha", nil)
	getNoQueryRequest := benchmarkRequest(http.MethodGet, "/items/42", nil)
	getStaticRequest := benchmarkRequest(http.MethodGet, "/static", nil)
	boundRequest := benchmarkRequest(http.MethodGet, "/bound/42?q=alpha", nil)
	boundRequest.Header.Set("X-Tenant", "acme")
	postBody := []byte(`{"name":"alpha","count":2}`)
	invalidBody := []byte(`{"name":"","count":0}`)
	rawRequest := benchmarkRequest(http.MethodGet, "/raw", nil)
	encodeFailureRequest := benchmarkRequest(http.MethodGet, "/encode-failure", nil)
	missingRequest := benchmarkRequest(http.MethodGet, "/missing", nil)

	b.Run("typed-get", func(b *testing.B) {
		writer := newBenchmarkWriter()
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			writer.reset()
			router.ServeHTTP(writer, getRequest)
		}
	})

	b.Run("typed-get-no-query", func(b *testing.B) {
		writer := newBenchmarkWriter()
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			writer.reset()
			router.ServeHTTP(writer, getNoQueryRequest)
		}
	})

	b.Run("typed-get-static", func(b *testing.B) {
		writer := newBenchmarkWriter()
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			writer.reset()
			router.ServeHTTP(writer, getStaticRequest)
		}
	})

	b.Run("typed-get-header", func(b *testing.B) {
		writer := newBenchmarkWriter()
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			writer.reset()
			router.ServeHTTP(writer, boundRequest)
		}
	})

	b.Run("typed-get-parallel", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			writer := newBenchmarkWriter()
			request := benchmarkRequest(http.MethodGet, "/items/42?q=alpha", nil)
			for pb.Next() {
				writer.reset()
				router.ServeHTTP(writer, request)
			}
		})
	})

	b.Run("typed-json-post", func(b *testing.B) {
		writer := newBenchmarkWriter()
		body := newBenchmarkBody(postBody)
		request := persistentJSONRequest(http.MethodPost, "/items", body)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			writer.reset()
			body.Reset(postBody)
			router.ServeHTTP(writer, request)
		}
	})

	b.Run("typed-validation-failure", func(b *testing.B) {
		writer := newBenchmarkWriter()
		body := newBenchmarkBody(invalidBody)
		request := persistentJSONRequest(http.MethodPost, "/items", body)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			writer.reset()
			body.Reset(invalidBody)
			router.ServeHTTP(writer, request)
		}
	})

	b.Run("typed-json-encode-failure", func(b *testing.B) {
		writer := newBenchmarkWriter()
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			writer.reset()
			router.ServeHTTP(writer, encodeFailureRequest)
		}
	})

	b.Run("raw", func(b *testing.B) {
		writer := newBenchmarkWriter()
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			writer.reset()
			router.ServeHTTP(writer, rawRequest)
		}
	})

	b.Run("not-found", func(b *testing.B) {
		writer := newBenchmarkWriter()
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			writer.reset()
			router.ServeHTTP(writer, missingRequest)
		}
	})
}

func BenchmarkHTTPMiddleware(b *testing.B) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := NewRouter(logger, "benchmark", "v1")
	router.Use(RequestID(), AccessLog(logger), Recover(logger), BodyLimit(1<<20))
	router.Handle(func(_ context.Context, in benchmarkGetInput) (benchmarkGetOutput, error) {
		return benchmarkGetOutput{ID: in.ID, Q: in.Q}, nil
	})
	request := benchmarkRequest(http.MethodGet, "/items/42?q=alpha", nil)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		writer := newBenchmarkWriter()
		router.ServeHTTP(writer, request)
	}
}

func BenchmarkHTTPBodyLimitReject(b *testing.B) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := NewRouter(logger, "benchmark", "v1")
	router.Use(BodyLimit(8))
	router.Handle(func(_ context.Context, in benchmarkPostInput) (benchmarkPostOutput, error) {
		return benchmarkPostOutput{ID: 1, Name: in.Body.Name}, nil
	})
	payload := []byte(`{"name":"alpha","count":2}`)
	for _, tc := range []struct {
		name          string
		contentLength int64
	}{
		{name: "declared", contentLength: int64(len(payload))},
		{name: "streamed", contentLength: -1},
	} {
		b.Run(tc.name, func(b *testing.B) {
			body := newBenchmarkBody(payload)
			request := persistentJSONRequest(http.MethodPost, "/items", body)
			request.ContentLength = tc.contentLength

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				writer := newBenchmarkWriter()
				body.Reset(payload)
				router.ServeHTTP(writer, request)
			}
		})
	}
}
