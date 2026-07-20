// Package payloadcrypto seals and opens producer payloads with AES-256-GCM. The
// plaintext body is never persisted; only the sealed ciphertext, a 12-byte
// nonce, and non-secret key metadata cross the acceptance barrier. The same
// sealer serves the memory and PostgreSQL profiles so ciphertext handling is
// identical across backends.
package payloadcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/hookship/hook-service/internal/runtime"
)

// KeySize is the AES-256 key length in bytes.
const KeySize = 32

// NonceSize is the AES-GCM nonce length in bytes.
const NonceSize = 12

// Sealer implements runtime.PayloadSealer with a single local key. Key rotation
// is modeled by keyReference/keyVersion, which are stored alongside ciphertext
// so a future multi-key resolver can select the right key on Open.
type Sealer struct {
	aead         cipher.AEAD
	keyReference string
	keyVersion   string
}

// NewSealer builds a sealer from a 32-byte key and its non-secret metadata.
func NewSealer(key []byte, keyReference, keyVersion string) (*Sealer, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("payload key must be %d bytes, got %d", KeySize, len(key))
	}
	if keyReference == "" {
		return nil, errors.New("key reference is required")
	}
	if keyVersion == "" {
		return nil, errors.New("key version is required")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("build AES cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("build GCM: %w", err)
	}
	return &Sealer{aead: aead, keyReference: keyReference, keyVersion: keyVersion}, nil
}

// Seal encrypts plaintext, returning ciphertext and the metadata needed to
// reproduce and later delete the exact bytes.
func (s *Sealer) Seal(plaintext []byte) (runtime.SealedPayload, error) {
	if len(plaintext) == 0 {
		return runtime.SealedPayload{}, errors.New("payload must not be empty")
	}
	nonce := make([]byte, NonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return runtime.SealedPayload{}, fmt.Errorf("generate nonce: %w", err)
	}
	ciphertext := s.aead.Seal(nil, nonce, plaintext, s.additionalData())
	return runtime.SealedPayload{
		Ciphertext:       ciphertext,
		Nonce:            nonce,
		KeyReference:     s.keyReference,
		KeyVersion:       s.keyVersion,
		CiphertextSHA256: runtime.SHA256(ciphertext),
	}, nil
}

// Open decrypts a sealed payload after verifying the ciphertext checksum.
func (s *Sealer) Open(sealed runtime.SealedPayload) ([]byte, error) {
	if len(sealed.Nonce) != NonceSize {
		return nil, fmt.Errorf("nonce must be %d bytes", NonceSize)
	}
	if runtime.SHA256(sealed.Ciphertext) != sealed.CiphertextSHA256 {
		return nil, errors.New("ciphertext checksum does not match")
	}
	plaintext, err := s.aead.Open(nil, sealed.Nonce, sealed.Ciphertext, s.additionalData())
	if err != nil {
		return nil, fmt.Errorf("open sealed payload: %w", err)
	}
	return plaintext, nil
}

// additionalData binds the key metadata into the AEAD so ciphertext cannot be
// reinterpreted under a different declared key reference.
func (s *Sealer) additionalData() []byte {
	return []byte("hookship-payload-v1\x00" + s.keyReference + "\x00" + s.keyVersion)
}
