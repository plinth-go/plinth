// Package cors provides an explicit allowlist CORS middleware. It is optional:
// applications that terminate CORS at a gateway may keep using that boundary.
package cors

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/plinth-go/plinth/kernel"
)

var defaultMethods = []string{
	http.MethodGet,
	http.MethodHead,
	http.MethodPost,
	http.MethodPut,
	http.MethodPatch,
	http.MethodDelete,
	http.MethodOptions,
}

var defaultHeaders = []string{
	"Accept",
	"Accept-Language",
	"Authorization",
	"Content-Language",
	"Content-Type",
	"X-CSRF-Token",
	"X-Request-ID",
}

var defaultExposedHeaders = []string{
	"Retry-After",
	"X-Request-ID",
}

// Config describes one explicit cross-origin policy. Origins are matched
// exactly after simple scheme/host normalization; "*" is supported only
// without credentials.
type Config struct {
	AllowedOrigins   []string
	AllowedMethods   []string
	AllowedHeaders   []string
	ExposedHeaders   []string
	AllowCredentials bool
	MaxAge           time.Duration
}

type policy struct {
	allowAnyOrigin   bool
	origins          map[string]bool
	methods          map[string]bool
	headers          map[string]bool
	exposedHeaders   string
	allowCredentials bool
	maxAge           string
}

// New validates cfg and returns a middleware. An empty AllowedOrigins list
// denies every cross-origin request rather than silently allowing all origins.
func New(cfg Config) (func(http.Handler) http.Handler, error) {
	if len(cfg.AllowedOrigins) == 0 {
		return nil, errors.New("cors allowed origins must not be empty")
	}
	policy := &policy{
		origins:          make(map[string]bool, len(cfg.AllowedOrigins)),
		methods:          make(map[string]bool, len(cfg.AllowedMethods)),
		headers:          make(map[string]bool, len(cfg.AllowedHeaders)),
		allowCredentials: cfg.AllowCredentials,
	}
	for _, origin := range cfg.AllowedOrigins {
		origin = strings.TrimSpace(origin)
		if origin == "*" {
			policy.allowAnyOrigin = true
			continue
		}
		normalized, err := normalizeOrigin(origin)
		if err != nil {
			return nil, err
		}
		policy.origins[normalized] = true
	}
	if policy.allowAnyOrigin && cfg.AllowCredentials {
		return nil, errors.New("cors wildcard origin cannot be combined with credentials")
	}
	if len(policy.origins) == 0 && !policy.allowAnyOrigin {
		return nil, errors.New("cors allowed origins must not be empty")
	}
	methods := cfg.AllowedMethods
	if len(methods) == 0 {
		methods = defaultMethods
	}
	for _, method := range methods {
		method = strings.ToUpper(strings.TrimSpace(method))
		if method == "" || method == "*" || !validToken(method) {
			return nil, fmt.Errorf("cors method %q is invalid", method)
		}
		policy.methods[method] = true
	}
	headers := cfg.AllowedHeaders
	if len(headers) == 0 {
		headers = defaultHeaders
	}
	for _, header := range headers {
		header = strings.TrimSpace(header)
		if header == "" || header == "*" || !validToken(header) {
			return nil, fmt.Errorf("cors header %q is invalid", header)
		}
		policy.headers[strings.ToLower(header)] = true
	}
	exposed := cfg.ExposedHeaders
	if len(exposed) == 0 {
		exposed = defaultExposedHeaders
	}
	normalizedExposed := make([]string, 0, len(exposed))
	for _, header := range exposed {
		header = strings.TrimSpace(header)
		if header == "" || !validToken(header) {
			return nil, fmt.Errorf("cors exposed header %q is invalid", header)
		}
		normalizedExposed = append(normalizedExposed, header)
	}
	policy.exposedHeaders = strings.Join(normalizedExposed, ", ")
	if cfg.MaxAge < 0 {
		return nil, errors.New("cors max age must not be negative")
	}
	if cfg.MaxAge > 0 {
		policy.maxAge = strconv.FormatInt(int64(cfg.MaxAge/time.Second), 10)
	}
	return policy.middleware, nil
}

func (p *policy) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Origin")
		allowed := p.originAllowed(origin)
		preflight := r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
		if !allowed {
			if preflight {
				kernel.WriteProblem(w, r, kernel.Forbidden("origin is not allowed"), nil)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if p.allowAnyOrigin {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		} else {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
		if p.allowCredentials {
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}
		if p.exposedHeaders != "" {
			w.Header().Set("Access-Control-Expose-Headers", p.exposedHeaders)
		}
		if !preflight {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Access-Control-Request-Method")
		w.Header().Add("Vary", "Access-Control-Request-Headers")
		method := strings.ToUpper(strings.TrimSpace(r.Header.Get("Access-Control-Request-Method")))
		if !p.methods[method] {
			kernel.WriteProblem(w, r, kernel.Forbidden("method is not allowed"), nil)
			return
		}
		requestedHeaders := r.Header.Get("Access-Control-Request-Headers")
		if !p.headersAllowed(requestedHeaders) {
			kernel.WriteProblem(w, r, kernel.Forbidden("request headers are not allowed"), nil)
			return
		}
		w.Header().Set("Access-Control-Allow-Methods", method)
		if requestedHeaders = strings.TrimSpace(requestedHeaders); requestedHeaders != "" {
			w.Header().Set("Access-Control-Allow-Headers", requestedHeaders)
		}
		if p.maxAge != "" {
			w.Header().Set("Access-Control-Max-Age", p.maxAge)
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func (p *policy) originAllowed(origin string) bool {
	if p.allowAnyOrigin {
		return true
	}
	normalized, err := normalizeOrigin(origin)
	return err == nil && p.origins[normalized]
}

func (p *policy) headersAllowed(raw string) bool {
	for _, header := range strings.Split(raw, ",") {
		header = strings.TrimSpace(header)
		if header == "" {
			continue
		}
		if !p.headers[strings.ToLower(header)] {
			return false
		}
	}
	return true
}

func normalizeOrigin(origin string) (string, error) {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil {
		return "", fmt.Errorf("cors origin %q is invalid", origin)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("cors origin %q must use http or https", origin)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" && parsed.Path != "/" {
		return "", fmt.Errorf("cors origin %q must contain only scheme and host", origin)
	}
	return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host), nil
}

func validToken(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		char := value[i]
		if char <= 0x20 || char >= 0x7f || strings.ContainsRune("()<>@,;:\\\"/[]?={}", rune(char)) {
			return false
		}
	}
	return true
}
