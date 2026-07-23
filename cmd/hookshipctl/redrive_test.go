package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// evalGetenv returns a getenv closure selecting the in-memory evaluation
// backend, which resolves the well-known default evaluation assignment without
// a config file or database.
func evalGetenv(extra map[string]string) func(string) string {
	base := map[string]string{
		"HOOKSHIP_BACKEND":         "memory",
		"HOOKSHIP_EVALUATION_MODE": "true",
	}
	for key, value := range extra {
		base[key] = value
	}
	return func(key string) string { return base[key] }
}

func TestRedriveListEmpty(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := runRedrive(context.Background(), []string{"list"}, evalGetenv(nil), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no dead jobs") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRedriveListJSONEmpty(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := runRedrive(context.Background(), []string{"list", "-output=json"}, evalGetenv(nil), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "[]" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRedriveRunAllNoDeadJobs(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := runRedrive(context.Background(),
		[]string{"run", "-assignment=asg_evaluation_local", "-all"},
		evalGetenv(nil), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "redriven 0/0") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRedriveRunRequiresAssignment(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := runRedrive(context.Background(), []string{"run"}, evalGetenv(nil), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--assignment is required") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRedriveRunExclusiveTarget(t *testing.T) {
	t.Parallel()
	cases := [][]string{
		{"run", "-assignment=asg_evaluation_local"},
		{"run", "-assignment=asg_evaluation_local", "-job=x", "-all"},
	}
	for _, args := range cases {
		var stdout, stderr bytes.Buffer
		code := runRedrive(context.Background(), args, evalGetenv(nil), &stdout, &stderr)
		if code != 2 {
			t.Fatalf("args %v: code = %d, stderr = %q", args, code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "exactly one of --job or --all") {
			t.Fatalf("args %v: stderr = %q", args, stderr.String())
		}
	}
}

func TestRedriveRunUnknownJobFails(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := runRedrive(context.Background(),
		[]string{"run", "-assignment=asg_evaluation_local", "-job=does-not-exist"},
		evalGetenv(nil), &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "redriven 0/1") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRedriveListUnknownAssignment(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := runRedrive(context.Background(),
		[]string{"list", "-assignment=nope"}, evalGetenv(nil), &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "unknown assignment") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRedriveUnknownSubcommand(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := runRedrive(context.Background(), []string{"bogus"}, evalGetenv(nil), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
}
