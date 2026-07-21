package keyprovider

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hookship/hook-service/internal/awsv4"
	"github.com/hookship/hook-service/internal/payloadcrypto"
)

// fakeKMS is a minimal AWS KMS emulator sufficient to exercise the provider. It
// models a reversible wrap (`kms:<keyid>:<base64 plaintext>`) so Decrypt,
// Encrypt, and GenerateDataKey round-trip, and requires a SigV4 Authorization
// header so request signing is verified end to end.
type fakeKMS struct{}

func (fakeKMS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), awsv4.Algorithm) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	target := r.Header.Get("X-Amz-Target")
	body, _ := io.ReadAll(r.Body)
	var in map[string]string
	_ = json.Unmarshal(body, &in)
	respond := func(v any) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		_ = json.NewEncoder(w).Encode(v)
	}
	switch {
	case strings.HasSuffix(target, "GenerateDataKey"):
		key := make([]byte, payloadcrypto.KeySize)
		_, _ = rand.Read(key)
		blob := "kms:" + in["KeyId"] + ":" + base64.StdEncoding.EncodeToString(key)
		respond(map[string]string{
			"CiphertextBlob": base64.StdEncoding.EncodeToString([]byte(blob)),
			"Plaintext":      base64.StdEncoding.EncodeToString(key),
			"KeyId":          in["KeyId"],
		})
	case strings.HasSuffix(target, "Decrypt"):
		blob, _ := base64.StdEncoding.DecodeString(in["CiphertextBlob"])
		parts := strings.SplitN(string(blob), ":", 3)
		if len(parts) != 3 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		respond(map[string]string{"Plaintext": parts[2], "KeyId": parts[1]})
	case strings.HasSuffix(target, "Encrypt"):
		blob := "kms:" + in["KeyId"] + ":" + in["Plaintext"]
		respond(map[string]string{
			"CiphertextBlob": base64.StdEncoding.EncodeToString([]byte(blob)),
			"KeyId":          in["KeyId"],
		})
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

func newTestKMSClient(t *testing.T, server *httptest.Server) *kmsClient {
	t.Helper()
	client, err := newKMSClient(KMSConfig{
		Region:      "us-east-1",
		Endpoint:    server.URL,
		Credentials: awsv4.Credentials{AccessKeyID: "test", SecretAccessKey: "secret"},
		transportRT: server.Client().Transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func newTestKMSProvider(t *testing.T, server *httptest.Server, keyID, wrapped string) *KMSProvider {
	t.Helper()
	provider, err := NewKMS(KMSConfig{
		KeyID:          keyID,
		Region:         "us-east-1",
		Endpoint:       server.URL,
		Credentials:    awsv4.Credentials{AccessKeyID: "test", SecretAccessKey: "secret"},
		WrappedDataKey: wrapped,
		transportRT:    server.Client().Transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func TestStaticProviderStableMaterial(t *testing.T) {
	key := make([]byte, payloadcrypto.KeySize)
	for i := range key {
		key[i] = byte(i)
	}
	provider, err := NewStatic(key, "")
	if err != nil {
		t.Fatal(err)
	}
	m1, err := provider.Active(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m2, _ := provider.Active(context.Background())
	if m1.Version != m2.Version || m1.Reference != m2.Reference {
		t.Fatal("static material must be stable")
	}
	if m1.Reference != "static://cell/dek" {
		t.Fatalf("reference = %q", m1.Reference)
	}
	if _, err := NewStatic(make([]byte, 16), ""); err == nil {
		t.Fatal("expected error for short key")
	}
}

func TestKMSProviderUnwrapsAndSeals(t *testing.T) {
	server := httptest.NewServer(fakeKMS{})
	defer server.Close()
	ctx := context.Background()

	wrapped, _, err := newTestKMSClient(t, server).generateDataKey(ctx, "alias/hookship")
	if err != nil {
		t.Fatal(err)
	}
	wrappedB64 := base64.StdEncoding.EncodeToString(wrapped)

	provider := newTestKMSProvider(t, server, "alias/hookship", wrappedB64)
	material, err := provider.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(material.Key) != payloadcrypto.KeySize {
		t.Fatalf("dek length = %d", len(material.Key))
	}
	if !strings.HasPrefix(material.Version, "dek-") {
		t.Fatalf("version = %q", material.Version)
	}

	sealer, err := NewSealer(material)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sealer.Seal([]byte("secret-payload"))
	if err != nil {
		t.Fatal(err)
	}
	opened, err := sealer.Open(sealed)
	if err != nil || string(opened) != "secret-payload" {
		t.Fatalf("round-trip: %v %q", err, opened)
	}
}

// TestKMSRewrapPreservesPayloadBytes proves rotation: rewrapping the DEK under a
// new KMS key yields a provider whose DEK, reference, and version are identical,
// so payloads sealed before rotation still open afterwards and their bytes are
// unchanged.
func TestKMSRewrapPreservesPayloadBytes(t *testing.T) {
	server := httptest.NewServer(fakeKMS{})
	defer server.Close()
	ctx := context.Background()

	wrapped, _, err := newTestKMSClient(t, server).generateDataKey(ctx, "alias/original")
	if err != nil {
		t.Fatal(err)
	}
	first := newTestKMSProvider(t, server, "alias/original", base64.StdEncoding.EncodeToString(wrapped))
	m1, err := first.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sealer1, _ := NewSealer(m1)
	sealed, _ := sealer1.Seal([]byte("payload-before-rotation"))

	rewrapped, err := first.Rewrap(ctx, "alias/rotated")
	if err != nil {
		t.Fatal(err)
	}
	if rewrapped == base64.StdEncoding.EncodeToString(wrapped) {
		t.Fatal("rewrap should change the wrapped blob")
	}

	second := newTestKMSProvider(t, server, "alias/rotated", rewrapped)
	m2, err := second.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m2.Version != m1.Version || m2.Reference != m1.Reference {
		t.Fatalf("rewrap changed DEK identity: %q/%q vs %q/%q", m2.Reference, m2.Version, m1.Reference, m1.Version)
	}
	sealer2, _ := NewSealer(m2)
	opened, err := sealer2.Open(sealed)
	if err != nil || string(opened) != "payload-before-rotation" {
		t.Fatalf("post-rotation open failed: %v %q", err, opened)
	}
}

func TestNewKMSRejectsIncompleteConfig(t *testing.T) {
	cases := []KMSConfig{
		{Region: "us-east-1", WrappedDataKey: "AAAA", Credentials: awsv4.Credentials{AccessKeyID: "a", SecretAccessKey: "s"}},
		{KeyID: "k", Region: "us-east-1", Credentials: awsv4.Credentials{AccessKeyID: "a", SecretAccessKey: "s"}},
		{KeyID: "k", WrappedDataKey: "AAAA", Credentials: awsv4.Credentials{AccessKeyID: "a", SecretAccessKey: "s"}},
		{KeyID: "k", Region: "us-east-1", WrappedDataKey: "AAAA"},
	}
	for i, cfg := range cases {
		if _, err := NewKMS(cfg); err == nil {
			t.Fatalf("case %d: expected error", i)
		}
	}
}
