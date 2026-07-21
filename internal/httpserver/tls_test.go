package httpserver

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hookship/hook-service/internal/buildinfo"
	"github.com/hookship/hook-service/internal/config"
)

func writeSelfSignedCert(t *testing.T) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath = filepath.Join(dir, "server.crt")
	keyPath = filepath.Join(dir, "server.key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

func waitReady(t *testing.T, server *Server) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if server.readiness.Ready() && server.Addr() != "" {
			return server.Addr()
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("server did not become ready with a bound address")
	return ""
}

func TestServeTLSAcceptsHTTPSAndRejectsPlaintext(t *testing.T) {
	t.Parallel()
	certPath, keyPath := writeSelfSignedCert(t)

	cfg := config.Default()
	cfg.ListenAddress = "127.0.0.1:0"
	cfg.ShutdownTimeout = time.Second
	// Direct TLS termination is independent of backend selection; a complete
	// cert/key pair is sufficient to switch the listener to HTTPS.
	cfg.Runtime.TLSCertFile = certPath
	cfg.Runtime.TLSKeyFile = keyPath

	server, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), buildinfo.Info{Version: "test"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if !server.TLSEnabled() {
		t.Fatal("expected TLS to be enabled")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx) }()
	addr := waitReady(t, server)

	// HTTPS request succeeds.
	httpsClient := &http.Client{
		Timeout:   2 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
	response, err := httpsClient.Get("https://" + addr + "/healthz")
	if err != nil {
		cancel()
		t.Fatalf("HTTPS request failed: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("HTTPS status = %d", response.StatusCode)
	}
	_ = response.Body.Close()

	// A plaintext HTTP request to the TLS port is rejected: Go's TLS server
	// answers 400 "Client sent an HTTP request to an HTTPS server" (or the
	// connection fails) rather than serving a 200 health response.
	plaintextClient := &http.Client{Timeout: 2 * time.Second}
	plaintextResponse, plaintextErr := plaintextClient.Get("http://" + addr + "/healthz")
	if plaintextErr == nil {
		body, _ := io.ReadAll(io.LimitReader(plaintextResponse.Body, 512))
		_ = plaintextResponse.Body.Close()
		if plaintextResponse.StatusCode == http.StatusOK {
			cancel()
			t.Fatalf("plaintext request to the TLS listener was served a 200: %s", body)
		}
		if plaintextResponse.StatusCode != http.StatusBadRequest {
			cancel()
			t.Fatalf("plaintext request returned unexpected status %d", plaintextResponse.StatusCode)
		}
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestNewFailsFastOnUnreadableCert(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.ListenAddress = "127.0.0.1:0"
	cfg.Runtime.TLSCertFile = filepath.Join(t.TempDir(), "missing.crt")
	cfg.Runtime.TLSKeyFile = filepath.Join(t.TempDir(), "missing.key")

	_, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), buildinfo.Info{Version: "test"})
	if err == nil {
		t.Fatal("expected New to fail fast on an unreadable certificate")
	}
}

func TestNewRejectsPartialTLSPair(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.ListenAddress = "127.0.0.1:0"
	cfg.Runtime.TLSCertFile = "/tmp/only-cert.pem"
	// No key set: configuration validation must reject the partial pair.
	if _, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), buildinfo.Info{Version: "test"}); err == nil {
		t.Fatal("expected New to reject a partial TLS pair")
	}
}

func TestPlaintextServerStillServesLoopback(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.ListenAddress = "127.0.0.1:0"
	cfg.ShutdownTimeout = time.Second

	server, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), buildinfo.Info{Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if server.TLSEnabled() {
		t.Fatal("loopback evaluation server must remain plaintext")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx) }()
	addr := waitReady(t, server)

	response, err := (&http.Client{Timeout: 2 * time.Second}).Get("http://" + addr + "/healthz")
	if err != nil {
		cancel()
		t.Fatalf("plaintext loopback request failed: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("status = %d", response.StatusCode)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}
