// Package runtime defines the backend-agnostic ports and domain model shared by
// every HookShip native runtime profile. Concrete backends (memory, PostgreSQL,
// and PostgreSQL-derived dispatch relays) implement these ports; the ingest
// handler and delivery worker are written only against them.
//
// The package intentionally imports no concrete backend so that the same
// canonical identifiers, acceptance semantics, and state transitions hold across
// every profile without an import cycle.
package runtime

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"
)

// Clock returns the current time. Production backends use the system clock;
// tests inject deterministic clocks.
type Clock interface {
	Now() time.Time
}

// SystemClock reads the wall clock in UTC.
type SystemClock struct{}

// Now returns the current UTC time truncated to microseconds to match the
// PostgreSQL timestamptz resolution used by the durable profiles.
func (SystemClock) Now() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

// IDSource mints canonical, sortable, prefixed identifiers that are stable
// across profiles. The canonical shape is `<prefix>_<26-char Crockford ULID>`.
type IDSource interface {
	NewID(prefix string) string
}

// Hash is a SHA-256 digest. It mirrors the durability package layout so adapters
// can convert without copying semantics.
type Hash [sha256.Size]byte

// SHA256 hashes the supplied bytes.
func SHA256(value []byte) Hash { return sha256.Sum256(value) }

// String renders the digest as lowercase hexadecimal.
func (h Hash) String() string { return hex.EncodeToString(h[:]) }

// ParseHash decodes a lowercase hexadecimal SHA-256 digest.
func ParseHash(value string) (Hash, error) {
	var result Hash
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return result, errors.New("hash must be 64 lowercase hexadecimal characters")
	}
	if value != strings.ToLower(value) {
		return result, errors.New("hash must be lowercase hexadecimal")
	}
	copy(result[:], decoded)
	return result, nil
}

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// ULIDSource generates monotonic ULID-style identifiers. It is safe for
// concurrent use and never blocks on the system entropy source per call after
// initialization.
type ULIDSource struct {
	clock Clock

	mu       sync.Mutex
	lastMS   uint64
	lastRand [10]byte
}

// NewULIDSource builds an identifier source backed by the supplied clock.
func NewULIDSource(clock Clock) *ULIDSource {
	if clock == nil {
		clock = SystemClock{}
	}
	return &ULIDSource{clock: clock}
}

// NewID returns `<prefix>_<ulid>`. When called within the same millisecond it
// increments the random component so identifiers remain strictly increasing,
// preserving claim ordering in the durable queue.
func (s *ULIDSource) NewID(prefix string) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	ms := uint64(s.clock.Now().UnixMilli())
	if ms == s.lastMS {
		incrementRandom(&s.lastRand)
	} else {
		s.lastMS = ms
		if _, err := rand.Read(s.lastRand[:]); err != nil {
			// crypto/rand does not fail on supported platforms; fall back to a
			// time-derived seed rather than panicking in the ingest hot path.
			binary.BigEndian.PutUint64(s.lastRand[:8], uint64(time.Now().UnixNano()))
		}
	}

	var raw [16]byte
	raw[0] = byte(ms >> 40)
	raw[1] = byte(ms >> 32)
	raw[2] = byte(ms >> 24)
	raw[3] = byte(ms >> 16)
	raw[4] = byte(ms >> 8)
	raw[5] = byte(ms)
	copy(raw[6:], s.lastRand[:])

	return prefix + "_" + encodeCrockford(raw)
}

func incrementRandom(value *[10]byte) {
	for i := len(value) - 1; i >= 0; i-- {
		value[i]++
		if value[i] != 0 {
			return
		}
	}
}

// encodeCrockford renders 16 bytes as a 26-character Crockford base32 ULID.
func encodeCrockford(raw [16]byte) string {
	var out [26]byte
	out[0] = crockford[(raw[0]&224)>>5]
	out[1] = crockford[raw[0]&31]
	out[2] = crockford[(raw[1]&248)>>3]
	out[3] = crockford[((raw[1]&7)<<2)|((raw[2]&192)>>6)]
	out[4] = crockford[(raw[2]&62)>>1]
	out[5] = crockford[((raw[2]&1)<<4)|((raw[3]&240)>>4)]
	out[6] = crockford[((raw[3]&15)<<1)|((raw[4]&128)>>7)]
	out[7] = crockford[(raw[4]&124)>>2]
	out[8] = crockford[((raw[4]&3)<<3)|((raw[5]&224)>>5)]
	out[9] = crockford[raw[5]&31]
	out[10] = crockford[(raw[6]&248)>>3]
	out[11] = crockford[((raw[6]&7)<<2)|((raw[7]&192)>>6)]
	out[12] = crockford[(raw[7]&62)>>1]
	out[13] = crockford[((raw[7]&1)<<4)|((raw[8]&240)>>4)]
	out[14] = crockford[((raw[8]&15)<<1)|((raw[9]&128)>>7)]
	out[15] = crockford[(raw[9]&124)>>2]
	out[16] = crockford[((raw[9]&3)<<3)|((raw[10]&224)>>5)]
	out[17] = crockford[raw[10]&31]
	out[18] = crockford[(raw[11]&248)>>3]
	out[19] = crockford[((raw[11]&7)<<2)|((raw[12]&192)>>6)]
	out[20] = crockford[(raw[12]&62)>>1]
	out[21] = crockford[((raw[12]&1)<<4)|((raw[13]&240)>>4)]
	out[22] = crockford[((raw[13]&15)<<1)|((raw[14]&128)>>7)]
	out[23] = crockford[(raw[14]&124)>>2]
	out[24] = crockford[((raw[14]&3)<<3)|((raw[15]&224)>>5)]
	out[25] = crockford[raw[15]&31]
	return string(out[:])
}
