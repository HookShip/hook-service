package archtest

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Source-size ratchet. Large files erode readability and SRP, so non-generated
// Go and shell sources are capped. Files that are already over the cap are
// grandfathered with their current size as the ceiling, which prevents growth
// while the decomposition work lands. As a hotspot is split, its allowlist entry
// must be lowered or removed — the test enforces that so the ratchet only ever
// tightens.
const (
	defaultGoMax    = 450
	defaultShellMax = 250
	// tightenSlack is how far below its ceiling an allowlisted file may drift
	// before the test asks for the ceiling to be lowered. It keeps ceilings
	// honest without failing on every small edit.
	tightenSlack = 40
)

// goAllowlist maps a repo-relative path to its permitted maximum line count.
// Empty: every non-generated production Go file is under the default cap after
// the decomposition work. Add an entry only for a genuinely irreducible hotspot,
// and prefer splitting instead.
var goAllowlist = map[string]int{}

// shellAllowlist maps a repo-relative path to its permitted maximum line count.
// deploy/scripts/install.sh is the single-VM installer: it renders the systemd
// units for the selected --prefix, installs the hardened service/timer units and
// the operational backup scripts, and drives migrate/start/readiness. Splitting
// a safety-critical installer into sourced fragments would hurt auditability, so
// it carries a deliberate ceiling; lower it if the file shrinks.
var shellAllowlist = map[string]int{
	"deploy/scripts/install.sh": 268,
}

func TestSourceSizeRatchet(t *testing.T) {
	root := moduleRoot(t)
	usedGo := map[string]bool{}
	usedShell := map[string]bool{}

	skipDirs := map[string]bool{".git": true, "node_modules": true, "bin": true, "dist": true}

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)

		switch {
		case strings.HasSuffix(rel, ".go") && !strings.HasSuffix(rel, "_test.go"):
			if isGeneratedGo(t, path) {
				return nil
			}
			checkFile(t, path, rel, goAllowlist, defaultGoMax, usedGo)
		case strings.HasSuffix(rel, ".sh"):
			checkFile(t, path, rel, shellAllowlist, defaultShellMax, usedShell)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	assertAllowlistFresh(t, goAllowlist, usedGo, defaultGoMax, "goAllowlist")
	assertAllowlistFresh(t, shellAllowlist, usedShell, defaultShellMax, "shellAllowlist")
}

func checkFile(t *testing.T, path, rel string, allowlist map[string]int, defaultMax int, used map[string]bool) {
	t.Helper()
	lines := countLines(t, path)
	ceiling := defaultMax
	if listed, ok := allowlist[rel]; ok {
		ceiling = listed
		used[rel] = true
		if lines <= defaultMax {
			t.Errorf("%s is now %d lines (<= cap %d): remove it from the allowlist", rel, lines, defaultMax)
			return
		}
		if ceiling-lines > tightenSlack {
			t.Errorf("%s is now %d lines but its allowlist ceiling is %d: lower the ceiling to %d", rel, lines, ceiling, lines)
			return
		}
	}
	if lines > ceiling {
		t.Errorf("%s is %d lines, over its limit of %d. Split it or (only for a genuine hotspot) raise its allowlist ceiling deliberately.", rel, lines, ceiling)
	}
}

// assertAllowlistFresh fails if an allowlist entry no longer matches a real
// source file, so stale entries cannot accumulate.
func assertAllowlistFresh(t *testing.T, allowlist map[string]int, used map[string]bool, defaultMax int, name string) {
	t.Helper()
	stale := make([]string, 0)
	for rel := range allowlist {
		if !used[rel] {
			stale = append(stale, rel)
		}
	}
	sort.Strings(stale)
	for _, rel := range stale {
		t.Errorf("%s has a stale entry for %s (file missing or now under the %d-line cap): remove it", name, rel, defaultMax)
	}
}

func isGeneratedGo(t *testing.T, path string) bool {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	lines := 0
	for scanner.Scan() && lines < 15 {
		if strings.HasPrefix(scanner.Text(), "// Code generated ") {
			return true
		}
		lines++
	}
	return false
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	count := 0
	for scanner.Scan() {
		count++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return count
}
