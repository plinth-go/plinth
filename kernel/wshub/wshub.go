// Package wshub provides the optional gorilla/websocket integration for
// Plinth's stream registry. Applications that do not import this package do not
// compile or link the WebSocket dependency.
package wshub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"runtime/debug"
	"strings"
	"time"

	gorilla "github.com/gorilla/websocket"
	"github.com/plinth-go/plinth/kernel"
	"github.com/plinth-go/plinth/utils"
)

// Hub upgrades WebSocket requests and registers connections with the framework
// stream registry.
type Hub struct {
	streams  *kernel.StreamRegistry
	logger   *slog.Logger
	upgrader gorilla.Upgrader
}

// NewHub creates a WebSocket hub backed by streams.
func NewHub(streams *kernel.StreamRegistry, logger *slog.Logger) *Hub {
	logger = utils.IfNilElse(logger, slog.Default())
	return &Hub{
		streams: streams,
		logger:  logger,
		upgrader: gorilla.Upgrader{
			HandshakeTimeout: 10 * time.Second,
			ReadBufferSize:   4 << 10,
			WriteBufferSize:  4 << 10,
			CheckOrigin:      sameOrigin,
		},
	}
}

// Serve upgrades and registers the connection with the shutdown registry. The
// callback owns the read/write loop and must return when ctx is cancelled.
func (h *Hub) Serve(w http.ResponseWriter, req *http.Request, fn func(context.Context, *gorilla.Conn) error) (err error) {
	if isNilInterface(w) {
		return errors.New("websocket: response writer is nil")
	}
	if isNilInterface(req) {
		return errors.New("websocket: request is nil")
	}
	if h == nil {
		http.Error(w, "websocket hub is unavailable", http.StatusInternalServerError)
		return errors.New("websocket: hub is nil")
	}
	if fn == nil {
		http.Error(w, "websocket callback is required", http.StatusInternalServerError)
		return errors.New("websocket: callback is nil")
	}
	if h.streams == nil {
		http.Error(w, "websocket registry unavailable", http.StatusServiceUnavailable)
		return kernel.ErrStreamsClosed
	}
	if h.streams.Closed() {
		http.Error(w, "server is shutting down", http.StatusServiceUnavailable)
		return kernel.ErrStreamsClosed
	}

	conn, err := h.upgrader.Upgrade(w, req, nil)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(req.Context())
	remove, ok := h.streams.Add(func() {
		cancel()
		_ = conn.Close()
	})
	if !ok {
		cancel()
		_ = conn.WriteControl(gorilla.CloseMessage,
			gorilla.FormatCloseMessage(gorilla.CloseGoingAway, "server shutting down"),
			time.Now().Add(time.Second))
		_ = conn.Close()
		return kernel.ErrStreamsClosed
	}
	defer func() {
		remove()
		cancel()
		code := gorilla.CloseNormalClosure
		reason := "connection closed"
		if recovered := recover(); recovered != nil {
			code = gorilla.CloseInternalServerErr
			reason = "internal server error"
			path := ""
			if req.URL != nil {
				path = req.URL.Path
			}
			logger := utils.IfNilElse(h.logger, slog.Default())
			logger.Error("websocket callback panicked",
				"method", req.Method,
				"path", path,
				"panic", recovered,
				"stack", string(debug.Stack()),
			)
			err = fmt.Errorf("websocket callback panicked: %v", recovered)
		}
		h.writeControl(conn, code, reason)
		_ = conn.Close()
	}()

	return fn(ctx, conn)
}

func (h *Hub) writeControl(conn *gorilla.Conn, code int, reason string) {
	deadline := time.Now().Add(time.Second)
	if err := conn.WriteControl(gorilla.CloseMessage,
		gorilla.FormatCloseMessage(code, reason), deadline); err != nil {
		h.logger.Debug("websocket close control", "error", err)
	}
}

func sameOrigin(r *http.Request) bool {
	if r == nil {
		return false
	}
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	}
	if len(origins) != 1 {
		return false
	}
	origin := origins[0]
	if origin == "" {
		return false
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return false
	}
	if parsed.User != nil || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.ForceQuery {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host)
}

// NormalizeError converts normal WebSocket close and cancellation errors into
// nil or context.Canceled so applications can keep ordinary errors.
func NormalizeError(err error) error {
	if isNilInterface(err) {
		return nil
	}
	if gorilla.IsCloseError(err, gorilla.CloseNormalClosure, gorilla.CloseGoingAway) {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	return err
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
