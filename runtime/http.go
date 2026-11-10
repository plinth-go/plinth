package runtime

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"reflect"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/plinth-go/plinth/utils"
)

type StreamCloser interface {
	CloseStreams()
}

// HTTPConfig controls the standard-library server policy. Start from
// DefaultHTTPConfig and override only what the deployment needs. A zero
// duration disables that timeout; zero MaxHeaderBytes uses net/http's default.
type HTTPConfig struct {
	// ReadHeaderTimeout bounds how long the server waits for request headers.
	ReadHeaderTimeout time.Duration
	// ReadTimeout bounds reading the entire request, including the body.
	ReadTimeout time.Duration
	// IdleTimeout bounds how long an idle keep-alive connection is retained.
	IdleTimeout time.Duration
	// MaxHeaderBytes bounds request header bytes; zero uses net/http's default.
	MaxHeaderBytes int
	// TLSCertFile and TLSKeyFile enable TLS from a PEM certificate/key pair.
	// Set both or neither. They are mutually exclusive with TLSConfig.
	TLSCertFile string
	TLSKeyFile  string
	// TLSConfig enables TLS with a caller-owned configuration. NewHTTP clones
	// it before use. It must contain a certificate, GetCertificate, or
	// GetConfigForClient, and is mutually exclusive with TLSCertFile/TLSKeyFile.
	TLSConfig *tls.Config
	// EnableHTTP2 enables HTTP/2 over TLS. Plaintext servers remain HTTP/1 only.
	EnableHTTP2 bool
	// Protocols optionally replaces the framework's protocol selection. It is
	// mutually exclusive with EnableHTTP2. When nil, the server uses HTTP/1 and
	// adds TLS HTTP/2 only when EnableHTTP2 is true. Set UnencryptedHTTP2 here
	// for h2c; set HTTP2 only together with TLS.
	Protocols *http.Protocols
	// Listen optionally replaces the default TCP listener creation. It receives
	// the start context and the configured network/address ("tcp"/HTTPConfig
	// address). Returning an error aborts Start before any server state exists.
	Listen func(ctx context.Context, network, address string) (net.Listener, error)
	// ConfigureServer optionally customizes uncommon http.Server fields such as
	// ConnContext, ConnState, or TLSNextProto. It runs after framework defaults
	// are applied and before the listener is created. Returning an error aborts
	// Start. The framework reapplies Handler, timeouts, TLSConfig, Protocols,
	// ErrorLog, and BaseContext after the callback, so use the common HTTPConfig
	// fields for those values instead of overriding them here.
	ConfigureServer func(*http.Server) error
}

// DefaultHTTPConfig returns the framework's safe server defaults. WriteTimeout
// is intentionally not configurable because SSE and WebSocket need
// long-lived writes; streaming writes are bounded by StreamRegistry/SSE.
func DefaultHTTPConfig() HTTPConfig {
	return HTTPConfig{
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    http.DefaultMaxHeaderBytes,
	}
}

// HTTPOption customizes the HTTP component during construction.
type HTTPOption func(*httpServerSettings)

type httpServerSettings struct {
	config    HTTPConfig
	configErr error
}

// WithHTTPConfig replaces the default server policy with config.
func WithHTTPConfig(config HTTPConfig) HTTPOption {
	return func(settings *httpServerSettings) {
		if settings.configErr != nil {
			return
		}
		if config.ReadHeaderTimeout < 0 {
			settings.configErr = errors.New("http read header timeout must not be negative")
			return
		}
		if config.ReadTimeout < 0 {
			settings.configErr = errors.New("http read timeout must not be negative")
			return
		}
		if config.IdleTimeout < 0 {
			settings.configErr = errors.New("http idle timeout must not be negative")
			return
		}
		if config.MaxHeaderBytes < 0 {
			settings.configErr = errors.New("http max header bytes must not be negative")
			return
		}
		settings.config = config
	}
}

type HTTP struct {
	addr    string
	handler http.Handler
	logger  *slog.Logger
	streams StreamCloser
	config  httpServerSettings

	mu         sync.Mutex
	srv        *http.Server
	listener   net.Listener
	baseCancel context.CancelFunc
	failed     chan struct{}
	serveErr   error
	closed     bool
}

func NewHTTP(addr string, handler http.Handler, streams StreamCloser, logger *slog.Logger, options ...HTTPOption) *HTTP {
	logger = utils.IfNilElse(logger, slog.Default())
	logger = logger.With("component", "http")
	if isNilInterface(streams) {
		streams = nil
	}
	settings := httpServerSettings{config: DefaultHTTPConfig()}
	for _, option := range options {
		if option == nil {
			settings.configErr = errors.New("http option must not be nil")
			break
		}
		option(&settings)
	}
	if settings.configErr == nil && settings.config.TLSConfig != nil {
		settings.config.TLSConfig = settings.config.TLSConfig.Clone()
	}
	return &HTTP{addr: strings.TrimSpace(addr), handler: handler, streams: streams, logger: logger, config: settings}
}

func (h *HTTP) Name() string { return "http" }

func (h *HTTP) Start(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if isNilHTTPHandler(h.handler) {
		return errors.New("http handler is nil")
	}
	if h.closed {
		return errors.New("http component already stopped")
	}
	if h.srv != nil {
		return errors.New("http component already started")
	}
	if h.addr == "" {
		return errors.New("http listen address is required")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("start http server: %w", err)
	}
	if h.config.configErr != nil {
		return h.config.configErr
	}
	tlsConfig, err := h.prepareTLS()
	if err != nil {
		return err
	}
	protocols, err := h.selectProtocols(tlsConfig)
	if err != nil {
		return err
	}
	baseCtx, cancel := context.WithCancel(context.Background())
	started := false
	defer func() {
		if !started {
			cancel()
		}
	}()
	srv := h.newServer(baseCtx, tlsConfig, protocols)
	if configure := h.config.config.ConfigureServer; configure != nil {
		if err := configure(srv); err != nil {
			return fmt.Errorf("configure http server: %w", err)
		}
		h.applyServerPolicy(srv, baseCtx, tlsConfig, protocols)
	}

	listen := h.config.config.Listen
	if listen == nil {
		listen = func(ctx context.Context, network, address string) (net.Listener, error) {
			var listenConfig net.ListenConfig
			return listenConfig.Listen(ctx, network, address)
		}
	}
	listener, err := listen(ctx, "tcp", h.addr)
	if err != nil {
		if listener != nil {
			_ = listener.Close()
		}
		return fmt.Errorf("listen on %q: %w", h.addr, err)
	}

	h.listener = listener
	h.baseCancel = cancel
	h.failed = make(chan struct{})
	h.srv = srv
	started = true

	go func() {
		var err error
		if tlsConfig != nil {
			err = srv.ServeTLS(listener, "", "")
		} else {
			err = srv.Serve(listener)
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			h.mu.Lock()
			if h.serveErr == nil {
				h.serveErr = err
				close(h.failed)
			}
			h.mu.Unlock()
			h.logger.Error("http server stopped", "error", err)
		}
	}()

	h.logger.Info("http listening", "addr", listener.Addr().String(), "tls", tlsConfig != nil, "protocols", protocols.String())
	return nil
}

func (h *HTTP) selectProtocols(tlsConfig *tls.Config) (*http.Protocols, error) {
	configured := h.config.config.Protocols
	if configured != nil {
		if h.config.config.EnableHTTP2 {
			return nil, errors.New("http EnableHTTP2 and Protocols are mutually exclusive")
		}
		protocols := new(http.Protocols)
		*protocols = *configured
		if !protocols.HTTP1() && !protocols.HTTP2() && !protocols.UnencryptedHTTP2() {
			return nil, errors.New("http Protocols must enable at least one protocol")
		}
		if protocols.HTTP2() && tlsConfig == nil {
			return nil, errors.New("http HTTP/2 requires TLS; use UnencryptedHTTP2 for cleartext")
		}
		return protocols, nil
	}
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	if h.config.config.EnableHTTP2 {
		protocols.SetHTTP2(true)
	}
	return protocols, nil
}

func (h *HTTP) newServer(baseCtx context.Context, tlsConfig *tls.Config, protocols *http.Protocols) *http.Server {
	srv := &http.Server{}
	h.applyServerPolicy(srv, baseCtx, tlsConfig, protocols)
	return srv
}

func (h *HTTP) applyServerPolicy(srv *http.Server, baseCtx context.Context, tlsConfig *tls.Config, protocols *http.Protocols) {
	srv.Handler = h.handler
	srv.ReadHeaderTimeout = h.config.config.ReadHeaderTimeout
	srv.ReadTimeout = h.config.config.ReadTimeout
	// WriteTimeout is intentionally zero: SSE and WebSocket need long-lived writes.
	srv.WriteTimeout = 0
	srv.IdleTimeout = h.config.config.IdleTimeout
	srv.MaxHeaderBytes = h.config.config.MaxHeaderBytes
	srv.TLSConfig = tlsConfig
	srv.Protocols = protocols
	srv.ErrorLog = slog.NewLogLogger(h.logger.Handler(), slog.LevelError)
	srv.BaseContext = func(net.Listener) context.Context {
		return baseCtx
	}
}

func (h *HTTP) prepareTLS() (*tls.Config, error) {
	config := h.config.config
	certFile := strings.TrimSpace(config.TLSCertFile)
	keyFile := strings.TrimSpace(config.TLSKeyFile)
	if (certFile == "") != (keyFile == "") {
		return nil, errors.New("http TLS certificate and key files must be configured together")
	}
	if config.TLSConfig != nil && certFile != "" {
		return nil, errors.New("http TLSConfig and certificate files are mutually exclusive")
	}
	if config.EnableHTTP2 && config.TLSConfig == nil && certFile == "" {
		return nil, errors.New("http HTTP/2 requires TLS")
	}

	var tlsConfig *tls.Config
	switch {
	case config.TLSConfig != nil:
		tlsConfig = config.TLSConfig.Clone()
		if len(tlsConfig.Certificates) == 0 && tlsConfig.GetCertificate == nil && tlsConfig.GetConfigForClient == nil {
			return nil, errors.New("http TLSConfig has no certificate source")
		}
	case certFile != "":
		certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("load http TLS certificate: %w", err)
		}
		tlsConfig = &tls.Config{
			Certificates: []tls.Certificate{certificate},
			MinVersion:   tls.VersionTLS12,
		}
	}
	return tlsConfig, nil
}

func isNilHTTPHandler(handler http.Handler) bool {
	return isNilInterface(handler)
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

// Failed closes when Serve exits with an unexpected error.
func (h *HTTP) Failed() <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.failed
}

// Failure returns the unexpected Serve error, if any.
func (h *HTTP) Failure() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.serveErr
}

func (h *HTTP) Stop(ctx context.Context) error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	streams := h.streams
	baseCancel := h.baseCancel
	srv := h.srv
	listener := h.listener
	h.mu.Unlock()

	var errs []error
	if streams != nil {
		if err := closeStreams(streams, h.logger); err != nil {
			errs = append(errs, err)
		}
	}
	if baseCancel != nil {
		baseCancel()
	}
	if srv != nil {
		err := srv.Shutdown(ctx)
		if err != nil {
			_ = srv.Close()
			errs = append(errs, err)
		}
	}
	if srv == nil && listener != nil {
		if err := listener.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func closeStreams(streams StreamCloser, logger *slog.Logger) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.Error("http stream closer panicked", "panic", recovered, "stack", string(debug.Stack()))
			err = fmt.Errorf("close streams panicked: %v", recovered)
		}
	}()
	streams.CloseStreams()
	return nil
}
