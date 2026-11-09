package runtime

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHTTPConfigDefaults(t *testing.T) {
	component := NewHTTP("127.0.0.1:0", http.NotFoundHandler(), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := component.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = component.Stop(ctx)
	})

	component.mu.Lock()
	server := component.srv
	component.mu.Unlock()
	if server.ReadHeaderTimeout != 5*time.Second {
		t.Fatalf("ReadHeaderTimeout = %s", server.ReadHeaderTimeout)
	}
	if server.ReadTimeout != 30*time.Second {
		t.Fatalf("ReadTimeout = %s", server.ReadTimeout)
	}
	if server.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout = %s", server.WriteTimeout)
	}
	if server.IdleTimeout != 90*time.Second {
		t.Fatalf("IdleTimeout = %s", server.IdleTimeout)
	}
	if server.MaxHeaderBytes != http.DefaultMaxHeaderBytes {
		t.Fatalf("MaxHeaderBytes = %d", server.MaxHeaderBytes)
	}
}

func TestHTTPConfigOverridesServerPolicy(t *testing.T) {
	config := DefaultHTTPConfig()
	config.ReadHeaderTimeout = time.Second
	config.ReadTimeout = 2 * time.Second
	config.IdleTimeout = 0
	config.MaxHeaderBytes = 8 << 10
	component := NewHTTP("127.0.0.1:0", http.NotFoundHandler(), nil, nil, WithHTTPConfig(config))
	if err := component.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = component.Stop(ctx)
	})

	component.mu.Lock()
	server := component.srv
	component.mu.Unlock()
	if server.ReadHeaderTimeout != time.Second || server.ReadTimeout != 2*time.Second || server.IdleTimeout != 0 {
		t.Fatalf("timeouts = %s/%s/%s", server.ReadHeaderTimeout, server.ReadTimeout, server.IdleTimeout)
	}
	if server.MaxHeaderBytes != 8<<10 {
		t.Fatalf("MaxHeaderBytes = %d", server.MaxHeaderBytes)
	}
}

func TestHTTPConfigRejectsInvalidOptionsBeforeListening(t *testing.T) {
	certificate, _ := testHTTPTLSMaterial(t)
	tests := []struct {
		name    string
		options []HTTPOption
		want    string
	}{
		{name: "nil option", options: []HTTPOption{nil}, want: "option must not be nil"},
		{name: "read header timeout", options: []HTTPOption{WithHTTPConfig(HTTPConfig{ReadHeaderTimeout: -1})}, want: "read header timeout"},
		{name: "read timeout", options: []HTTPOption{WithHTTPConfig(HTTPConfig{ReadTimeout: -1})}, want: "read timeout"},
		{name: "idle timeout", options: []HTTPOption{WithHTTPConfig(HTTPConfig{IdleTimeout: -1})}, want: "idle timeout"},
		{name: "max headers", options: []HTTPOption{WithHTTPConfig(HTTPConfig{MaxHeaderBytes: -1})}, want: "max header bytes"},
		{name: "certificate without key", options: []HTTPOption{WithHTTPConfig(HTTPConfig{TLSCertFile: "cert.pem"})}, want: "configured together"},
		{name: "config and files", options: []HTTPOption{WithHTTPConfig(HTTPConfig{TLSConfig: &tls.Config{Certificates: []tls.Certificate{certificate}}, TLSCertFile: "cert.pem", TLSKeyFile: "key.pem"})}, want: "mutually exclusive"},
		{name: "HTTP2 without TLS", options: []HTTPOption{WithHTTPConfig(HTTPConfig{EnableHTTP2: true})}, want: "requires TLS"},
		{name: "TLS without certificate source", options: []HTTPOption{WithHTTPConfig(HTTPConfig{TLSConfig: &tls.Config{}})}, want: "no certificate source"},
		{name: "missing certificate file", options: []HTTPOption{WithHTTPConfig(HTTPConfig{TLSCertFile: "missing-cert.pem", TLSKeyFile: "missing-key.pem"})}, want: "load http TLS certificate"},
		{name: "empty protocols", options: []HTTPOption{WithHTTPConfig(HTTPConfig{Protocols: &http.Protocols{}})}, want: "at least one protocol"},
		{name: "TLS HTTP2 protocols without TLS", options: []HTTPOption{WithHTTPConfig(HTTPConfig{Protocols: func() *http.Protocols {
			protocols := new(http.Protocols)
			protocols.SetHTTP2(true)
			return protocols
		}()})}, want: "requires TLS"},
		{name: "EnableHTTP2 and Protocols", options: []HTTPOption{WithHTTPConfig(HTTPConfig{
			TLSConfig:   &tls.Config{Certificates: []tls.Certificate{certificate}},
			EnableHTTP2: true,
			Protocols:   &http.Protocols{},
		})}, want: "mutually exclusive"},
		{name: "configure server", options: []HTTPOption{WithHTTPConfig(HTTPConfig{ConfigureServer: func(*http.Server) error { return errors.New("configure failed") }})}, want: "configure http server"},
		{name: "listen hook", options: []HTTPOption{WithHTTPConfig(HTTPConfig{Listen: func(context.Context, string, string) (net.Listener, error) { return nil, errors.New("listen failed") }})}, want: "listen on"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			component := NewHTTP("127.0.0.1:0", http.NotFoundHandler(), nil, nil, tc.options...)
			err := component.Start(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Start error = %v", err)
			}
			if component.srv != nil || component.listener != nil {
				t.Fatal("invalid config created server state")
			}
		})
	}
}

type httpConnContextKey struct{}

func TestHTTPConfigConfigureServerWiring(t *testing.T) {
	config := DefaultHTTPConfig()
	config.ConfigureServer = func(server *http.Server) error {
		server.ConnContext = func(ctx context.Context, _ net.Conn) context.Context {
			return context.WithValue(ctx, httpConnContextKey{}, "from-hook")
		}
		server.ConnState = func(net.Conn, http.ConnState) {}
		server.Handler = http.NotFoundHandler()
		server.ReadTimeout = time.Hour
		server.Protocols = nil
		server.ErrorLog = nil
		server.BaseContext = nil
		return nil
	}
	component := startHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Context().Value(httpConnContextKey{}).(string))
	}), nil, WithHTTPConfig(config))

	response, err := http.Get(httpTestServerURL(t, component))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "from-hook" {
		t.Fatalf("body = %q", data)
	}

	component.mu.Lock()
	server := component.srv
	component.mu.Unlock()
	if server.Handler == nil || server.ReadTimeout != config.ReadTimeout || server.Protocols == nil || server.ErrorLog == nil || server.BaseContext == nil {
		t.Fatalf("framework policy was not restored: %#v", server)
	}
}

func TestHTTPConfigListenHook(t *testing.T) {
	var called bool
	config := DefaultHTTPConfig()
	config.Listen = func(ctx context.Context, network, address string) (net.Listener, error) {
		called = true
		if network != "tcp" {
			t.Fatalf("network = %q", network)
		}
		var listenConfig net.ListenConfig
		return listenConfig.Listen(ctx, network, address)
	}
	component := startHTTPTestServer(t, http.NotFoundHandler(), nil, WithHTTPConfig(config))
	response, err := http.Get(httpTestServerURL(t, component))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if !called {
		t.Fatal("Listen hook was not called")
	}
}

func TestHTTPConfigProtocolsOverride(t *testing.T) {
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	config := DefaultHTTPConfig()
	config.Protocols = protocols
	component := startHTTPTestServer(t, http.NotFoundHandler(), nil, WithHTTPConfig(config))

	component.mu.Lock()
	server := component.srv
	component.mu.Unlock()
	if server.Protocols.HTTP1() || !server.Protocols.UnencryptedHTTP2() {
		t.Fatalf("protocols = %s", server.Protocols.String())
	}
}

func TestHTTPConfigTLSObjectAndHTTP2(t *testing.T) {
	certificate, roots := testHTTPTLSMaterial(t)
	config := DefaultHTTPConfig()
	config.TLSConfig = &tls.Config{Certificates: []tls.Certificate{certificate}}
	config.EnableHTTP2 = true
	component := startHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}), nil, WithHTTPConfig(config))

	response := httpTestRequest(t, component, roots, true)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if response.ProtoMajor != 2 {
		t.Fatalf("protocol = %s", response.Proto)
	}
}

func TestHTTPConfigTLSFilesDefaultToHTTP1(t *testing.T) {
	certificate, roots := testHTTPTLSMaterial(t)
	certFile, keyFile := writeHTTPTestCertificate(t, certificate)
	config := DefaultHTTPConfig()
	config.TLSCertFile = certFile
	config.TLSKeyFile = keyFile
	component := startHTTPTestServer(t, http.NotFoundHandler(), nil, WithHTTPConfig(config))

	response := httpTestRequest(t, component, roots, true)
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if response.ProtoMajor != 1 {
		t.Fatalf("protocol = %s", response.Proto)
	}
}

func testHTTPTLSMaterial(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	fixture := httptest.NewTLSServer(http.NotFoundHandler())
	defer fixture.Close()
	certificate := fixture.TLS.Certificates[0]
	roots := x509.NewCertPool()
	roots.AddCert(fixture.Certificate())
	return certificate, roots
}

func writeHTTPTestCertificate(t *testing.T, certificate tls.Certificate) (string, string) {
	t.Helper()
	var certPEM bytes.Buffer
	for _, encoded := range certificate.Certificate {
		if err := pem.Encode(&certPEM, &pem.Block{Type: "CERTIFICATE", Bytes: encoded}); err != nil {
			t.Fatal(err)
		}
	}
	privateKey, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	var keyPEM bytes.Buffer
	if err := pem.Encode(&keyPEM, &pem.Block{Type: "PRIVATE KEY", Bytes: privateKey}); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	certFile := filepath.Join(directory, "cert.pem")
	keyFile := filepath.Join(directory, "key.pem")
	if err := os.WriteFile(certFile, certPEM.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, keyPEM.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

func httpTestRequest(t *testing.T, component *HTTP, roots *x509.CertPool, forceHTTP2 bool) *http.Response {
	t.Helper()
	address := strings.TrimPrefix(httpTestServerURL(t, component), "http://")
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: roots},
		ForceAttemptHTTP2: forceHTTP2,
	}}
	t.Cleanup(client.CloseIdleConnections)
	response, err := client.Get("https://" + address + "/")
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestHTTPConfigMaxHeaderBytesAppliesToRealConnections(t *testing.T) {
	config := DefaultHTTPConfig()
	config.MaxHeaderBytes = 1 << 10
	component := startHTTPTestServer(t, http.NotFoundHandler(), nil, WithHTTPConfig(config))
	baseURL := httpTestServerURL(t, component)
	address := strings.TrimPrefix(baseURL, "http://")

	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: example.com\r\nX-Large: %s\r\nConnection: close\r\n\r\n", strings.Repeat("a", 16<<10))
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestHTTPConfigReadHeaderTimeoutClosesSlowClient(t *testing.T) {
	config := DefaultHTTPConfig()
	config.ReadHeaderTimeout = 50 * time.Millisecond
	config.ReadTimeout = time.Second
	config.IdleTimeout = time.Second
	component := startHTTPTestServer(t, http.NotFoundHandler(), nil, WithHTTPConfig(config))
	address := strings.TrimPrefix(httpTestServerURL(t, component), "http://")

	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: example.com\r\nX-Incomplete: value"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	data, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if elapsed < 30*time.Millisecond {
		t.Fatalf("connection closed after %s, before the configured timeout", elapsed)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("connection stayed open for %s", elapsed)
	}
	if len(data) != 0 {
		response, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(data)), nil)
		if err != nil {
			t.Fatalf("parse timeout response: %v; raw %q", err, data)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusRequestTimeout {
			t.Fatalf("timeout response status = %d", response.StatusCode)
		}
	}
}

func TestHTTPConfigIdleTimeoutClosesKeepAliveConnection(t *testing.T) {
	config := DefaultHTTPConfig()
	config.ReadHeaderTimeout = time.Second
	config.ReadTimeout = time.Second
	config.IdleTimeout = 100 * time.Millisecond
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})
	component := startHTTPTestServer(t, handler, nil, WithHTTPConfig(config))
	address := strings.TrimPrefix(httpTestServerURL(t, component), "http://")

	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)
	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: example.com\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()

	time.Sleep(250 * time.Millisecond)
	if err := conn.SetDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	_, writeErr := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: example.com\r\n\r\n")
	if writeErr != nil {
		return
	}
	second, readErr := http.ReadResponse(reader, nil)
	if readErr != nil {
		return
	}
	second.Body.Close()
	t.Fatal("idle keep-alive connection accepted a second request")
}
