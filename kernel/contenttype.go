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
	produces    compiledProduces
	logger      *slog.Logger
	baseHeaders headerSnapshot
	decided     bool
	failed      bool
}

type compiledProduces struct {
	raw        []string
	mediaTypes []string
}

func compileProduces(raw []string) compiledProduces {
	compiled := compiledProduces{
		raw:        raw,
		mediaTypes: make([]string, len(raw)),
	}
	for i, value := range raw {
		mediaType, _, err := mime.ParseMediaType(value)
		if err == nil {
			compiled.mediaTypes[i] = strings.ToLower(mediaType)
		}
	}
	return compiled
}

func (p compiledProduces) singleConcrete() bool {
	if len(p.mediaTypes) != 1 {
		return false
	}
	mediaType := p.mediaTypes[0]
	return mediaType != "" && mediaType != "*/*" && !strings.HasSuffix(mediaType, "/*")
}

func (p compiledProduces) matches(actual string) bool {
	actualType, _, err := mime.ParseMediaType(actual)
	if err != nil {
		return false
	}
	actualType = strings.ToLower(actualType)
	for _, declaredType := range p.mediaTypes {
		if declaredType != "" && mediaTypeValueMatches(actualType, declaredType) {
			return true
		}
	}
	return false
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
		if g.produces.singleConcrete() {
			g.Header().Set("Content-Type", g.produces.raw[0])
			return true
		}
		if len(g.produces.raw) == 1 {
			return g.reject(fmt.Errorf("typed Responder must set a concrete Content-Type for wildcard produces %q", g.produces.raw[0]))
		}
		return g.reject(fmt.Errorf("typed Responder did not set Content-Type; declared produces: %v", g.produces.raw))
	}
	if g.produces.matches(contentType) {
		return true
	}
	return g.reject(fmt.Errorf("typed Responder wrote Content-Type %q, not declared in produces %v", contentType, g.produces.raw))
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
	g.baseHeaders.restore(g.w.Header())
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
	return mediaTypeValueMatches(actualType, declaredType)
}

func mediaTypeValueMatches(actualType, declaredType string) bool {
	switch {
	case declaredType == "*/*":
		return true
	case strings.HasSuffix(declaredType, "/*"):
		return strings.HasPrefix(actualType, strings.TrimSuffix(declaredType, "*"))
	default:
		return actualType == declaredType
	}
}
