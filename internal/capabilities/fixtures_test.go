package capabilities_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/hookship/hook-service/internal/capabilities"
)

var update = flag.Bool("update", false, "regenerate the canonical backend capability fixtures")

func fixturesDir() string {
	return filepath.Join("..", "..", "spec", "backend-capabilities", "v1", "fixtures")
}

func schemaPath() string {
	return filepath.Join("..", "..", "spec", "backend-capabilities", "v1", "schema.json")
}

// TestCanonicalFixturesMatchOutput guarantees the authoritative fixtures are the
// byte-exact output of the Go marshaler (and therefore of `hookshipctl backend
// describe`). Run with -update to regenerate them.
func TestCanonicalFixturesMatchOutput(t *testing.T) {
	dir := fixturesDir()
	for _, document := range capabilities.Profiles() {
		document := document
		t.Run(document.Profile, func(t *testing.T) {
			canonical, err := capabilities.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, document.Profile+".json")
			if *update {
				if err := os.WriteFile(path, canonical, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			fixture, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read fixture %s: %v (run go test -run TestCanonicalFixtures -update)", path, err)
			}
			if !bytes.Equal(canonical, fixture) {
				t.Fatalf("fixture %s is not the byte-exact marshaler output", path)
			}
		})
	}
}

// TestFixturesAreValidCanonicalDocuments checks structural invariants that must
// hold for every fixture regardless of tooling availability.
func TestFixturesAreValidCanonicalDocuments(t *testing.T) {
	entries, err := os.ReadDir(fixturesDir())
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		seen++
		data, err := os.ReadFile(filepath.Join(fixturesDir(), entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var document capabilities.Document
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&document); err != nil {
			t.Fatalf("%s: does not decode into the canonical Document: %v", entry.Name(), err)
		}
		if document.Schema != capabilities.SchemaID {
			t.Fatalf("%s: $schema = %q", entry.Name(), document.Schema)
		}
		if document.SchemaVersion != capabilities.SchemaVersion {
			t.Fatalf("%s: schemaVersion = %q", entry.Name(), document.SchemaVersion)
		}
		if document.Kind != capabilities.Kind {
			t.Fatalf("%s: kind = %q", entry.Name(), document.Kind)
		}
		if document.Profile+".json" != entry.Name() {
			t.Fatalf("%s: profile %q does not match filename", entry.Name(), document.Profile)
		}
		// Round-trips byte-exactly.
		reencoded, err := capabilities.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(reencoded, data) {
			t.Fatalf("%s: does not round-trip byte-exactly", entry.Name())
		}
	}
	if seen != len(capabilities.Profiles()) {
		t.Fatalf("expected %d fixtures, found %d", len(capabilities.Profiles()), seen)
	}
}

// TestFixturesValidateAgainstSchema runs the independent Draft 2020-12 validator
// against the fixtures. It skips only when Python/jsonschema is unavailable, so
// the schema is exercised wherever the toolchain exists (as in CI).
func TestFixturesValidateAgainstSchema(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	if err := exec.Command(python, "-c", "import jsonschema").Run(); err != nil {
		t.Skip("python jsonschema package is unavailable")
	}
	script := filepath.Join("..", "..", "scripts", "validate-backend-capabilities.py")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("validator script missing: %v", err)
	}
	command := exec.Command(python, script)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("backend capability schema validation failed: %v\n%s", err, output)
	}
	_ = schemaPath()
}

// TestContractChecksums verifies the deterministic SHA-256 manifest matches the
// schema and fixture bytes, keeping the authoritative contract tamper-evident.
func TestContractChecksums(t *testing.T) {
	contractDir := filepath.Join("..", "..", "spec", "backend-capabilities", "v1")
	manifestPath := filepath.Join(contractDir, "checksums.txt")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	expected := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != sha256.Size*2 {
			t.Fatalf("invalid manifest line %q", line)
		}
		if _, err := hex.DecodeString(fields[0]); err != nil {
			t.Fatalf("invalid checksum %q", fields[0])
		}
		expected[fields[1]] = fields[0]
		order = append(order, fields[1])
	}
	if !sort.StringsAreSorted(order) {
		t.Fatal("checksums.txt paths must be sorted")
	}

	var actual []string
	err = filepath.WalkDir(contractDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(contractDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		// The manifest covers the schema and fixtures, not README/checksums.
		if rel == "checksums.txt" || rel == "README.md" {
			return nil
		}
		actual = append(actual, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(actual)
	if len(actual) != len(order) {
		t.Fatalf("manifest lists %d files, directory has %d", len(order), len(actual))
	}
	for _, rel := range actual {
		content, err := os.ReadFile(filepath.Join(contractDir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(content)
		if got := hex.EncodeToString(sum[:]); got != expected[rel] {
			t.Fatalf("%s checksum = %s, want %s", rel, got, expected[rel])
		}
	}
}
