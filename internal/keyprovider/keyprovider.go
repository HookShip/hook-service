// Package keyprovider is the payload key-management seam. Payloads are sealed
// with a 32-byte data encryption key (DEK); a Provider resolves the active DEK
// and its stable reference/version. Two providers ship:
//
//   - StaticProvider wraps a locally supplied key (the default, preserving the
//     original static-key behavior).
//   - KMSProvider uses envelope encryption: the DEK is wrapped by an
//     AWS-compatible KMS key and unwrapped at startup via KMS Decrypt. Rotating
//     the KMS key rewraps the DEK without changing the DEK plaintext, so the
//     exact sealed payload bytes are never altered and previously sealed
//     payloads remain decryptable.
//
// The DEK reference and version are derived from the DEK plaintext, so they are
// stable across restarts, replicas, and rewraps — which is what keeps Open
// working for historical payloads.
package keyprovider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/hookship/hook-service/internal/payloadcrypto"
)

// Material is the active data encryption key plus its stable, payload-free
// identity. Key is secret and never logged.
type Material struct {
	Key       []byte
	Reference string
	Version   string
}

// Provider resolves the active payload data encryption key.
type Provider interface {
	// Active returns the current DEK material used to seal new payloads and to
	// open previously sealed payloads. Implementations may cache the result.
	Active(ctx context.Context) (Material, error)
	// Name identifies the provider for operator status output.
	Name() string
}

// deriveVersion returns a stable version token bound to the DEK plaintext, so a
// rewrapped-but-identical DEK keeps the same version and old payloads still open.
func deriveVersion(key []byte) string {
	sum := sha256.Sum256(append([]byte("hookship-dek-v1\x00"), key...))
	return "dek-" + hex.EncodeToString(sum[:])[:16]
}

// StaticProvider serves a fixed, locally supplied DEK.
type StaticProvider struct {
	material Material
}

// NewStatic builds a static provider from a 32-byte key and a stable reference.
func NewStatic(key []byte, reference string) (*StaticProvider, error) {
	if len(key) != payloadcrypto.KeySize {
		return nil, fmt.Errorf("static key must be %d bytes", payloadcrypto.KeySize)
	}
	if reference == "" {
		reference = "static://cell/dek"
	}
	return &StaticProvider{material: Material{
		Key:       append([]byte(nil), key...),
		Reference: reference,
		Version:   deriveVersion(key),
	}}, nil
}

// Active returns the static DEK material.
func (p *StaticProvider) Active(ctx context.Context) (Material, error) {
	if len(p.material.Key) != payloadcrypto.KeySize {
		return Material{}, errors.New("static provider is not initialized")
	}
	return p.material, nil
}

// Name identifies the provider.
func (p *StaticProvider) Name() string { return "static" }

// NewSealer builds a payload sealer from resolved key material. It is shared by
// backend construction so every provider produces an identical AES-256-GCM
// sealer bound to the DEK reference and version.
func NewSealer(material Material) (*payloadcrypto.Sealer, error) {
	return payloadcrypto.NewSealer(material.Key, material.Reference, material.Version)
}
