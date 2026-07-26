package postgres

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// fakeRow is a rowScanner double that assigns canned values into scan
// destinations by reflection, so the pure scan helpers can be exercised without
// a database.
type fakeRow struct {
	values []any
	err    error
}

func (f fakeRow) Scan(dest ...any) error {
	if f.err != nil {
		return f.err
	}
	if len(dest) != len(f.values) {
		return errors.New("scan arity mismatch")
	}
	for i, d := range dest {
		reflect.ValueOf(d).Elem().Set(reflect.ValueOf(f.values[i]))
	}
	return nil
}

func TestScanJob(t *testing.T) {
	runAt := time.Unix(1700000000, 0).UTC()
	row := fakeRow{values: []any{
		"job_1", "asg", "dispatch", "ing", "pay", "del", "snap", "ready", runAt, 2, 5, "retryable_5xx",
	}}
	job, err := scanJob(row)
	if err != nil {
		t.Fatalf("scanJob: %v", err)
	}
	if job.JobID != "job_1" || job.Kind != "dispatch" || job.State != "ready" {
		t.Fatalf("scanned job = %+v", job)
	}
	if job.Attempts != 2 || job.MaxAttempts != 5 || job.LastErrorCode != "retryable_5xx" {
		t.Fatalf("scanned counters = %+v", job)
	}
	if !job.RunAt.Equal(runAt) {
		t.Fatalf("run at = %v, want %v", job.RunAt, runAt)
	}

	if _, err := scanJob(fakeRow{err: errors.New("boom")}); err == nil {
		t.Fatal("scanJob must propagate a scan error")
	}
}

func TestScanObjectPayloadStage(t *testing.T) {
	checksum := SHA256([]byte("ciphertext"))
	staged := time.Unix(1700000000, 0).UTC()
	expires := staged.Add(48 * time.Hour)
	base := []any{
		"asg", "led", "pay", "staging", "kref", "kver",
		[]byte("nonce-bytes--"), checksum[:], int64(42),
		"swa_1", 1, "staging-key", "final-key", expires, staged,
		pgtype.Timestamptz{},
	}
	stage, err := scanObjectPayloadStage(fakeRow{values: base})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if stage.PayloadID != "pay" || stage.State != "staging" || stage.CiphertextSize != 42 {
		t.Fatalf("scanned stage = %+v", stage)
	}
	if stage.CiphertextSHA256 != checksum {
		t.Fatal("checksum not copied")
	}
	if stage.FinalizedAt != nil {
		t.Fatal("finalizedAt should be nil when invalid")
	}

	// A valid finalized timestamp is surfaced as a pointer.
	withFinal := append([]any(nil), base...)
	withFinal[15] = pgtype.Timestamptz{Time: staged, Valid: true}
	stage2, err := scanObjectPayloadStage(fakeRow{values: withFinal})
	if err != nil {
		t.Fatal(err)
	}
	if stage2.FinalizedAt == nil || !stage2.FinalizedAt.Equal(staged) {
		t.Fatal("finalizedAt should be set")
	}

	// A wrong-length checksum is rejected.
	badChecksum := append([]any(nil), base...)
	badChecksum[7] = []byte{0x01, 0x02}
	if _, err := scanObjectPayloadStage(fakeRow{values: badChecksum}); err == nil {
		t.Fatal("short checksum must be rejected")
	}
}

func TestHashHelpers(t *testing.T) {
	h := SHA256([]byte("payload"))
	if h.String() == "" || len(h.String()) != 64 {
		t.Fatalf("hash string = %q", h.String())
	}
	parsed, err := ParseHash(h.String())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed != h {
		t.Fatal("round-trip mismatch")
	}
	if _, err := ParseHash("not-hex"); err == nil {
		t.Fatal("invalid hash must be rejected")
	}
	if _, err := ParseHash("ABCDEF"); err == nil {
		t.Fatal("uppercase/short hash must be rejected")
	}
}

func TestValidateStableHeaders(t *testing.T) {
	ok := StableHeaders{EventID: "e", EventType: "t", Route: "r", ContentType: "c"}
	if err := validateStableHeaders(ok); err != nil {
		t.Fatalf("valid headers rejected: %v", err)
	}
	bad := StableHeaders{EventID: "", EventType: "t", Route: "r", ContentType: "c"}
	if err := validateStableHeaders(bad); err == nil {
		t.Fatal("empty event id must be rejected")
	}
}

func TestLeaseKeyConstructors(t *testing.T) {
	jk := jobLeaseKey(JobLease{AssignmentID: "asg", JobID: "job", Owner: "own", Token: "tok", Attempt: 3})
	if jk.assignmentID != "asg" || jk.id != "job" || jk.owner != "own" || jk.token != "tok" || jk.attempt != 3 {
		t.Fatalf("job lease key = %+v", jk)
	}
	ok := outboxLeaseKey(OutboxLease{AssignmentID: "asg", OutboxID: "obx", Owner: "own", Token: "tok", Attempt: 4})
	if ok.id != "obx" || ok.attempt != 4 {
		t.Fatalf("outbox lease key = %+v", ok)
	}
}

func TestNullIfEmpty(t *testing.T) {
	if v := nullIfEmpty(""); v != nil {
		t.Fatalf("empty = %v, want nil", v)
	}
	if v := nullIfEmpty("x"); v != "x" {
		t.Fatalf("non-empty = %v, want x", v)
	}
}

func TestParseMigrationVersion(t *testing.T) {
	v, err := parseMigrationVersion("000007_add_thing.up.sql")
	if err != nil || v != 7 {
		t.Fatalf("parse = %d, %v", v, err)
	}
	for _, bad := range []string{"7_x.up.sql", "abcdef_x.up.sql", "000000_x.up.sql", "short"} {
		if _, err := parseMigrationVersion(bad); err == nil {
			t.Fatalf("%q must be rejected", bad)
		}
	}
}
