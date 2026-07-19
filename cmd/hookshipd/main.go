package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/hookship/hook-service/internal/buildinfo"
	"github.com/hookship/hook-service/internal/config"
	"github.com/hookship/hook-service/internal/httpserver"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(
	ctx context.Context,
	args []string,
	getenv func(string) string,
	stdout io.Writer,
	stderr io.Writer,
) int {
	flags := flag.NewFlagSet("hookshipd", flag.ContinueOnError)
	flags.SetOutput(stderr)
	showVersion := flags.Bool("version", false, "print build metadata and exit")
	checkConfig := flags.Bool("check-config", false, "validate configuration and exit")
	flags.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: hookshipd [-check-config] [-version]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "hookshipd: unexpected arguments: %v\n", flags.Args())
		flags.Usage()
		return 2
	}
	if *showVersion {
		_, _ = fmt.Fprintf(stdout, "hookshipd %s\n", buildinfo.Current())
		return 0
	}

	cfg, err := config.FromEnv(getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "hookshipd: invalid configuration: %v\n", err)
		return 2
	}
	if *checkConfig {
		_, _ = fmt.Fprintln(stdout, "hookshipd configuration is valid")
		return 0
	}

	logger := slog.New(slog.NewJSONHandler(stderr, &slog.HandlerOptions{
		Level: logLevel(cfg.LogLevel),
	}))
	server, err := httpserver.New(cfg, logger, buildinfo.Current())
	if err != nil {
		logger.Error("create HTTP server", slog.String("error", err.Error()))
		return 1
	}
	if err := server.Run(ctx); err != nil {
		logger.Error("HTTP server stopped with an error", slog.String("error", err.Error()))
		return 1
	}
	return 0
}

func logLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
