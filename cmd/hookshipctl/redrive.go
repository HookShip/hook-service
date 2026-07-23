package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/hookship/hook-service/internal/backend"
	"github.com/hookship/hook-service/internal/config"
	"github.com/hookship/hook-service/internal/runtime"
)

// runRedrive is the operator-facing dead-letter redrive surface. It never
// mutates database state directly: it uses the same runtime backend ports the
// worker uses (ListDeadJobs, RedriveJob), so a redrive is an explicit,
// supported control action rather than an ad-hoc SQL edit. It prints only
// payload-free job metadata and never logs secrets or payload bytes.
func runRedrive(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "Usage: hookshipctl redrive <list|run> [options]")
		return 2
	}
	switch args[0] {
	case "list":
		return runRedriveList(ctx, args[1:], getenv, stdout, stderr)
	case "run":
		return runRedriveRun(ctx, args[1:], getenv, stdout, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "hookshipctl redrive: unknown subcommand %q\n", args[0])
		return 2
	}
}

// deadJobView is the payload-free projection of a dead dispatch job that the
// redrive surface prints. It intentionally carries no payload bytes, secrets,
// or endpoint URLs — only stable identifiers and delivery outcome metadata.
type deadJobView struct {
	AssignmentID  string    `json:"assignmentId"`
	JobID         string    `json:"jobId"`
	DeliveryID    string    `json:"deliveryId"`
	State         string    `json:"state"`
	Attempts      int       `json:"attempts"`
	MaxAttempts   int       `json:"maxAttempts"`
	LastErrorCode string    `json:"lastErrorCode,omitempty"`
	RunAt         time.Time `json:"runAt"`
}

func newDeadJobView(job runtime.Job) deadJobView {
	return deadJobView{
		AssignmentID:  job.AssignmentID,
		JobID:         job.JobID,
		DeliveryID:    job.DeliveryID,
		State:         job.State,
		Attempts:      job.Attempts,
		MaxAttempts:   job.MaxAttempts,
		LastErrorCode: job.LastErrorCode,
		RunAt:         job.RunAt,
	}
}

// openRedriveBackend builds the configured runtime backend from the same
// configuration the daemon uses. The caller owns closing it.
func openRedriveBackend(ctx context.Context, getenv func(string) string) (runtime.Backend, error) {
	runtimeCfg, err := config.LoadRuntime(getenv)
	if err != nil {
		return nil, err
	}
	if !runtimeCfg.Selected() {
		return nil, errors.New("no backend selected (set HOOKSHIP_BACKEND)")
	}
	assignments, err := backend.ResolveAssignments(runtimeCfg)
	if err != nil {
		return nil, err
	}
	return backend.Build(ctx, backend.Deps{Runtime: runtimeCfg, Assignments: assignments})
}

// selectAssignmentIDs returns the assignment ids to operate on: the single
// requested id (verified to exist) or every registered assignment.
func selectAssignmentIDs(ctx context.Context, be runtime.Backend, only string) ([]string, error) {
	assignments, err := be.Assignments(ctx)
	if err != nil {
		return nil, err
	}
	if only == "" {
		ids := make([]string, 0, len(assignments))
		for _, assignment := range assignments {
			ids = append(ids, assignment.ID)
		}
		return ids, nil
	}
	for _, assignment := range assignments {
		if assignment.ID == only {
			return []string{only}, nil
		}
	}
	return nil, fmt.Errorf("unknown assignment %q", only)
}

func runRedriveList(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("hookshipctl redrive list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	assignmentID := flags.String("assignment", "", "restrict to one assignment id (default: all)")
	limit := flags.Int("limit", 50, "max dead jobs listed per assignment (1-10000)")
	output := flags.String("output", "text", "output format: text or json")
	timeout := flags.Duration("timeout", 30*time.Second, "operation timeout")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "hookshipctl redrive list: unexpected arguments: %v\n", flags.Args())
		return 2
	}
	if !validOutput(*output) {
		_, _ = fmt.Fprintln(stderr, "hookshipctl redrive list: output must be text or json")
		return 2
	}
	if *limit < 1 || *limit > 10_000 {
		_, _ = fmt.Fprintln(stderr, "hookshipctl redrive list: limit must be between 1 and 10000")
		return 2
	}

	opCtx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	be, err := openRedriveBackend(opCtx, getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "hookshipctl redrive list: %v\n", err)
		return 1
	}
	defer be.Close()

	ids, err := selectAssignmentIDs(opCtx, be, *assignmentID)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "hookshipctl redrive list: %v\n", err)
		return 1
	}
	views := make([]deadJobView, 0)
	for _, id := range ids {
		jobs, err := be.ListDeadJobs(opCtx, id, *limit)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "hookshipctl redrive list: %v\n", err)
			return 1
		}
		for _, job := range jobs {
			views = append(views, newDeadJobView(job))
		}
	}

	if *output == "json" {
		if err := writeJSON(stdout, views); err != nil {
			_, _ = fmt.Fprintf(stderr, "hookshipctl redrive list: %v\n", err)
			return 1
		}
		return 0
	}
	if len(views) == 0 {
		_, _ = fmt.Fprintln(stdout, "no dead jobs")
		return 0
	}
	for _, view := range views {
		_, _ = fmt.Fprintf(stdout, "%-24s %-26s attempts=%d/%d error=%s\n",
			view.AssignmentID, view.JobID, view.Attempts, view.MaxAttempts, view.LastErrorCode)
	}
	return 0
}

func runRedriveRun(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("hookshipctl redrive run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	assignmentID := flags.String("assignment", "", "assignment id (required)")
	jobID := flags.String("job", "", "one dead job id to redrive")
	all := flags.Bool("all", false, "redrive every dead job for the assignment")
	limit := flags.Int("limit", 100, "max dead jobs redriven when --all (1-10000)")
	delay := flags.Duration("delay", 0, "schedule redriven jobs this far in the future")
	output := flags.String("output", "text", "output format: text or json")
	timeout := flags.Duration("timeout", 60*time.Second, "operation timeout")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "hookshipctl redrive run: unexpected arguments: %v\n", flags.Args())
		return 2
	}
	if !validOutput(*output) {
		_, _ = fmt.Fprintln(stderr, "hookshipctl redrive run: output must be text or json")
		return 2
	}
	if *assignmentID == "" {
		_, _ = fmt.Fprintln(stderr, "hookshipctl redrive run: --assignment is required")
		return 2
	}
	if (*jobID == "") == !*all {
		_, _ = fmt.Fprintln(stderr, "hookshipctl redrive run: specify exactly one of --job or --all")
		return 2
	}
	if *all && (*limit < 1 || *limit > 10_000) {
		_, _ = fmt.Fprintln(stderr, "hookshipctl redrive run: limit must be between 1 and 10000")
		return 2
	}
	if *delay < 0 {
		_, _ = fmt.Fprintln(stderr, "hookshipctl redrive run: delay must not be negative")
		return 2
	}

	opCtx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	be, err := openRedriveBackend(opCtx, getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "hookshipctl redrive run: %v\n", err)
		return 1
	}
	defer be.Close()

	if _, err := selectAssignmentIDs(opCtx, be, *assignmentID); err != nil {
		_, _ = fmt.Fprintf(stderr, "hookshipctl redrive run: %v\n", err)
		return 1
	}

	var targets []string
	if *jobID != "" {
		targets = []string{*jobID}
	} else {
		jobs, err := be.ListDeadJobs(opCtx, *assignmentID, *limit)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "hookshipctl redrive run: %v\n", err)
			return 1
		}
		for _, job := range jobs {
			targets = append(targets, job.JobID)
		}
	}

	var runAt time.Time
	if *delay > 0 {
		runAt = time.Now().UTC().Add(*delay)
	}
	type redriveOutcome struct {
		JobID string `json:"jobId"`
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	outcomes := make([]redriveOutcome, 0, len(targets))
	redriven := 0
	for _, id := range targets {
		if err := be.RedriveJob(opCtx, *assignmentID, id, runAt); err != nil {
			outcomes = append(outcomes, redriveOutcome{JobID: id, OK: false, Error: err.Error()})
			continue
		}
		redriven++
		outcomes = append(outcomes, redriveOutcome{JobID: id, OK: true})
	}

	if *output == "json" {
		result := struct {
			AssignmentID string           `json:"assignmentId"`
			Requested    int              `json:"requested"`
			Redriven     int              `json:"redriven"`
			Outcomes     []redriveOutcome `json:"outcomes"`
		}{AssignmentID: *assignmentID, Requested: len(targets), Redriven: redriven, Outcomes: outcomes}
		if err := writeJSON(stdout, result); err != nil {
			_, _ = fmt.Fprintf(stderr, "hookshipctl redrive run: %v\n", err)
			return 1
		}
	} else {
		_, _ = fmt.Fprintf(stdout, "assignment %s: redriven %d/%d dead job(s)\n", *assignmentID, redriven, len(targets))
		for _, outcome := range outcomes {
			if !outcome.OK {
				_, _ = fmt.Fprintf(stdout, "  failed %s: %s\n", outcome.JobID, outcome.Error)
			}
		}
	}
	if redriven != len(targets) {
		return 1
	}
	return 0
}
