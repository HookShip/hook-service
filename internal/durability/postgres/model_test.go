package postgres

import (
	"testing"
)

func TestRequestFingerprintBindsStableRequest(t *testing.T) {
	body := SHA256([]byte("ciphertext is not used here; this is the raw-body hash"))
	headers := StableHeaders{
		EventID:     "evt_01",
		EventType:   "order.created",
		Route:       "orders.primary",
		ContentType: "application/json",
	}
	first := RequestFingerprint(body, headers)
	if first != RequestFingerprint(body, headers) {
		t.Fatal("request fingerprint is not deterministic")
	}
	headers.Route = "orders.secondary"
	if first == RequestFingerprint(body, headers) {
		t.Fatal("request fingerprint did not bind the route")
	}
	if first == RequestFingerprint(SHA256([]byte("different")), StableHeaders{
		EventID:     "evt_01",
		EventType:   "order.created",
		Route:       "orders.primary",
		ContentType: "application/json",
	}) {
		t.Fatal("request fingerprint did not bind the body hash")
	}
}

func TestObjectKeysAreDeterministicAndScoped(t *testing.T) {
	staging, final, err := ObjectKeys("asg_test_01", "pay_test_01")
	if err != nil {
		t.Fatal(err)
	}
	repeatedStaging, repeatedFinal, err := ObjectKeys("asg_test_01", "pay_test_01")
	if err != nil {
		t.Fatal(err)
	}
	if staging != repeatedStaging || final != repeatedFinal {
		t.Fatal("object identities are not deterministic")
	}
	otherStaging, otherFinal, err := ObjectKeys("asg_test_02", "pay_test_01")
	if err != nil {
		t.Fatal(err)
	}
	if staging == otherStaging || final == otherFinal {
		t.Fatal("object identities are not assignment scoped")
	}
	if staging == final {
		t.Fatal("staging and final object identities must differ")
	}
}

func TestMigrationManifest(t *testing.T) {
	if err := ValidateMigrationManifest(); err != nil {
		t.Fatal(err)
	}
}
