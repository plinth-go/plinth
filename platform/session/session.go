// Package session provides cookie-backed sessions with automatic save through
// alexedwards/scs. Redis persistence lives in the optional platform/session/redis
// subpackage; applications that do not import it do not link go-redis.
package session

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/alexedwards/scs/v2/memstore"
	"github.com/plinth-go/plinth/kernel"
)

// Store is the persistence contract used by scs.
type Store = scs.Store

// Config controls session lifetime and cookie policy.
type Config struct {
	Lifetime    time.Duration
	IdleTimeout time.Duration
	CookieName  string
	Domain      string
	Path        string
	SameSite    http.SameSite
	Secure      bool
	Partitioned bool
	// Persist defaults to true. Set NoPersist to create browser-session cookies.
	NoPersist bool
	Logger    *slog.Logger
}

// Manager wraps scs.SessionManager and exposes its common session methods.
// All methods accept the request context from the handler.
type Manager struct {
	*scs.SessionManager
}

// New builds a manager. Cookie HttpOnly is always enabled; Secure should be
// enabled whenever the public origin uses HTTPS.
func New(store Store, cfg Config) (*Manager, error) {
	if isNilStore(store) {
		return nil, errors.New("session store is nil")
	}
	if cfg.Lifetime == 0 {
		cfg.Lifetime = 24 * time.Hour
	}
	if cfg.Lifetime < 0 {
		return nil, errors.New("session lifetime must be positive")
	}
	if cfg.IdleTimeout < 0 {
		return nil, errors.New("session idle timeout must not be negative")
	}
	if cfg.IdleTimeout > 0 && cfg.IdleTimeout > cfg.Lifetime {
		return nil, errors.New("session idle timeout must not exceed lifetime")
	}
	name := strings.TrimSpace(cfg.CookieName)
	if name == "" {
		name = "plinth_session"
	}
	path := strings.TrimSpace(cfg.Path)
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		return nil, errors.New("session cookie path must start with /")
	}
	if cfg.SameSite == 0 {
		cfg.SameSite = http.SameSiteLaxMode
	}
	if cfg.SameSite < http.SameSiteDefaultMode || cfg.SameSite > http.SameSiteNoneMode {
		return nil, errors.New("session SameSite mode is invalid")
	}
	if cfg.SameSite == http.SameSiteNoneMode && !cfg.Secure {
		return nil, errors.New("session SameSite=None requires Secure")
	}
	if cfg.Partitioned && !cfg.Secure {
		return nil, errors.New("session partitioned cookies require Secure")
	}
	cookie := &http.Cookie{Name: name, Value: "token", Path: path, Domain: strings.TrimSpace(cfg.Domain)}
	if err := cookie.Valid(); err != nil {
		return nil, fmt.Errorf("session cookie: %w", err)
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	manager := scs.New()
	manager.Lifetime = cfg.Lifetime
	manager.IdleTimeout = cfg.IdleTimeout
	manager.Store = store
	manager.HashTokenInStore = true
	manager.Cookie.Name = name
	manager.Cookie.Domain = strings.TrimSpace(cfg.Domain)
	manager.Cookie.Path = path
	manager.Cookie.Secure = cfg.Secure
	manager.Cookie.HttpOnly = true
	manager.Cookie.SameSite = cfg.SameSite
	manager.Cookie.Partitioned = cfg.Partitioned
	manager.Cookie.Persist = !cfg.NoPersist
	manager.ErrorFunc = func(w http.ResponseWriter, r *http.Request, err error) {
		logger.Error("session middleware failed", "method", r.Method, "path", r.URL.Path, "error", err)
		kernel.WriteProblem(w, r, kernel.Internal(err), logger.Error)
	}
	return &Manager{SessionManager: manager}, nil
}

// Middleware loads the session, exposes it through request context, and saves
// modifications after the handler returns.
func (m *Manager) Middleware(next http.Handler) http.Handler {
	if m == nil || m.SessionManager == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			logger := slog.Default()
			logger.Error("session manager is nil", "method", r.Method, "path", r.URL.Path)
			kernel.WriteProblem(w, r, kernel.Internal(errors.New("session manager is nil")), logger.Error)
		})
	}
	return m.LoadAndSave(next)
}

// NewMemoryStore returns a process-local store. Use a durable store for
// multi-instance deployments.
func NewMemoryStore() Store { return memstore.New() }

func isNilStore(store Store) bool {
	if store == nil {
		return true
	}
	value := reflect.ValueOf(store)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
