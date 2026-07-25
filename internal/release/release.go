// Package release contains the deterministic, dependency-free building blocks
// used by the repo-native release tool in scripts/release. It produces
// reproducible archives, checksum manifests, and SPDX SBOMs from the standard
// library only, so a snapshot or tagged build is verifiable locally without any
// third-party release toolchain.
//
// The functions here are intentionally pure: they take explicit inputs and
// return bytes, so they are unit-testable and produce byte-identical output for
// identical inputs. The orchestration (invoking the Go toolchain, reading the
// working tree, writing files) lives in scripts/release. SPDX SBOM rendering
// lives in spdx.go.
package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Target is one OS/architecture the release tool builds for.
type Target struct {
	OS   string
	Arch string
}

// String renders the target as os_arch, used in artifact names.
func (t Target) String() string { return t.OS + "_" + t.Arch }

// Targets is the supported Linux/macOS matrix. Windows is intentionally
// excluded: hook-service ships systemd units and Linux/macOS operator tooling
// only, so it makes no Windows release claim.
func Targets() []Target {
	return []Target{
		{OS: "linux", Arch: "amd64"},
		{OS: "linux", Arch: "arm64"},
		{OS: "darwin", Arch: "amd64"},
		{OS: "darwin", Arch: "arm64"},
	}
}

// Binaries are the two artifacts every archive contains.
func Binaries() []string { return []string{"hookshipd", "hookshipctl"} }

// GHCRHost is the container registry host for published images.
const GHCRHost = "ghcr.io"

// NormalizeImageRef returns the lowercase GHCR image reference for an
// "owner/repo" repository slug (such as GITHUB_REPOSITORY). GHCR and the OCI
// distribution spec require lowercase names, while GitHub owners and repos may
// be mixed case, so the reference is lowercased exactly once here and reused as
// the single normalized subject for the pushed tags, the cosign signature, and
// the provenance attestation.
//
//	"HookShip/hook-service" -> "ghcr.io/hookship/hook-service"
func NormalizeImageRef(repository string) (string, error) {
	repo := strings.TrimSpace(repository)
	if repo == "" {
		return "", errors.New("repository is required (owner/repo)")
	}
	lower := strings.ToLower(repo)
	parts := strings.Split(lower, "/")
	if len(parts) < 2 {
		return "", fmt.Errorf("repository must be owner/repo, got %q", repository)
	}
	for _, part := range parts {
		if !isImageSegment(part) {
			return "", fmt.Errorf("repository segment %q is not a valid image name component", part)
		}
	}
	return GHCRHost + "/" + lower, nil
}

// isImageSegment reports whether s is a non-empty lowercase image path segment
// ([a-z0-9._-]+). Uppercase input is rejected so callers must lowercase first.
func isImageSegment(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// ArchiveFile is one entry in a release archive.
type ArchiveFile struct {
	Name string
	Mode int64
	Body []byte
}

// WriteTarGz writes a deterministic gzip-compressed tar of files. Entries are
// sorted by name, ownership is zeroed, and both the tar mtime and the gzip
// header mtime are fixed to modTime so identical inputs yield identical bytes.
func WriteTarGz(files []ArchiveFile, modTime time.Time) ([]byte, error) {
	ordered := append([]ArchiveFile(nil), files...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })

	var buf bytes.Buffer
	gz, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, fmt.Errorf("create gzip writer: %w", err)
	}
	// A zero ModTime and empty Name keep the gzip header byte-stable.
	gz.ModTime = time.Time{}
	gz.OS = 255 // unknown, avoids embedding the host OS byte

	tw := tar.NewWriter(gz)
	mtime := modTime.UTC().Truncate(time.Second)
	for _, file := range ordered {
		if file.Name == "" {
			return nil, fmt.Errorf("archive entry has an empty name")
		}
		header := &tar.Header{
			Name:     file.Name,
			Mode:     file.Mode,
			Size:     int64(len(file.Body)),
			ModTime:  mtime,
			Typeflag: tar.TypeReg,
			Uid:      0,
			Gid:      0,
			Uname:    "",
			Gname:    "",
			Format:   tar.FormatGNU,
		}
		if err := tw.WriteHeader(header); err != nil {
			return nil, fmt.Errorf("write tar header for %s: %w", file.Name, err)
		}
		if _, err := tw.Write(file.Body); err != nil {
			return nil, fmt.Errorf("write tar body for %s: %w", file.Name, err)
		}
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("close tar writer: %w", err)
	}
	if err := gz.Close(); err != nil {
		return nil, fmt.Errorf("close gzip writer: %w", err)
	}
	return buf.Bytes(), nil
}

// Checksum returns the lowercase hex SHA-256 of data.
func Checksum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ChecksumEntry pairs an artifact name with its SHA-256.
type ChecksumEntry struct {
	Name   string
	SHA256 string
}

// ChecksumsFile renders a sha256sum-compatible manifest: "<hex>  <name>\n"
// lines sorted by name, matching the format `sha256sum -c` expects.
func ChecksumsFile(entries []ChecksumEntry) []byte {
	ordered := append([]ChecksumEntry(nil), entries...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
	var buf bytes.Buffer
	for _, entry := range ordered {
		fmt.Fprintf(&buf, "%s  %s\n", entry.SHA256, entry.Name)
	}
	return buf.Bytes()
}

// Module is a Go module dependency captured from `go version -m`.
type Module struct {
	Path    string
	Version string
	Sum     string
}

// ParseGoVersionModules parses `go version -m <binary>` output, returning the
// main module path and the sorted, de-duplicated dependency list. Replaced
// modules ("=>" lines) are resolved to their replacement target.
func ParseGoVersionModules(output []byte) (mainModule string, mods []Module, err error) {
	seen := make(map[string]Module)
	lines := strings.Split(string(output), "\n")
	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		if !strings.HasPrefix(line, "\t") {
			continue
		}
		fields := strings.Split(strings.TrimPrefix(line, "\t"), "\t")
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "mod":
			mainModule = fields[1]
		case "dep":
			if len(fields) < 3 {
				continue
			}
			mod := Module{Path: fields[1], Version: fields[2]}
			if len(fields) >= 4 {
				mod.Sum = fields[3]
			}
			seen[mod.Path] = mod
		case "=>":
			// A replacement immediately follows its dep line and overrides it.
			if len(fields) < 3 {
				continue
			}
			mod := Module{Path: fields[1], Version: fields[2]}
			if len(fields) >= 4 {
				mod.Sum = fields[3]
			}
			seen[mod.Path] = mod
		}
	}
	if mainModule == "" {
		return "", nil, fmt.Errorf("no main module found in go version -m output")
	}
	mods = make([]Module, 0, len(seen))
	for _, mod := range seen {
		mods = append(mods, mod)
	}
	sort.Slice(mods, func(i, j int) bool { return mods[i].Path < mods[j].Path })
	return mainModule, mods, nil
}
