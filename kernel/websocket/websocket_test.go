package websocket

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gorilla "github.com/gorilla/websocket"
	"github.com/plinth-go/plinth/kernel"
)

type typedNilWebSocketError struct{}

func (*typedNilWebSocketError) Error() string { return "typed nil websocket error" }

type nilResponseWriter struct{}

func (*nilResponseWriter) Header() http.Header { panic("nil response writer must not be called") }

func (*nilResponseWriter) Write([]byte) (int, error) { panic("nil response writer must not be called") }

func (*nilResponseWriter) WriteHeader(int) { panic("nil response writer must not be called") }

func TestHubEcho(t *testing.T) {
	streams := kernel.NewStreamRegistry()
	hub := NewHub(streams, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = hub.Serve(w, r, func(ctx context.Context, conn *gorilla.Conn) error {
			messageType, data, err := conn.ReadMessage()
			if err != nil {
				return err
			}
			if err := conn.WriteMessage(messageType, data); err != nil {
				return err
			}
			<-ctx.Done()
			return ctx.Err()
		})
	}))
	defer server.Close()
	defer streams.CloseStreams()

	url := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, response, err := gorilla.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if err := conn.WriteMessage(gorilla.TextMessage, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	messageType, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if messageType != gorilla.TextMessage || string(data) != "hello" {
		t.Fatalf("message = type %d, data %q", messageType, data)
	}
}

func TestHubRejectsNilCallbackBeforeUpgrade(t *testing.T) {
	streams := kernel.NewStreamRegistry()
	hub := NewHub(streams, nil)
	rec := httptest.NewRecorder()
	if err := hub.Serve(rec, httptest.NewRequest(http.MethodGet, "/ws", nil), nil); err == nil {
		t.Fatal("nil websocket callback was accepted")
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestHubClosesPanickingCallbackWithInternalError(t *testing.T) {
	streams := kernel.NewStreamRegistry()
	var logs bytes.Buffer
	hub := NewHub(streams, slog.New(slog.NewTextHandler(&logs, nil)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = hub.Serve(w, r, func(context.Context, *gorilla.Conn) error {
			panic("callback boom")
		})
	}))
	defer server.Close()
	defer streams.CloseStreams()

	url := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := gorilla.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}

	_, _, err = conn.ReadMessage()
	var closeErr *gorilla.CloseError
	if !errors.As(err, &closeErr) || closeErr.Code != gorilla.CloseInternalServerErr {
		t.Fatalf("close error = %v", err)
	}
	if !strings.Contains(logs.String(), "websocket callback panicked") || !strings.Contains(logs.String(), "callback boom") {
		t.Fatalf("logs = %q", logs.String())
	}
}

func TestHubRejectsNilRegistryBeforeUpgrade(t *testing.T) {
	hub := NewHub(nil, nil)
	rec := httptest.NewRecorder()
	err := hub.Serve(rec, httptest.NewRequest(http.MethodGet, "/ws", nil), func(context.Context, *gorilla.Conn) error {
		return nil
	})
	if !errors.Is(err, kernel.ErrStreamsClosed) {
		t.Fatalf("error = %v, want ErrStreamsClosed", err)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestHubRejectsNilInputsBeforeUpgrade(t *testing.T) {
	var writer *nilResponseWriter
	hub := NewHub(kernel.NewStreamRegistry(), nil)
	if err := hub.Serve(writer, httptest.NewRequest(http.MethodGet, "/ws", nil), func(context.Context, *gorilla.Conn) error { return nil }); err == nil {
		t.Fatal("typed-nil response writer was accepted")
	}
	if err := hub.Serve(httptest.NewRecorder(), nil, func(context.Context, *gorilla.Conn) error { return nil }); err == nil {
		t.Fatal("nil request was accepted")
	}

	var nilHub *Hub
	rec := httptest.NewRecorder()
	if err := nilHub.Serve(rec, httptest.NewRequest(http.MethodGet, "/ws", nil), func(context.Context, *gorilla.Conn) error { return nil }); err == nil {
		t.Fatal("nil hub was accepted")
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestNormalizeErrorTreatsTypedNilAsNil(t *testing.T) {
	var typedNil *typedNilWebSocketError
	if err := NormalizeError(typedNil); err != nil {
		t.Fatalf("error = %v", err)
	}
}

func TestSameOriginHostComparisonIsCaseInsensitive(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)
	req.Host = "Example.COM"
	req.Header.Set("Origin", "https://example.com")
	if !sameOrigin(req) {
		t.Fatal("case-insensitive same-host origin was rejected")
	}

	req.Header.Set("Origin", "https://evil.example")
	if sameOrigin(req) {
		t.Fatal("cross-origin request was accepted")
	}
}

func TestSameOriginRejectsMalformedOrigins(t *testing.T) {
	for _, tc := range []struct {
		name    string
		origins []string
		want    bool
	}{
		{name: "missing", want: true},
		{name: "empty", origins: []string{""}, want: false},
		{name: "multiple", origins: []string{"https://example.com", "https://example.com"}, want: false},
		{name: "relative", origins: []string{"//example.com"}, want: false},
		{name: "userinfo", origins: []string{"https://user@example.com"}, want: false},
		{name: "path", origins: []string{"https://example.com/path"}, want: false},
		{name: "query", origins: []string{"https://example.com?x=1"}, want: false},
		{name: "fragment", origins: []string{"https://example.com#fragment"}, want: false},
		{name: "opaque", origins: []string{"https:example.com"}, want: false},
		{name: "unsupported scheme", origins: []string{"ftp://example.com"}, want: false},
		{name: "valid uppercase scheme", origins: []string{"HTTPS://example.com"}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)
			for _, origin := range tc.origins {
				req.Header.Add("Origin", origin)
			}
			if got := sameOrigin(req); got != tc.want {
				t.Fatalf("sameOrigin = %v, want %v", got, tc.want)
			}
		})
	}
}
