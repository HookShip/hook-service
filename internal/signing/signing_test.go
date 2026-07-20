package signing_test

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/hookship/hook-service/internal/signing"
)

type vectorFile struct {
	Positive []positiveVector `json:"positive"`
	Negative []negativeVector `json:"negative"`
}

type positiveVector struct {
	Name              string `json:"name"`
	Secret            string `json:"secret"`
	MessageID         string `json:"messageId"`
	Timestamp         int64  `json:"timestamp"`
	PayloadBase64     string `json:"payloadBase64"`
	ExpectedSignature string `json:"expectedSignature"`
}

type negativeVector struct {
	Name            string `json:"name"`
	Secret          string `json:"secret"`
	MessageID       string `json:"messageId"`
	Timestamp       int64  `json:"timestamp"`
	PayloadBase64   string `json:"payloadBase64"`
	SignatureHeader string `json:"signatureHeader"`
}

func loadVectors(t *testing.T) vectorFile {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "standard-webhooks-vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file vectorFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	return file
}

func decode(t *testing.T, encoded string) []byte {
	t.Helper()
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestSignMatchesPositiveVectors(t *testing.T) {
	vectors := loadVectors(t)
	if len(vectors.Positive) == 0 {
		t.Fatal("no positive vectors loaded")
	}
	for _, vector := range vectors.Positive {
		t.Run(vector.Name, func(t *testing.T) {
			signer, err := signing.NewSigner([]byte(vector.Secret))
			if err != nil {
				t.Fatal(err)
			}
			payload := decode(t, vector.PayloadBase64)
			signature, err := signer.Sign(vector.MessageID, time.Unix(vector.Timestamp, 0), payload)
			if err != nil {
				t.Fatal(err)
			}
			if signature.Value != vector.ExpectedSignature {
				t.Fatalf("signature = %q, want %q", signature.Value, vector.ExpectedSignature)
			}
			headers := signature.Headers()
			if headers[signing.HeaderID] != vector.MessageID {
				t.Fatalf("webhook-id header mismatch: %q", headers[signing.HeaderID])
			}
			// A fresh verify against the same content must pass with a wide tolerance.
			if err := signer.Verify(vector.MessageID, signature.Timestamp, signature.Value, payload, 100*365*24*time.Hour); err != nil {
				t.Fatalf("verify of own signature failed: %v", err)
			}
		})
	}
}

func TestVerifyRejectsNegativeVectors(t *testing.T) {
	vectors := loadVectors(t)
	if len(vectors.Negative) == 0 {
		t.Fatal("no negative vectors loaded")
	}
	for _, vector := range vectors.Negative {
		t.Run(vector.Name, func(t *testing.T) {
			signer, err := signing.NewSigner([]byte(vector.Secret))
			if err != nil {
				t.Fatal(err)
			}
			payload := decode(t, vector.PayloadBase64)
			err = signer.Verify(
				vector.MessageID,
				strconv.FormatInt(vector.Timestamp, 10),
				vector.SignatureHeader,
				payload,
				100*365*24*time.Hour,
			)
			if err == nil {
				t.Fatalf("expected verification to fail for %q", vector.Name)
			}
		})
	}
}

func TestVerifyEnforcesTimestampTolerance(t *testing.T) {
	signer, err := signing.NewSigner([]byte("whsec_AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="))
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"k":"v"}`)
	old := time.Now().Add(-time.Hour)
	signature, err := signer.Sign("msg_tolerance00000000000000A", old, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := signer.Verify("msg_tolerance00000000000000A", signature.Timestamp, signature.Value, payload, 5*time.Minute); err == nil {
		t.Fatal("expected stale timestamp to be rejected")
	}
	if err := signer.Verify("msg_tolerance00000000000000A", signature.Timestamp, signature.Value, payload, 2*time.Hour); err != nil {
		t.Fatalf("expected in-tolerance timestamp to pass: %v", err)
	}
}

func TestSignRejectsInvalidMessageID(t *testing.T) {
	signer, err := signing.NewSigner([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "has.period", "has space", "has\nnewline"} {
		if _, err := signer.Sign(bad, time.Now(), []byte("x")); err == nil {
			t.Fatalf("expected rejection of message id %q", bad)
		}
	}
}

func TestNewSignerRejectsEmptySecret(t *testing.T) {
	if _, err := signing.NewSigner(nil); err == nil {
		t.Fatal("expected empty secret rejection")
	}
	if _, err := signing.NewSigner([]byte("whsec_")); err == nil {
		t.Fatal("expected empty decoded whsec secret rejection")
	}
}
