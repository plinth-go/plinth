package kernel

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strings"
)

var errResponderContract = errors.New("typed responder contract violation")

type contentTypeGuard struct {
	w           http.ResponseWriter
	req         *http.Request
	produces    []string
	logger      *slog.Logger
	baseHeaders http.Header
	decided     bool
	failed      bool
}

func (g *contentTypeGuard) Header() http.Header { return g.w.Header() }

func (g *contentTypeGuard) WriteHeader(code int) {
	if g.decided {
		return
	}
	if code != http.StatusOK {
		g.reject(fmt.Errorf("typed Responder wrote status %d; typed Responder outputs must use 200", code))
		return
	}
	if !g.decide() {
		return
	}
	g.w.WriteHeader(code)
}

func (g *contentTypeGuard) Write(p []byte) (int, error) {
	if !g.decide() {
		return 0, errResponderContract
	}
	return g.w.Write(p)
}

func (g *contentTypeGuard) Flush() {
	_ = g.FlushError()
}

func (g *contentTypeGuard) FlushError() error {
	if !g.decide() {
		return errResponderContract
	}
	flush, ok := responseFlusher(g.w)
	if !ok {
		return http.ErrNotSupported
	}
	return flush()
}

func (g *contentTypeGuard) ReadFrom(src io.Reader) (int64, error) {
	if !g.decide() {
		return 0, errResponderContract
	}
	if rf, ok := responseReaderFrom(g.w); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(struct{ io.Writer }{g}, src)
}

func (g *contentTypeGuard) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, http.ErrNotSupported
}

func (g *contentTypeGuard) Unwrap() http.ResponseWriter { return g.w }

func (g *contentTypeGuard) decide() bool {
	if g.decided {
		return !g.failed
	}
	g.decided = true

	contentType := g.Header().Get("Content-Type")
	if contentType == "" {
		if len(g.produces) == 1 {
			if isConcreteMediaType(g.produces[0]) {
				g.Header().Set("Content-Type", g.produces[0])
				return true
			}
			return g.reject(fmt.Errorf("typed Responder must set a concrete Content-Type for wildcard produces %q", g.produces[0]))
		}
		return g.reject(fmt.Errorf("typed Responder did not set Content-Type; declared produces: %v", g.produces))
	}
	for _, declared := range g.produces {
		if mediaTypeMatches(contentType, declared) {
			return true
		}
	}
	return g.reject(fmt.Errorf("typed Responder wrote Content-Type %q, not declared in produces %v", contentType, g.produces))
}

func isConcreteMediaType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return false
	}
	mediaType = strings.ToLower(mediaType)
	return mediaType != "*/*" && !strings.HasSuffix(mediaType, "/*")
}

func (g *contentTypeGuard) reject(err error) bool {
	g.fail(err, "typed responder contract violation")
	return false
}

func (g *contentTypeGuard) fail(err error, message string) {
	g.decided = true
	g.failed = true
	g.restoreHeaders()
	if g.logger != nil {
		g.logger.Error(message,
			"method", g.req.Method, "path", g.req.URL.Path, "error", err)
	}
	WriteProblem(g.w, g.req, Internal(err), nil)
}

func (g *contentTypeGuard) restoreHeaders() {
	resetHeaders(g.w.Header(), g.baseHeaders)
}

func mediaTypeMatches(actual, declared string) bool {
	actualType, _, err := mime.ParseMediaType(actual)
	if err != nil {
		return false
	}
	declaredType, _, err := mime.ParseMediaType(declared)
	if err != nil {
		return false
	}
	actualType = strings.ToLower(actualType)
	declaredType = strings.ToLower(declaredType)
	switch {
	case declaredType == "*/*":
		return true
	case strings.HasSuffix(declaredType, "/*"):
		return strings.HasPrefix(actualType, strings.TrimSuffix(declaredType, "*"))
	default:
		return actualType == declaredType
	}
}
