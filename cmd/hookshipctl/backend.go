package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/hookship/hook-service/internal/assignmentconfig"
	"github.com/hookship/hook-service/internal/backend"
	"github.com/hookship/hook-service/internal/capabilities"
	"github.com/hookship/hook-service/internal/config"
)

func runMigrate(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("hookshipctl migrate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	timeout := flags.Duration("timeout", 60*time.Second, "migration timeout")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	runtimeCfg, err := config.LoadRuntime(getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "hookshipctl migrate: %v\n", err)
		return 2
	}
	if !runtimeCfg.Selected() {
		_, _ = fmt.Fprintln(stderr, "hookshipctl migrate: set HOOKSHIP_BACKEND")
		return 2
	}
	migrateCtx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	if err := backend.Migrate(migrateCtx, runtimeCfg); err != nil {
		_, _ = fmt.Fprintf(stderr, "hookshipctl migrate: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, "hookshipctl migrate: migrations applied")
	return 0
}

func runDoctor(_ context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("hookshipctl doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	output := flags.String("output", "text", "output format: text or json")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	runtimeCfg, err := config.LoadRuntime(getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "hookshipctl doctor: %v\n", err)
		return 2
	}
	if !runtimeCfg.Selected() {
		_, _ = fmt.Fprintln(stderr, "hookshipctl doctor: no backend selected (set HOOKSHIP_BACKEND)")
		return 2
	}
	listen := getenv("HOOKSHIP_LISTEN_ADDRESS")
	if listen == "" {
		listen = "127.0.0.1:8080"
	}
	selection, err := runtimeCfg.SelectionForListen(listen)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "hookshipctl doctor: %v\n", err)
		return 1
	}
	result := capabilities.Validate(selection)

	if *output == "json" {
		if err := writeJSON(stdout, result); err != nil {
			_, _ = fmt.Fprintf(stderr, "hookshipctl doctor: %v\n", err)
			return 1
		}
	} else {
		_, _ = fmt.Fprintf(stdout, "backend: %s\n", runtimeCfg.Backend)
		_, _ = fmt.Fprintf(stdout, "selection valid: %t\n", result.OK)
		for _, finding := range result.Errors {
			_, _ = fmt.Fprintf(stdout, "  error: %s (%s)\n", finding.Reason, finding.ReasonCode)
		}
		for _, finding := range result.Warnings {
			_, _ = fmt.Fprintf(stdout, "  warning: %s (%s)\n", finding.Reason, finding.ReasonCode)
		}
	}
	if !result.OK {
		return 1
	}
	return 0
}

func runBackend(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "Usage: hookshipctl backend <list|describe|validate>")
		return 2
	}
	switch args[0] {
	case "list":
		return runBackendList(args[1:], stdout, stderr)
	case "describe":
		return runBackendDescribe(args[1:], stdout, stderr)
	case "validate":
		return runBackendValidate(args[1:], getenv, stdout, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "hookshipctl backend: unknown subcommand %q\n", args[0])
		return 2
	}
}

func runBackendList(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("hookshipctl backend list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	output := flags.String("output", "text", "output format: text or json")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	documents := capabilities.Profiles()
	if *output == "json" {
		if err := writeJSON(stdout, documents); err != nil {
			_, _ = fmt.Fprintf(stderr, "hookshipctl backend list: %v\n", err)
			return 1
		}
		return 0
	}
	for _, document := range documents {
		_, _ = fmt.Fprintf(stdout, "%-16s %-12s %s\n", document.Profile, document.Properties.Status, document.Properties.Summary)
	}
	return 0
}

func runBackendDescribe(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("hookshipctl backend describe", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 1 {
		_, _ = fmt.Fprintln(stderr, "Usage: hookshipctl backend describe <profile>")
		return 2
	}
	document, ok := capabilities.Lookup(flags.Arg(0))
	if !ok {
		_, _ = fmt.Fprintf(stderr, "hookshipctl backend describe: unknown profile %q\n", flags.Arg(0))
		return 2
	}
	if err := writeJSON(stdout, document); err != nil {
		_, _ = fmt.Fprintf(stderr, "hookshipctl backend describe: %v\n", err)
		return 1
	}
	return 0
}

func runBackendValidate(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("hookshipctl backend validate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "assignment config file to validate")
	output := flags.String("output", "text", "output format: text or json")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	runtimeCfg, err := config.LoadRuntime(getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "hookshipctl backend validate: %v\n", err)
		return 2
	}
	if *configPath != "" {
		file, err := assignmentconfig.Load(*configPath)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "hookshipctl backend validate: %v\n", err)
			return 2
		}
		if file.Backend != "" {
			runtimeCfg.Backend = file.Backend
		}
		runtimeCfg.EvaluationMode = file.EvaluationMode
		if file.PayloadBackend != "" {
			runtimeCfg.PayloadBackend = file.PayloadBackend
		}
	}
	if !runtimeCfg.Selected() {
		_, _ = fmt.Fprintln(stderr, "hookshipctl backend validate: no backend selected")
		return 2
	}
	listen := getenv("HOOKSHIP_LISTEN_ADDRESS")
	if listen == "" {
		listen = "127.0.0.1:8080"
	}
	selection, err := runtimeCfg.SelectionForListen(listen)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "hookshipctl backend validate: %v\n", err)
		return 1
	}
	result := capabilities.Validate(selection)

	if *output == "json" {
		if err := writeJSON(stdout, result); err != nil {
			_, _ = fmt.Fprintf(stderr, "hookshipctl backend validate: %v\n", err)
			return 1
		}
	} else {
		_, _ = fmt.Fprintf(stdout, "backend: %s\nvalid: %t\n", runtimeCfg.Backend, result.OK)
		for _, finding := range result.Errors {
			_, _ = fmt.Fprintf(stdout, "  error: %s (%s)\n", finding.Reason, finding.ReasonCode)
		}
		for _, finding := range result.Warnings {
			_, _ = fmt.Fprintf(stdout, "  warning: %s (%s)\n", finding.Reason, finding.ReasonCode)
		}
	}
	if !result.OK {
		return 1
	}
	return 0
}
