// Package release contains the deterministic, dependency-free building blocks
// used by the repo-native release tool in scripts/release. It produces
// reproducible archives, checksum manifests, and SPDX SBOMs from the standard
// library only, so a snapshot or tagged build is verifiable locally without any
// third-party release toolchain.
//
// The functions here are intentionally pure: they take explicit inputs and
// return bytes, so they are unit-testable and produce byte-identical output for
// identical inputs. The orchestration (invoking the Go toolchain, reading the
// working tree, writing files) lives in scripts/release.
package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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

// SPDXInput describes one SPDX document for a single build target.
type SPDXInput struct {
	// AppName is the release/document name, e.g. "hook-service".
	AppName string
	// Version is the release version, e.g. "v0.1.0" or a snapshot string.
	Version string
	// SourceURL is the canonical repository URL (never invented).
	SourceURL string
	// Target is the os/arch this SBOM describes.
	Target Target
	// Binaries are the shipped executables in this target's archive.
	Binaries []string
	// MainModule is the module path of the built program.
	MainModule string
	// Modules is the dependency set embedded in the binaries.
	Modules []Module
	// Created is the SBOM creation time (fixed for reproducibility).
	Created time.Time
	// Creator identifies the tool that produced the SBOM.
	Creator string
}

// RenderSPDX renders a deterministic SPDX 2.3 JSON document for one target. The
// document namespace embeds a content-derived suffix so it is both unique and
// reproducible for identical inputs.
func RenderSPDX(in SPDXInput) ([]byte, error) {
	if in.AppName == "" || in.Version == "" || in.MainModule == "" {
		return nil, fmt.Errorf("SPDX input requires appName, version, and mainModule")
	}
	created := in.Created.UTC().Truncate(time.Second).Format("2006-01-02T15:04:05Z")

	type checksum struct {
		Algorithm     string `json:"algorithm"`
		ChecksumValue string `json:"checksumValue"`
	}
	type externalRef struct {
		ReferenceCategory string `json:"referenceCategory"`
		ReferenceType     string `json:"referenceType"`
		ReferenceLocator  string `json:"referenceLocator"`
	}
	type pkg struct {
		SPDXID           string        `json:"SPDXID"`
		Name             string        `json:"name"`
		VersionInfo      string        `json:"versionInfo,omitempty"`
		DownloadLocation string        `json:"downloadLocation"`
		FilesAnalyzed    bool          `json:"filesAnalyzed"`
		LicenseConcluded string        `json:"licenseConcluded"`
		LicenseDeclared  string        `json:"licenseDeclared"`
		CopyrightText    string        `json:"copyrightText"`
		Checksums        []checksum    `json:"checksums,omitempty"`
		ExternalRefs     []externalRef `json:"externalRefs,omitempty"`
	}
	type relationship struct {
		SPDXElementID      string `json:"spdxElementId"`
		RelationshipType   string `json:"relationshipType"`
		RelatedSPDXElement string `json:"relatedSpdxElement"`
	}
	type document struct {
		SPDXVersion       string `json:"spdxVersion"`
		DataLicense       string `json:"dataLicense"`
		SPDXID            string `json:"SPDXID"`
		Name              string `json:"name"`
		DocumentNamespace string `json:"documentNamespace"`
		CreationInfo      struct {
			Created  string   `json:"created"`
			Creators []string `json:"creators"`
		} `json:"creationInfo"`
		Packages      []pkg          `json:"packages"`
		Relationships []relationship `json:"relationships"`
	}

	rootID := "SPDXRef-Package-" + sanitizeSPDXID(in.AppName)
	packages := []pkg{{
		SPDXID:           rootID,
		Name:             in.AppName,
		VersionInfo:      in.Version,
		DownloadLocation: sourceOrDefault(in.SourceURL),
		FilesAnalyzed:    false,
		LicenseConcluded: "Apache-2.0",
		LicenseDeclared:  "Apache-2.0",
		CopyrightText:    "NOASSERTION",
	}}
	relationships := []relationship{{
		SPDXElementID:      "SPDXRef-DOCUMENT",
		RelationshipType:   "DESCRIBES",
		RelatedSPDXElement: rootID,
	}}

	for _, binary := range in.Binaries {
		binID := "SPDXRef-Binary-" + sanitizeSPDXID(binary+"-"+in.Target.String())
		packages = append(packages, pkg{
			SPDXID:           binID,
			Name:             binary,
			VersionInfo:      in.Version,
			DownloadLocation: sourceOrDefault(in.SourceURL),
			FilesAnalyzed:    false,
			LicenseConcluded: "Apache-2.0",
			LicenseDeclared:  "Apache-2.0",
			CopyrightText:    "NOASSERTION",
		})
		relationships = append(relationships, relationship{
			SPDXElementID:      rootID,
			RelationshipType:   "CONTAINS",
			RelatedSPDXElement: binID,
		})
	}

	for i, mod := range in.Modules {
		modID := fmt.Sprintf("SPDXRef-Module-%d-%s", i, sanitizeSPDXID(mod.Path))
		entry := pkg{
			SPDXID:           modID,
			Name:             mod.Path,
			VersionInfo:      mod.Version,
			DownloadLocation: "https://proxy.golang.org/" + moduleProxyPath(mod.Path) + "/@v/" + mod.Version + ".zip",
			FilesAnalyzed:    false,
			LicenseConcluded: "NOASSERTION",
			LicenseDeclared:  "NOASSERTION",
			CopyrightText:    "NOASSERTION",
			ExternalRefs: []externalRef{{
				ReferenceCategory: "PACKAGE-MANAGER",
				ReferenceType:     "purl",
				ReferenceLocator:  "pkg:golang/" + mod.Path + "@" + mod.Version,
			}},
		}
		if hexSum := goSumToHex(mod.Sum); hexSum != "" {
			entry.Checksums = []checksum{{Algorithm: "SHA256", ChecksumValue: hexSum}}
		}
		packages = append(packages, entry)
		relationships = append(relationships, relationship{
			SPDXElementID:      rootID,
			RelationshipType:   "DEPENDS_ON",
			RelatedSPDXElement: modID,
		})
	}

	var doc document
	doc.SPDXVersion = "SPDX-2.3"
	doc.DataLicense = "CC0-1.0"
	doc.SPDXID = "SPDXRef-DOCUMENT"
	doc.Name = fmt.Sprintf("%s-%s-%s", in.AppName, in.Version, in.Target.String())
	doc.DocumentNamespace = namespace(in)
	doc.CreationInfo.Created = created
	creator := in.Creator
	if creator == "" {
		creator = "hook-service release tool"
	}
	doc.CreationInfo.Creators = []string{"Tool: " + creator}
	doc.Packages = packages
	doc.Relationships = relationships

	rendered, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal SPDX: %w", err)
	}
	return append(rendered, '\n'), nil
}

// namespace derives a unique but reproducible SPDX document namespace under the
// real source URL, with a content-hash suffix guaranteeing uniqueness.
func namespace(in SPDXInput) string {
	hasher := sha256.New()
	fmt.Fprintf(hasher, "%s\n%s\n%s\n%s\n", in.AppName, in.Version, in.Target.String(), in.MainModule)
	for _, mod := range in.Modules {
		fmt.Fprintf(hasher, "%s@%s\n", mod.Path, mod.Version)
	}
	suffix := hex.EncodeToString(hasher.Sum(nil))[:16]
	base := strings.TrimSuffix(sourceOrDefault(in.SourceURL), "/")
	return fmt.Sprintf("%s/sbom/%s/%s-%s", base, in.Version, in.Target.String(), suffix)
}

func sourceOrDefault(sourceURL string) string {
	if strings.TrimSpace(sourceURL) == "" {
		return "NOASSERTION"
	}
	return sourceURL
}

// sanitizeSPDXID keeps only characters allowed in an SPDX identifier
// ([a-zA-Z0-9.-]), replacing everything else with a hyphen.
func sanitizeSPDXID(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

// moduleProxyPath lower-cases upper-case letters using the Go module proxy
// escaping (a capital X becomes "!x") so the download location is well-formed.
func moduleProxyPath(path string) string {
	var b strings.Builder
	for _, r := range path {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte('!')
			b.WriteRune(r + ('a' - 'A'))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// goSumToHex converts a go.sum style "h1:<base64>" module hash into a hex
// string suitable for an SPDX SHA256 checksum field. The h1 hash is the SHA-256
// of the module's file tree, so presenting it as SHA256 is accurate. An
// unrecognized value yields an empty string so the checksum is simply omitted.
func goSumToHex(sum string) string {
	const prefix = "h1:"
	if !strings.HasPrefix(sum, prefix) {
		return ""
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sum, prefix))
	if err != nil || len(decoded) != sha256.Size {
		return ""
	}
	return hex.EncodeToString(decoded)
}
