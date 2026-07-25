package postgres

import (
	"testing"

	"github.com/hookship/hook-service/internal/runtime"
)

// TestMapDispatchState covers the pure durable->protocol dispatch-state
// translation, including the fail-safe default.
func TestMapDispatchState(t *testing.T) {
	cases := map[string]string{
		"ready":     "queued",
		"retry":     "queued",
		"leased":    "claimed",
		"succeeded": "completed",
		"dead":      "failed",
		"unknown":   "queued", // fail-safe default
		"":          "queued",
	}
	for in, want := range cases {
		if got := mapDispatchState(in); got != want {
			t.Errorf("mapDispatchState(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestProviderVersion covers the promotion provenance token: a store-reported
// version is used verbatim, and an unversioned store falls back to a
// deterministic checksum-derived token so the ledger always records a non-empty
// version.
func TestProviderVersion(t *testing.T) {
	checksum := runtime.SHA256([]byte("ciphertext"))

	if got := providerVersion("etag-123", checksum); got != "etag-123" {
		t.Fatalf("provider version = %q, want the store version", got)
	}
	got := providerVersion("", checksum)
	want := "sha256-" + checksum.String()
	if got != want {
		t.Fatalf("fallback version = %q, want %q", got, want)
	}
	// Deterministic for the same checksum.
	if providerVersion("", checksum) != got {
		t.Fatal("fallback version must be deterministic")
	}
}

// TestPayloadBackendName covers the payload-store status label without a
// database or object store.
func TestPayloadBackendName(t *testing.T) {
	if got := (&Backend{}).PayloadBackendName(); got != "postgres" {
		t.Fatalf("default payload backend = %q, want postgres", got)
	}
}
