package release

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

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
	// hash.Hash.Write never returns an error; the results are checked to satisfy
	// errcheck without changing behavior.
	_, _ = fmt.Fprintf(hasher, "%s\n%s\n%s\n%s\n", in.AppName, in.Version, in.Target.String(), in.MainModule)
	for _, mod := range in.Modules {
		_, _ = fmt.Fprintf(hasher, "%s@%s\n", mod.Path, mod.Version)
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
