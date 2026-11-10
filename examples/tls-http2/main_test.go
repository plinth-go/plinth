package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/plinth-go/plinth/runtime"
)

func TestHTTPConfigModes(t *testing.T) {
	direct, err := httpConfig(options{
		mode:     modeDirect,
		certFile: "cert.pem",
		keyFile:  "key.pem",
	})
	if err != nil {
		t.Fatal(err)
	}
	if direct.TLSCertFile != "cert.pem" || direct.TLSKeyFile != "key.pem" || !direct.EnableHTTP2 {
		t.Fatalf("direct config = %#v", direct)
	}

	proxy, err := httpConfig(options{mode: modeProxy})
	if err != nil {
		t.Fatal(err)
	}
	if proxy.TLSCertFile != "" || proxy.TLSKeyFile != "" || proxy.EnableHTTP2 {
		t.Fatalf("proxy config = %#v", proxy)
	}

	if _, err := httpConfig(options{mode: "unknown"}); err == nil {
		t.Fatal("unknown mode was accepted")
	}
}

func TestGenerateDevCertificateIsDevelopmentScoped(t *testing.T) {
	certFile := filepath.Join(t.TempDir(), ".dev-tls", "tls.crt")
	keyFile := filepath.Join(filepath.Dir(certFile), "tls.key")
	if err := generateDevCertificate(certFile, keyFile); err != nil {
		t.Fatal(err)
	}
	certificatePEM, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(mustLoadKeyPair(t, certFile, keyFile).Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(certificate.Subject.Organization) != 1 || certificate.Subject.Organization[0] != "Plinth development only" {
		t.Fatalf("organization = %#v", certificate.Subject.Organization)
	}
	if !strings.Contains(string(certificatePEM), "BEGIN CERTIFICATE") {
		t.Fatal("certificate PEM is malformed")
	}
	if len(certificate.DNSNames) != 1 || certificate.DNSNames[0] != "localhost" {
		t.Fatalf("DNS names = %#v", certificate.DNSNames)
	}
	if err := generateDevCertificate(certFile, keyFile); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second generation error = %v, want os.ErrExist", err)
	}
}

func TestDirectModeNegotiatesHTTP2(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "tls.crt")
	keyFile := filepath.Join(dir, "tls.key")
	if err := generateDevCertificate(certFile, keyFile); err != nil {
		t.Fatal(err)
	}
	addr := startExampleServer(t, options{
		mode:     modeDirect,
		addr:     "127.0.0.1:0",
		certFile: certFile,
		keyFile:  keyFile,
	})

	roots := x509.NewCertPool()
	certificatePEM, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatal(err)
	}
	if !roots.AppendCertsFromPEM(certificatePEM) {
		t.Fatal("development certificate was not added to the root pool")
	}
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			ForceAttemptHTTP2: true,
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
				RootCAs:    roots,
			},
		},
	}
	response, err := client.Get("https://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body := decodeResponse(t, response)
	if response.ProtoMajor != 2 || body.Protocol != "HTTP/2.0" || !body.TLS {
		t.Fatalf("proto=%s body=%#v", response.Proto, body)
	}
}

func TestProxyModeServesPlaintextHTTP1(t *testing.T) {
	addr := startExampleServer(t, options{
		mode: modeProxy,
		addr: "127.0.0.1:0",
	})
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{},
	}
	response, err := client.Get("http://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body := decodeResponse(t, response)
	if response.ProtoMajor != 1 || body.Protocol != "HTTP/1.1" || body.TLS {
		t.Fatalf("proto=%s body=%#v", response.Proto, body)
	}
}

func startExampleServer(t *testing.T, opts options) string {
	t.Helper()
	config, err := httpConfig(opts)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	config.Listen = func(context.Context, string, string) (net.Listener, error) {
		return listener, nil
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	component := runtime.NewHTTP(opts.addr, newHandler(), nil, logger, runtime.WithHTTPConfig(config))
	if err := component.Start(context.Background()); err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := component.Stop(ctx); err != nil {
			t.Errorf("stop server: %v", err)
		}
	})
	return listener.Addr().String()
}

func decodeResponse(t *testing.T, httpResponse *http.Response) response {
	t.Helper()
	var body response
	if err := json.NewDecoder(httpResponse.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

func mustLoadKeyPair(t *testing.T, certFile, keyFile string) tls.Certificate {
	t.Helper()
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	return pair
}
