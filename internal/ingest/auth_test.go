package ingest_test

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hookship/hook-service/internal/ingest"
)

type signingVectors struct {
	Vectors []struct {
		Name      string `json:"name"`
		SecretHex string `json:"secretHex"`
		Request   struct {
			Method         string `json:"method"`
			Path           string `json:"path"`
			CredentialID   string `json:"credentialId"`
			Timestamp      string `json:"timestamp"`
			Nonce          string `json:"nonce"`
			IdempotencyKey string `json:"idempotencyKey"`
			EventID        string `json:"eventId"`
			EventType      string `json:"eventType"`
			Route          string `json:"route"`
			ContentType    string `json:"contentType"`
			BodySha256     string `json:"bodySha256"`
		} `json:"request"`
		ExpectedCanonical string `json:"expectedCanonical"`
		ExpectedSignature string `json:"expectedSignature"`
	} `json:"vectors"`
	SignatureEncodingCases []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
		Valid bool   `json:"valid"`
	} `json:"signatureEncodingCases"`
}

func loadSigningVectors(t *testing.T) signingVectors {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "spec", "v1", "fixtures", "signing-vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vectors signingVectors
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	return vectors
}

func toSignedRequest(v signingVectors, i int) ingest.SignedRequest {
	r := v.Vectors[i].Request
	return ingest.SignedRequest{
		Method:         r.Method,
		Path:           r.Path,
		CredentialID:   r.CredentialID,
		Timestamp:      r.Timestamp,
		Nonce:          r.Nonce,
		IdempotencyKey: r.IdempotencyKey,
		EventID:        r.EventID,
		EventType:      r.EventType,
		Route:          r.Route,
		ContentType:    r.ContentType,
		BodySHA256Hex:  r.BodySha256,
	}
}

func TestCanonicalMatchesSpecVectors(t *testing.T) {
	vectors := loadSigningVectors(t)
	if len(vectors.Vectors) == 0 {
		t.Fatal("no signing vectors loaded")
	}
	for i, vector := range vectors.Vectors {
		request := toSignedRequest(vectors, i)
		if got := ingest.Canonical(request); got != vector.ExpectedCanonical {
			t.Fatalf("%s: canonical mismatch\n got: %q\nwant: %q", vector.Name, got, vector.ExpectedCanonical)
		}
	}
}

func TestSignatureMatchesSpecVectors(t *testing.T) {
	vectors := loadSigningVectors(t)
	for i, vector := range vectors.Vectors {
		secret, err := hex.DecodeString(vector.SecretHex)
		if err != nil {
			t.Fatal(err)
		}
		request := toSignedRequest(vectors, i)
		if got := ingest.ComputeSignature(secret, request); got != vector.ExpectedSignature {
			t.Fatalf("%s: signature = %q, want %q", vector.Name, got, vector.ExpectedSignature)
		}
		if err := ingest.VerifySignature(secret, request, vector.ExpectedSignature); err != nil {
			t.Fatalf("%s: verify failed: %v", vector.Name, err)
		}
	}
}

func TestVerifyRejectsNonCanonicalSignatureAlias(t *testing.T) {
	vectors := loadSigningVectors(t)
	secret, _ := hex.DecodeString(vectors.Vectors[0].SecretHex)
	request := toSignedRequest(vectors, 0)
	for _, encoding := range vectors.SignatureEncodingCases {
		err := ingest.VerifySignature(secret, request, encoding.Value)
		if encoding.Valid && err != nil {
			t.Fatalf("%s: expected valid signature, got %v", encoding.Name, err)
		}
		if !encoding.Valid && err == nil {
			t.Fatalf("%s: expected rejection of non-canonical alias", encoding.Name)
		}
	}
}

func TestVerifyRejectsTamperedRequest(t *testing.T) {
	vectors := loadSigningVectors(t)
	secret, _ := hex.DecodeString(vectors.Vectors[0].SecretHex)
	request := toSignedRequest(vectors, 0)
	request.Route = "orders.tampered"
	if err := ingest.VerifySignature(secret, request, vectors.Vectors[0].ExpectedSignature); err == nil {
		t.Fatal("expected tampered request to fail verification")
	}
}

func TestParseAuthorization(t *testing.T) {
	credential, signature, err := ingest.ParseAuthorization("Hookship-HMAC Credential=cred_test_01,Signature=lAaAdzTJfPJSb2EO_3oGM0ykD3OXt7dr0MEZk8NrfrM")
	if err != nil {
		t.Fatal(err)
	}
	if credential != "cred_test_01" || signature != "lAaAdzTJfPJSb2EO_3oGM0ykD3OXt7dr0MEZk8NrfrM" {
		t.Fatalf("parsed credential=%q signature=%q", credential, signature)
	}
	for _, bad := range []string{
		"Bearer token",
		"Hookship-HMAC Credential=only",
		"Hookship-HMAC Credential=a,Signature=b,Extra=c",
		"Hookship-HMAC Credential=a,Unknown=b",
	} {
		if _, _, err := ingest.ParseAuthorization(bad); err == nil {
			t.Fatalf("expected rejection of %q", bad)
		}
	}
}

func TestValidateTimestampWindowAndFormat(t *testing.T) {
	now, _ := time.Parse(ingest.TimestampLayout, "2026-07-19T16:00:00Z")
	if _, err := ingest.ValidateTimestamp("2026-07-19T16:00:00Z", now, ingest.ReplayWindow); err != nil {
		t.Fatalf("valid timestamp rejected: %v", err)
	}
	for _, bad := range []string{
		"2026-07-19T16:00:00z",      // lowercase z
		"2026-07-19T16:00:00.5Z",    // fractional
		"2026-07-19T16:00:00+00:00", // offset
		"2026-07-19T16:00Z",         // omitted seconds
		"2026-07-19T16:10:00Z",      // outside window (10 min)
	} {
		if _, err := ingest.ValidateTimestamp(bad, now, ingest.ReplayWindow); err == nil {
			t.Fatalf("expected rejection of timestamp %q", bad)
		}
	}
}
