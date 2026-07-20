package payloadcrypto_test

import (
	"bytes"
	"testing"

	"github.com/hookship/hook-service/internal/payloadcrypto"
	"github.com/hookship/hook-service/internal/runtime"
)

func newTestSealer(t *testing.T) *payloadcrypto.Sealer {
	t.Helper()
	key := make([]byte, payloadcrypto.KeySize)
	for i := range key {
		key[i] = byte(i)
	}
	sealer, err := payloadcrypto.NewSealer(key, "local://test/key/1", "1")
	if err != nil {
		t.Fatal(err)
	}
	return sealer
}

func TestSealOpenRoundTrip(t *testing.T) {
	sealer := newTestSealer(t)
	plaintext := []byte(`{"order_id":"123","amount":12.50}`)
	sealed, err := sealer.Seal(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if len(sealed.Nonce) != payloadcrypto.NonceSize {
		t.Fatalf("nonce size = %d", len(sealed.Nonce))
	}
	if bytes.Equal(sealed.Ciphertext, plaintext) {
		t.Fatal("ciphertext must not equal plaintext")
	}
	if sealed.CiphertextSHA256 != runtime.SHA256(sealed.Ciphertext) {
		t.Fatal("ciphertext checksum mismatch")
	}
	opened, err := sealer.Open(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(opened, plaintext) {
		t.Fatalf("round trip mismatch: %q", opened)
	}
}

func TestSealUsesFreshNonce(t *testing.T) {
	sealer := newTestSealer(t)
	first, err := sealer.Seal([]byte("same"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := sealer.Seal([]byte("same"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first.Nonce, second.Nonce) {
		t.Fatal("nonces must differ across seals")
	}
	if bytes.Equal(first.Ciphertext, second.Ciphertext) {
		t.Fatal("ciphertexts must differ under fresh nonces")
	}
}

func TestOpenRejectsTamperedCiphertext(t *testing.T) {
	sealer := newTestSealer(t)
	sealed, err := sealer.Seal([]byte("secret payload"))
	if err != nil {
		t.Fatal(err)
	}
	tampered := sealed
	tampered.Ciphertext = append([]byte{}, sealed.Ciphertext...)
	tampered.Ciphertext[0] ^= 0xff
	tampered.CiphertextSHA256 = runtime.SHA256(tampered.Ciphertext)
	if _, err := sealer.Open(tampered); err == nil {
		t.Fatal("expected authentication failure on tampered ciphertext")
	}
}

func TestOpenRejectsWrongKeyMetadata(t *testing.T) {
	sealer := newTestSealer(t)
	sealed, err := sealer.Seal([]byte("secret payload"))
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, payloadcrypto.KeySize)
	for i := range key {
		key[i] = byte(i)
	}
	other, err := payloadcrypto.NewSealer(key, "local://test/key/2", "2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Open(sealed); err == nil {
		t.Fatal("expected AEAD additional-data mismatch to fail")
	}
}

func TestNewSealerValidatesInputs(t *testing.T) {
	if _, err := payloadcrypto.NewSealer(make([]byte, 16), "ref", "1"); err == nil {
		t.Fatal("expected short key rejection")
	}
	if _, err := payloadcrypto.NewSealer(make([]byte, payloadcrypto.KeySize), "", "1"); err == nil {
		t.Fatal("expected empty reference rejection")
	}
}
