package awsv4

import (
	"bytes"
	"encoding/hex"
	"net/http"
	"testing"
	"time"
)

// signingTime is the timestamp used by the AWS SigV4 published test vectors.
var signingTime = time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)

func testCreds() Credentials {
	return Credentials{
		AccessKeyID:     "AKIDEXAMPLE",
		SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
	}
}

// TestDeriveSigningKeyMatchesAWSVector checks the chained-HMAC signing key
// against the exact value published in the AWS "deriving the signing key"
// reference, anchoring the whole algorithm to a known-good vector.
func TestDeriveSigningKeyMatchesAWSVector(t *testing.T) {
	key := deriveSigningKey("wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "20120215", "us-east-1", "iam")
	got := hex.EncodeToString(key)
	const want = "f4780e2d9f65fa895f9c67b32ce1baf0b0d8a43505a000a1a9e090d414db404d"
	if got != want {
		t.Fatalf("signing key = %s, want %s", got, want)
	}
}

func TestEmptyPayloadHashConstant(t *testing.T) {
	if got := PayloadHash(nil); got != EmptyPayloadHash {
		t.Fatalf("empty payload hash = %s", got)
	}
}

// TestSignGetVanilla verifies the full canonical-request/string-to-sign pipeline
// against a signature computed from the AWS get-vanilla vector with the
// mandatory x-amz-content-sha256 header the signer always adds.
func TestSignGetVanilla(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := Sign(req, "service", "us-east-1", testCreds(), EmptyPayloadHash, signingTime); err != nil {
		t.Fatal(err)
	}
	const want = "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, " +
		"SignedHeaders=host;x-amz-content-sha256;x-amz-date, " +
		"Signature=726c5c4879a6b4ccbbd3b24edbd6b8826d34f87450fbbf4e85546fc7ba9c1642"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("authorization =\n%s\nwant\n%s", got, want)
	}
	if req.Header.Get("X-Amz-Date") != "20150830T123600Z" {
		t.Fatalf("x-amz-date = %q", req.Header.Get("X-Amz-Date"))
	}
}

// TestSignS3Put covers a path-style S3 PUT with a body and Content-Type, which
// is the object-store PayloadStore's exact request shape.
func TestSignS3Put(t *testing.T) {
	body := []byte("ciphertext")
	req, err := http.NewRequest(http.MethodPut, "https://s3.local:9000/mybucket/hookship/v1/a/b", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if err := Sign(req, "s3", "us-east-1", testCreds(), PayloadHash(body), signingTime); err != nil {
		t.Fatal(err)
	}
	const want = "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/s3/aws4_request, " +
		"SignedHeaders=content-type;host;x-amz-content-sha256;x-amz-date, " +
		"Signature=135780c4219ef94192930a70ab97bbd6ff2fd58c141eb56343db5bc44212b9dc"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("authorization =\n%s\nwant\n%s", got, want)
	}
}

func TestSignSessionTokenHeader(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/", nil)
	creds := testCreds()
	creds.SessionToken = "session-token-value"
	if err := Sign(req, "service", "us-east-1", creds, EmptyPayloadHash, signingTime); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("X-Amz-Security-Token") != "session-token-value" {
		t.Fatal("session token header not set")
	}
	if !bytes.Contains([]byte(req.Header.Get("Authorization")), []byte("x-amz-security-token")) {
		t.Fatal("session token must be a signed header")
	}
}

func TestSignRejectsMissingCredentials(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/", nil)
	if err := Sign(req, "s3", "us-east-1", Credentials{}, EmptyPayloadHash, signingTime); err == nil {
		t.Fatal("expected error for empty credentials")
	}
}

func TestURIEncode(t *testing.T) {
	cases := []struct {
		in          string
		encodeSlash bool
		want        string
	}{
		{"a/b", false, "a/b"},
		{"a/b", true, "a%2Fb"},
		{"hello world", true, "hello%20world"},
		{"key~with.dashes-_", true, "key~with.dashes-_"},
		{"plus+eq=", true, "plus%2Beq%3D"},
	}
	for _, tc := range cases {
		if got := uriEncode(tc.in, tc.encodeSlash); got != tc.want {
			t.Fatalf("uriEncode(%q, %v) = %q, want %q", tc.in, tc.encodeSlash, got, tc.want)
		}
	}
}

func TestCanonicalQuerySorts(t *testing.T) {
	if got := canonicalQuery("b=2&a=1&a=0"); got != "a=0&a=1&b=2" {
		t.Fatalf("canonicalQuery = %q", got)
	}
}
