package runtime

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPStopDrainsAcceptedRequests(t *testing.T) {
	const requests = 8

	var inFlight atomic.Int32
	started := make(chan struct{}, requests)
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, _ *http.Request) {
		inFlight.Add(1)
		defer inFlight.Add(-1)
		started <- struct{}{}
		<-release
		w.WriteHeader(http.StatusNoContent)
	})
	component := NewHTTP("127.0.0.1:0", mux, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := component.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = component.Stop(ctx)
	})
	baseURL := httpTestServerURL(t, component)

	client := &http.Client{
		Transport: &http.Transport{DisableKeepAlives: true},
		Timeout:   5 * time.Second,
	}
	var clients sync.WaitGroup
	errs := make(chan error, requests)
	for range requests {
		clients.Add(1)
		go func() {
			defer clients.Done()
			response, err := client.Get(baseURL + "/slow")
			if err == nil {
				defer response.Body.Close()
				_, err = io.Copy(io.Discard, response.Body)
				if err == nil && response.StatusCode != http.StatusNoContent {
					err = &unexpectedStatusError{status: response.StatusCode}
				}
			}
			errs <- err
		}()
	}
	for range requests {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("request handler did not start")
		}
	}

	stopDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		stopDone <- component.Stop(ctx)
	}()
	waitForListenerClosed(t, baseURL)
	select {
	case err := <-stopDone:
		t.Fatalf("Stop returned before accepted requests completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	clients.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Stop did not return after accepted requests completed")
	}
	if remaining := inFlight.Load(); remaining != 0 {
		t.Fatalf("in-flight requests = %d", remaining)
	}
}

func TestHTTPStopReturnsDeadlineErrorForStuckHandler(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseHandler := func() { releaseOnce.Do(func() { close(release) }) }
	handlerDone := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stuck", func(http.ResponseWriter, *http.Request) {
		close(started)
		<-release
		close(handlerDone)
	})
	component := NewHTTP("127.0.0.1:0", mux, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := component.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		releaseHandler()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = component.Stop(ctx)
	})
	baseURL := httpTestServerURL(t, component)

	clientDone := make(chan error, 1)
	go func() {
		client := &http.Client{
			Transport: &http.Transport{DisableKeepAlives: true},
			Timeout:   5 * time.Second,
		}
		response, err := client.Get(baseURL + "/stuck")
		if err == nil {
			response.Body.Close()
		}
		clientDone <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request handler did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := component.Stop(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Stop took %s after its deadline", elapsed)
	}
	select {
	case <-clientDone:
	case <-time.After(time.Second):
		t.Fatal("client remained blocked after forced server close")
	}

	releaseHandler()
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("stuck handler did not finish after release")
	}
}

type unexpectedStatusError struct {
	status int
}

func (e *unexpectedStatusError) Error() string {
	return "unexpected HTTP status: " + http.StatusText(e.status)
}

func waitForListenerClosed(t *testing.T, baseURL string) {
	t.Helper()
	address := baseURL[len("http://"):]
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, 20*time.Millisecond)
		if err != nil {
			return
		}
		_ = conn.Close()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("listener still accepted new connections after Stop")
}
