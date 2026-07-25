package worker

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/hookship/hook-service/internal/runtime"
)

// TestLogSettlementSurfacesFailures proves the single settlement helper never
// silently drops a settlement error: a generic failure is logged at warn with
// its op and work id, a fenced lease is logged at debug (expected), and a nil
// error logs nothing.
func TestLogSettlementSurfacesFailures(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	w := &Worker{options: Options{Logger: logger}}

	w.logSettlement("complete", "job_1", nil)
	if buf.Len() != 0 {
		t.Fatalf("nil error must not log, got %q", buf.String())
	}

	w.logSettlement("complete", "job_2", runtime.ErrLeaseLost)
	if out := buf.String(); !strings.Contains(out, "level=DEBUG") || !strings.Contains(out, "lease lost") {
		t.Fatalf("fenced lease must log at debug, got %q", out)
	}
	buf.Reset()

	w.logSettlement("retry", "job_3", errors.New("database unavailable"))
	out := buf.String()
	for _, want := range []string{"level=WARN", "settlement failed", "database unavailable", "op=retry", "work_id=job_3"} {
		if !strings.Contains(out, want) {
			t.Fatalf("warn log missing %q, got %q", want, out)
		}
	}
}
