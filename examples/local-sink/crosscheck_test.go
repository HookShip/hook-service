package main

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"

	"github.com/hookship/hook-service/internal/signing"
)

// This cross-check guards that the standalone example verifier in main.go and
// the production signer agree byte-for-byte on the Standard Webhooks scheme,
// without main.go importing the production signing package (its verification is
// deliberately self-contained so external integrators can copy it). The test
// itself may import both sides to prove they interoperate.

// crossCheckGolden is a locked golden vector: the production signer must emit
// exactly this signature, the standalone example verifier must accept it, and
// the production verifier must accept it too.
var crossCheckGolden = struct {
	id, ts, value string
	body          []byte
}{
	id:    "msg_crosscheck_1",
	ts:    "1700000000",
	value: "v1,mAwij8mOSXi0OEjHNzbdWOVJn5Qikj3eU9tFsAYemIw=",
	body:  []byte(`{"event":"order.created","id":42}`),
}

func TestSignerAndExampleAgreeOnGoldenVector(t *testing.T) {
	signer, err := signing.NewSigner([]byte(evaluationSigningSecret))
	if err != nil {
		t.Fatalf("build signer: %v", err)
	}

	// 1. The production signer emits exactly the golden signature.
	ts, err := time.Parse(time.RFC3339, "2023-11-14T22:13:20Z") // unix 1700000000
	if err != nil {
		t.Fatal(err)
	}
	sig, err := signer.Sign(crossCheckGolden.id, ts, crossCheckGolden.body)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if sig.Timestamp != crossCheckGolden.ts {
		t.Fatalf("timestamp = %s, want %s", sig.Timestamp, crossCheckGolden.ts)
	}
	if sig.Value != crossCheckGolden.value {
		t.Fatalf("signature = %s, want golden %s", sig.Value, crossCheckGolden.value)
	}

	// 2. The standalone example verifier accepts the production signature.
	if !verify(crossCheckGolden.id, crossCheckGolden.ts, crossCheckGolden.value, crossCheckGolden.body) {
		t.Fatal("example verify rejected the production signature")
	}

	// 3. The production verifier accepts the same signature (vice versa). The
	//    timestamp is historical, so allow a wide tolerance.
	if err := signer.Verify(crossCheckGolden.id, crossCheckGolden.ts, crossCheckGolden.value, crossCheckGolden.body, 100*365*24*time.Hour); err != nil {
		t.Fatalf("production verify rejected the golden signature: %v", err)
	}
}

func TestExampleVerifyRejectsTamperedSignatureAndBody(t *testing.T) {
	// A tampered signature is rejected by the example verifier.
	if verify(crossCheckGolden.id, crossCheckGolden.ts, "v1,AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", crossCheckGolden.body) {
		t.Fatal("example verify accepted a tampered signature")
	}
	// A tampered body no longer matches the golden signature.
	if verify(crossCheckGolden.id, crossCheckGolden.ts, crossCheckGolden.value, []byte(`{"event":"order.created","id":43}`)) {
		t.Fatal("example verify accepted a signature over a different body")
	}
	// A missing/empty signature header is rejected.
	if verify(crossCheckGolden.id, crossCheckGolden.ts, "", crossCheckGolden.body) {
		t.Fatal("example verify accepted an empty signature header")
	}
}

// TestExampleMainDoesNotImportProductionSigning enforces that the example stays
// pedagogically independent: main.go must verify signatures with its own copy of
// the scheme and never import the production signing package. Only this test
// imports production signing, to prove interoperability above.
func TestExampleMainDoesNotImportProductionSigning(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}
	for _, imp := range file.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if strings.Contains(path, "internal/signing") {
			t.Fatalf("main.go must stay independent but imports %s", path)
		}
	}
}
