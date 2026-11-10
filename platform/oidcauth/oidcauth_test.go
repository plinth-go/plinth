package oidcauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jwtlib "github.com/golang-jwt/jwt/v5"
)

func TestVerifierDiscoversProviderAndVerifiesToken(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const keyID = "test-key"
	var server *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                server.URL,
			"jwks_uri":                              server.URL + "/keys",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "kid": keyID, "use": "sig", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
		}}})
	})
	server = httptest.NewServer(mux)
	defer server.Close()

	verifier, err := NewVerifier(context.Background(), Config{
		IssuerURL: server.URL, ClientID: "client-id", HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	token := jwtlib.NewWithClaims(jwtlib.SigningMethodRS256, jwtlib.MapClaims{
		"iss":  server.URL,
		"aud":  "client-id",
		"sub":  "user-1",
		"exp":  time.Now().Add(time.Minute).Unix(),
		"iat":  time.Now().Add(-time.Second).Unix(),
		"name": "Test User",
	})
	token.Header["kid"] = keyID
	raw, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := verifier.Verify(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if principal.Subject != "user-1" || principal.Issuer != server.URL || len(principal.Audience) != 1 || principal.Audience[0] != "client-id" {
		t.Fatalf("principal = %#v", principal)
	}
	if principal.Claims["name"] != "Test User" {
		t.Fatalf("claims = %#v", principal.Claims)
	}
}

func TestVerifierRejectsInvalidConfiguration(t *testing.T) {
	if _, err := NewVerifier(context.Background(), Config{}); err == nil {
		t.Fatal("empty config was accepted")
	}
	if _, err := NewVerifier(context.Background(), Config{IssuerURL: "https://issuer"}); err == nil {
		t.Fatal("missing client ID was accepted")
	}
}
