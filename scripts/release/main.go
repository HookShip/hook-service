// Command release is the repo-native, deterministic release builder for
// hook-service. It is the GoReleaser-equivalent for this repository: instead of
// adding a third-party release toolchain, it drives the Go toolchain and the
// standard-library helpers in internal/release to produce reproducible
// cross-platform archives, a checksum manifest, SPDX SBOMs, and a metadata
// manifest — all verifiable locally with `go run` and no external publish.
//
// Usage:
//
//	go run ./scripts/release snapshot            # untagged, dirty-safe build
//	go run ./scripts/release build -version v0.1.0
//
// Neither mode publishes anything. The tag- and approval-gated GitHub Release
// and GHCR image workflow (.github/workflows/release.yml) invokes the snapshot
// build for verification and owns all external publishing under a protected
// environment. No credentials are read or written here.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hookship/hook-service/internal/release"
)

const (
	appName   = "hook-service"
	sourceURL = "https://github.com/hookship/hook-service"
	module    = "github.com/hookship/hook-service"
)

// bundledFiles ship alongside the binaries in every archive.
var bundledFiles = []string{"LICENSE", "NOTICE", "THIRD_PARTY_NOTICES.md", "README.md"}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "release: %v\n", err)
		os.Exit(1)
	}
}

type options struct {
	mode    string
	output  string
	version string
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: release <snapshot|build|image-ref> [-output dir] [-version vX.Y.Z]")
	}
	if args[0] == "image-ref" {
		return runImageRef(args[1:])
	}
	opts := options{mode: args[0], output: "dist"}
	rest := args[1:]
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "-output":
			if i+1 >= len(rest) {
				return fmt.Errorf("-output requires a value")
			}
			opts.output = rest[i+1]
			i++
		case "-version":
			if i+1 >= len(rest) {
				return fmt.Errorf("-version requires a value")
			}
			opts.version = rest[i+1]
			i++
		default:
			return fmt.Errorf("unknown argument %q", rest[i])
		}
	}

	root, err := repoRoot()
	if err != nil {
		return err
	}

	version, err := resolveVersion(root, opts)
	if err != nil {
		return err
	}
	commit := gitOutput(root, "rev-parse", "--short=12", "HEAD")
	if commit == "" {
		commit = "none"
	}
	buildDate := resolveBuildDate(root)

	switch opts.mode {
	case "snapshot", "build":
	default:
		return fmt.Errorf("mode must be snapshot or build, got %q", opts.mode)
	}

	outDir := filepath.Join(root, opts.output)
	if err := os.RemoveAll(outDir); err != nil {
		return fmt.Errorf("clean output dir: %w", err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	fmt.Printf("release: %s version=%s commit=%s date=%s\n", opts.mode, version, commit, buildDate.Format(time.RFC3339))

	bundled, err := readBundled(root)
	if err != nil {
		return err
	}

	type artifact struct {
		Name   string `json:"name"`
		SHA256 string `json:"sha256"`
		OS     string `json:"os,omitempty"`
		Arch   string `json:"arch,omitempty"`
		Kind   string `json:"kind"`
	}
	var artifacts []artifact
	var checksums []release.ChecksumEntry

	for _, target := range release.Targets() {
		stage, err := os.MkdirTemp("", "hookship-release-")
		if err != nil {
			return fmt.Errorf("create stage dir: %w", err)
		}
		files := make([]release.ArchiveFile, 0, len(release.Binaries())+len(bundledFiles))
		for _, binary := range release.Binaries() {
			binPath := filepath.Join(stage, binary)
			if err := buildBinary(root, target, binary, version, commit, buildDate, binPath); err != nil {
				os.RemoveAll(stage)
				return err
			}
			body, err := os.ReadFile(binPath)
			if err != nil {
				os.RemoveAll(stage)
				return fmt.Errorf("read built binary %s: %w", binary, err)
			}
			files = append(files, release.ArchiveFile{Name: binary, Mode: 0o755, Body: body})
		}
		for name, body := range bundled {
			files = append(files, release.ArchiveFile{Name: name, Mode: 0o644, Body: body})
		}

		archive, err := release.WriteTarGz(files, buildDate)
		if err != nil {
			os.RemoveAll(stage)
			return fmt.Errorf("archive %s: %w", target, err)
		}
		archiveName := fmt.Sprintf("%s_%s_%s.tar.gz", appName, version, target)
		if err := os.WriteFile(filepath.Join(outDir, archiveName), archive, 0o644); err != nil {
			os.RemoveAll(stage)
			return fmt.Errorf("write archive %s: %w", archiveName, err)
		}
		archiveSum := release.Checksum(archive)
		checksums = append(checksums, release.ChecksumEntry{Name: archiveName, SHA256: archiveSum})
		artifacts = append(artifacts, artifact{Name: archiveName, SHA256: archiveSum, OS: target.OS, Arch: target.Arch, Kind: "archive"})

		// SBOM derived from what the built binary actually embeds.
		mods, mainModule, err := binaryModules(root, filepath.Join(stage, release.Binaries()[0]))
		if err != nil {
			os.RemoveAll(stage)
			return err
		}
		sbom, err := release.RenderSPDX(release.SPDXInput{
			AppName:    appName,
			Version:    version,
			SourceURL:  sourceURL,
			Target:     target,
			Binaries:   release.Binaries(),
			MainModule: mainModule,
			Modules:    mods,
			Created:    buildDate,
			Creator:    appName + " release tool",
		})
		if err != nil {
			os.RemoveAll(stage)
			return fmt.Errorf("render SBOM %s: %w", target, err)
		}
		sbomName := fmt.Sprintf("%s_%s_%s.spdx.json", appName, version, target)
		if err := os.WriteFile(filepath.Join(outDir, sbomName), sbom, 0o644); err != nil {
			os.RemoveAll(stage)
			return fmt.Errorf("write SBOM %s: %w", sbomName, err)
		}
		sbomSum := release.Checksum(sbom)
		checksums = append(checksums, release.ChecksumEntry{Name: sbomName, SHA256: sbomSum})
		artifacts = append(artifacts, artifact{Name: sbomName, SHA256: sbomSum, OS: target.OS, Arch: target.Arch, Kind: "sbom"})

		os.RemoveAll(stage)
		fmt.Printf("  built %s (%s)\n", archiveName, shortSum(archiveSum))
	}

	checksumsName := fmt.Sprintf("%s_%s_checksums.txt", appName, version)
	if err := os.WriteFile(filepath.Join(outDir, checksumsName), release.ChecksumsFile(checksums), 0o644); err != nil {
		return fmt.Errorf("write checksums: %w", err)
	}

	manifest := struct {
		App       string     `json:"app"`
		Version   string     `json:"version"`
		Commit    string     `json:"commit"`
		BuildDate string     `json:"buildDate"`
		Mode      string     `json:"mode"`
		Source    string     `json:"source"`
		Artifacts []artifact `json:"artifacts"`
	}{
		App:       appName,
		Version:   version,
		Commit:    commit,
		BuildDate: buildDate.UTC().Format(time.RFC3339),
		Mode:      opts.mode,
		Source:    sourceURL,
		Artifacts: artifacts,
	}
	manifestBytes, err := jsonIndent(manifest)
	if err != nil {
		return err
	}
	manifestName := fmt.Sprintf("%s_%s_manifest.json", appName, version)
	if err := os.WriteFile(filepath.Join(outDir, manifestName), manifestBytes, 0o644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}

	fmt.Printf("release: wrote %d artifacts + checksums + manifest to %s\n", len(artifacts), opts.output)
	return nil
}

// runImageRef prints the normalized lowercase GHCR image reference for the
// repository, defaulting to the GITHUB_REPOSITORY environment variable. It is
// the single source of truth the release workflow uses so the pushed image, the
// cosign subject, and the provenance attestation all share one lowercase name.
func runImageRef(args []string) error {
	repository := os.Getenv("GITHUB_REPOSITORY")
	if len(args) == 1 {
		repository = args[0]
	} else if len(args) > 1 {
		return fmt.Errorf("usage: release image-ref [owner/repo]")
	}
	ref, err := release.NormalizeImageRef(repository)
	if err != nil {
		return err
	}
	fmt.Println(ref)
	return nil
}

func buildBinary(root string, target release.Target, binary, version, commit string, buildDate time.Time, outPath string) error {
	ldflags := strings.Join([]string{
		"-s", "-w", "-buildid=",
		"-X", module + "/internal/buildinfo.Version=" + version,
		"-X", module + "/internal/buildinfo.Commit=" + commit,
		"-X", module + "/internal/buildinfo.Date=" + buildDate.UTC().Format("2006-01-02T15:04:05Z"),
	}, " ")
	cmd := exec.Command("go", "build",
		"-trimpath",
		"-buildvcs=false",
		"-ldflags", ldflags,
		"-o", outPath,
		"./cmd/"+binary,
	)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"GOOS="+target.OS,
		"GOARCH="+target.Arch,
		"GOFLAGS=-mod=readonly",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build %s for %s: %v: %s", binary, target, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func binaryModules(root, binPath string) ([]release.Module, string, error) {
	cmd := exec.Command("go", "version", "-m", binPath)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, "", fmt.Errorf("go version -m %s: %w", binPath, err)
	}
	mainModule, mods, err := release.ParseGoVersionModules(out)
	if err != nil {
		return nil, "", err
	}
	return mods, mainModule, nil
}

func readBundled(root string) (map[string][]byte, error) {
	bundled := make(map[string][]byte, len(bundledFiles))
	for _, name := range bundledFiles {
		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			return nil, fmt.Errorf("read bundled file %s: %w", name, err)
		}
		bundled[name] = body
	}
	return bundled, nil
}

func resolveVersion(root string, opts options) (string, error) {
	if opts.version != "" {
		return opts.version, nil
	}
	described := gitOutput(root, "describe", "--tags", "--always", "--dirty")
	if described == "" {
		described = "0.0.0"
	}
	if opts.mode == "snapshot" {
		return described + "-snapshot", nil
	}
	return described, nil
}

// resolveBuildDate prefers SOURCE_DATE_EPOCH, then the HEAD commit time, then
// now — so builds are reproducible when a source date or a committed tree is
// available.
func resolveBuildDate(root string) time.Time {
	if epoch := strings.TrimSpace(os.Getenv("SOURCE_DATE_EPOCH")); epoch != "" {
		if seconds, err := strconv.ParseInt(epoch, 10, 64); err == nil {
			return time.Unix(seconds, 0).UTC()
		}
	}
	if commitTime := gitOutput(root, "show", "-s", "--format=%ct", "HEAD"); commitTime != "" {
		if seconds, err := strconv.ParseInt(commitTime, 10, 64); err == nil {
			return time.Unix(seconds, 0).UTC()
		}
	}
	return time.Now().UTC()
}

func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not locate go.mod from the working directory")
		}
		dir = parent
	}
}

func gitOutput(root string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func shortSum(sum string) string {
	if len(sum) <= 12 {
		return sum
	}
	return sum[:12]
}

func jsonIndent(value any) ([]byte, error) {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal manifest: %w", err)
	}
	return append(body, '\n'), nil
}
