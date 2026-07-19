package specvalidation

import (
	"bytes"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

type documentCache map[string]any

func TestProtocolSpecJSONDocuments(t *testing.T) {
	root := specRoot(t)
	var files []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && filepath.Ext(path) == ".json" {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no JSON protocol artifacts found")
	}
	sort.Strings(files)

	cache := documentCache{}
	for _, path := range files {
		path := path
		t.Run(relative(root, path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := rejectDuplicateJSONKeys(data); err != nil {
				t.Fatal(err)
			}
			if _, err := cache.load(path); err != nil {
				t.Fatal(err)
			}
		})
	}

	schemaFiles, err := filepath.Glob(filepath.Join(root, "schemas", "*.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(schemaFiles)
	for _, path := range schemaFiles {
		path := path
		t.Run("schema/"+filepath.Base(path), func(t *testing.T) {
			value, err := cache.load(path)
			if err != nil {
				t.Fatal(err)
			}
			schema, ok := value.(map[string]any)
			if !ok {
				t.Fatal("schema root must be an object")
			}
			if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
				t.Fatal("schema must declare JSON Schema draft 2020-12")
			}
			id, ok := schema["$id"].(string)
			if !ok || !strings.HasPrefix(id, "urn:hookship:spec:v1:schema:") {
				t.Fatal("schema must have a versioned HookShip URN $id")
			}
			if title, ok := schema["title"].(string); !ok || title == "" {
				t.Fatal("schema must have a title")
			}
			if err := checkSchemaKeywords(schema, "$"); err != nil {
				t.Fatal(err)
			}
		})
	}

	for _, path := range files {
		value, err := cache.load(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := checkReferences(root, cache, path, value); err != nil {
			t.Fatalf("%s: %v", relative(root, path), err)
		}
	}
}

func TestProtocolSpecSchemaRegistry(t *testing.T) {
	root := specRoot(t)
	cache := documentCache{}
	registry, err := loadSchemaRegistry(root, cache)
	if err != nil {
		t.Fatal(err)
	}
	schemaFiles, err := filepath.Glob(filepath.Join(root, "schemas", "*.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(registry) != len(schemaFiles) {
		t.Fatalf("registry has %d schemas, filesystem has %d", len(registry), len(schemaFiles))
	}
	for id, path := range registry {
		value, err := cache.load(path)
		if err != nil {
			t.Fatal(err)
		}
		schema := value.(map[string]any)
		if schema["$id"] != id {
			t.Fatalf("%s: registry id %q does not match $id %q", relative(root, path), id, schema["$id"])
		}
	}
}

func TestProtocolSpecFixtures(t *testing.T) {
	root := specRoot(t)
	cache := documentCache{}
	goldenSchemaPath := filepath.Join(root, "schemas", "golden-http-outcome.schema.json")
	goldenSchema, err := cache.load(goldenSchemaPath)
	if err != nil {
		t.Fatal(err)
	}

	type golden struct {
		name  string
		value map[string]any
	}
	var fixtures []golden
	for _, name := range []string{"accepted", "duplicate", "conflict", "rejected"} {
		path := filepath.Join(root, "fixtures", name+".json")
		value, err := cache.load(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateJSONSchema(cache, root, goldenSchemaPath, goldenSchema, value, "$"); err != nil {
			t.Fatalf("%s.json: %v", name, err)
		}
		object := value.(map[string]any)
		if object["case"] != name {
			t.Fatalf("%s.json: case does not match filename", name)
		}
		fixtures = append(fixtures, golden{name: name, value: object})
	}

	byName := make(map[string]map[string]any, len(fixtures))
	for _, fixture := range fixtures {
		byName[fixture.name] = fixture.value
		verifyGoldenRequest(t, fixture.name, fixture.value)
		verifyGoldenResponse(t, fixture.name, fixture.value)
	}

	acceptedRequest := byName["accepted"]["request"].(map[string]any)
	duplicateRequest := byName["duplicate"]["request"].(map[string]any)
	conflictRequest := byName["conflict"]["request"].(map[string]any)
	acceptedHeaders := acceptedRequest["headers"].(map[string]any)
	duplicateHeaders := duplicateRequest["headers"].(map[string]any)
	conflictHeaders := conflictRequest["headers"].(map[string]any)

	if acceptedHeaders["Idempotency-Key"] != duplicateHeaders["Idempotency-Key"] ||
		acceptedHeaders["X-Hookship-Body-SHA256"] != duplicateHeaders["X-Hookship-Body-SHA256"] {
		t.Fatal("duplicate fixture must reuse the accepted idempotency key and body hash")
	}
	if acceptedHeaders["X-Hookship-Nonce"] == duplicateHeaders["X-Hookship-Nonce"] {
		t.Fatal("duplicate retry must use a fresh nonce")
	}
	if acceptedHeaders["Idempotency-Key"] != conflictHeaders["Idempotency-Key"] ||
		acceptedHeaders["X-Hookship-Body-SHA256"] == conflictHeaders["X-Hookship-Body-SHA256"] {
		t.Fatal("conflict fixture must reuse the key with a different body hash")
	}

	acceptedBody := byName["accepted"]["expected"].(map[string]any)["body"].(map[string]any)
	duplicateBody := byName["duplicate"]["expected"].(map[string]any)["body"].(map[string]any)
	for _, key := range []string{"ingestId", "acceptedAt", "idempotencyExpiresAt", "bodySha256", "eventId", "eventType", "route"} {
		if acceptedBody[key] != duplicateBody[key] {
			t.Fatalf("duplicate response must preserve original %s", key)
		}
	}
	acceptedAt, err := time.Parse(time.RFC3339, acceptedBody["acceptedAt"].(string))
	if err != nil {
		t.Fatal(err)
	}
	expiresAt, err := time.Parse(time.RFC3339, acceptedBody["idempotencyExpiresAt"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if expiresAt.Sub(acceptedAt) < 24*time.Hour {
		t.Fatal("idempotency retry horizon must be at least 24 hours")
	}
	acceptedResponseHeaders := byName["accepted"]["expected"].(map[string]any)["headers"].(map[string]any)
	if acceptedResponseHeaders["Hookship-Idempotency-Expires-At"] != acceptedBody["idempotencyExpiresAt"] {
		t.Fatal("receipt body and idempotency expiry header must match")
	}

	conflictBody := byName["conflict"]["expected"].(map[string]any)["body"].(map[string]any)
	mutated := cloneJSON(t, conflictBody).(map[string]any)
	mutated["retryable"] = true
	problemSchemaPath := filepath.Join(root, "schemas", "problem-details.schema.json")
	problemSchema, err := cache.load(problemSchemaPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateJSONSchema(cache, root, problemSchemaPath, problemSchema, mutated, "$"); err == nil {
		t.Fatal("problem schema accepted a retryable idempotency conflict")
	}
	notFound := make([]map[string]any, 0, 2)
	for _, name := range []string{"not-found-unknown.json", "not-found-foreign.json"} {
		value, err := cache.load(filepath.Join(root, "fixtures", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := validateJSONSchema(cache, root, problemSchemaPath, problemSchema, value, "$"); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		notFound = append(notFound, value.(map[string]any))
	}
	for _, field := range []string{"type", "title", "status", "detail", "code", "retryable"} {
		if !reflect.DeepEqual(notFound[0][field], notFound[1][field]) {
			t.Fatalf("unknown and foreign 404 differ in %s", field)
		}
	}

	statusSchemaPath := filepath.Join(root, "schemas", "event-ingest-status.schema.json")
	statusSchema, err := cache.load(statusSchemaPath)
	if err != nil {
		t.Fatal(err)
	}
	status, err := cache.load(filepath.Join(root, "fixtures", "status.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateJSONSchema(cache, root, statusSchemaPath, statusSchema, status, "$"); err != nil {
		t.Fatal(err)
	}
	if containsForbiddenPayloadField(status) {
		t.Fatal("status fixture contains a raw-payload field")
	}
}

func TestProtocolSpecSigningVectors(t *testing.T) {
	root := specRoot(t)
	cache := documentCache{}
	schemaPath := filepath.Join(root, "schemas", "signing-vectors.schema.json")
	schema, err := cache.load(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	vectorPath := filepath.Join(root, "fixtures", "signing-vectors.json")
	value, err := cache.load(vectorPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateJSONSchema(cache, root, schemaPath, schema, value, "$"); err != nil {
		t.Fatal(err)
	}

	for _, raw := range value.(map[string]any)["vectors"].([]any) {
		vector := raw.(map[string]any)
		name := vector["name"].(string)
		t.Run(name, func(t *testing.T) {
			secretHex := vector["secretHex"].(string)
			secret, err := hex.DecodeString(secretHex)
			if err != nil {
				t.Fatal(err)
			}
			if len(secret) != sha256.Size || hex.EncodeToString(secret) != secretHex {
				t.Fatal("public HMAC test key must be canonical 32-byte lowercase hex")
			}
			request := vector["request"].(map[string]any)
			if !validWholeSecondUTC(request["timestamp"].(string)) {
				t.Fatal("signing vector timestamp is not exact whole-second UTC")
			}
			body, err := base64.StdEncoding.DecodeString(request["bodyBase64"].(string))
			if err != nil {
				t.Fatal(err)
			}
			bodySum := sha256.Sum256(body)
			bodyHash := hex.EncodeToString(bodySum[:])
			if bodyHash != request["bodySha256"] {
				t.Fatalf("body hash = %s, want %s", bodyHash, request["bodySha256"])
			}
			canonical := canonicalRequest(request)
			if canonical != vector["expectedCanonical"] {
				t.Fatalf("canonical string mismatch\n got: %q\nwant: %q", canonical, vector["expectedCanonical"])
			}
			mac := hmac.New(sha256.New, secret)
			_, _ = mac.Write([]byte(canonical))
			signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
			if signature != vector["expectedSignature"] {
				t.Fatalf("signature = %s, want %s", signature, vector["expectedSignature"])
			}
			if _, err := decodeCanonicalRawBase64URL(vector["expectedSignature"].(string), sha256.Size); err != nil {
				t.Fatalf("expected signature is not canonical base64url: %v", err)
			}
		})
	}

	cases := value.(map[string]any)["signatureEncodingCases"].([]any)
	var decoded [][]byte
	for _, raw := range cases {
		entry := raw.(map[string]any)
		bytesValue, err := decodeCanonicalRawBase64URL(entry["value"].(string), sha256.Size)
		if entry["valid"].(bool) && err != nil {
			t.Fatalf("%s: canonical encoding rejected: %v", entry["name"], err)
		}
		if !entry["valid"].(bool) && err == nil {
			t.Fatalf("%s: non-canonical encoding accepted", entry["name"])
		}
		padding := strings.Repeat("=", (4-len(entry["value"].(string))%4)%4)
		rawBytes, decodeErr := base64.URLEncoding.DecodeString(entry["value"].(string) + padding)
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		decoded = append(decoded, rawBytes)
		if entry["valid"].(bool) && !bytes.Equal(bytesValue, rawBytes) {
			t.Fatal("canonical decoder changed HMAC bytes")
		}
	}
	if len(decoded) != 2 || !bytes.Equal(decoded[0], decoded[1]) {
		t.Fatal("HMAC alias vector must decode to the same bytes")
	}
	aliasCandidate := cloneJSON(t, value).(map[string]any)
	aliasCandidate["vectors"].([]any)[0].(map[string]any)["expectedSignature"] =
		cases[1].(map[string]any)["value"]
	if err := validateJSONSchema(cache, root, schemaPath, schema, aliasCandidate, "$"); err == nil {
		t.Fatal("signing vector schema accepted non-canonical HMAC signature")
	}
}

func TestProtocolSpecTimestampVectors(t *testing.T) {
	root := specRoot(t)
	cache := documentCache{}
	schemaPath := filepath.Join(root, "schemas", "timestamp-vectors.schema.json")
	schema, err := cache.load(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	value, err := cache.load(filepath.Join(root, "fixtures", "timestamp-vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateJSONSchema(cache, root, schemaPath, schema, value, "$"); err != nil {
		t.Fatal(err)
	}
	for _, raw := range value.(map[string]any)["cases"].([]any) {
		entry := raw.(map[string]any)
		got := validWholeSecondUTC(entry["value"].(string))
		if got != entry["valid"].(bool) {
			t.Fatalf("%s: validity = %t, want %t", entry["name"], got, entry["valid"])
		}
	}

	signingSchemaPath := filepath.Join(root, "schemas", "signing-vectors.schema.json")
	signingSchema, err := cache.load(signingSchemaPath)
	if err != nil {
		t.Fatal(err)
	}
	signingVectors, err := cache.load(filepath.Join(root, "fixtures", "signing-vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range value.(map[string]any)["cases"].([]any) {
		entry := raw.(map[string]any)
		candidate := cloneJSON(t, signingVectors).(map[string]any)
		candidate["vectors"].([]any)[0].(map[string]any)["request"].(map[string]any)["timestamp"] = entry["value"]
		err := validateJSONSchema(cache, root, signingSchemaPath, signingSchema, candidate, "$")
		if entry["valid"].(bool) && err != nil {
			t.Fatalf("%s: signing schema rejected valid timestamp: %v", entry["name"], err)
		}
		if !entry["valid"].(bool) && err == nil {
			t.Fatalf("%s: signing schema accepted invalid timestamp", entry["name"])
		}
	}
}

func TestProtocolSpecConfigurationVectors(t *testing.T) {
	root := specRoot(t)
	cache := documentCache{}
	schemaPath := filepath.Join(root, "schemas", "configuration-vectors.schema.json")
	schema, err := cache.load(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	value, err := cache.load(filepath.Join(root, "fixtures", "configuration-vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateJSONSchema(cache, root, schemaPath, schema, value, "$"); err != nil {
		t.Fatal(err)
	}
	vector := value.(map[string]any)
	bundle := vector["bundle"].(map[string]any)
	if err := validateConfigurationSemantics(bundle); err != nil {
		t.Fatal(err)
	}
	canonical, err := canonicalizeRFC8785(bundle["content"])
	if err != nil {
		t.Fatal(err)
	}
	if string(canonical) != vector["expectedCanonicalContent"] {
		t.Fatalf("canonical content mismatch\n got: %s\nwant: %s", canonical, vector["expectedCanonicalContent"])
	}
	sum := sha256.Sum256(canonical)
	if got := hex.EncodeToString(sum[:]); got != vector["expectedContentSha256"] {
		t.Fatalf("content checksum = %s, want %s", got, vector["expectedContentSha256"])
	}
	message := configurationSignatureMessage(bundle)
	if message != vector["expectedSignatureMessage"] {
		t.Fatalf("signature message mismatch\n got: %q\nwant: %q", message, vector["expectedSignatureMessage"])
	}
	publicTestKey := vector["publicTestKey"].(map[string]any)
	if publicTestKey["keyId"] != bundle["signature"].(map[string]any)["keyId"] {
		t.Fatal("configuration vector public key ID does not match signature key ID")
	}
	publicKey, err := base64.StdEncoding.DecodeString(publicTestKey["publicKeyBase64"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if len(publicKey) != ed25519.PublicKeySize {
		t.Fatalf("public key length = %d", len(publicKey))
	}
	if err := verifyConfigurationBundle(bundle, ed25519.PublicKey(publicKey)); err != nil {
		t.Fatal(err)
	}

	for _, raw := range vector["mutations"].([]any) {
		mutation := raw.(map[string]any)
		mutated := cloneJSON(t, bundle).(map[string]any)
		if err := replaceJSONPointer(mutated, mutation["jsonPointer"].(string), mutation["replacement"]); err != nil {
			t.Fatal(err)
		}
		err := verifyConfigurationBundle(mutated, ed25519.PublicKey(publicKey))
		if err == nil {
			t.Fatalf("%s: mutated bundle verified", mutation["name"])
		}
		switch mutation["expectedFailure"] {
		case "content_checksum":
			if !errors.Is(err, errConfigurationContentChecksum) {
				t.Fatalf("%s: got %v, want content checksum failure", mutation["name"], err)
			}
		case "signature_encoding":
			if !errors.Is(err, errConfigurationSignatureEncoding) {
				t.Fatalf("%s: got %v, want signature encoding failure", mutation["name"], err)
			}
			originalText := bundle["signature"].(map[string]any)["value"].(string)
			aliasText := mutated["signature"].(map[string]any)["value"].(string)
			originalBytes, originalErr := base64.RawURLEncoding.DecodeString(originalText)
			aliasBytes, aliasErr := base64.RawURLEncoding.DecodeString(aliasText)
			if originalErr != nil || aliasErr != nil || !bytes.Equal(originalBytes, aliasBytes) {
				t.Fatalf("%s: alias must decode to the original signature bytes", mutation["name"])
			}
			bundleSchemaPath := filepath.Join(root, "schemas", "configuration-bundle.schema.json")
			bundleSchema, schemaErr := cache.load(bundleSchemaPath)
			if schemaErr != nil {
				t.Fatal(schemaErr)
			}
			if schemaErr := validateJSONSchema(cache, root, bundleSchemaPath, bundleSchema, mutated, "$"); schemaErr == nil {
				t.Fatalf("%s: configuration schema accepted signature alias", mutation["name"])
			}
		case "signature":
			if !errors.Is(err, errConfigurationSignature) {
				t.Fatalf("%s: got %v, want signature failure", mutation["name"], err)
			}
		}

		bundleSchemaPath := filepath.Join(root, "schemas", "configuration-bundle.schema.json")
		bundleSchema, err := cache.load(bundleSchemaPath)
		if err != nil {
			t.Fatal(err)
		}
		for _, raw := range vector["integerBoundaryCases"].([]any) {
			entry := raw.(map[string]any)
			candidate := cloneJSON(t, bundle).(map[string]any)
			if err := replaceJSONPointer(candidate, entry["jsonPointer"].(string), entry["value"]); err != nil {
				t.Fatal(err)
			}
			err := validateJSONSchema(cache, root, bundleSchemaPath, bundleSchema, candidate, "$")
			if entry["valid"].(bool) && err != nil {
				t.Fatalf("%s: maximum safe integer rejected: %v", entry["name"], err)
			}
			if !entry["valid"].(bool) && err == nil {
				t.Fatalf("%s: unsafe integer accepted", entry["name"])
			}
			if !entry["valid"].(bool) {
				if strings.HasPrefix(entry["jsonPointer"].(string), "/content/") {
					if _, err := canonicalizeRFC8785(candidate["content"]); err == nil {
						t.Fatalf("%s: canonicalizer accepted unsafe integer", entry["name"])
					}
				} else if err := verifyConfigurationBundle(candidate, ed25519.PublicKey(publicKey)); !errors.Is(err, errConfigurationIntegerRange) {
					t.Fatalf("%s: verifier got %v, want integer range failure", entry["name"], err)
				}
			}
		}
	}
}

func TestProtocolSpecFleetReports(t *testing.T) {
	root := specRoot(t)
	cache := documentCache{}
	schemaPath := filepath.Join(root, "schemas", "fleet-registration.schema.json")
	schema, err := cache.load(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"fleet-accepted.json", "fleet-applied.json", "fleet-rejected.json"} {
		value, err := cache.load(filepath.Join(root, "fixtures", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := validateJSONSchema(cache, root, schemaPath, schema, value, "$"); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := validateFleetSemantics(value.(map[string]any)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}

	applied, err := cache.load(filepath.Join(root, "fixtures", "fleet-applied.json"))
	if err != nil {
		t.Fatal(err)
	}
	contradictory := cloneJSON(t, applied).(map[string]any)
	contradictory["application"].(map[string]any)["lastKnownGood"].(map[string]any)["contentChecksum"] =
		"1dcaf1234b3749710bc6d5b7f14cf271bf082851ad6582334b28d5970df08be0"
	if err := validateJSONSchema(cache, root, schemaPath, schema, contradictory, "$"); err != nil {
		t.Fatalf("cross-field mutation should remain structurally valid: %v", err)
	}
	if err := validateFleetSemantics(contradictory); err == nil {
		t.Fatal("cross-field validation accepted applied candidate/LKG mismatch")
	}

	vectors, err := cache.load(filepath.Join(root, "fixtures", "configuration-vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range vectors.(map[string]any)["fleetGenerationBoundaryCases"].([]any) {
		entry := raw.(map[string]any)
		fixture, err := cache.load(filepath.Join(root, "fixtures", entry["fixture"].(string)))
		if err != nil {
			t.Fatal(err)
		}
		candidate := cloneJSON(t, fixture).(map[string]any)
		if err := replaceJSONPointer(candidate, entry["jsonPointer"].(string), entry["value"]); err != nil {
			t.Fatal(err)
		}
		err = validateJSONSchema(cache, root, schemaPath, schema, candidate, "$")
		if entry["valid"].(bool) && err != nil {
			t.Fatalf("%s: maximum safe generation rejected: %v", entry["name"], err)
		}
		if !entry["valid"].(bool) && err == nil {
			t.Fatalf("%s: unsafe fleet generation accepted", entry["name"])
		}
	}
}

func TestProtocolSpecWithDraft202012Validator(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	if err := exec.Command(python, "-c", "import jsonschema, referencing").Run(); err != nil {
		t.Skip("python jsonschema/referencing packages are unavailable")
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate validator source")
	}
	script := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", "scripts", "validate-jsonschema.py"))
	command := exec.Command(python, script)
	command.Dir = filepath.Dir(filepath.Dir(script))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("standards validator failed: %v\n%s", err, output)
	}
}

func TestProtocolSpecOpenAPI(t *testing.T) {
	root := specRoot(t)
	cache := documentCache{}
	path := filepath.Join(root, "openapi.json")
	value, err := cache.load(path)
	if err != nil {
		t.Fatal(err)
	}
	api := value.(map[string]any)
	if api["openapi"] != "3.1.0" {
		t.Fatal("OpenAPI version must be 3.1.0")
	}
	if api["x-hookship-schema-registry"] != "schema-registry.json" {
		t.Fatal("OpenAPI must identify the Draft 2020-12 schema registry")
	}
	transport := api["x-hookship-transport-security"].(map[string]any)
	if transport["nonLoopbackScheme"] != "https" ||
		transport["minimumTlsVersion"] != "1.2" ||
		transport["preferredTlsVersion"] != "1.3" ||
		transport["redirectPolicy"] != "forbid" {
		t.Fatal("OpenAPI transport security extension is incomplete")
	}
	for _, raw := range api["servers"].([]any) {
		if !strings.HasPrefix(raw.(map[string]any)["url"].(string), "https://") {
			t.Fatal("OpenAPI must not advertise a plaintext server")
		}
	}
	paths := api["paths"].(map[string]any)
	if len(paths) != 2 {
		t.Fatalf("OpenAPI must expose exactly two paths, got %d", len(paths))
	}
	post := paths["/v1/event-ingests"].(map[string]any)["post"].(map[string]any)
	get := paths["/v1/event-ingests/{ingest_id}"].(map[string]any)["get"].(map[string]any)
	requireResponses(t, post, "200", "202", "413", "422", "429", "503")
	requireResponses(t, get, "200", "404", "429", "503")
	if _, exists := get["responses"].(map[string]any)["403"]; exists {
		t.Fatal("status lookup must not distinguish foreign ingests with HTTP 403")
	}
	expectedProblemComponents := map[string]string{
		"400": "Problem400",
		"401": "Problem401",
		"403": "Problem403",
		"404": "Problem404",
		"409": "Problem409",
		"413": "Problem413",
		"415": "Problem415",
		"422": "Problem422",
		"429": "Problem429",
		"503": "Problem503",
	}
	responseComponents := api["components"].(map[string]any)["responses"].(map[string]any)
	for status, component := range expectedProblemComponents {
		response := responseComponents[component].(map[string]any)
		schemaRef := response["content"].(map[string]any)["application/problem+json"].(map[string]any)["schema"].(map[string]any)["$ref"].(string)
		if !strings.HasSuffix(schemaRef, "#/$defs/status"+status) {
			t.Fatalf("%s schema does not bind HTTP %s", component, status)
		}
	}
	for _, operation := range []map[string]any{post, get} {
		for status, raw := range operation["responses"].(map[string]any) {
			component, isProblem := expectedProblemComponents[status]
			if !isProblem {
				continue
			}
			if raw.(map[string]any)["$ref"] != "#/components/responses/"+component {
				t.Fatalf("HTTP %s does not use its status-specific problem component", status)
			}
		}
	}

	parameters := post["parameters"].([]any)
	for _, forbidden := range []string{"tenant", "environment", "producer"} {
		for _, parameter := range parameters {
			ref := parameter.(map[string]any)["$ref"].(string)
			if strings.Contains(strings.ToLower(ref), forbidden) {
				t.Fatalf("producer ingest must not accept client-selected %s identity", forbidden)
			}
		}
	}
	timestamp := api["components"].(map[string]any)["parameters"].(map[string]any)["Timestamp"].(map[string]any)
	timestampSchema := timestamp["schema"].(map[string]any)
	if timestampSchema["pattern"] != "^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$" {
		t.Fatal("OpenAPI timestamp is not constrained to exact whole-second UTC")
	}

	statusSchemaPath := filepath.Join(root, "schemas", "event-ingest-status.schema.json")
	statusValue, err := cache.load(statusSchemaPath)
	if err != nil {
		t.Fatal(err)
	}
	properties := statusValue.(map[string]any)["properties"].(map[string]any)
	for _, forbidden := range []string{"payload", "rawBody", "bodyBase64", "headers", "downloadUrl"} {
		if _, exists := properties[forbidden]; exists {
			t.Fatalf("status schema exposes forbidden payload field %q", forbidden)
		}
	}
}

func TestProtocolSpecOpenAPIProblemResponses(t *testing.T) {
	root := specRoot(t)
	cache := documentCache{}
	openAPIPath := filepath.Join(root, "openapi.json")
	openAPIValue, err := cache.load(openAPIPath)
	if err != nil {
		t.Fatal(err)
	}
	vectorSchemaPath := filepath.Join(root, "schemas", "problem-response-vectors.schema.json")
	vectorSchema, err := cache.load(vectorSchemaPath)
	if err != nil {
		t.Fatal(err)
	}
	vectorValue, err := cache.load(filepath.Join(root, "fixtures", "problem-responses.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateJSONSchema(cache, root, vectorSchemaPath, vectorSchema, vectorValue, "$"); err != nil {
		t.Fatal(err)
	}

	vectors := map[string]map[string]any{}
	for _, raw := range vectorValue.(map[string]any)["responses"].([]any) {
		entry := raw.(map[string]any)
		vectors[entry["status"].(json.Number).String()] = entry
	}
	components := openAPIValue.(map[string]any)["components"].(map[string]any)["responses"].(map[string]any)
	for status, entry := range vectors {
		component := components["Problem"+status].(map[string]any)
		schemaRef := component["content"].(map[string]any)["application/problem+json"].(map[string]any)["schema"].(map[string]any)["$ref"].(string)
		if schemaRef != entry["schemaRef"] {
			t.Fatalf("HTTP %s OpenAPI ref = %s, want %s", status, schemaRef, entry["schemaRef"])
		}
		targetPath, targetSchema, err := resolveReference(root, cache, openAPIPath, schemaRef)
		if err != nil {
			t.Fatal(err)
		}
		body := entry["body"].(map[string]any)
		if err := validateJSONSchema(cache, root, targetPath, targetSchema, body, "$"); err != nil {
			t.Fatalf("HTTP %s matching fixture rejected: %v", status, err)
		}
		if err := validateJSONSchema(cache, root, targetPath, targetSchema, map[string]any{}, "$"); err == nil {
			t.Fatalf("HTTP %s fragment accepted an empty object", status)
		}
		withExtra := cloneJSON(t, body).(map[string]any)
		withExtra["unexpected"] = true
		if err := validateJSONSchema(cache, root, targetPath, targetSchema, withExtra, "$"); err == nil {
			t.Fatalf("HTTP %s fragment accepted an extra property", status)
		}
	}
}

func TestProtocolSpecChecksums(t *testing.T) {
	root := specRoot(t)
	manifestPath := filepath.Join(root, "checksums.txt")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	expected := map[string]string{}
	var manifestOrder []string
	for lineNumber, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != sha256.Size*2 {
			t.Fatalf("checksums.txt:%d: invalid manifest line", lineNumber+1)
		}
		if _, err := hex.DecodeString(fields[0]); err != nil {
			t.Fatalf("checksums.txt:%d: invalid SHA-256: %v", lineNumber+1, err)
		}
		if _, duplicate := expected[fields[1]]; duplicate {
			t.Fatalf("checksums.txt:%d: duplicate path %s", lineNumber+1, fields[1])
		}
		expected[fields[1]] = fields[0]
		manifestOrder = append(manifestOrder, fields[1])
	}
	if !sort.StringsAreSorted(manifestOrder) {
		t.Fatal("checksums.txt paths must be sorted")
	}

	actualPaths := []string{}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || path == manifestPath {
			return nil
		}
		actualPaths = append(actualPaths, filepath.ToSlash(relative(root, path)))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(actualPaths)
	if !reflect.DeepEqual(actualPaths, manifestOrder) {
		t.Fatalf("checksum manifest paths differ\n got: %v\nwant: %v", manifestOrder, actualPaths)
	}

	for _, relativePath := range actualPaths {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relativePath)))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(content)
		if got := hex.EncodeToString(sum[:]); got != expected[relativePath] {
			t.Fatalf("%s checksum = %s, want %s", relativePath, got, expected[relativePath])
		}
	}
}

func verifyGoldenRequest(t *testing.T, name string, fixture map[string]any) {
	t.Helper()
	request := fixture["request"].(map[string]any)
	headers := request["headers"].(map[string]any)
	body, err := base64.StdEncoding.DecodeString(request["bodyBase64"].(string))
	if err != nil {
		t.Fatalf("%s: invalid bodyBase64: %v", name, err)
	}
	sum := sha256.Sum256(body)
	bodyHash := hex.EncodeToString(sum[:])
	if bodyHash != headers["X-Hookship-Body-SHA256"] {
		t.Fatalf("%s: body hash mismatch", name)
	}

	authorization := headers["Authorization"].(string)
	const prefix = "Hookship-HMAC Credential=cred_test_01,Signature="
	if !strings.HasPrefix(authorization, prefix) {
		t.Fatalf("%s: invalid test authorization", name)
	}
	authorizationSignature := strings.TrimPrefix(authorization, prefix)
	if _, err := decodeCanonicalRawBase64URL(authorizationSignature, sha256.Size); err != nil {
		t.Fatalf("%s: non-canonical authorization signature: %v", name, err)
	}
	requestFields := map[string]any{
		"method":         request["method"],
		"path":           request["path"],
		"credentialId":   "cred_test_01",
		"timestamp":      headers["X-Hookship-Timestamp"],
		"nonce":          headers["X-Hookship-Nonce"],
		"idempotencyKey": headers["Idempotency-Key"],
		"eventId":        headers["X-Hookship-Event-ID"],
		"eventType":      headers["X-Hookship-Event-Type"],
		"route":          headers["X-Hookship-Route"],
		"contentType":    headers["Content-Type"],
		"bodySha256":     headers["X-Hookship-Body-SHA256"],
	}
	secret, err := hex.DecodeString(publicHMACTestKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(canonicalRequest(requestFields)))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if signature != authorizationSignature {
		t.Fatalf("%s: fixture signature mismatch", name)
	}
}

func verifyGoldenResponse(t *testing.T, name string, fixture map[string]any) {
	t.Helper()
	expected := fixture["expected"].(map[string]any)
	status, err := strconv.Atoi(expected["status"].(json.Number).String())
	if err != nil {
		t.Fatalf("%s: invalid expected status: %v", name, err)
	}
	body := expected["body"].(map[string]any)
	if bodyStatus, exists := body["status"]; exists {
		value, err := strconv.Atoi(bodyStatus.(json.Number).String())
		if err != nil || value != status {
			t.Fatalf("%s: problem status does not match HTTP status", name)
		}
	}
	if containsForbiddenPayloadField(body) {
		t.Fatalf("%s: response fixture contains a raw-payload field", name)
	}

	switch name {
	case "accepted":
		if status != 202 || body["duplicate"] != false || body["idempotencyState"] != "committed" {
			t.Fatal("accepted fixture must be HTTP 202 with committed idempotency")
		}
	case "duplicate":
		if status != 200 || body["duplicate"] != true || body["idempotencyState"] != "committed" {
			t.Fatal("duplicate fixture must be HTTP 200 only for committed idempotency")
		}
	case "conflict":
		if status != 409 || body["code"] != "idempotency_conflict" || body["retryable"] != false {
			t.Fatal("conflict fixture must be a non-retryable idempotency conflict")
		}
	case "rejected":
		if status != 422 || body["code"] != "invalid_event" || body["retryable"] != false {
			t.Fatal("rejected fixture must be a non-retryable invalid event")
		}
	}
}

func containsForbiddenPayloadField(value any) bool {
	forbidden := map[string]bool{
		"payload":     true,
		"rawBody":     true,
		"bodyBase64":  true,
		"headers":     true,
		"downloadUrl": true,
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if forbidden[key] || containsForbiddenPayloadField(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsForbiddenPayloadField(child) {
				return true
			}
		}
	}
	return false
}

var wholeSecondUTCPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$`)

const maxSafeInteger int64 = 9007199254740991
const publicHMACTestKeyHex = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

func validWholeSecondUTC(value string) bool {
	if !wholeSecondUTCPattern.MatchString(value) {
		return false
	}
	_, err := time.Parse("2006-01-02T15:04:05Z", value)
	return err == nil
}

func safeJSONInteger(value json.Number) (int64, bool) {
	number, err := strconv.ParseInt(value.String(), 10, 64)
	return number, err == nil && number >= -maxSafeInteger && number <= maxSafeInteger
}

func decodeCanonicalRawBase64URL(value string, size int) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil {
		return nil, err
	}
	if len(decoded) != size {
		return nil, fmt.Errorf("decoded length = %d, want %d", len(decoded), size)
	}
	if base64.RawURLEncoding.EncodeToString(decoded) != value {
		return nil, errors.New("base64url text is not the canonical unpadded encoding")
	}
	return decoded, nil
}

func cloneJSON(t *testing.T, value any) any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var clone any
	if err := decoder.Decode(&clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

var (
	errConfigurationContentChecksum   = errors.New("configuration content checksum mismatch")
	errConfigurationIntegerRange      = errors.New("configuration integer exceeds Number.MAX_SAFE_INTEGER")
	errConfigurationSignatureEncoding = errors.New("configuration signature encoding is not canonical")
	errConfigurationSignature         = errors.New("configuration signature invalid")
)

func verifyConfigurationBundle(bundle map[string]any, publicKey ed25519.PublicKey) error {
	if _, ok := safeJSONInteger(bundle["generation"].(json.Number)); !ok {
		return errConfigurationIntegerRange
	}
	canonical, err := canonicalizeRFC8785(bundle["content"])
	if err != nil {
		return err
	}
	sum := sha256.Sum256(canonical)
	if hex.EncodeToString(sum[:]) != bundle["contentChecksum"] {
		return errConfigurationContentChecksum
	}
	signature := bundle["signature"].(map[string]any)
	decoded, err := decodeCanonicalRawBase64URL(signature["value"].(string), ed25519.SignatureSize)
	if err != nil {
		return errConfigurationSignatureEncoding
	}
	if !ed25519.Verify(publicKey, []byte(configurationSignatureMessage(bundle)), decoded) {
		return errConfigurationSignature
	}
	return nil
}

func configurationSignatureMessage(bundle map[string]any) string {
	signature := bundle["signature"].(map[string]any)
	return strings.Join([]string{
		"hookship-configuration-bundle-v1",
		"schema-version:" + bundle["schemaVersion"].(string),
		"bundle-id:" + bundle["bundleId"].(string),
		"generation:" + bundle["generation"].(json.Number).String(),
		"content-sha256:" + bundle["contentChecksum"].(string),
		"key-id:" + signature["keyId"].(string),
		"signed-at:" + signature["signedAt"].(string),
	}, "\n")
}

func validateConfigurationSemantics(bundle map[string]any) error {
	content := bundle["content"].(map[string]any)
	endpoints, err := resourceIDs(content["endpoints"].([]any), "endpointId")
	if err != nil {
		return err
	}
	retries, err := resourceIDs(content["retryPolicies"].([]any), "retryPolicyId")
	if err != nil {
		return err
	}
	throttles, err := resourceIDs(content["throttlePolicies"].([]any), "throttlePolicyId")
	if err != nil {
		return err
	}
	payloads, err := resourceIDs(content["payloadPolicies"].([]any), "payloadPolicyId")
	if err != nil {
		return err
	}
	secrets, err := resourceIDs(content["sealedSecrets"].([]any), "secretId")
	if err != nil {
		return err
	}
	if _, err := resourceIDs(content["subscriptions"].([]any), "subscriptionId"); err != nil {
		return err
	}
	for _, raw := range content["endpoints"].([]any) {
		endpoint := raw.(map[string]any)
		if !secrets[endpoint["signingSecretId"].(string)] {
			return fmt.Errorf("endpoint %s references an unknown sealed secret", endpoint["endpointId"])
		}
	}
	for _, raw := range content["subscriptions"].([]any) {
		subscription := raw.(map[string]any)
		checks := []struct {
			field string
			set   map[string]bool
		}{
			{"endpointId", endpoints},
			{"retryPolicyId", retries},
			{"throttlePolicyId", throttles},
			{"payloadPolicyId", payloads},
		}
		for _, check := range checks {
			if !check.set[subscription[check.field].(string)] {
				return fmt.Errorf("subscription %s has unresolved %s", subscription["subscriptionId"], check.field)
			}
		}
	}
	for _, raw := range content["retryPolicies"].([]any) {
		policy := raw.(map[string]any)
		initial, _ := strconv.ParseInt(policy["initialDelaySeconds"].(json.Number).String(), 10, 64)
		maximum, _ := strconv.ParseInt(policy["maxDelaySeconds"].(json.Number).String(), 10, 64)
		if initial > maximum {
			return fmt.Errorf("retry policy %s initial delay exceeds maximum", policy["retryPolicyId"])
		}
	}
	for _, raw := range content["sealedSecrets"].([]any) {
		secret := raw.(map[string]any)
		ciphertext, err := base64.StdEncoding.DecodeString(secret["ciphertextBase64"].(string))
		if err != nil {
			return err
		}
		sum := sha256.Sum256(ciphertext)
		if hex.EncodeToString(sum[:]) != secret["ciphertextSha256"] {
			return fmt.Errorf("sealed secret %s ciphertext checksum mismatch", secret["secretId"])
		}
	}
	if hasForbiddenConfigurationField(content) {
		return errors.New("configuration contains a plaintext or private-key field")
	}
	return nil
}

func resourceIDs(resources []any, idField string) (map[string]bool, error) {
	ids := make(map[string]bool, len(resources))
	for _, raw := range resources {
		resource := raw.(map[string]any)
		id := resource[idField].(string)
		if ids[id] {
			return nil, fmt.Errorf("duplicate %s %q", idField, id)
		}
		ids[id] = true
	}
	return ids, nil
}

func hasForbiddenConfigurationField(value any) bool {
	forbidden := map[string]bool{
		"plaintext":   true,
		"privateKey":  true,
		"secretValue": true,
		"hmacSecret":  true,
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if forbidden[key] || hasForbiddenConfigurationField(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if hasForbiddenConfigurationField(child) {
				return true
			}
		}
	}
	return false
}

func validateFleetSemantics(report map[string]any) error {
	candidate := report["candidate"].(map[string]any)
	application := report["application"].(map[string]any)
	supported := report["supportedVersions"].(map[string]any)["configurationBundle"].([]any)
	compatible := false
	for _, version := range supported {
		if version == candidate["bundleSchemaVersion"] {
			compatible = true
			break
		}
	}
	if !compatible {
		return errors.New("candidate compatibility acknowledgement is outside supported versions")
	}

	state := application["state"].(string)
	lastKnownGood, hasLastKnownGood := application["lastKnownGood"].(map[string]any)
	switch state {
	case "applied":
		if !hasLastKnownGood || !sameBundleIdentity(candidate, lastKnownGood) {
			return errors.New("applied report last-known-good must equal the candidate")
		}
		if application["appliedAt"] != lastKnownGood["appliedAt"] {
			return errors.New("applied report timestamps must match")
		}
	case "accepted", "rejected":
		if hasLastKnownGood && sameBundleIdentity(candidate, lastKnownGood) {
			return fmt.Errorf("%s candidate cannot already be last-known-good", state)
		}
		if hasLastKnownGood {
			candidateGeneration, _ := strconv.ParseInt(candidate["generation"].(json.Number).String(), 10, 64)
			lkgGeneration, _ := strconv.ParseInt(lastKnownGood["generation"].(json.Number).String(), 10, 64)
			if candidateGeneration <= lkgGeneration {
				return fmt.Errorf("%s candidate generation must be newer than last-known-good", state)
			}
		}
	default:
		return fmt.Errorf("unknown application state %q", state)
	}
	return nil
}

func sameBundleIdentity(candidate, lastKnownGood map[string]any) bool {
	return candidate["bundleId"] == lastKnownGood["bundleId"] &&
		reflect.DeepEqual(candidate["generation"], lastKnownGood["generation"]) &&
		candidate["contentChecksum"] == lastKnownGood["contentChecksum"]
}

func replaceJSONPointer(root any, pointer string, replacement any) error {
	parts := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	current := root
	for index, rawPart := range parts {
		part := strings.ReplaceAll(strings.ReplaceAll(rawPart, "~1", "/"), "~0", "~")
		last := index == len(parts)-1
		switch typed := current.(type) {
		case map[string]any:
			if _, exists := typed[part]; !exists {
				return fmt.Errorf("JSON pointer part %q does not exist", part)
			}
			if last {
				typed[part] = replacement
				return nil
			}
			current = typed[part]
		case []any:
			itemIndex, err := strconv.Atoi(part)
			if err != nil || itemIndex < 0 || itemIndex >= len(typed) {
				return fmt.Errorf("invalid JSON pointer array index %q", part)
			}
			if last {
				typed[itemIndex] = replacement
				return nil
			}
			current = typed[itemIndex]
		default:
			return fmt.Errorf("JSON pointer traverses scalar at %q", part)
		}
	}
	return errors.New("empty JSON pointer")
}

func canonicalizeRFC8785(value any) ([]byte, error) {
	var output bytes.Buffer
	if err := writeCanonicalJSON(&output, value); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func writeCanonicalJSON(output *bytes.Buffer, value any) error {
	switch typed := value.(type) {
	case nil:
		output.WriteString("null")
	case bool:
		if typed {
			output.WriteString("true")
		} else {
			output.WriteString("false")
		}
	case string:
		writeCanonicalString(output, typed)
	case json.Number:
		number, ok := safeJSONInteger(typed)
		if !ok {
			return errConfigurationIntegerRange
		}
		output.WriteString(strconv.FormatInt(number, 10))
	case []any:
		output.WriteByte('[')
		for index, child := range typed {
			if index > 0 {
				output.WriteByte(',')
			}
			if err := writeCanonicalJSON(output, child); err != nil {
				return err
			}
		}
		output.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(left, right int) bool {
			return compareUTF16(keys[left], keys[right]) < 0
		})
		output.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				output.WriteByte(',')
			}
			writeCanonicalString(output, key)
			output.WriteByte(':')
			if err := writeCanonicalJSON(output, typed[key]); err != nil {
				return err
			}
		}
		output.WriteByte('}')
	default:
		return fmt.Errorf("unsupported canonical JSON type %T", value)
	}
	return nil
}

func writeCanonicalString(output *bytes.Buffer, value string) {
	const hexDigits = "0123456789abcdef"
	output.WriteByte('"')
	for _, character := range value {
		switch character {
		case '"', '\\':
			output.WriteByte('\\')
			output.WriteRune(character)
		case '\b':
			output.WriteString(`\b`)
		case '\t':
			output.WriteString(`\t`)
		case '\n':
			output.WriteString(`\n`)
		case '\f':
			output.WriteString(`\f`)
		case '\r':
			output.WriteString(`\r`)
		default:
			if character < 0x20 {
				output.WriteString(`\u00`)
				output.WriteByte(hexDigits[byte(character)>>4])
				output.WriteByte(hexDigits[byte(character)&0x0f])
			} else {
				output.WriteRune(character)
			}
		}
	}
	output.WriteByte('"')
}

func compareUTF16(left, right string) int {
	leftUnits := utf16.Encode([]rune(left))
	rightUnits := utf16.Encode([]rune(right))
	limit := len(leftUnits)
	if len(rightUnits) < limit {
		limit = len(rightUnits)
	}
	for index := 0; index < limit; index++ {
		if leftUnits[index] < rightUnits[index] {
			return -1
		}
		if leftUnits[index] > rightUnits[index] {
			return 1
		}
	}
	switch {
	case len(leftUnits) < len(rightUnits):
		return -1
	case len(leftUnits) > len(rightUnits):
		return 1
	default:
		return 0
	}
}

func canonicalRequest(request map[string]any) string {
	return strings.Join([]string{
		"hookship-hmac-sha256-v1",
		"method:" + request["method"].(string),
		"path:" + request["path"].(string),
		"credential-id:" + request["credentialId"].(string),
		"timestamp:" + request["timestamp"].(string),
		"nonce:" + request["nonce"].(string),
		"idempotency-key:" + request["idempotencyKey"].(string),
		"event-id:" + request["eventId"].(string),
		"event-type:" + request["eventType"].(string),
		"route:" + request["route"].(string),
		"content-type:" + request["contentType"].(string),
		"body-sha256:" + request["bodySha256"].(string),
	}, "\n")
}

func requireResponses(t *testing.T, operation map[string]any, statuses ...string) {
	t.Helper()
	responses := operation["responses"].(map[string]any)
	for _, status := range statuses {
		if _, ok := responses[status]; !ok {
			t.Fatalf("operation is missing response %s", status)
		}
	}
}

func specRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate spec validation source")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", "spec", "v1"))
	if _, err := os.Stat(root); err != nil {
		t.Fatal(err)
	}
	return root
}

func relative(root, path string) string {
	value, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return value
}

func (cache documentCache) load(path string) (any, error) {
	path = filepath.Clean(path)
	if value, ok := cache[path]; ok {
		return value, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, fmt.Errorf("trailing JSON data: %w", err)
	}
	cache[path] = value
	return value, nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := consumeJSONValue(decoder, "$"); err != nil {
		return err
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("unexpected trailing token %v", token)
		}
		return err
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder, path string) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("%s: object key is not a string", path)
			}
			if seen[key] {
				return fmt.Errorf("%s: duplicate object key %q", path, key)
			}
			seen[key] = true
			if err := consumeJSONValue(decoder, path+"."+key); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return fmt.Errorf("%s: invalid object terminator", path)
		}
	case '[':
		index := 0
		for decoder.More() {
			if err := consumeJSONValue(decoder, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
			index++
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return fmt.Errorf("%s: invalid array terminator", path)
		}
	default:
		return fmt.Errorf("%s: unexpected delimiter %q", path, delim)
	}
	return nil
}

func checkSchemaKeywords(schema map[string]any, path string) error {
	allowed := map[string]bool{
		"$schema": true, "$id": true, "$ref": true, "$defs": true,
		"title": true, "description": true, "type": true, "const": true,
		"enum": true, "oneOf": true, "allOf": true, "required": true,
		"properties": true, "additionalProperties": true, "items": true,
		"minLength": true, "maxLength": true, "pattern": true, "format": true,
		"minimum": true, "maximum": true, "minItems": true, "maxItems": true,
		"uniqueItems": true, "minProperties": true, "maxProperties": true,
	}
	for key := range schema {
		if !allowed[key] {
			return fmt.Errorf("%s: unsupported JSON Schema keyword %q", path, key)
		}
	}
	if properties, ok := schema["properties"].(map[string]any); ok {
		for name, child := range properties {
			if err := checkSchemaKeywords(child.(map[string]any), path+".properties."+name); err != nil {
				return err
			}
		}
	}
	if definitions, ok := schema["$defs"].(map[string]any); ok {
		for name, child := range definitions {
			if err := checkSchemaKeywords(child.(map[string]any), path+".$defs."+name); err != nil {
				return err
			}
		}
	}
	for _, key := range []string{"oneOf", "allOf"} {
		if alternatives, ok := schema[key].([]any); ok {
			for index, child := range alternatives {
				if err := checkSchemaKeywords(child.(map[string]any), fmt.Sprintf("%s.%s[%d]", path, key, index)); err != nil {
					return err
				}
			}
		}
	}
	if child, ok := schema["items"].(map[string]any); ok {
		if err := checkSchemaKeywords(child, path+".items"); err != nil {
			return err
		}
	}
	if child, ok := schema["additionalProperties"].(map[string]any); ok {
		if err := checkSchemaKeywords(child, path+".additionalProperties"); err != nil {
			return err
		}
	}
	return nil
}

func checkReferences(root string, cache documentCache, documentPath string, value any) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "$ref" {
				ref, ok := child.(string)
				if !ok {
					return errors.New("$ref must be a string")
				}
				if _, _, err := resolveReference(root, cache, documentPath, ref); err != nil {
					return fmt.Errorf("%s: %w", ref, err)
				}
			}
			if err := checkReferences(root, cache, documentPath, child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := checkReferences(root, cache, documentPath, child); err != nil {
				return err
			}
		}
	}
	return nil
}

func loadSchemaRegistry(root string, cache documentCache) (map[string]string, error) {
	value, err := cache.load(filepath.Join(root, "schema-registry.json"))
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok || object["schemaVersion"] != "1" ||
		object["dialect"] != "https://json-schema.org/draft/2020-12/schema" {
		return nil, errors.New("invalid schema registry header")
	}
	resources, ok := object["resources"].([]any)
	if !ok {
		return nil, errors.New("schema registry resources must be an array")
	}
	registry := make(map[string]string, len(resources))
	for index, raw := range resources {
		resource, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("schema registry resource %d is not an object", index)
		}
		id, idOK := resource["id"].(string)
		pathValue, pathOK := resource["path"].(string)
		if !idOK || !pathOK || !strings.HasPrefix(id, "urn:hookship:spec:v1:schema:") {
			return nil, fmt.Errorf("schema registry resource %d is invalid", index)
		}
		if _, duplicate := registry[id]; duplicate {
			return nil, fmt.Errorf("duplicate schema registry id %q", id)
		}
		path := filepath.Clean(filepath.Join(root, filepath.FromSlash(pathValue)))
		relativePath, err := filepath.Rel(root, path)
		if err != nil || relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("schema registry path escapes spec/v1: %q", pathValue)
		}
		registry[id] = path
	}
	return registry, nil
}

func resolveReference(root string, cache documentCache, documentPath, ref string) (string, any, error) {
	filePart, fragment, _ := strings.Cut(ref, "#")
	targetPath := documentPath
	if filePart != "" {
		if !strings.HasPrefix(filePart, "urn:hookship:spec:v1:schema:") {
			return "", nil, fmt.Errorf("cross-document reference must use a registered absolute HookShip URN: %s", ref)
		}
		registry, err := loadSchemaRegistry(root, cache)
		if err != nil {
			return "", nil, err
		}
		var exists bool
		targetPath, exists = registry[filePart]
		if !exists {
			return "", nil, fmt.Errorf("unregistered schema id %q", filePart)
		}
	}
	relativePath, err := filepath.Rel(root, targetPath)
	if err != nil || relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
		return "", nil, fmt.Errorf("reference escapes spec/v1: %s", ref)
	}
	value, err := cache.load(targetPath)
	if err != nil {
		return "", nil, err
	}
	if filePart != "" {
		target, ok := value.(map[string]any)
		if !ok || target["$id"] != filePart {
			return "", nil, fmt.Errorf("registered schema %q has a mismatched $id", filePart)
		}
	}
	if fragment == "" {
		return targetPath, value, nil
	}
	if !strings.HasPrefix(fragment, "/") {
		return "", nil, fmt.Errorf("unsupported reference fragment #%s", fragment)
	}
	current := value
	for _, rawPart := range strings.Split(strings.TrimPrefix(fragment, "/"), "/") {
		part := strings.ReplaceAll(strings.ReplaceAll(rawPart, "~1", "/"), "~0", "~")
		object, ok := current.(map[string]any)
		if !ok {
			return "", nil, fmt.Errorf("fragment traverses non-object at %q", part)
		}
		current, ok = object[part]
		if !ok {
			return "", nil, fmt.Errorf("fragment part %q does not exist", part)
		}
	}
	return targetPath, current, nil
}

func validateJSONSchema(cache documentCache, root, schemaPath string, schemaValue, instance any, instancePath string) error {
	schema, ok := schemaValue.(map[string]any)
	if !ok {
		return fmt.Errorf("%s: schema is not an object", instancePath)
	}
	if ref, ok := schema["$ref"].(string); ok {
		targetPath, target, err := resolveReference(root, cache, schemaPath, ref)
		if err != nil {
			return fmt.Errorf("%s: %w", instancePath, err)
		}
		return validateJSONSchema(cache, root, targetPath, target, instance, instancePath)
	}
	if alternatives, ok := schema["oneOf"].([]any); ok {
		matches := 0
		var lastErr error
		for _, alternative := range alternatives {
			if err := validateJSONSchema(cache, root, schemaPath, alternative, instance, instancePath); err == nil {
				matches++
			} else {
				lastErr = err
			}
		}
		if matches != 1 {
			return fmt.Errorf("%s: oneOf matched %d alternatives (last error: %v)", instancePath, matches, lastErr)
		}
	}
	if alternatives, ok := schema["allOf"].([]any); ok {
		for _, alternative := range alternatives {
			if err := validateJSONSchema(cache, root, schemaPath, alternative, instance, instancePath); err != nil {
				return err
			}
		}
	}
	if expected, ok := schema["const"]; ok && !reflect.DeepEqual(expected, instance) {
		return fmt.Errorf("%s: value does not match const", instancePath)
	}
	if values, ok := schema["enum"].([]any); ok {
		matched := false
		for _, value := range values {
			if reflect.DeepEqual(value, instance) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s: value is not in enum", instancePath)
		}
	}
	if expectedType, ok := schema["type"].(string); ok && !matchesJSONType(expectedType, instance) {
		return fmt.Errorf("%s: value is not type %s", instancePath, expectedType)
	}

	switch value := instance.(type) {
	case map[string]any:
		if err := validateObject(cache, root, schemaPath, schema, value, instancePath); err != nil {
			return err
		}
	case []any:
		if err := validateArray(cache, root, schemaPath, schema, value, instancePath); err != nil {
			return err
		}
	case string:
		if err := validateString(schema, value, instancePath); err != nil {
			return err
		}
	case json.Number:
		if err := validateNumber(schema, value, instancePath); err != nil {
			return err
		}
	}
	return nil
}

func matchesJSONType(expected string, value any) bool {
	switch expected {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		_, ok := value.(json.Number)
		return ok
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		_, err := strconv.ParseInt(number.String(), 10, 64)
		return err == nil
	case "null":
		return value == nil
	default:
		return false
	}
}

func validateObject(cache documentCache, root, schemaPath string, schema, value map[string]any, instancePath string) error {
	if minimum, ok := schema["minProperties"].(json.Number); ok {
		count, _ := strconv.Atoi(minimum.String())
		if len(value) < count {
			return fmt.Errorf("%s: has fewer than %d properties", instancePath, count)
		}
	}
	if maximum, ok := schema["maxProperties"].(json.Number); ok {
		count, _ := strconv.Atoi(maximum.String())
		if len(value) > count {
			return fmt.Errorf("%s: has more than %d properties", instancePath, count)
		}
	}
	if required, ok := schema["required"].([]any); ok {
		for _, rawName := range required {
			name := rawName.(string)
			if _, exists := value[name]; !exists {
				return fmt.Errorf("%s: missing required property %q", instancePath, name)
			}
		}
	}
	properties, _ := schema["properties"].(map[string]any)
	for name, child := range value {
		if propertySchema, exists := properties[name]; exists {
			if err := validateJSONSchema(cache, root, schemaPath, propertySchema, child, instancePath+"."+name); err != nil {
				return err
			}
			continue
		}
		switch additional := schema["additionalProperties"].(type) {
		case bool:
			if !additional {
				return fmt.Errorf("%s: unexpected property %q", instancePath, name)
			}
		case map[string]any:
			if err := validateJSONSchema(cache, root, schemaPath, additional, child, instancePath+"."+name); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateArray(cache documentCache, root, schemaPath string, schema map[string]any, value []any, instancePath string) error {
	if minimum, ok := schema["minItems"].(json.Number); ok {
		count, _ := strconv.Atoi(minimum.String())
		if len(value) < count {
			return fmt.Errorf("%s: has fewer than %d items", instancePath, count)
		}
	}
	if maximum, ok := schema["maxItems"].(json.Number); ok {
		count, _ := strconv.Atoi(maximum.String())
		if len(value) > count {
			return fmt.Errorf("%s: has more than %d items", instancePath, count)
		}
	}
	if schema["uniqueItems"] == true {
		seen := map[string]bool{}
		for index, item := range value {
			encoded, err := json.Marshal(item)
			if err != nil {
				return err
			}
			key := string(encoded)
			if seen[key] {
				return fmt.Errorf("%s[%d]: duplicate item", instancePath, index)
			}
			seen[key] = true
		}
	}
	if itemSchema, ok := schema["items"].(map[string]any); ok {
		for index, item := range value {
			if err := validateJSONSchema(cache, root, schemaPath, itemSchema, item, fmt.Sprintf("%s[%d]", instancePath, index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateString(schema map[string]any, value, instancePath string) error {
	length := utf8.RuneCountInString(value)
	if minimum, ok := schema["minLength"].(json.Number); ok {
		count, _ := strconv.Atoi(minimum.String())
		if length < count {
			return fmt.Errorf("%s: string is shorter than %d", instancePath, count)
		}
	}
	if maximum, ok := schema["maxLength"].(json.Number); ok {
		count, _ := strconv.Atoi(maximum.String())
		if length > count {
			return fmt.Errorf("%s: string is longer than %d", instancePath, count)
		}
	}
	if pattern, ok := schema["pattern"].(string); ok {
		compatiblePattern := strings.ReplaceAll(pattern, "(?:", "(")
		expression, err := regexp.Compile(compatiblePattern)
		if err != nil {
			return fmt.Errorf("%s: invalid pattern %q: %w", instancePath, pattern, err)
		}
		if !expression.MatchString(value) {
			return fmt.Errorf("%s: string does not match %q", instancePath, pattern)
		}
	}
	if format, ok := schema["format"].(string); ok {
		switch format {
		case "date-time":
			if _, err := time.Parse(time.RFC3339, value); err != nil {
				return fmt.Errorf("%s: invalid date-time: %w", instancePath, err)
			}
		case "uri":
			parsed, err := url.Parse(value)
			if err != nil || parsed.Scheme == "" {
				return fmt.Errorf("%s: invalid absolute URI", instancePath)
			}
		case "uri-reference":
			if _, err := url.Parse(value); err != nil {
				return fmt.Errorf("%s: invalid URI reference", instancePath)
			}
		}
	}
	return nil
}

func validateNumber(schema map[string]any, value json.Number, instancePath string) error {
	if schema["type"] == "integer" {
		number, err := strconv.ParseInt(value.String(), 10, 64)
		if err != nil {
			return fmt.Errorf("%s: invalid integer: %w", instancePath, err)
		}
		if minimum, ok := schema["minimum"].(json.Number); ok {
			bound, err := strconv.ParseInt(minimum.String(), 10, 64)
			if err != nil {
				return fmt.Errorf("%s: invalid integer minimum: %w", instancePath, err)
			}
			if number < bound {
				return fmt.Errorf("%s: number is less than %s", instancePath, minimum)
			}
		}
		if maximum, ok := schema["maximum"].(json.Number); ok {
			bound, err := strconv.ParseInt(maximum.String(), 10, 64)
			if err != nil {
				return fmt.Errorf("%s: invalid integer maximum: %w", instancePath, err)
			}
			if number > bound {
				return fmt.Errorf("%s: number is greater than %s", instancePath, maximum)
			}
		}
		return nil
	}
	number, err := value.Float64()
	if err != nil {
		return fmt.Errorf("%s: invalid number: %w", instancePath, err)
	}
	if minimum, ok := schema["minimum"].(json.Number); ok {
		bound, _ := minimum.Float64()
		if number < bound {
			return fmt.Errorf("%s: number is less than %s", instancePath, minimum)
		}
	}
	if maximum, ok := schema["maximum"].(json.Number); ok {
		bound, _ := maximum.Float64()
		if number > bound {
			return fmt.Errorf("%s: number is greater than %s", instancePath, maximum)
		}
	}
	return nil
}
