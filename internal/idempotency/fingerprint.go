// Package idempotency defines the canonical, dependency-free request
// fingerprint shared by every HookShip runtime profile and the PostgreSQL
// durability layer. Centralizing it here guarantees the idempotency identity is
// byte-identical across profiles: a fingerprint computed by the ingest handler,
// the memory backend, and the PostgreSQL repository always agree.
//
// The package intentionally imports nothing from runtime, config, or any
// backend, so it can be consumed from either direction without an import cycle.
package idempotency

import (
	"crypto/sha256"
	"encoding/binary"
)

// Size is the length in bytes of a fingerprint digest (SHA-256).
const Size = sha256.Size

// domainSeparator binds the fingerprint to this scheme and version so a digest
// can never collide with an unrelated SHA-256 pre-image. It is part of the wire
// contract: changing it changes every fingerprint and MUST be gated behind a new
// fingerprint version.
const domainSeparator = "hookship-idempotency-fingerprint-v1\x00"

// Headers are the stable, signed producer identity headers bound into the
// fingerprint. Replay-only fields (timestamp, nonce, signature, credential ID)
// are intentionally excluded so a legitimate retry with fresh replay fields
// still matches.
type Headers struct {
	EventID     string
	EventType   string
	Route       string
	ContentType string
}

// Fingerprint binds the exact body hash and every stable signed header into a
// single canonical digest. Each header value is length-prefixed with a
// big-endian uint32 so distinct field boundaries can never alias (for example
// {"ab",""} and {"a","b"} produce different digests). The byte layout is a
// stable cross-profile contract; changing it breaks idempotency identity and is
// only permitted alongside a new domainSeparator version.
func Fingerprint(bodySHA256 [Size]byte, headers Headers) [Size]byte {
	digest := sha256.New()
	_, _ = digest.Write([]byte(domainSeparator))
	_, _ = digest.Write(bodySHA256[:])
	for _, value := range []string{
		headers.EventID,
		headers.EventType,
		headers.Route,
		headers.ContentType,
	} {
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(value)))
		_, _ = digest.Write(length[:])
		_, _ = digest.Write([]byte(value))
	}
	var result [Size]byte
	copy(result[:], digest.Sum(nil))
	return result
}
