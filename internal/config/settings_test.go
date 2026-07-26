package config

import (
	"testing"
)

func TestSettingsAccessors(t *testing.T) {
	if (RuntimeConfig{Backend: "postgres"}).Selected() != true {
		t.Fatal("Selected should be true with a backend")
	}
	if (RuntimeConfig{Backend: "  "}).Selected() != false {
		t.Fatal("Selected should be false for blank backend")
	}
	if (RuntimeConfig{EvaluationMode: true}).Production() {
		t.Fatal("evaluation mode is not production")
	}
	if !(RuntimeConfig{}).Production() {
		t.Fatal("non-evaluation is production")
	}

	// Loopback delivery: evaluation mode OR explicit opt-in.
	if !(RuntimeConfig{EvaluationMode: true}).AllowLoopbackDelivery() {
		t.Fatal("evaluation should allow loopback")
	}
	if !(RuntimeConfig{Transport: TransportSettings{AllowLoopback: true}}).AllowLoopbackDelivery() {
		t.Fatal("explicit opt-in should allow loopback")
	}
	if (RuntimeConfig{}).AllowLoopbackDelivery() {
		t.Fatal("default must not allow loopback")
	}

	if !(RuntimeConfig{Transport: TransportSettings{AllowPrivate: true}}).AllowPrivateDelivery() {
		t.Fatal("explicit opt-in should allow private")
	}
	if (RuntimeConfig{}).AllowPrivateDelivery() {
		t.Fatal("default must not allow private")
	}

	// Plaintext delivery: evaluation mode OR explicit opt-in.
	if !(RuntimeConfig{EvaluationMode: true}).AllowPlaintextDelivery() {
		t.Fatal("evaluation should allow plaintext")
	}
	if !(RuntimeConfig{Transport: TransportSettings{AllowPlaintext: true}}).AllowPlaintextDelivery() {
		t.Fatal("explicit opt-in should allow plaintext")
	}

	// HasTLS requires both files.
	if (RuntimeConfig{Transport: TransportSettings{TLSCertFile: "c"}}).HasTLS() {
		t.Fatal("cert only is not a complete TLS pair")
	}
	if !(RuntimeConfig{Transport: TransportSettings{TLSCertFile: "c", TLSKeyFile: "k"}}).HasTLS() {
		t.Fatal("complete pair should report HasTLS")
	}

	if !(RuntimeConfig{PayloadBackend: "object-store"}).UsesObjectStore() {
		t.Fatal("object-store payload backend should report UsesObjectStore")
	}
	if (RuntimeConfig{}).UsesObjectStore() {
		t.Fatal("default must not use object store")
	}
	if !(RuntimeConfig{KeyProvider: "aws-kms"}).UsesKMS() {
		t.Fatal("aws-kms should report UsesKMS")
	}
	if (RuntimeConfig{}).UsesKMS() {
		t.Fatal("default must not use KMS")
	}
}

func TestObjectStoreSettingsConfigured(t *testing.T) {
	complete := ObjectStoreSettings{
		Endpoint: "e", Region: "r", Bucket: "b", AccessKeyID: "a", SecretAccessKey: "s",
	}
	if !complete.Configured() {
		t.Fatal("complete object-store settings should be Configured")
	}
	missing := complete
	missing.Bucket = ""
	if missing.Configured() {
		t.Fatal("missing bucket must not be Configured")
	}
}

func TestPayloadKeyAccessor(t *testing.T) {
	if (RuntimeConfig{}).PayloadKey() != nil {
		t.Fatal("no key should return nil")
	}
	key := make([]byte, 32)
	if got := (RuntimeConfig{payloadKey: key}).PayloadKey(); len(got) != 32 {
		t.Fatalf("payload key length = %d", len(got))
	}
}
