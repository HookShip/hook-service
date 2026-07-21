package capabilities_test

import (
	"encoding/json"
	"testing"

	"github.com/hookship/hook-service/internal/capabilities"
)

func TestProfilesAreMachineReadableAndVersioned(t *testing.T) {
	documents := capabilities.Profiles()
	if len(documents) < 4 {
		t.Fatalf("expected at least four profiles, got %d", len(documents))
	}
	for _, document := range documents {
		if document.SchemaVersion != capabilities.SchemaVersion {
			t.Fatalf("profile %s has wrong schema version %q", document.Profile, document.SchemaVersion)
		}
		if _, err := json.Marshal(document); err != nil {
			t.Fatalf("profile %s is not JSON-serializable: %v", document.Profile, err)
		}
	}
}

func TestMemoryNeverClaimsDurableProtocolV1(t *testing.T) {
	document, ok := capabilities.Lookup(capabilities.ProfileMemory)
	if !ok {
		t.Fatal("memory profile missing")
	}
	if document.Properties.Durability != capabilities.DurabilityEphemeral {
		t.Fatalf("memory durability must be ephemeral, got %s", document.Properties.Durability)
	}
	if document.Capabilities.DurableProtocolV1.Supported {
		t.Fatal("memory must never claim durable protocol-v1 acknowledgement")
	}
	if document.Capabilities.AcknowledgementBarrier.Supported {
		t.Fatal("memory must never claim an acknowledgement barrier")
	}
}

func TestPostgresProfilesKeepPostgresSourceOfTruth(t *testing.T) {
	for _, name := range []string{
		capabilities.ProfilePostgres,
		capabilities.ProfilePostgresRedis,
		capabilities.ProfilePostgresKafka,
	} {
		document, ok := capabilities.Lookup(name)
		if !ok {
			t.Fatalf("profile %s missing", name)
		}
		if !document.Capabilities.DurableProtocolV1.Supported {
			t.Fatalf("profile %s must provide durable protocol-v1 acknowledgement", name)
		}
		if document.Properties.DeliverySemantics != capabilities.DeliveryAtLeastOnce {
			t.Fatalf("profile %s must be at-least-once, never exactly-once", name)
		}
	}
}

func TestValidateRejectsInvalidCombinations(t *testing.T) {
	cases := []struct {
		name       string
		selection  capabilities.Selection
		wantReason capabilities.ReasonCode
	}{
		{
			name:       "unknown profile",
			selection:  capabilities.Selection{Profile: "bogus"},
			wantReason: capabilities.ReasonUnknownProfile,
		},
		{
			name:       "memory without evaluation mode",
			selection:  capabilities.Selection{Profile: capabilities.ProfileMemory, EvaluationMode: false},
			wantReason: capabilities.ReasonEvaluationRequired,
		},
		{
			name:       "memory in production",
			selection:  capabilities.Selection{Profile: capabilities.ProfileMemory, EvaluationMode: true, Production: true},
			wantReason: capabilities.ReasonProductionForbidden,
		},
		{
			name:       "redis profile without redis",
			selection:  capabilities.Selection{Profile: capabilities.ProfilePostgresRedis},
			wantReason: capabilities.ReasonMissingService,
		},
		{
			name:       "kafka profile without kafka",
			selection:  capabilities.Selection{Profile: capabilities.ProfilePostgresKafka},
			wantReason: capabilities.ReasonMissingService,
		},
		{
			name:       "non-loopback plaintext",
			selection:  capabilities.Selection{Profile: capabilities.ProfilePostgres, PlaintextListen: true, LoopbackOnly: false},
			wantReason: capabilities.ReasonPlaintextExposed,
		},
		{
			name:       "exactly once requested",
			selection:  capabilities.Selection{Profile: capabilities.ProfilePostgres, RequireExactlyOnce: true},
			wantReason: capabilities.ReasonExactlyOnce,
		},
		{
			name:       "kafka native ledger unsupported",
			selection:  capabilities.Selection{Profile: capabilities.ProfileKafkaNativeLedger, KafkaConfigured: true},
			wantReason: capabilities.ReasonNativeLedgerUnbuilt,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := capabilities.Validate(tc.selection)
			if result.OK {
				t.Fatalf("expected validation failure for %s", tc.name)
			}
			if !hasReason(result.Errors, tc.wantReason) {
				t.Fatalf("expected reason %q, got %+v", tc.wantReason, result.Errors)
			}
		})
	}
}

func TestValidateAcceptsSupportedSelections(t *testing.T) {
	valid := []capabilities.Selection{
		{Profile: capabilities.ProfileMemory, EvaluationMode: true, LoopbackOnly: true, PlaintextListen: true},
		{Profile: capabilities.ProfilePostgres, Production: true, PayloadBackend: capabilities.PayloadPostgres},
		{Profile: capabilities.ProfilePostgresRedis, RedisConfigured: true},
		{Profile: capabilities.ProfilePostgresKafka, KafkaConfigured: true},
	}
	for _, selection := range valid {
		result := capabilities.Validate(selection)
		if !result.OK {
			t.Fatalf("expected %s to be valid, got %+v", selection.Profile, result.Errors)
		}
	}
}

func TestValidateAllowsWiredObjectStoreOnDurableProfiles(t *testing.T) {
	for _, profile := range []string{
		capabilities.ProfilePostgres,
		capabilities.ProfilePostgresRedis,
		capabilities.ProfilePostgresKafka,
	} {
		result := capabilities.Validate(capabilities.Selection{
			Profile:         profile,
			Production:      true,
			RedisConfigured: true,
			KafkaConfigured: true,
			PayloadBackend:  capabilities.PayloadObjectStore,
		})
		if !result.OK {
			t.Fatalf("%s: wired object store must be allowed in production, got %+v", profile, result.Errors)
		}
		// The object-store backend is wired now: no "not wired" warning is emitted.
		if warning := findReason(result.Warnings, capabilities.ReasonExperimentalObjectPort); warning != nil {
			t.Fatalf("%s: object store must no longer warn as unwired: %+v", profile, warning)
		}
	}
	// The durable profiles advertise the wired capability in their documents.
	document, _ := capabilities.Lookup(capabilities.ProfilePostgres)
	if !document.Capabilities.ObjectStorePayloadBackend.Supported {
		t.Fatal("postgres profile must advertise a supported object-store payload backend")
	}
	if document.Capabilities.ObjectStorePayloadBackend.ReasonCode != capabilities.ReasonObjectStorePayload {
		t.Fatalf("object-store capability reason = %q", document.Capabilities.ObjectStorePayloadBackend.ReasonCode)
	}
}

func TestValidateBlocksObjectStoreOnEphemeralProfile(t *testing.T) {
	result := capabilities.Validate(capabilities.Selection{
		Profile:        capabilities.ProfileMemory,
		EvaluationMode: true,
		PayloadBackend: capabilities.PayloadObjectStore,
	})
	if result.OK {
		t.Fatal("object store must require a durable profile")
	}
	if !hasReason(result.Errors, capabilities.ReasonObjectStoreDurable) {
		t.Fatalf("expected object_store_requires_durable_profile error, got %+v", result.Errors)
	}
	// The evaluation profile advertises the object-store backend as unsupported.
	document, _ := capabilities.Lookup(capabilities.ProfileMemory)
	if document.Capabilities.ObjectStorePayloadBackend.Supported {
		t.Fatal("memory profile must not advertise a supported object-store payload backend")
	}
}

func hasReason(findings []capabilities.Finding, reason capabilities.ReasonCode) bool {
	return findReason(findings, reason) != nil
}

func findReason(findings []capabilities.Finding, reason capabilities.ReasonCode) *capabilities.Finding {
	for i := range findings {
		if findings[i].ReasonCode == reason {
			return &findings[i]
		}
	}
	return nil
}
