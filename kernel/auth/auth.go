// Package auth provides a Bearer-token middleware contract without depending on
// a JWT or OIDC implementation. Concrete verifiers live in optional packages.
package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/plinth-go/plinth/kernel"
)

var principalKey = kernel.NewContextKey[Principal]("auth.principal")

// Principal is the verified identity attached to a request.
type Principal struct {
	Subject   string
	Issuer    string
	Audience  []string
	ExpiresAt time.Time
	Claims    map[string]any
}

// Verifier validates one raw bearer token.
type Verifier interface {
	Verify(context.Context, string) (Principal, error)
}

// Config controls credential extraction and the 401 challenge.
type Config struct {
	Header   string
	Realm    string
	Optional bool
}

// New validates config and builds a middleware. Invalid or missing credentials
// produce an RFC 9457 401 response; optional mode only permits a missing header.
func New(verifier Verifier, cfg Config, logger *slog.Logger) (func(http.Handler) http.Handler, error) {
	if verifier == nil {
		return nil, errors.New("auth verifier is nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	cfg.Header = strings.TrimSpace(cfg.Header)
	if cfg.Header == "" {
		cfg.Header = "Authorization"
	}
	cfg.Realm = strings.TrimSpace(cfg.Realm)
	if cfg.Realm == "" {
		cfg.Realm = "api"
	}
	if strings.ContainsAny(cfg.Realm, "\"\r\n") {
		return nil, errors.New("auth realm must not contain quotes or control characters")
	}
	challenge := "Bearer realm=" + strconv.Quote(cfg.Realm)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if next == nil {
				kernel.WriteProblem(w, r, kernel.Internal(errors.New("auth next handler is nil")), logger.Error)
				return
			}
			raw, present, err := bearerToken(r.Header.Values(cfg.Header))
			if err != nil {
				unauthorized(w, r, challenge, err, logger)
				return
			}
			if !present && cfg.Optional {
				next.ServeHTTP(w, r)
				return
			}
			if !present {
				unauthorized(w, r, challenge, errors.New("authorization credentials are required"), logger)
				return
			}
			principal, err := verifier.Verify(r.Context(), raw)
			if err != nil {
				unauthorized(w, r, challenge, errors.New("bearer token is invalid"), logger)
				return
			}
			next.ServeHTTP(w, r.WithContext(principalKey.With(r.Context(), principal)))
		})
	}, nil
}

// FromContext returns the verified principal, when present.
func FromContext(ctx context.Context) (Principal, bool) {
	return principalKey.Get(ctx)
}

// WithPrincipal attaches a principal to ctx. It is useful in tests and for
// service-to-service call paths that already authenticated earlier.
func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return principalKey.With(ctx, principal)
}

func bearerToken(values []string) (string, bool, error) {
	if len(values) == 0 {
		return "", false, nil
	}
	if len(values) != 1 {
		return "", true, errors.New("multiple authorization headers")
	}
	value := strings.TrimSpace(values[0])
	if value == "" {
		return "", true, errors.New("authorization header is empty")
	}
	scheme, token, ok := strings.Cut(value, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", true, errors.New("authorization scheme must be Bearer")
	}
	token = strings.TrimSpace(token)
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", true, errors.New("bearer token is malformed")
	}
	return token, true, nil
}

func unauthorized(w http.ResponseWriter, r *http.Request, challenge string, err error, logger *slog.Logger) {
	w.Header().Set("WWW-Authenticate", challenge)
	kernel.WriteProblem(w, r, &kernel.HTTPError{Status: http.StatusUnauthorized, Title: "Unauthorized", Detail: err.Error()}, logger.Error)
}
