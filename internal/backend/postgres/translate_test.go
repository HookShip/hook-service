package postgres

import (
	"errors"
	"fmt"
	"testing"

	durable "github.com/hookship/hook-service/internal/durability/postgres"
	"github.com/hookship/hook-service/internal/runtime"
)

// allDurableSentinels is every durability sentinel the backend can observe. The
// tests below assert an explicit mapping for each so a newly added sentinel can
// never silently fall through to an unclassified error.
var allDurableSentinels = []error{
	durable.ErrNotFound,
	durable.ErrLeaseLost,
	durable.ErrNotPending,
	durable.ErrPayloadConflict,
	durable.ErrSnapshotConflict,
	durable.ErrPayloadUnavailable,
}

func TestTranslateError(t *testing.T) {
	cases := []struct {
		name string
		in   error
		want error
	}{
		{"nil", nil, nil},
		{"not-found", durable.ErrNotFound, runtime.ErrNotFound},
		{"lease-lost", durable.ErrLeaseLost, runtime.ErrLeaseLost},
		{"not-pending", durable.ErrNotPending, runtime.ErrUnavailable},
		{"payload-conflict", durable.ErrPayloadConflict, runtime.ErrConflict},
		{"snapshot-conflict", durable.ErrSnapshotConflict, runtime.ErrConflict},
		{"payload-unavailable", durable.ErrPayloadUnavailable, runtime.ErrPayloadObjectNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := translateError(c.in); got != c.want {
				t.Fatalf("translateError(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestTranslateAcceptanceError(t *testing.T) {
	cases := []struct {
		name string
		in   error
		want error
	}{
		{"nil", nil, nil},
		{"not-pending", durable.ErrNotPending, runtime.ErrUnavailable},
		{"not-found", durable.ErrNotFound, runtime.ErrUnavailable},
		{"payload-conflict", durable.ErrPayloadConflict, runtime.ErrUnavailable},
		{"payload-unavailable", durable.ErrPayloadUnavailable, runtime.ErrUnavailable},
		{"snapshot-conflict", durable.ErrSnapshotConflict, runtime.ErrUnavailable},
		{"lease-lost", durable.ErrLeaseLost, runtime.ErrUnavailable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := translateAcceptanceError(c.in); got != c.want {
				t.Fatalf("translateAcceptanceError(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// TestTranslateErrorEverySentinelIsClassified proves both translators return a
// runtime sentinel (never the raw durability sentinel) for every known
// durability sentinel. This is the exhaustiveness guard.
func TestTranslateErrorEverySentinelIsClassified(t *testing.T) {
	for _, sentinel := range allDurableSentinels {
		if got := translateError(sentinel); got == sentinel {
			t.Errorf("translateError left %v unclassified", sentinel)
		}
		if got := translateAcceptanceError(sentinel); got != runtime.ErrUnavailable {
			t.Errorf("translateAcceptanceError(%v) = %v, want ErrUnavailable", sentinel, got)
		}
	}
}

// TestTranslateErrorWrappedSentinel proves translation follows wrapped errors,
// which is how the durability layer returns most sentinels.
func TestTranslateErrorWrappedSentinel(t *testing.T) {
	wrapped := fmt.Errorf("scan row: %w", durable.ErrNotFound)
	if got := translateError(wrapped); got != runtime.ErrNotFound {
		t.Fatalf("translateError(wrapped ErrNotFound) = %v, want ErrNotFound", got)
	}
	if got := translateAcceptanceError(wrapped); got != runtime.ErrUnavailable {
		t.Fatalf("translateAcceptanceError(wrapped ErrNotFound) = %v, want ErrUnavailable", got)
	}
}

// TestTranslateErrorPassthrough proves an unrelated error is returned unchanged
// so infrastructure failures are not masked as domain sentinels.
func TestTranslateErrorPassthrough(t *testing.T) {
	other := errors.New("connection reset by peer")
	if got := translateError(other); got != other {
		t.Fatalf("translateError(other) = %v, want the original error", got)
	}
	if got := translateAcceptanceError(other); got != other {
		t.Fatalf("translateAcceptanceError(other) = %v, want the original error", got)
	}
}
