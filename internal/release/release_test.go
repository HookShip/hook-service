package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

func TestWriteTarGzIsDeterministic(t *testing.T) {
	files := []ArchiveFile{
		{Name: "hookshipd", Mode: 0o755, Body: []byte("binary-d")},
		{Name: "LICENSE", Mode: 0o644, Body: []byte("license")},
		{Name: "hookshipctl", Mode: 0o755, Body: []byte("binary-ctl")},
	}
	modTime := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	first, err := WriteTarGz(files, modTime)
	if err != nil {
		t.Fatalf("first archive: %v", err)
	}
	// Reorder the input to prove entries are sorted, not input-ordered.
	reordered := []ArchiveFile{files[1], files[2], files[0]}
	second, err := WriteTarGz(reordered, modTime)
	if err != nil {
		t.Fatalf("second archive: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("archives differ for identical inputs in different order")
	}

	// A different mtime must change the bytes, proving mtime is embedded.
	other, err := WriteTarGz(files, modTime.Add(time.Hour))
	if err != nil {
		t.Fatalf("third archive: %v", err)
	}
	if bytes.Equal(first, other) {
		t.Fatalf("archives identical despite different mtime")
	}
}

func TestWriteTarGzContents(t *testing.T) {
	files := []ArchiveFile{
		{Name: "hookshipd", Mode: 0o755, Body: []byte("d")},
		{Name: "README.md", Mode: 0o644, Body: []byte("readme")},
	}
	archive, err := WriteTarGz(files, time.Unix(1000, 0))
	if err != nil {
		t.Fatalf("archive: %v", err)
	}

	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		names = append(names, header.Name)
		if header.Uid != 0 || header.Gid != 0 {
			t.Fatalf("archive entry %s has non-zero ownership", header.Name)
		}
		if header.Uname != "" || header.Gname != "" {
			t.Fatalf("archive entry %s has non-empty owner names", header.Name)
		}
	}
	if want := []string{"README.md", "hookshipd"}; strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("names = %v, want sorted %v", names, want)
	}
}

func TestChecksumsFileFormat(t *testing.T) {
	entries := []ChecksumEntry{
		{Name: "b.tar.gz", SHA256: "bbbb"},
		{Name: "a.tar.gz", SHA256: "aaaa"},
	}
	got := string(ChecksumsFile(entries))
	want := "aaaa  a.tar.gz\nbbbb  b.tar.gz\n"
	if got != want {
		t.Fatalf("checksums = %q, want %q", got, want)
	}
}

func TestChecksumMatchesKnownVector(t *testing.T) {
	// SHA-256 of the empty input is a well-known constant.
	if got := Checksum(nil); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("empty checksum = %s", got)
	}
}

func TestParseGoVersionModules(t *testing.T) {
	output := "bin/hookshipd: go1.26.5\n" +
		"\tpath\tgithub.com/hookship/hook-service/cmd/hookshipd\n" +
		"\tmod\tgithub.com/hookship/hook-service\t(devel)\n" +
		"\tdep\tgithub.com/redis/go-redis/v9\tv9.7.3\th1:redis=\n" +
		"\tdep\tgithub.com/jackc/pgx/v5\tv5.10.0\th1:pgx=\n" +
		"\tdep\tgithub.com/old/mod\tv1.0.0\th1:old=\n" +
		"\t=>\tgithub.com/old/mod\tv1.0.1\th1:new=\n"

	main, mods, err := ParseGoVersionModules([]byte(output))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if main != "github.com/hookship/hook-service" {
		t.Fatalf("main module = %s", main)
	}
	if len(mods) != 3 {
		t.Fatalf("expected 3 modules, got %d: %+v", len(mods), mods)
	}
	// Sorted by path: jackc, old, redis.
	if mods[0].Path != "github.com/jackc/pgx/v5" || mods[0].Version != "v5.10.0" {
		t.Fatalf("first module = %+v", mods[0])
	}
	// Replacement overrode the original version.
	if mods[1].Path != "github.com/old/mod" || mods[1].Version != "v1.0.1" {
		t.Fatalf("replaced module = %+v", mods[1])
	}
}

func TestParseGoVersionModulesRequiresMain(t *testing.T) {
	if _, _, err := ParseGoVersionModules([]byte("no module lines here")); err == nil {
		t.Fatalf("expected error when no main module present")
	}
}

func TestRenderSPDXValidAndDeterministic(t *testing.T) {
	in := SPDXInput{
		AppName:    "hook-service",
		Version:    "v0.1.0",
		SourceURL:  "https://github.com/hookship/hook-service",
		Target:     Target{OS: "linux", Arch: "amd64"},
		Binaries:   []string{"hookshipd", "hookshipctl"},
		MainModule: "github.com/hookship/hook-service",
		Modules: []Module{
			{Path: "github.com/jackc/pgx/v5", Version: "v5.10.0", Sum: "h1:" + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="},
		},
		Created: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Creator: "hook-service release tool",
	}

	first, err := RenderSPDX(in)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	second, err := RenderSPDX(in)
	if err != nil {
		t.Fatalf("render again: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("SPDX output not deterministic")
	}

	var doc map[string]any
	if err := json.Unmarshal(first, &doc); err != nil {
		t.Fatalf("SPDX is not valid JSON: %v", err)
	}
	if doc["spdxVersion"] != "SPDX-2.3" {
		t.Fatalf("spdxVersion = %v", doc["spdxVersion"])
	}
	if doc["dataLicense"] != "CC0-1.0" {
		t.Fatalf("dataLicense = %v", doc["dataLicense"])
	}
	if doc["SPDXID"] != "SPDXRef-DOCUMENT" {
		t.Fatalf("SPDXID = %v", doc["SPDXID"])
	}
	ns, _ := doc["documentNamespace"].(string)
	if !strings.HasPrefix(ns, "https://github.com/hookship/hook-service/sbom/v0.1.0/linux_amd64-") {
		t.Fatalf("documentNamespace = %v", ns)
	}
	packages, ok := doc["packages"].([]any)
	if !ok || len(packages) != 4 { // root + 2 binaries + 1 module
		t.Fatalf("expected 4 packages, got %d", len(packages))
	}
}

func TestRenderSPDXRequiresFields(t *testing.T) {
	if _, err := RenderSPDX(SPDXInput{Version: "v1", MainModule: "m"}); err == nil {
		t.Fatalf("expected error for missing appName")
	}
}

func TestGoSumToHex(t *testing.T) {
	// "h1:" + base64 of 32 zero bytes decodes to 64 hex zeros.
	got := goSumToHex("h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if got != strings.Repeat("0", 64) {
		t.Fatalf("goSumToHex = %q", got)
	}
	if goSumToHex("not-an-h1-hash") != "" {
		t.Fatalf("expected empty for non-h1 input")
	}
	if goSumToHex("") != "" {
		t.Fatalf("expected empty for empty input")
	}
}

func TestTargetsAndBinaries(t *testing.T) {
	if len(Targets()) != 4 {
		t.Fatalf("expected 4 targets")
	}
	for _, target := range Targets() {
		if target.OS != "linux" && target.OS != "darwin" {
			t.Fatalf("unexpected OS %s", target.OS)
		}
	}
	if got := (Target{OS: "linux", Arch: "arm64"}).String(); got != "linux_arm64" {
		t.Fatalf("target string = %s", got)
	}
	if strings.Join(Binaries(), ",") != "hookshipd,hookshipctl" {
		t.Fatalf("binaries = %v", Binaries())
	}
}
