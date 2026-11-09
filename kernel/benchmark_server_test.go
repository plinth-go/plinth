package kernel

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"sync"
	"testing"
)

func benchmarkServerClient(server *httptest.Server) *http.Client {
	base, _ := server.Client().Transport.(*http.Transport)
	transport := base.Clone()
	transport.MaxIdleConns = 256
	transport.MaxIdleConnsPerHost = 256
	transport.DisableCompression = true
	return &http.Client{Transport: transport}
}

func benchmarkGet(client *http.Client, target string) error {
	response, err := client.Get(target)
	if err != nil {
		return err
	}
	_, readErr := io.Copy(io.Discard, response.Body)
	closeErr := response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return &statusError{status: response.StatusCode}
	}
	if readErr != nil {
		return readErr
	}
	return closeErr
}

type statusError struct{ status int }

func (e *statusError) Error() string { return http.StatusText(e.status) }

func BenchmarkHTTPServer(b *testing.B) {
	b.Run("keepalive-get", func(b *testing.B) {
		server := httptest.NewServer(benchmarkRouter(b))
		defer server.Close()
		client := benchmarkServerClient(server)
		defer client.CloseIdleConnections()
		target := server.URL + "/items/42?q=alpha"

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := benchmarkGet(client, target); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("keepalive-get-parallel", func(b *testing.B) {
		server := httptest.NewServer(benchmarkRouter(b))
		defer server.Close()
		client := benchmarkServerClient(server)
		defer client.CloseIdleConnections()
		target := server.URL + "/items/42?q=alpha"

		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if err := benchmarkGet(client, target); err != nil {
					b.Error(err)
					return
				}
			}
		})
	})

	b.Run("keepalive-json-post", func(b *testing.B) {
		server := httptest.NewServer(benchmarkRouter(b))
		defer server.Close()
		client := benchmarkServerClient(server)
		defer client.CloseIdleConnections()
		target := server.URL + "/items"
		body := []byte(`{"name":"alpha","count":2}`)

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, target, bytes.NewReader(body))
			if err != nil {
				b.Fatal(err)
			}
			request.Header.Set("Content-Type", "application/json")
			response, err := client.Do(request)
			if err != nil {
				b.Fatal(err)
			}
			_, readErr := io.Copy(io.Discard, response.Body)
			closeErr := response.Body.Close()
			if response.StatusCode != http.StatusOK {
				b.Fatalf("status = %d", response.StatusCode)
			}
			if readErr != nil {
				b.Fatal(readErr)
			}
			if closeErr != nil {
				b.Fatal(closeErr)
			}
		}
	})

	b.Run("tls-get", func(b *testing.B) {
		server := httptest.NewTLSServer(benchmarkRouter(b))
		defer server.Close()
		client := benchmarkServerClient(server)
		defer client.CloseIdleConnections()
		target := server.URL + "/items/42?q=alpha"

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := benchmarkGet(client, target); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func TestHTTPServerKeepAliveReusesConnection(t *testing.T) {
	server := httptest.NewServer(benchmarkRouter(t))
	defer server.Close()
	client := benchmarkServerClient(server)
	defer client.CloseIdleConnections()

	if err := benchmarkGet(client, server.URL+"/items/42?q=alpha"); err != nil {
		t.Fatal(err)
	}
	var reused bool
	request, err := http.NewRequest(http.MethodGet, server.URL+"/items/42?q=alpha", nil)
	if err != nil {
		t.Fatal(err)
	}
	trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused }}
	request = request.WithContext(httptrace.WithClientTrace(request.Context(), trace))
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if !reused {
		t.Fatal("second keep-alive request did not reuse the connection")
	}
}

func TestHTTPServerConcurrentRequests(t *testing.T) {
	server := httptest.NewServer(benchmarkRouter(t))
	defer server.Close()
	client := benchmarkServerClient(server)
	defer client.CloseIdleConnections()
	target := server.URL + "/items/42?q=alpha"

	var workers sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := 0; i < 20; i++ {
				if err := benchmarkGet(client, target); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	workers.Wait()
}
