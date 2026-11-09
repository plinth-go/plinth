// Package csrf provides Redis- or memory-backed CSRF token middleware. A
// random token is stored in a cookie and must be echoed in a request header for
// state-changing methods. The store keeps only a SHA-256 token identifier.
package csrf

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/plinth-go/plinth/kernel"
)

const (
	defaultCookieName = "plinth_csrf"
	defaultHeaderName = "X-CSRF-Token"
	defaultTTL        = time.Hour
	tokenBytes        = 32
	maxMemoryEntries  = 50_000
	memorySweepEvery  = 1_024
)

var errMemoryStoreFull = errors.New("csrf memory store is full")

// Store persists token identifiers until their expiry. Implementations must
// treat values as opaque and enforce the supplied TTL.
type Store interface {
	Put(context.Context, string, time.Duration) error
	Exists(context.Context, string) (bool, error)
	Delete(context.Context, string) error
}

type MemoryStore struct {
	mu         sync.Mutex
	entries    map[string]time.Time
	maxEntries int
	puts       int
	lastSweep  time.Time
	now        func() time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		entries: make(map[string]time.Time), maxEntries: maxMemoryEntries, now: time.Now,
	}
}

func (s *MemoryStore) Put(_ context.Context, id string, ttl time.Duration) error {
	if err := validateStoreID(id, ttl); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	_, exists := s.entries[id]
	if !exists && len(s.entries) >= s.maxEntries {
		if now.Sub(s.lastSweep) >= time.Second {
			s.sweepExpired(now)
			s.lastSweep = now
		}
		if len(s.entries) >= s.maxEntries {
			return errMemoryStoreFull
		}
	}
	if len(s.entries) >= s.maxEntries || s.puts >= memorySweepEvery {
		s.sweepExpired(now)
		s.puts = 0
		s.lastSweep = now
	}
	s.entries[id] = now.Add(ttl)
	s.puts++
	return nil
}

func (s *MemoryStore) Exists(_ context.Context, id string) (bool, error) {
	if err := validateStoreID(id, time.Second); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	expires, ok := s.entries[id]
	if !ok {
		return false, nil
	}
	if !s.now().Before(expires) {
		delete(s.entries, id)
		return false, nil
	}
	return true, nil
}

func (s *MemoryStore) Delete(_ context.Context, id string) error {
	if err := validateStoreID(id, time.Second); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, id)
	return nil
}

func (s *MemoryStore) sweepExpired(now time.Time) {
	for id, expires := range s.entries {
		if !now.Before(expires) {
			delete(s.entries, id)
		}
	}
}

// Config controls cookie and request-token behavior.
type Config struct {
	Store      Store
	CookieName string
	HeaderName string
	Path       string
	Domain     string
	Secure     bool
	HttpOnly   bool
	SameSite   http.SameSite
	TTL        time.Duration
	Methods    []string
	Logger     *slog.Logger
	// Token extracts the submitted token. The default reads HeaderName.
	Token func(*http.Request) string
}

type csrfMiddleware struct {
	store      Store
	cookieName string
	headerName string
	path       string
	domain     string
	secure     bool
	httpOnly   bool
	sameSite   http.SameSite
	ttl        time.Duration
	methods    map[string]bool
	logger     *slog.Logger
	token      func(*http.Request) string
}

type tokenContextKey struct{}

// TokenFromContext returns the currently issued cookie token, when present.
// It is useful for rendering a hidden form field without parsing the cookie in
// every handler.
func TokenFromContext(ctx context.Context) string {
	token, _ := ctx.Value(tokenContextKey{}).(string)
	return token
}

// New validates cfg and returns CSRF middleware.
func New(cfg Config) (func(http.Handler) http.Handler, error) {
	if isNilStore(cfg.Store) {
		return nil, errors.New("csrf store is nil")
	}
	cookieName := strings.TrimSpace(cfg.CookieName)
	if cookieName == "" {
		cookieName = defaultCookieName
	}
	headerName := strings.TrimSpace(cfg.HeaderName)
	if headerName == "" {
		headerName = defaultHeaderName
	}
	path := strings.TrimSpace(cfg.Path)
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		return nil, errors.New("csrf cookie path must start with /")
	}
	if cfg.TTL == 0 {
		cfg.TTL = defaultTTL
	}
	if cfg.TTL < 0 {
		return nil, errors.New("csrf TTL must be positive")
	}
	if cfg.SameSite == 0 {
		cfg.SameSite = http.SameSiteLaxMode
	}
	if cfg.SameSite < http.SameSiteDefaultMode || cfg.SameSite > http.SameSiteNoneMode {
		return nil, errors.New("csrf SameSite mode is invalid")
	}
	if cfg.SameSite == http.SameSiteNoneMode && !cfg.Secure {
		return nil, errors.New("csrf SameSite=None requires Secure")
	}
	cookie := &http.Cookie{Name: cookieName, Value: "token", Path: path, Domain: strings.TrimSpace(cfg.Domain)}
	if err := cookie.Valid(); err != nil {
		return nil, fmt.Errorf("csrf cookie: %w", err)
	}
	methods := cfg.Methods
	if len(methods) == 0 {
		methods = []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}
	}
	required := make(map[string]bool, len(methods))
	for _, method := range methods {
		method = strings.ToUpper(strings.TrimSpace(method))
		if method == "" || !validToken(method) {
			return nil, fmt.Errorf("csrf method %q is invalid", method)
		}
		required[method] = true
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	tokenFunc := cfg.Token
	if tokenFunc == nil {
		tokenFunc = func(r *http.Request) string { return r.Header.Get(headerName) }
	}
	return (&csrfMiddleware{
		store: cfg.Store, cookieName: cookieName, headerName: headerName, path: path,
		domain: strings.TrimSpace(cfg.Domain), secure: cfg.Secure, httpOnly: cfg.HttpOnly,
		sameSite: cfg.SameSite, ttl: cfg.TTL, methods: required, logger: logger, token: tokenFunc,
	}).middleware, nil
}

func (m *csrfMiddleware) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := strings.ToUpper(r.Method)
		if !m.methods[method] {
			token, err := m.ensureToken(w, r)
			if err != nil {
				m.logger.Error("issue csrf token", "method", r.Method, "path", r.URL.Path, "error", err)
				kernel.WriteProblem(w, r, kernel.Internal(err), m.logger.Error)
				return
			}
			if token != "" {
				r = r.WithContext(context.WithValue(r.Context(), tokenContextKey{}, token))
			}
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie(m.cookieName)
		if err != nil || !validIssuedToken(cookie.Value) {
			m.reject(w, r)
			return
		}
		submitted := strings.TrimSpace(m.token(r))
		if !validIssuedToken(submitted) || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(submitted)) != 1 {
			m.reject(w, r)
			return
		}
		exists, err := m.store.Exists(r.Context(), tokenID(cookie.Value))
		if err != nil {
			m.logger.Error("check csrf token", "method", r.Method, "path", r.URL.Path, "error", err)
			kernel.WriteProblem(w, r, kernel.Internal(err), m.logger.Error)
			return
		}
		if !exists {
			m.reject(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), tokenContextKey{}, cookie.Value)))
	})
}

func (m *csrfMiddleware) ensureToken(w http.ResponseWriter, r *http.Request) (string, error) {
	if cookie, err := r.Cookie(m.cookieName); err == nil && validIssuedToken(cookie.Value) {
		exists, err := m.store.Exists(r.Context(), tokenID(cookie.Value))
		if err != nil {
			return "", err
		}
		if exists {
			return cookie.Value, nil
		}
	}
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	if err := m.store.Put(r.Context(), tokenID(token), m.ttl); err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{
		Name: m.cookieName, Value: token, Path: m.path, Domain: m.domain,
		MaxAge: int(m.ttl / time.Second), Expires: time.Now().Add(m.ttl),
		Secure: m.secure, HttpOnly: m.httpOnly, SameSite: m.sameSite,
	})
	return token, nil
}

func (m *csrfMiddleware) reject(w http.ResponseWriter, r *http.Request) {
	m.logger.Warn("csrf validation failed", "method", r.Method, "path", r.URL.Path)
	kernel.WriteProblem(w, r, kernel.Forbidden("invalid CSRF token"), m.logger.Error)
}

func randomToken() (string, error) {
	var value [tokenBytes]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}

func tokenID(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func validIssuedToken(token string) bool {
	if len(token) != base64.RawURLEncoding.EncodedLen(tokenBytes) {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(decoded) == tokenBytes
}

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

func validateStoreID(id string, ttl time.Duration) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("csrf token id is required")
	}
	if ttl <= 0 {
		return errors.New("csrf token TTL must be positive")
	}
	return nil
}

func validToken(value string) bool {
	for i := 0; i < len(value); i++ {
		char := value[i]
		if char <= 0x20 || char >= 0x7f || strings.ContainsRune("()<>@,;:\\\"/[]?={}", rune(char)) {
			return false
		}
	}
	return value != ""
}
