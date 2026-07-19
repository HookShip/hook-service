// Package httpserver owns the hookshipd HTTP lifecycle and operational endpoints.
package httpserver

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/hookship/hook-service/internal/buildinfo"
	"github.com/hookship/hook-service/internal/config"
)

// Server is the bounded operational HTTP process.
type Server struct {
	config     config.Config
	logger     *slog.Logger
	readiness  *Readiness
	httpServer *http.Server
}

// New constructs a validated server.
func New(cfg config.Config, logger *slog.Logger, info buildinfo.Info) (*Server, error) {
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
		Handler:           newHandler(readiness, info),
		ReadHeaderTimeout: cfg.ReadHeader,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    cfg.MaxHeaderBytes,
		ErrorLog:          log.New(slogWriter{logger: logger}, "", 0),
	}
	return server, nil
}

// Run listens until the context is canceled or the HTTP server fails.
func (s *Server) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.config.ListenAddress)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.config.ListenAddress, err)
	}

	serveErrors := make(chan error, 1)
	s.readiness.Set(true)
	s.logger.Info(
		"http server started",
		slog.String("address", listener.Addr().String()),
		slog.Int("max_header_bytes", s.config.MaxHeaderBytes),
	)
	go func() {
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
