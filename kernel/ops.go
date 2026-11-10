package kernel

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/plinth-go/plinth/utils"
	"github.com/swaggest/swgui/v5emb"
)

// ReadyCheck is one dependency probe evaluated by the readiness endpoint.
type ReadyCheck struct {
	Name  string
	Check func(context.Context) error
}

// ReadyConfig configures the default readiness handler.
type ReadyConfig struct {
	Checks  []ReadyCheck
	Timeout time.Duration
	Logger  *slog.Logger
}

// OpsSettings is the embeddable environment configuration for the standard
// operational endpoints. Its positive enabled flags avoid the double-negative
// configuration required by OpsConfig.Disable* fields.
type OpsSettings struct {
	// LivenessEnabled registers the liveness endpoint.
	LivenessEnabled bool `env:"LIVENESS_ENABLED,default=true" envdoc:"Register the liveness endpoint."`
	// LivenessPath is the liveness endpoint path.
	LivenessPath string `env:"LIVENESS_PATH,default=/livez" envdoc:"Liveness endpoint path." validate:"startswith=/"`
	// ReadinessEnabled registers the readiness endpoint.
	ReadinessEnabled bool `env:"READINESS_ENABLED,default=true" envdoc:"Register the readiness endpoint."`
	// ReadinessPath is the readiness endpoint path.
	ReadinessPath string `env:"READINESS_PATH,default=/readyz" envdoc:"Readiness endpoint path." validate:"startswith=/"`
	// OpenAPIEnabled registers the OpenAPI document and Swagger UI.
	OpenAPIEnabled bool `env:"OPENAPI_ENABLED,default=true" envdoc:"Register the OpenAPI document and Swagger UI."`
	// OpenAPIPath is the OpenAPI document path.
	OpenAPIPath string `env:"OPENAPI_PATH,default=/openapi.json" envdoc:"OpenAPI document path." validate:"startswith=/"`
	// DocsPath is the Swagger UI path. A trailing slash is added if omitted.
	DocsPath string `env:"DOCS_PATH,default=/docs/" envdoc:"Swagger UI path; a trailing slash is added when omitted." validate:"startswith=/"`
}

// DefaultOpsSettings returns settings with every standard operational endpoint
// enabled. It matches the defaults declared by the env tags.
func DefaultOpsSettings() OpsSettings {
	return OpsSettings{
		LivenessEnabled:  true,
		LivenessPath:     "/livez",
		ReadinessEnabled: true,
		ReadinessPath:    "/readyz",
		OpenAPIEnabled:   true,
		OpenAPIPath:      "/openapi.json",
		DocsPath:         "/docs/",
	}
}

// Apply returns cfg with its runtime switches and paths derived from the
// positive settings. All other OpsConfig fields are preserved. Empty settings
// paths leave the corresponding runtime paths unchanged. The zero value
// disables every standard operational endpoint; settings loaded through
// app.LoadEnv use the declared true defaults when their environment variables
// are absent.
func (s OpsSettings) Apply(cfg OpsConfig) OpsConfig {
	cfg.DisableLive = !s.LivenessEnabled
	cfg.DisableReady = !s.ReadinessEnabled
	cfg.DisableOpenAPI = !s.OpenAPIEnabled
	if s.LivenessPath != "" {
		cfg.LivePath = s.LivenessPath
	}
	if s.ReadinessPath != "" {
		cfg.ReadyPath = s.ReadinessPath
	}
	if s.OpenAPIPath != "" {
		cfg.OpenAPIPath = s.OpenAPIPath
	}
	if s.DocsPath != "" {
		cfg.DocsPath = s.DocsPath
	}
	return cfg
}

// OpsConfig configures the standard operational endpoints. Empty paths select
// the conventional /livez, /readyz, /openapi.json, and /docs/ routes relative
// to the router or group they are registered on.
type OpsConfig struct {
	LivePath    string
	ReadyPath   string
	OpenAPIPath string
	DocsPath    string

	// DisableLive, DisableReady, and DisableOpenAPI prevent the corresponding
	// routes from being registered. DisableOpenAPI also disables the Swagger UI.
	// The handlers remain available through Livez, Readyz, and
	// Router.OpenAPIHandler for registration on an authorized group.
	DisableLive    bool
	DisableReady   bool
	DisableOpenAPI bool

	Checks  []ReadyCheck
	Timeout time.Duration
	Logger  *slog.Logger

	// Live, Ready, OpenAPI, and Docs override the default handlers when non-nil.
	Live    http.HandlerFunc
	Ready   http.HandlerFunc
	OpenAPI http.HandlerFunc
	Docs    http.Handler
}

// RegisterOps registers the enabled standard operational routes.
// The routes are hidden from the generated OpenAPI document.
func (r *Router) RegisterOps(cfg OpsConfig) {
	r.registerOps("", nil, nil, cfg)
}

// RegisterOps registers the enabled standard operational routes beneath the
// group prefix, after the group's middleware. This allows applications to
// expose readiness or OpenAPI metadata only through an authenticated group.
func (g *Group) RegisterOps(cfg OpsConfig) {
	g.router.registerOps(g.prefix, g.layers(), g.options(nil), cfg)
	g.freeze()
}

func (r *Router) registerOps(prefix string, mw []Middleware, opts []Option, cfg OpsConfig) {
	cfg.LivePath = utils.IfEmptyElse(cfg.LivePath, "/livez")
	cfg.ReadyPath = utils.IfEmptyElse(cfg.ReadyPath, "/readyz")
	cfg.OpenAPIPath = utils.IfEmptyElse(cfg.OpenAPIPath, "/openapi.json")
	cfg.DocsPath = utils.IfEmptyElse(cfg.DocsPath, "/docs/")
	if !strings.HasSuffix(cfg.DocsPath, "/") {
		cfg.DocsPath += "/"
	}

	routes := make([]string, 0, 4)
	if !cfg.DisableLive {
		routes = append(routes, cfg.LivePath)
	}
	if !cfg.DisableReady {
		routes = append(routes, cfg.ReadyPath)
	}
	if !cfg.DisableOpenAPI {
		routes = append(routes, cfg.OpenAPIPath, cfg.DocsPath)
		if cfg.Docs != nil && isNilHTTPHandler(cfg.Docs) {
			panic("kernel: operational docs handler is nil")
		}
	}
	r.preflightOpsRoutes(prefix, routes)

	live := cfg.Live
	if live == nil {
		live = Livez
	}
	ready := cfg.Ready
	if ready == nil {
		ready = Readyz(ReadyConfig{Checks: cfg.Checks, Timeout: cfg.Timeout, Logger: cfg.Logger})
	}
	openapi := cfg.OpenAPI
	if openapi == nil {
		openapi = r.OpenAPIHandler()
	}

	if !cfg.DisableLive {
		r.raw(prefix, mw, http.MethodGet, cfg.LivePath, noStore(live), opsOptions(opts)...)
	}
	if !cfg.DisableReady {
		r.raw(prefix, mw, http.MethodGet, cfg.ReadyPath, noStore(ready), opsOptions(opts)...)
	}
	if !cfg.DisableOpenAPI {
		r.raw(prefix, mw, http.MethodGet, cfg.OpenAPIPath, noStore(openapi), opsOptions(opts)...)
		docs := cfg.Docs
		if docs == nil {
			specURL := joinRoute(prefix, cfg.OpenAPIPath)
			docsURL := joinRoute(prefix, cfg.DocsPath)
			docs = v5emb.New(r.title, specURL, docsURL)
		}
		r.raw(prefix, mw, http.MethodGet, cfg.DocsPath, noStore(docs), opsOptions(opts)...)
	}
}

func (r *Router) preflightOpsRoutes(prefix string, paths []string) {
	seen := make(map[string]string)
	for _, meta := range r.operationSnapshot() {
		seen[meta.Method+"\x00"+meta.Path] = "existing route"
	}
	routes := make([]routePattern, 0, len(paths))
	for _, path := range paths {
		full := joinRoute(prefix, path)
		key := http.MethodGet + "\x00" + full
		if previous, ok := seen[key]; ok {
			panic(fmt.Sprintf("kernel: operational route GET %s conflicts with %s", full, previous))
		}
		seen[key] = "operational route"
		routes = append(routes, routePattern{method: http.MethodGet, path: full})
	}
	r.preflightRoutes(routes...)
}

func noStore(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		ow := &opsResponseWriter{ResponseWriter: w}
		h.ServeHTTP(ow, r)
		if !ow.wroteHeader {
			ow.WriteHeader(http.StatusOK)
		}
	})
}

// opsResponseWriter applies the operational-endpoint cache and sniffing policy
// immediately before the response is committed, including custom handlers that
// set their own headers.
type opsResponseWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

func (w *opsResponseWriter) WriteHeader(status int) {
	if status >= 100 && status <= 199 && status != http.StatusSwitchingProtocols {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.ResponseWriter.WriteHeader(status)
		return
	}
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.ResponseWriter.WriteHeader(status)
}

func (w *opsResponseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

func (w *opsResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *opsResponseWriter) Flush() {
	_ = w.FlushError()
}

func (w *opsResponseWriter) FlushError() error {
	flush, ok := responseFlusher(w.ResponseWriter)
	if !ok {
		return http.ErrNotSupported
	}
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return flush()
}

func (w *opsResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := responseHijacker(w.ResponseWriter)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	conn, rw, err := h.Hijack()
	if err == nil {
		w.wroteHeader = true
	}
	return conn, rw, err
}

func (w *opsResponseWriter) ReadFrom(src io.Reader) (int64, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if rf, ok := responseReaderFrom(w.ResponseWriter); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(struct{ io.Writer }{w}, src)
}

func opsOptions(opts []Option) []Option {
	combined := append([]Option(nil), opts...)
	return append(combined, Hidden())
}

// Livez reports process liveness without touching dependencies.
func Livez(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	_ = WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Readyz evaluates every configured dependency. A failed check returns HTTP
// 503 so a load balancer stops sending new traffic while existing work drains.
func Readyz(cfg ReadyConfig) http.HandlerFunc {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 2 * time.Second
	}
	checksConfig := normalizeReadyChecks(cfg.Checks)
	logger := utils.IfNilElse(cfg.Logger, slog.Default())

	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), cfg.Timeout)
		defer cancel()

		checks := make(map[string]string, len(checksConfig))
		healthy := true
		for _, check := range checksConfig {
			name := check.Name
			if check.Check == nil {
				checks[name] = "down"
				healthy = false
				logger.Error("readiness check is not configured", "dependency", name)
				continue
			}
			if err := runReadyCheck(ctx, check.Check); err != nil {
				checks[name] = "down"
				healthy = false
				logger.Warn("readiness check failed", "dependency", name, "error", err)
				continue
			}
			checks[name] = "ok"
		}

		status := http.StatusOK
		state := "ok"
		if !healthy {
			status = http.StatusServiceUnavailable
			state = "unavailable"
		}
		w.Header().Set("Cache-Control", "no-store")
		if err := WriteJSON(w, status, map[string]any{"status": state, "checks": checks}); err != nil {
			logger.Warn("write readiness response", "error", err)
		}
	}
}

func runReadyCheck(ctx context.Context, check func(context.Context) error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("readiness check panic: %v\n%s", recovered, debug.Stack())
		}
	}()
	err = check(ctx)
	if err == nil {
		return nil
	}
	if isNilInterface(err) {
		return fmt.Errorf("readiness check returned a typed-nil %T error", err)
	}
	return err
}

func normalizeReadyChecks(checks []ReadyCheck) []ReadyCheck {
	normalized := make([]ReadyCheck, len(checks))
	seen := make(map[string]bool, len(checks))
	for i, check := range checks {
		name := strings.TrimSpace(check.Name)
		if name == "" {
			name = fmt.Sprintf("check_%d", i+1)
		}
		if seen[name] {
			panic(fmt.Sprintf("kernel: duplicate readiness check name %q", name))
		}
		seen[name] = true
		check.Name = name
		normalized[i] = check
	}
	return normalized
}

// OpenAPIHandler serves the router's OpenAPI document with no-store caching so
// authorized mounts are not retained by shared caches.
func (r *Router) OpenAPIHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		if err := writeBody(w, r.OpenAPIJSON()); err != nil {
			r.logger.Error("write OpenAPI response", "error", err)
		}
	}
}

// WriteJSON writes a JSON response with the standard content type and nosniff
// header. The value is encoded before any header is committed. The status must
// be a final status that permits a body.
func WriteJSON(w http.ResponseWriter, status int, value any) error {
	if isNilInterface(w) {
		return fmt.Errorf("write JSON response: response writer is required")
	}
	if status < http.StatusOK || status > 599 || isBodylessStatus(status) {
		return fmt.Errorf("write JSON response: status %d does not permit a JSON body", status)
	}
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(value); err != nil {
		return fmt.Errorf("encode JSON response: %w", err)
	}
	setJSONHeaders(w.Header())
	w.WriteHeader(status)
	if err := writeBody(w, body.Bytes()); err != nil {
		return fmt.Errorf("write JSON response: %w", err)
	}
	return nil
}
