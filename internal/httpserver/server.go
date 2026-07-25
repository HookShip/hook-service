// Package httpserver owns the hookshipd HTTP lifecycle and operational endpoints.
package httpserver

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/hookship/hook-service/internal/buildinfo"
	"github.com/hookship/hook-service/internal/config"
)

// Server is the bounded operational HTTP process.
type Server struct {
	config     config.Config
	logger     *slog.Logger
	readiness  *Readiness
	httpServer *http.Server
	tlsEnabled bool
	boundAddr  atomic.Pointer[string]
}

// Options configures optional profile routes and readiness behavior.
type Options struct {
	// Register mounts profile routes (e.g. the ingest handler) on the mux.
	Register func(*http.ServeMux)
	// ReadinessProbe reports backend health for /readyz.
	ReadinessProbe func() bool
	// Durability is surfaced in health/readiness responses (e.g. "ephemeral").
	Durability string
	// StatusProvider, when set, serves payload-free operator status JSON on
	// /statusz (build info, backend selection, and runtime metrics). It must
	// never return request bodies, payloads, secrets, or identifiers.
	StatusProvider func() any
}

// New constructs a validated health-only server.
func New(cfg config.Config, logger *slog.Logger, info buildinfo.Info) (*Server, error) {
	return NewWithOptions(cfg, logger, info, Options{})
}

// NewWithOptions constructs a validated server with optional profile routes.
func NewWithOptions(cfg config.Config, logger *slog.Logger, info buildinfo.Info, options Options) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate server configuration: %w", err)
	}
	if logger == nil {
		return nil, errors.New("logger is required")
	}

	readiness := &Readiness{}
	server := &Server{
		config:    cfg,
		logger:    logger,
		readiness: readiness,
	}
	server.httpServer = &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           newHandlerWithRoutes(readiness, info, options),
		ReadHeaderTimeout: cfg.ReadHeader,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    cfg.MaxHeaderBytes,
		ErrorLog:          log.New(slogWriter{logger: logger}, "", 0),
	}

	// Direct TLS termination: load and validate the certificate/key pair now so
	// a misconfiguration fails fast at construction rather than serving
	// plaintext. Configuration validation already rejects a partial pair.
	if cfg.Runtime.HasTLS() {
		certificate, err := tls.LoadX509KeyPair(cfg.Runtime.Transport.TLSCertFile, cfg.Runtime.Transport.TLSKeyFile)
		if err != nil {
			return nil, fmt.Errorf("load TLS certificate/key: %w", err)
		}
		server.httpServer.TLSConfig = &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{certificate},
		}
		server.tlsEnabled = true
	}
	return server, nil
}

// TLSEnabled reports whether the server terminates TLS directly.
func (s *Server) TLSEnabled() bool { return s.tlsEnabled }

// Addr returns the bound listen address once Run has started, or an empty
// string before then. It is primarily useful for tests binding an ephemeral
// port.
func (s *Server) Addr() string {
	if value := s.boundAddr.Load(); value != nil {
		return *value
	}
	return ""
}

// Run listens until the context is canceled or the HTTP server fails.
func (s *Server) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.config.ListenAddress)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.config.ListenAddress, err)
	}

	serveErrors := make(chan error, 1)
	boundAddr := listener.Addr().String()
	s.boundAddr.Store(&boundAddr)
	s.readiness.Set(true)
	s.logger.Info(
		"http server started",
		slog.String("address", boundAddr),
		slog.Bool("tls", s.tlsEnabled),
		slog.Int("max_header_bytes", s.config.MaxHeaderBytes),
	)
	go func() {
		if s.tlsEnabled {
			// The certificate is preloaded into TLSConfig, so empty file
			// arguments reuse it rather than re-reading from disk.
			serveErrors <- s.httpServer.ServeTLS(listener, "", "")
			return
		}
		serveErrors <- s.httpServer.Serve(listener)
	}()

	select {
	case err := <-serveErrors:
		s.readiness.Set(false)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
		s.readiness.Set(false)
		shutdownContext, cancel := context.WithTimeout(context.Background(), s.config.ShutdownTimeout)
		defer cancel()

		s.logger.Info("http server shutting down")
		if err := s.httpServer.Shutdown(shutdownContext); err != nil {
			_ = s.httpServer.Close()
			return fmt.Errorf("shut down HTTP server: %w", err)
		}

		err := <-serveErrors
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve HTTP during shutdown: %w", err)
		}
		s.logger.Info("http server stopped")
		return nil
	}
}

type slogWriter struct {
	logger *slog.Logger
}

func (w slogWriter) Write(message []byte) (int, error) {
	w.logger.Error("http server error", slog.String("detail", strings.TrimSpace(string(message))))
	return len(message), nil
}
