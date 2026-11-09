package jwtauth

import (
	"context"
	"strings"
	"testing"
	"time"

	jwtlib "github.com/golang-jwt/jwt/v5"
)

func TestHMACVerifier(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	verifier, err := NewVerifier(Config{
		Secret: secret, Issuer: "issuer", Audience: []string{"client"}, Leeway: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	token := signed(t, jwtlib.SigningMethodHS256, secret, jwtlib.MapClaims{
		"sub": "user-1", "iss": "issuer", "aud": []string{"client"},
		"exp": time.Now().Add(time.Minute).Unix(),
	})
	principal, err := verifier.Verify(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if principal.Subject != "user-1" || principal.Issuer != "issuer" || len(principal.Audience) != 1 || principal.Audience[0] != "client" {
		t.Fatalf("principal = %#v", principal)
	}
}

func TestVerifierRejectsAlgorithmAndClaimConfusion(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	verifier, err := NewVerifier(Config{Secret: secret, Issuer: "issuer", Audience: []string{"client"}})
	if err != nil {
		t.Fatal(err)
	}
	tests := []string{
		signed(t, jwtlib.SigningMethodHS512, secret, jwtlib.MapClaims{
			"sub": "user", "iss": "issuer", "aud": "client", "exp": time.Now().Add(time.Minute).Unix(),
		}),
		signed(t, jwtlib.SigningMethodHS256, secret, jwtlib.MapClaims{
			"sub": "user", "iss": "wrong", "aud": "client", "exp": time.Now().Add(time.Minute).Unix(),
		}),
		signed(t, jwtlib.SigningMethodHS256, secret, jwtlib.MapClaims{
			"sub": "user", "iss": "issuer", "aud": "wrong", "exp": time.Now().Add(time.Minute).Unix(),
		}),
		signed(t, jwtlib.SigningMethodHS256, secret, jwtlib.MapClaims{
			"sub": "user", "iss": "issuer", "aud": "client",
		}),
	}
	for _, token := range tests {
		if _, err := verifier.Verify(context.Background(), token); err == nil {
			t.Fatalf("token was accepted: %s", token)
		}
	}
}

func TestConfigValidation(t *testing.T) {
	for _, test := range []struct {
		name string
		cfg  Config
		want string
	}{
		{name: "no key", cfg: Config{}, want: "requires"},
		{name: "short secret", cfg: Config{Secret: []byte("short")}, want: "32 bytes"},
		{name: "both keys", cfg: Config{Secret: []byte("0123456789abcdef0123456789abcdef"), PublicKey: &struct{}{}}, want: "mutually exclusive"},
		{name: "none", cfg: Config{Secret: []byte("0123456789abcdef0123456789abcdef"), Algorithms: []string{"none"}}, want: "algorithm"},
		{name: "negative leeway", cfg: Config{Secret: []byte("0123456789abcdef0123456789abcdef"), Leeway: -time.Second}, want: "leeway"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewVerifier(test.cfg); err == nil || !contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestParsePublicKeyRejectsInvalidPEM(t *testing.T) {
	if _, err := ParsePublicKeyPEM([]byte("not pem")); err == nil {
		t.Fatal("invalid PEM was accepted")
	}
}

func signed(t *testing.T, method jwtlib.SigningMethod, key any, claims jwtlib.MapClaims) string {
	t.Helper()
	token := jwtlib.NewWithClaims(method, claims)
	value, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func contains(value, want string) bool { return len(want) == 0 || strings.Contains(value, want) }
