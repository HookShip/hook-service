package runtime_test

import (
	"crypto/sha256"
	"testing"

	durable "github.com/hookship/hook-service/internal/durability/postgres"
	"github.com/hookship/hook-service/internal/idempotency"
	"github.com/hookship/hook-service/internal/runtime"
)

// TestRequestFingerprintCrossProfileByteIdentity is the cross-profile guard for
// the idempotency identity: the runtime wrapper, the PostgreSQL durability
// wrapper, and the shared idempotency package must all produce the exact same
// fingerprint bytes for the same input. If they ever diverge, a duplicate
// accepted through one profile would be treated as new by another.
func TestRequestFingerprintCrossProfileByteIdentity(t *testing.T) {
	cases := []struct {
		name    string
		body    [32]byte
		headers idempotency.Headers
	}{
		{"empty", sha256.Sum256([]byte("the-exact-body")), idempotency.Headers{}},
		{"typical", sha256.Sum256([]byte("the-exact-body")), idempotency.Headers{
			EventID: "evt_1", EventType: "user.created", Route: "default", ContentType: "application/json",
		}},
		{"unicode", sha256.Sum256([]byte("payload")), idempotency.Headers{
			EventID: "évént", EventType: "t", Route: "r/ç", ContentType: "text/plain; charset=utf-8",
		}},
		{"zero-body", [32]byte{}, idempotency.Headers{EventID: "a", Route: "b"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			shared := idempotency.Fingerprint(c.body, c.headers)
			runtimeFP := runtime.RequestFingerprint(runtime.Hash(c.body), c.headers)
			durableFP := durable.RequestFingerprint(durable.Hash(c.body), c.headers)

			if [32]byte(runtimeFP) != shared {
				t.Errorf("runtime fingerprint %x != shared %x", runtimeFP, shared)
			}
			if [32]byte(durableFP) != shared {
				t.Errorf("durable fingerprint %x != shared %x", durableFP, shared)
			}
			if runtimeFP != runtime.Hash(durableFP) {
				t.Errorf("runtime fingerprint %x != durable fingerprint %x", runtimeFP, durableFP)
			}
		})
	}
}

// TestStableHeadersIsSharedType proves runtime.StableHeaders and the durability
// StableHeaders are the same underlying idempotency.Headers type, so no lossy
// conversion sits between the ingest path and the durable ledger.
func TestStableHeadersIsSharedType(t *testing.T) {
	shared := idempotency.Headers{EventID: "evt", EventType: "type", Route: "route", ContentType: "ct"}
	var asRuntime runtime.StableHeaders = shared
	var asDurable durable.StableHeaders = shared
	if asRuntime != asDurable {
		t.Fatal("runtime and durable StableHeaders are not the same value")
	}
}
