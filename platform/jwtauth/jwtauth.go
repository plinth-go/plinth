// Package jwtauth verifies signed bearer tokens for kernel/auth. It supports HMAC
// secrets and PEM-encoded RSA/ECDSA public keys with explicit algorithm
// allowlists.
package jwtauth

import (
	"context"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/plinth-go/plinth/kernel/auth"
)

// Config describes one token issuer. Set either Secret or PublicKey, not both.
// Algorithms defaults to HS256 for a secret and RS256 for a public key.
type Config struct {
	Secret     []byte
	PublicKey  any
	Algorithms []string
	Issuer     string
	Audience   []string
	Leeway     time.Duration
}

// Verifier implements auth.Verifier.
type Verifier struct {
	key        any
	algorithms []string
	issuer     string
	audience   []string
	leeway     time.Duration
}

// NewVerifier validates config and constructs a verifier.
func NewVerifier(cfg Config) (*Verifier, error) {
	if len(cfg.Secret) > 0 && cfg.PublicKey != nil {
		return nil, errors.New("jwt Secret and PublicKey are mutually exclusive")
	}
	if len(cfg.Secret) == 0 && cfg.PublicKey == nil {
		return nil, errors.New("jwt requires Secret or PublicKey")
	}
	if len(cfg.Secret) > 0 && len(cfg.Secret) < 32 {
		return nil, errors.New("jwt HMAC secret must be at least 32 bytes")
	}
	algorithms := append([]string(nil), cfg.Algorithms...)
	if len(algorithms) == 0 {
		if len(cfg.Secret) > 0 {
			algorithms = []string{"HS256"}
		} else {
			algorithms = []string{"RS256"}
		}
	}
	for index, algorithm := range algorithms {
		algorithm = strings.TrimSpace(algorithm)
		if algorithm == "" || strings.EqualFold(algorithm, "none") {
			return nil, fmt.Errorf("jwt algorithm at index %d is invalid", index)
		}
		algorithms[index] = algorithm
	}
	if cfg.Leeway < 0 {
		return nil, errors.New("jwt leeway must not be negative")
	}
	key := cfg.PublicKey
	if len(cfg.Secret) > 0 {
		key = cfg.Secret
	}
	return &Verifier{
		key: key, algorithms: algorithms, issuer: strings.TrimSpace(cfg.Issuer),
		audience: append([]string(nil), cfg.Audience...), leeway: cfg.Leeway,
	}, nil
}

// ParsePublicKeyPEM accepts PKIX, PKCS#1 RSA, and SEC1 ECDSA public keys.
func ParsePublicKeyPEM(value []byte) (any, error) {
	block, _ := pem.Decode(value)
	if block == nil {
		return nil, errors.New("jwt public key is not PEM")
	}
	if key, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		return key, nil
	}
	if key, err := x509.ParsePKCS1PublicKey(block.Bytes); err == nil {
		return key, nil
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		switch value := key.(type) {
		case *rsa.PrivateKey:
			return &value.PublicKey, nil
		case *ecdsa.PrivateKey:
			return &value.PublicKey, nil
		default:
			return nil, fmt.Errorf("jwt PKCS#8 key type %T is unsupported", key)
		}
	}
	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return &key.PublicKey, nil
	}
	return nil, errors.New("jwt public key encoding is unsupported")
}

func (v *Verifier) Verify(ctx context.Context, raw string) (auth.Principal, error) {
	if v == nil {
		return auth.Principal{}, errors.New("jwt verifier is nil")
	}
	options := []jwt.ParserOption{
		jwt.WithValidMethods(v.algorithms),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(v.leeway),
	}
	if v.issuer != "" {
		options = append(options, jwt.WithIssuer(v.issuer))
	}
	for _, audience := range v.audience {
		if audience = strings.TrimSpace(audience); audience != "" {
			options = append(options, jwt.WithAudience(audience))
		}
	}
	token, err := jwt.ParseWithClaims(raw, jwt.MapClaims{}, func(token *jwt.Token) (any, error) {
		return v.key, nil
	}, options...)
	if err != nil || token == nil || !token.Valid {
		return auth.Principal{}, errors.New("jwt is invalid")
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return auth.Principal{}, errors.New("jwt claims are invalid")
	}
	return auth.Principal{
		Subject:   claimString(claims, "sub"),
		Issuer:    claimString(claims, "iss"),
		Audience:  claimStrings(claims["aud"]),
		ExpiresAt: claimTime(claims, "exp"),
		Claims:    map[string]any(claims),
	}, nil
}

func claimString(claims jwt.MapClaims, name string) string {
	value, _ := claims[name].(string)
	return value
}

func claimStrings(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []string:
		return append([]string(nil), typed...)
	case []any:
		values := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				values = append(values, text)
			}
		}
		return values
	default:
		return nil
	}
}

func claimTime(claims jwt.MapClaims, name string) time.Time {
	switch value := claims[name].(type) {
	case float64:
		return time.Unix(int64(value), 0).UTC()
	case int64:
		return time.Unix(value, 0).UTC()
	case time.Time:
		return value.UTC()
	default:
		return time.Time{}
	}
}
