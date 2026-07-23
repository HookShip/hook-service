package idempotency

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// goldenVectors lock the exact fingerprint bytes for the v1 scheme. These
// digests are a wire contract: if any of them change, the idempotency identity
// has changed and existing durable ledgers would no longer match. They were
// captured from the reviewed pre-extraction implementation.
var goldenVectors = []struct {
	name    string
	body    [Size]byte
	headers Headers
	want    string
}{
	{
		name:    "empty-headers",
		body:    sha256.Sum256([]byte("the-exact-body")),
		headers: Headers{},
		want:    "33b061195a801fe853bba57895163ad7757acd2623223a742caff58a59c80f5e",
	},
	{
		name:    "typical",
		body:    sha256.Sum256([]byte("the-exact-body")),
		headers: Headers{EventID: "evt_1", EventType: "user.created", Route: "default", ContentType: "application/json"},
		want:    "803b0f660cb85d29c62cf6dd2404390e3467c938bdfcebfce79f2f9b50768060",
	},
	{
		name:    "unicode",
		body:    sha256.Sum256([]byte("the-exact-body")),
		headers: Headers{EventID: "évént", EventType: "t", Route: "r/ç", ContentType: "text/plain; charset=utf-8"},
		want:    "330c3ea6db3bebe1e333a605758d27ead5b9ea1dae9e1cdd42606565bc314431",
	},
	{
		name:    "zero-body-empty-headers",
		body:    [Size]byte{},
		headers: Headers{},
		want:    "89c57ca3017279116344d61dbd967ede1661504ae90cc2556192cfb28be9720c",
	},
}

func TestFingerprintGoldenVectors(t *testing.T) {
	for _, vector := range goldenVectors {
		fp := Fingerprint(vector.body, vector.headers)
		got := hex.EncodeToString(fp[:])
		if got != vector.want {
			t.Errorf("%s: fingerprint = %s, want %s", vector.name, got, vector.want)
		}
	}
}

// TestFingerprintFieldBoundariesDoNotAlias proves the length-prefix framing
// keeps adjacent header fields from aliasing.
func TestFingerprintFieldBoundariesDoNotAlias(t *testing.T) {
	body := sha256.Sum256([]byte("b"))
	a := Fingerprint(body, Headers{EventID: "ab", EventType: ""})
	b := Fingerprint(body, Headers{EventID: "a", EventType: "b"})
	if a == b {
		t.Fatal("field boundaries aliased: {ab,\"\"} and {a,b} produced the same fingerprint")
	}
}

// TestFingerprintDependsOnBody proves the body hash is bound into the digest.
func TestFingerprintDependsOnBody(t *testing.T) {
	h := Headers{EventID: "evt"}
	if Fingerprint(sha256.Sum256([]byte("x")), h) == Fingerprint(sha256.Sum256([]byte("y")), h) {
		t.Fatal("fingerprint did not depend on the body hash")
	}
}
