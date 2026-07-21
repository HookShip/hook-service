package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestBackendDescribeMatchesCanonicalFixtures ensures `hookshipctl backend
// describe <profile>` emits byte-exactly the authoritative fixtures under
// spec/backend-capabilities/v1, so the CLI output and the cross-repo contract
// never drift.
func TestBackendDescribeMatchesCanonicalFixtures(t *testing.T) {
	fixtures := filepath.Join("..", "..", "spec", "backend-capabilities", "v1", "fixtures")
	for _, profile := range []string{
		"memory", "postgres", "postgres-redis", "postgres-kafka", "kafka-native-ledger",
	} {
		profile := profile
		t.Run(profile, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), []string{"backend", "describe", profile}, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("exit=%d stderr=%s", code, stderr.String())
			}
			want, err := os.ReadFile(filepath.Join(fixtures, profile+".json"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(stdout.Bytes(), want) {
				t.Fatalf("backend describe %s output differs from the canonical fixture", profile)
			}
		})
	}
}

func TestBackendListAndValidate(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"backend", "list"}, &stdout, &stderr); code != 0 {
		t.Fatalf("backend list exit=%d stderr=%s", code, stderr.String())
	}
	if !bytes.Contains(stdout.Bytes(), []byte("postgres-kafka")) {
		t.Fatalf("backend list missing profile: %s", stdout.String())
	}
}
