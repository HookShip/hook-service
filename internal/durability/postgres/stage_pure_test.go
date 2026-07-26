package postgres

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

// These tests cover the pure, database-free decision logic of the decomposed
// object-stage modules: staging-intent preparation, identity verification, and
// the claim/supersede/promotion/acceptance input validation and lease
// construction.

func validStage() ObjectPayloadStage {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return ObjectPayloadStage{
		AssignmentID:     "asg",
		LedgerID:         "led",
		PayloadID:        "pay",
		KeyReference:     "kref",
		KeyVersion:       "kver",
		Nonce:            make([]byte, 12),
		CiphertextSHA256: SHA256([]byte("ciphertext")),
		CiphertextSize:   10,
		ExpiresAt:        now.Add(MinimumRetryHorizon + time.Hour),
		StagedAt:         now,
	}
}

func TestPrepareStageIntent(t *testing.T) {
	stage := validStage()
	staging, final, err := prepareStageIntent(&stage)
	if err != nil {
		t.Fatalf("valid stage rejected: %v", err)
	}
	wantStaging, wantFinal, _ := ObjectKeys("asg", "pay")
	if staging != wantStaging || final != wantFinal {
		t.Fatalf("keys = %s/%s, want %s/%s", staging, final, wantStaging, wantFinal)
	}
	if stage.StagedAt.Location() != time.UTC {
		t.Fatal("StagedAt not normalized to UTC")
	}

	// Expiry under the minimum horizon is rejected.
	tooSoon := validStage()
	tooSoon.ExpiresAt = tooSoon.StagedAt.Add(time.Hour)
	if _, _, err := prepareStageIntent(&tooSoon); err == nil {
		t.Fatal("expiry under the horizon must be rejected")
	}

	// Empty assignment is rejected.
	bad := validStage()
	bad.AssignmentID = ""
	if _, _, err := prepareStageIntent(&bad); err == nil {
		t.Fatal("empty assignment must be rejected")
	}
}

func TestVerifyStagedIdentity(t *testing.T) {
	stage := validStage()
	staging, final, _ := ObjectKeys("asg", "pay")
	matching := ObjectPayloadStageRecord{
		PayloadID:        stage.PayloadID,
		KeyReference:     stage.KeyReference,
		KeyVersion:       stage.KeyVersion,
		Nonce:            append([]byte(nil), stage.Nonce...),
		CiphertextSHA256: stage.CiphertextSHA256,
		CiphertextSize:   stage.CiphertextSize,
		StagingObjectKey: staging,
		FinalObjectKey:   final,
		ExpiresAt:        stage.ExpiresAt,
		StagedAt:         stage.StagedAt,
		State:            "staging",
	}
	if err := verifyStagedIdentity(matching, stage, staging, final); err != nil {
		t.Fatalf("matching identity rejected: %v", err)
	}

	conflict := matching
	conflict.CiphertextSize = 999
	if err := verifyStagedIdentity(conflict, stage, staging, final); !errors.Is(err, ErrPayloadConflict) {
		t.Fatalf("mismatched size = %v, want ErrPayloadConflict", err)
	}

	reclaimed := matching
	reclaimed.State = "orphaned"
	if err := verifyStagedIdentity(reclaimed, stage, staging, final); !errors.Is(err, ErrPayloadUnavailable) {
		t.Fatalf("orphaned state = %v, want ErrPayloadUnavailable", err)
	}
}

func TestValidateClaimInput(t *testing.T) {
	micros, err := validateClaimInput("asg", "led", "own", "tok", 30*time.Second)
	if err != nil || micros != 30_000_000 {
		t.Fatalf("valid claim = %d, %v", micros, err)
	}
	if _, err := validateClaimInput("", "led", "own", "tok", time.Second); err == nil {
		t.Fatal("empty assignment must be rejected")
	}
	if _, err := validateClaimInput("asg", "led", "own", "tok", 0); err == nil {
		t.Fatal("zero lease must be rejected")
	}
}

func TestValidateSupersedeInput(t *testing.T) {
	if _, err := validateSupersedeInput(validStage(), "own", "tok", 30*time.Second); err != nil {
		t.Fatalf("valid supersede rejected: %v", err)
	}
	shortNonce := validStage()
	shortNonce.Nonce = make([]byte, 4)
	if _, err := validateSupersedeInput(shortNonce, "own", "tok", time.Second); err == nil {
		t.Fatal("short nonce must be rejected")
	}
	badSize := validStage()
	badSize.CiphertextSize = 0
	if _, err := validateSupersedeInput(badSize, "own", "tok", time.Second); err == nil {
		t.Fatal("non-positive size must be rejected")
	}
}

func TestValidatePromotion(t *testing.T) {
	if err := validatePromotion(ObjectPromotion{ProviderStagingVersion: "s", ProviderFinalVersion: "f", Mode: "create_only"}); err != nil {
		t.Fatalf("create_only rejected: %v", err)
	}
	if err := validatePromotion(ObjectPromotion{ProviderStagingVersion: "s", ProviderFinalVersion: "f", Mode: "create_only", ExpectedFinalVersion: "x"}); err == nil {
		t.Fatal("expected-final-version with create_only must be rejected")
	}
	if err := validatePromotion(ObjectPromotion{ProviderStagingVersion: "s", ProviderFinalVersion: "f", Mode: "match_version"}); err == nil {
		t.Fatal("match_version without expected version must be rejected")
	}
	if err := validatePromotion(ObjectPromotion{ProviderStagingVersion: "s", ProviderFinalVersion: "f", Mode: "match_version", ExpectedFinalVersion: "v"}); err != nil {
		t.Fatalf("match_version with expected version rejected: %v", err)
	}
	if err := validatePromotion(ObjectPromotion{ProviderStagingVersion: "s", ProviderFinalVersion: "f", Mode: "bogus"}); err == nil {
		t.Fatal("unknown mode must be rejected")
	}
}

func TestValidateAcceptance(t *testing.T) {
	now := time.Now().UTC()
	acc := ObjectAcceptance{
		AcceptedAt:          now,
		IdempotencyExpires:  now.Add(MinimumRetryHorizon + time.Hour),
		DispatchMaxAttempts: 3,
	}
	acceptedAt, expiresAt, tombstone, err := validateAcceptance(&acc)
	if err != nil {
		t.Fatalf("valid acceptance rejected: %v", err)
	}
	if !acc.DispatchRunAt.Equal(acceptedAt) {
		t.Fatal("DispatchRunAt should default to acceptedAt")
	}
	if !tombstone.After(expiresAt) {
		t.Fatal("tombstone must be after expiry")
	}

	bad := acc
	bad.DispatchMaxAttempts = 0
	if _, _, _, err := validateAcceptance(&bad); err == nil {
		t.Fatal("zero dispatch max attempts must be rejected")
	}
}

func TestNewSupersedeLease(t *testing.T) {
	identity := validStage()
	expires := time.Now().UTC().Add(time.Minute)
	lease := newSupersedeLease(identity, "swa_x", 3, "sk", "fk", "own", "tok", expires)
	if lease.StageAttemptID != "swa_x" || lease.Attempt != 3 || lease.Owner != "own" || lease.Token != "tok" {
		t.Fatalf("lease fields = %+v", lease)
	}
	if !bytes.Equal(lease.Nonce, identity.Nonce) || lease.CiphertextSHA256 != identity.CiphertextSHA256 {
		t.Fatal("lease crypto identity mismatch")
	}
	if !lease.ExpiresAt.Equal(expires) {
		t.Fatal("lease expiry mismatch")
	}
}

func TestStageAttemptKeys(t *testing.T) {
	id, staging, final, err := stageAttemptKeys("asg", "pay", 2)
	if err != nil {
		t.Fatalf("stageAttemptKeys: %v", err)
	}
	if id != stageWriteAttemptID("asg", "pay", 2) {
		t.Fatal("attempt id must match the deterministic derivation")
	}
	wantStaging, wantFinal, _ := ObjectAttemptKeys("asg", "pay", 2)
	if staging != wantStaging || final != wantFinal {
		t.Fatalf("keys = %s/%s, want %s/%s", staging, final, wantStaging, wantFinal)
	}
	if _, _, _, err := stageAttemptKeys("asg", "pay", 0); err == nil {
		t.Fatal("non-positive attempt must be rejected")
	}
}
