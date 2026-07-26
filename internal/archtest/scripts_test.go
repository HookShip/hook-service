package archtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// scriptRefPattern matches shell/python scripts referenced from the Makefile or
// CI workflows, e.g. "./scripts/coverage.sh" or "deploy/scripts/test/run.sh".
var scriptRefPattern = regexp.MustCompile(`(?:\./)?((?:scripts|deploy/scripts)/[\w./-]+\.(?:sh|py))`)

// referencedScripts collects every script path referenced by the Makefile and
// the CI workflows.
func referencedScripts(t *testing.T, root string) []string {
	t.Helper()
	sources := []string{filepath.Join(root, "Makefile")}
	workflows, _ := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.yml"))
	sources = append(sources, workflows...)

	seen := map[string]bool{}
	for _, src := range sources {
		data, err := os.ReadFile(src)
		if err != nil {
			continue
		}
		for _, m := range scriptRefPattern.FindAllStringSubmatch(string(data), -1) {
			seen[m[1]] = true
		}
	}
	// coverage.sh is required at a minimum: the Makefile and CI invoke it, and it
	// was previously swallowed by an over-broad .gitignore pattern.
	seen["scripts/coverage.sh"] = true

	out := make([]string, 0, len(seen))
	for path := range seen {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

// TestReferencedScriptsAreTrackedAndExecutable guards against a script that the
// Makefile or CI invokes being untracked (for example, swallowed by .gitignore)
// so a fresh clone would fail. Every referenced .sh must also be executable.
func TestReferencedScriptsAreTrackedAndExecutable(t *testing.T) {
	root := moduleRoot(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	for _, rel := range referencedScripts(t, root) {
		t.Run(rel, func(t *testing.T) {
			// Tracked in git: --error-unmatch exits non-zero for an untracked path.
			cmd := exec.Command("git", "ls-files", "--error-unmatch", rel)
			cmd.Dir = root
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%s is referenced by Makefile/CI but is NOT tracked in git "+
					"(check .gitignore); git said: %s", rel, strings.TrimSpace(string(out)))
			}

			if strings.HasSuffix(rel, ".sh") {
				info, err := os.Stat(filepath.Join(root, rel))
				if err != nil {
					t.Fatalf("stat %s: %v", rel, err)
				}
				if info.Mode()&0o111 == 0 {
					t.Fatalf("%s must be executable (chmod +x)", rel)
				}
			}
		})
	}
}
