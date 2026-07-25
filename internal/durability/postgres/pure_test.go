package postgres

import (
	"strings"
	"testing"
	"time"
)

// These tests exercise the pure, database-free decision logic of the durability
// package so it can be verified without a running PostgreSQL: key derivation,
// text/crypto validation, time normalization, and duration bounds.

func TestStageWriteAttemptIDDeterministicAndScoped(t *testing.T) {
	id1 := stageWriteAttemptID("asg_a", "pay_1", 1)
	if id1 != stageWriteAttemptID("asg_a", "pay_1", 1) {
		t.Fatal("stage write attempt id must be deterministic")
	}
	if !strings.HasPrefix(id1, "swa_") {
		t.Fatalf("attempt id missing prefix: %s", id1)
	}
	// Each of assignment, payload, and attempt must change the identity.
	distinct := map[string]bool{
		stageWriteAttemptID("asg_a", "pay_1", 1): true,
		stageWriteAttemptID("asg_b", "pay_1", 1): true,
		stageWriteAttemptID("asg_a", "pay_2", 1): true,
		stageWriteAttemptID("asg_a", "pay_1", 2): true,
	}
	if len(distinct) != 4 {
		t.Fatalf("attempt id should vary with each input, got %d distinct", len(distinct))
	}
}

func TestObjectAttemptKeysScopeAndAttempt(t *testing.T) {
	s1, f1, err := ObjectAttemptKeys("asg", "pay", 1)
	if err != nil {
		t.Fatal(err)
	}
	if s1 == f1 {
		t.Fatal("staging and final keys must differ")
	}
	if !strings.Contains(s1, "/attempts/0000000001/staging/") || !strings.Contains(f1, "/attempts/0000000001/final/") {
		t.Fatalf("unexpected key shape: %s %s", s1, f1)
	}
	s2, _, err := ObjectAttemptKeys("asg", "pay", 2)
	if err != nil {
		t.Fatal(err)
	}
	if s2 == s1 {
		t.Fatal("different attempts must produce different keys")
	}
	if _, _, err := ObjectAttemptKeys("asg", "pay", 0); err == nil {
		t.Fatal("non-positive attempt must be rejected")
	}
	if _, _, err := ObjectAttemptKeys("", "pay", 1); err == nil {
		t.Fatal("empty assignment must be rejected")
	}
}

func TestValidateText(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		max     int
		wantErr bool
	}{
		{"ok", "hello", 255, false},
		{"empty", "", 255, true},
		{"leading space", " hello", 255, true},
		{"trailing space", "hello ", 255, true},
		{"too long", strings.Repeat("a", 10), 5, true},
		{"newline", "a\nb", 255, true},
		{"null", "a\x00b", 255, true},
		{"exact max", "abcde", 5, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateText("field", c.value, c.max)
			if (err != nil) != c.wantErr {
				t.Fatalf("validateText(%q) err = %v, wantErr = %v", c.value, err, c.wantErr)
			}
		})
	}
}

func TestNormalizeTimes(t *testing.T) {
	accepted := time.Date(2026, 1, 2, 3, 4, 5, 999, time.FixedZone("x", 3600))

	// Zero expiry defaults to accepted + minimum horizon and is UTC-truncated.
	gotAccepted, gotExpiry, gotTombstone, err := normalizeTimes(accepted, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if gotAccepted.Location() != time.UTC {
		t.Fatalf("accepted not normalized to UTC: %v", gotAccepted.Location())
	}
	if !gotExpiry.Equal(gotAccepted.Add(MinimumRetryHorizon)) {
		t.Fatalf("default expiry = %v, want accepted + %v", gotExpiry, MinimumRetryHorizon)
	}
	if !gotTombstone.Equal(gotExpiry.Add(MinimumTombstoneHorizon)) {
		t.Fatalf("tombstone = %v, want expiry + %v", gotTombstone, MinimumTombstoneHorizon)
	}

	// A zero accepted time is rejected.
	if _, _, _, err := normalizeTimes(time.Time{}, time.Time{}); err == nil {
		t.Fatal("zero accepted time must be rejected")
	}

	// An expiry below the minimum horizon is rejected.
	tooSoon := accepted.Add(time.Hour)
	if _, _, _, err := normalizeTimes(accepted, tooSoon); err == nil {
		t.Fatal("expiry under the minimum horizon must be rejected")
	}
}

func TestBoundedDurationMicroseconds(t *testing.T) {
	if _, err := boundedDurationMicroseconds("d", -time.Second, time.Hour, false); err == nil {
		t.Fatal("negative duration must be rejected")
	}
	if _, err := boundedDurationMicroseconds("d", 0, time.Hour, false); err == nil {
		t.Fatal("zero must be rejected when allowZero is false")
	}
	if got, err := boundedDurationMicroseconds("d", 0, time.Hour, true); err != nil || got != 0 {
		t.Fatalf("zero allowed = %d, %v", got, err)
	}
	if _, err := boundedDurationMicroseconds("d", 2*time.Hour, time.Hour, false); err == nil {
		t.Fatal("duration over the maximum must be rejected")
	}
	if got, err := boundedDurationMicroseconds("d", time.Second, time.Hour, false); err != nil || got != 1_000_000 {
		t.Fatalf("1s = %d micros, %v", got, err)
	}
}

func TestValidatePayloadCrypto(t *testing.T) {
	created := time.Now().UTC()
	expires := created.Add(MinimumRetryHorizon)
	validNonce := make([]byte, 12)

	if err := validatePayloadCrypto("kref", "kver", validNonce, 10, expires, created); err != nil {
		t.Fatalf("valid crypto rejected: %v", err)
	}
	if err := validatePayloadCrypto("kref", "kver", make([]byte, 4), 10, expires, created); err == nil {
		t.Fatal("too-short nonce must be rejected")
	}
	if err := validatePayloadCrypto("kref", "kver", validNonce, 0, expires, created); err == nil {
		t.Fatal("non-positive size must be rejected")
	}
	if err := validatePayloadCrypto("", "kver", validNonce, 10, expires, created); err == nil {
		t.Fatal("empty key reference must be rejected")
	}
}
