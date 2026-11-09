// Package oidcauth verifies OpenID Connect ID tokens for kernel/auth. Provider
// discovery and JWKS rotation are owned by go-oidc.
package oidcauth

import (
	"context"
	"errors"
	"net/http"
	"strings"

	coreoidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/plinth-go/plinth/kernel/auth"
)

// Config describes one OIDC provider and expected client audience.
type Config struct {
	IssuerURL  string
	ClientID   string
	HTTPClient *http.Client
}

// Verifier implements auth.Verifier.
type Verifier struct {
	verifier *coreoidc.IDTokenVerifier
}

// NewVerifier performs OIDC discovery and constructs a verifier. Discovery
// happens once; go-oidc handles remote-key refresh during verification.
func NewVerifier(ctx context.Context, cfg Config) (*Verifier, error) {
	issuer := strings.TrimSpace(cfg.IssuerURL)
	clientID := strings.TrimSpace(cfg.ClientID)
	if issuer == "" {
		return nil, errors.New("oidc issuer URL is required")
	}
	if clientID == "" {
		return nil, errors.New("oidc client ID is required")
	}
	if cfg.HTTPClient != nil {
		ctx = coreoidc.ClientContext(ctx, cfg.HTTPClient)
	}
	provider, err := coreoidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, err
	}
	return &Verifier{verifier: provider.Verifier(&coreoidc.Config{ClientID: clientID})}, nil
}

func (v *Verifier) Verify(ctx context.Context, raw string) (auth.Principal, error) {
	if v == nil || v.verifier == nil {
		return auth.Principal{}, errors.New("oidc verifier is nil")
	}
	token, err := v.verifier.Verify(ctx, raw)
	if err != nil {
		return auth.Principal{}, errors.New("oidc ID token is invalid")
	}
	claims := make(map[string]any)
	if err := token.Claims(&claims); err != nil {
		return auth.Principal{}, errors.New("oidc ID token claims are invalid")
	}
	return auth.Principal{
		Subject:   token.Subject,
		Issuer:    token.Issuer,
		Audience:  append([]string(nil), token.Audience...),
		ExpiresAt: token.Expiry,
		Claims:    claims,
	}, nil
}
