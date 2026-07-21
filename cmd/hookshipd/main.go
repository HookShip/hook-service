package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/hookship/hook-service/internal/buildinfo"
	"github.com/hookship/hook-service/internal/capabilities"
	"github.com/hookship/hook-service/internal/config"
	"github.com/hookship/hook-service/internal/daemon"
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
	if len(args) > 0 {
		switch args[0] {
		case "api", "worker", "all":
			return runMode(ctx, daemon.Mode(args[0]), args[1:], getenv, stdout, stderr)
		case "capabilities":
			return runCapabilities(args[1:], stdout, stderr)
		}
	}

	flags := flag.NewFlagSet("hookshipd", flag.ContinueOnError)
	flags.SetOutput(stderr)
	showVersion := flags.Bool("version", false, "print build metadata and exit")
	checkConfig := flags.Bool("check-config", false, "validate configuration and exit")
	flags.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: hookshipd [api|worker|all|capabilities] [-check-config] [-version]")
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

	logger := newLogger(stderr, cfg.LogLevel)
	if cfg.Runtime.Selected() {
		return runDaemon(ctx, daemon.ModeAll, cfg, logger)
	}
	// Backward-compatible health-only process when no backend is selected.
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

func runMode(
	ctx context.Context,
	mode daemon.Mode,
	args []string,
	getenv func(string) string,
	stdout io.Writer,
	stderr io.Writer,
) int {
	flags := flag.NewFlagSet("hookshipd "+string(mode), flag.ContinueOnError)
	flags.SetOutput(stderr)
	checkConfig := flags.Bool("check-config", false, "validate configuration and exit")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "hookshipd %s: unexpected arguments: %v\n", mode, flags.Args())
		return 2
	}
	cfg, err := config.FromEnv(getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "hookshipd %s: invalid configuration: %v\n", mode, err)
		return 2
	}
	if *checkConfig {
		_, _ = fmt.Fprintln(stdout, "hookshipd configuration is valid")
		return 0
	}
	logger := newLogger(stderr, cfg.LogLevel)
	return runDaemon(ctx, mode, cfg, logger)
}

func runDaemon(ctx context.Context, mode daemon.Mode, cfg config.Config, logger *slog.Logger) int {
	if err := daemon.Run(ctx, mode, cfg, logger, buildinfo.Current()); err != nil {
		logger.Error("hookshipd stopped with an error", slog.String("mode", string(mode)), slog.String("error", err.Error()))
		return 1
	}
	return 0
}

func runCapabilities(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("hookshipd capabilities", flag.ContinueOnError)
	flags.SetOutput(stderr)
	output := flags.String("output", "json", "output format: json or text")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "hookshipd capabilities: unexpected arguments: %v\n", flags.Args())
		return 2
	}
	documents := capabilities.Profiles()
	switch *output {
	case "json":
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(map[string]any{
			"schemaVersion": capabilities.SchemaVersion,
			"profiles":      documents,
		}); err != nil {
			_, _ = fmt.Fprintf(stderr, "hookshipd capabilities: %v\n", err)
			return 1
		}
	case "text":
		for _, document := range documents {
			_, _ = fmt.Fprintf(stdout, "%-16s %-12s durability=%-10s delivery=%-13s ordering=%-10s\n",
				document.Profile, document.Properties.Status, document.Properties.Durability, document.Properties.DeliverySemantics, document.Properties.Ordering)
		}
	default:
		_, _ = fmt.Fprintln(stderr, "hookshipd capabilities: output must be json or text")
		return 2
	}
	return 0
}

func newLogger(stderr io.Writer, level string) *slog.Logger {
	return slog.New(slog.NewJSONHandler(stderr, &slog.HandlerOptions{Level: logLevel(level)}))
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
