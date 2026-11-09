// Package response provides the small response-writer wrapper shared by the
// optional observability adapters. It is internal so it does not become part
// of the framework's public API.
package response

import (
	"bufio"
	"errors"
	"net"
	"net/http"
)

// Writer records the status and number of body bytes written by a handler.
// Unwrap makes it compatible with http.ResponseController, while the explicit
// forwarding methods preserve common streaming and upgrade interfaces.
type Writer struct {
	http.ResponseWriter
	Status int
	Bytes  int64
}

func (w *Writer) WriteHeader(status int) {
	// Informational responses are not the final response status. The sole
	// exception is 101 Switching Protocols, after which the connection is no
	// longer an ordinary HTTP response stream.
	if status >= 100 && status < 200 && status != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	if w.Status == 0 {
		w.Status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *Writer) Write(value []byte) (int, error) {
	if w.Status == 0 {
		w.Status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(value)
	w.Bytes += int64(n)
	return n, err
}

// Unwrap exposes the underlying writer to http.ResponseController.
func (w *Writer) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Flush forwards streaming flushes when the underlying writer supports them.
func (w *Writer) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		if w.Status == 0 {
			w.Status = http.StatusOK
		}
		flusher.Flush()
	}
}

// Hijack forwards protocol upgrades such as WebSocket connections.
func (w *Writer) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("observability: response writer does not support hijacking")
	}
	return hijacker.Hijack()
}

// CloseNotify forwards the deprecated close notification capability.
func (w *Writer) CloseNotify() <-chan bool {
	if notifier, ok := w.ResponseWriter.(http.CloseNotifier); ok {
		return notifier.CloseNotify()
	}
	channel := make(chan bool)
	return channel
}

// Push forwards HTTP/2 server pushes when the underlying writer supports them.
func (w *Writer) Push(target string, options *http.PushOptions) error {
	pusher, ok := w.ResponseWriter.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return pusher.Push(target, options)
}

// StatusCode returns the status observed from the handler. A handler that
// returns without writing is equivalent to an implicit 200 response.
func (w *Writer) StatusCode() int {
	if w.Status == 0 {
		return http.StatusOK
	}
	return w.Status
}
