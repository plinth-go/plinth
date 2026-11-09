package runtime

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	gorilla "github.com/gorilla/websocket"
	"github.com/plinth-go/plinth/kernel"
	"github.com/plinth-go/plinth/kernel/websocket"
)

func startHTTPTestServer(t *testing.T, handler http.Handler, streams StreamCloser, options ...HTTPOption) *HTTP {
	t.Helper()
	component := NewHTTP("127.0.0.1:0", handler, streams, slog.New(slog.NewTextHandler(io.Discard, nil)), options...)
	if err := component.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = component.Stop(ctx)
	})
	return component
}

func httpTestServerURL(t *testing.T, component *HTTP) string {
	t.Helper()
	component.mu.Lock()
	defer component.mu.Unlock()
	return "http://" + component.listener.Addr().String()
}

func TestHTTPStopCancelsRealSSEStream(t *testing.T) {
	streams := kernel.NewStreamRegistry()
	started := make(chan struct{})
	handlerDone := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		handlerDone <- streams.ServeSSE(w, r, func(ctx context.Context, sse *kernel.SSE) error {
			if err := sse.Event("ready", "1"); err != nil {
				return err
			}
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
	})
	component := startHTTPTestServer(t, mux, streams)
	baseURL := httpTestServerURL(t, component)

	response, err := http.Get(baseURL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(line) != "event: ready" {
		t.Fatalf("first SSE line = %q", line)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := component.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-handlerDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("SSE handler error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SSE handler did not return after HTTP shutdown")
	}
}

func TestHTTPStopClosesRealWebSocketStream(t *testing.T) {
	streams := kernel.NewStreamRegistry()
	hub := websocket.NewHub(streams, slog.New(slog.NewTextHandler(io.Discard, nil)))
	started := make(chan struct{})
	handlerDone := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws", func(w http.ResponseWriter, r *http.Request) {
		handlerDone <- hub.Serve(w, r, func(ctx context.Context, _ *gorilla.Conn) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
	})
	component := startHTTPTestServer(t, mux, streams)
	baseURL := httpTestServerURL(t, component)

	conn, response, err := gorilla.DefaultDialer.Dial("ws"+strings.TrimPrefix(baseURL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d", response.StatusCode)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("WebSocket handler did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := component.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-handlerDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("WebSocket handler error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("WebSocket handler did not return after HTTP shutdown")
	}
}
