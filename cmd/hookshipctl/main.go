package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/hookship/hook-service/internal/buildinfo"
	"github.com/hookship/hook-service/internal/diagnostics"
)

const defaultDiagnosticsURL = "http://127.0.0.1:8080"

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}

	switch args[0] {
	case "version":
		return runVersion(args[1:], stdout, stderr)
	case "diagnostics":
		return runDiagnostics(ctx, args[1:], stdout, stderr)
	case "doctor":
		return runDoctor(ctx, args[1:], os.Getenv, stdout, stderr)
	case "migrate":
		return runMigrate(ctx, args[1:], os.Getenv, stdout, stderr)
	case "backend":
		return runBackend(args[1:], os.Getenv, stdout, stderr)
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "hookshipctl: unknown command %q\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func runVersion(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("hookshipctl version", flag.ContinueOnError)
	flags.SetOutput(stderr)
	output := flags.String("output", "text", "output format: text or json")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "hookshipctl version: unexpected arguments: %v\n", flags.Args())
		return 2
	}
	if !validOutput(*output) {
		_, _ = fmt.Fprintln(stderr, "hookshipctl version: output must be text or json")
		return 2
	}

	info := buildinfo.Current()
	if *output == "json" {
		if err := writeJSON(stdout, info); err != nil {
			_, _ = fmt.Fprintf(stderr, "hookshipctl version: write output: %v\n", err)
			return 1
		}
		return 0
	}
	_, _ = fmt.Fprintf(stdout, "hookshipctl %s\n", info)
	return 0
}

func runDiagnostics(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("hookshipctl diagnostics", flag.ContinueOnError)
	flags.SetOutput(stderr)
	target := flags.String("url", defaultDiagnosticsURL, "hookshipd base URL")
	timeout := flags.Duration("timeout", 3*time.Second, "overall diagnostics timeout")
	output := flags.String("output", "text", "output format: text or json")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "hookshipctl diagnostics: unexpected arguments: %v\n", flags.Args())
		return 2
	}
	if *timeout <= 0 {
		_, _ = fmt.Fprintln(stderr, "hookshipctl diagnostics: timeout must be greater than zero")
		return 2
	}
	if !validOutput(*output) {
		_, _ = fmt.Fprintln(stderr, "hookshipctl diagnostics: output must be text or json")
		return 2
	}

	diagnosticsContext, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	client := &http.Client{
		Timeout:   *timeout,
		Transport: diagnosticsTransport(),
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	report, err := diagnostics.Run(
		diagnosticsContext,
		client,
		*target,
		"hookshipctl/"+buildinfo.Current().Version,
	)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "hookshipctl diagnostics: %v\n", err)
		return 2
	}

	if *output == "json" {
		if err := writeJSON(stdout, report); err != nil {
			_, _ = fmt.Fprintf(stderr, "hookshipctl diagnostics: write output: %v\n", err)
			return 1
		}
	} else {
		writeDiagnosticsText(stdout, report)
	}
	if !report.OK {
		return 1
	}
	return 0
}

func writeDiagnosticsText(output io.Writer, report diagnostics.Report) {
	_, _ = fmt.Fprintf(output, "target: %s\n", report.Target)
	writeProbeText(output, report.Health)
	writeProbeText(output, report.Readiness)
	if report.OK {
		_, _ = fmt.Fprintln(output, "overall: ok")
	} else {
		_, _ = fmt.Fprintln(output, "overall: failed")
	}
}

func writeProbeText(output io.Writer, result diagnostics.ProbeResult) {
	if result.OK {
		_, _ = fmt.Fprintf(output, "%s: %s (HTTP %d)\n", result.Name, result.Status, result.HTTPStatus)
		return
	}
	detail := result.Error
	if detail == "" {
		detail = "probe failed"
	}
	if result.HTTPStatus != 0 {
		_, _ = fmt.Fprintf(output, "%s: failed (HTTP %d: %s)\n", result.Name, result.HTTPStatus, detail)
		return
	}
	_, _ = fmt.Fprintf(output, "%s: failed (%s)\n", result.Name, detail)
}

func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func validOutput(output string) bool {
	switch output {
	case "text", "json":
		return true
	default:
		return false
	}
}

func diagnosticsTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableCompression = true
	transport.MaxConnsPerHost = 2
	transport.MaxIdleConns = 2
	transport.MaxIdleConnsPerHost = 2
	transport.MaxResponseHeaderBytes = 64 << 10
	return transport
}

func printUsage(output io.Writer) {
	_, _ = fmt.Fprintln(output, "Usage: hookshipctl <command> [options]")
	_, _ = fmt.Fprintln(output, "")
	_, _ = fmt.Fprintln(output, "Commands:")
	_, _ = fmt.Fprintln(output, "  diagnostics        Probe hookshipd health and readiness")
	_, _ = fmt.Fprintln(output, "  doctor             Validate configuration and backend selection")
	_, _ = fmt.Fprintln(output, "  migrate            Apply PostgreSQL durability migrations")
	_, _ = fmt.Fprintln(output, "  backend list       List native runtime profiles and capabilities")
	_, _ = fmt.Fprintln(output, "  backend describe   Describe one profile's capability document")
	_, _ = fmt.Fprintln(output, "  backend validate   Validate a backend selection")
	_, _ = fmt.Fprintln(output, "  version            Print build metadata")
}
