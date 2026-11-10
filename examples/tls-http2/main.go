package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/plinth-go/plinth/runtime"
)

const (
	modeDirect = "direct"
	modeProxy  = "proxy"
)

type options struct {
	mode            string
	addr            string
	certFile        string
	keyFile         string
	generateDevCert bool
}

type response struct {
	Message  string `json:"message"`
	Protocol string `json:"protocol"`
	TLS      bool   `json:"tls"`
}

func main() {
	var opts options
	flag.StringVar(&opts.mode, "mode", modeDirect, "direct TLS or reverse-proxy listener")
	flag.StringVar(&opts.addr, "addr", "127.0.0.1:8443", "HTTP listen address")
	flag.StringVar(&opts.certFile, "cert", ".dev-tls/tls.crt", "development certificate PEM")
	flag.StringVar(&opts.keyFile, "key", ".dev-tls/tls.key", "development private-key PEM")
	flag.BoolVar(&opts.generateDevCert, "generate-dev-cert", false, "generate a development-only localhost certificate and exit")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "positional arguments are not supported")
		os.Exit(2)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, logger, opts); err != nil {
		logger.Error("example failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger, opts options) error {
	if opts.generateDevCert {
		return generateDevCertificate(opts.certFile, opts.keyFile)
	}
	config, err := httpConfig(opts)
	if err != nil {
		return err
	}
	component := runtime.NewHTTP(opts.addr, newHandler(), nil, logger, runtime.WithHTTPConfig(config))
	service := runtime.New(logger, 5*time.Second)
	service.Add(component)
	return service.Run(ctx)
}

func httpConfig(opts options) (runtime.HTTPConfig, error) {
	config := runtime.DefaultHTTPConfig()
	switch opts.mode {
	case modeDirect:
		if strings.TrimSpace(opts.certFile) == "" || strings.TrimSpace(opts.keyFile) == "" {
			return config, fmt.Errorf("direct mode requires -cert and -key")
		}
		config.TLSCertFile = opts.certFile
		config.TLSKeyFile = opts.keyFile
		config.EnableHTTP2 = true
	case modeProxy:
	default:
		return config, fmt.Errorf("unknown mode %q; use %q or %q", opts.mode, modeDirect, modeProxy)
	}
	return config, nil
}

func newHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_ = json.NewEncoder(w).Encode(response{
			Message:  "Plinth TLS/HTTP example",
			Protocol: request.Proto,
			TLS:      request.TLS != nil,
		})
	})
	return mux
}

func generateDevCertificate(certFile, keyFile string) error {
	if strings.TrimSpace(certFile) == "" || strings.TrimSpace(keyFile) == "" {
		return fmt.Errorf("certificate and key paths are required")
	}
	if filepath.Clean(certFile) == filepath.Clean(keyFile) {
		return fmt.Errorf("certificate and key paths must differ")
	}

	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate private key: %w", err)
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return fmt.Errorf("generate certificate serial: %w", err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"Plinth development only"},
			CommonName:   "localhost",
		},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return fmt.Errorf("create certificate: %w", err)
	}
	privateKeyDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		return fmt.Errorf("marshal private key: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(certFile), 0o700); err != nil {
		return fmt.Errorf("create certificate directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(keyFile), 0o700); err != nil {
		return fmt.Errorf("create key directory: %w", err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})
	if err := writeExclusive(certFile, certificatePEM, 0o644); err != nil {
		return fmt.Errorf("write development certificate: %w", err)
	}
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privateKeyDER})
	if err := writeExclusive(keyFile, privateKeyPEM, 0o600); err != nil {
		_ = os.Remove(certFile)
		return fmt.Errorf("write development key: %w", err)
	}
	fmt.Printf("created development-only files %s and %s\n", certFile, keyFile)
	fmt.Println("do not use these certificates outside local development")
	return nil
}

func writeExclusive(path string, data []byte, mode os.FileMode) (err error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
	}()
	_, err = file.Write(data)
	return err
}
