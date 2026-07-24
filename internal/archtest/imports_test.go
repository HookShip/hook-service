package archtest

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

const modulePath = "github.com/hookship/hook-service"

// moduleRoot walks up from this test file to the directory containing go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve caller for module root")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found walking up from test file")
		}
		dir = parent
	}
}

// transitiveModuleImports returns every module-internal package reachable from
// start through non-test source, following imports transitively. Standard
// library and third-party imports are ignored; only in-module packages are
// walked so the guard reflects our own layering.
func transitiveModuleImports(t *testing.T, root, start string) map[string]bool {
	t.Helper()
	seen := map[string]bool{}
	var visit func(pkg string)
	visit = func(pkg string) {
		if seen[pkg] {
			return
		}
		seen[pkg] = true
		for _, imp := range directImports(t, root, pkg) {
			if strings.HasPrefix(imp, modulePath+"/") {
				visit(imp)
			}
		}
	}
	visit(start)
	delete(seen, start)
	return seen
}

// directImports parses the non-test Go files of an in-module package and returns
// its direct import paths.
func directImports(t *testing.T, root, pkg string) []string {
	t.Helper()
	rel := strings.TrimPrefix(pkg, modulePath+"/")
	dir := filepath.Join(root, filepath.FromSlash(rel))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read package dir %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	var imports []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, spec := range parsed.Imports {
			imports = append(imports, strings.Trim(spec.Path.Value, `"`))
		}
	}
	return imports
}

// TestGuardedPackagesHaveNoConcreteBackendOrConfigImports keeps the core domain
// packages free of concrete backend, config, and durability imports so the
// dependency direction (concrete backends depend on the abstractions, never the
// reverse) cannot silently invert.
func TestGuardedPackagesHaveNoConcreteBackendOrConfigImports(t *testing.T) {
	root := moduleRoot(t)

	forbiddenPrefixes := []string{
		modulePath + "/internal/backend",
		modulePath + "/internal/config",
		modulePath + "/internal/durability",
		modulePath + "/internal/daemon",
		modulePath + "/internal/httpserver",
	}

	guarded := []string{
		modulePath + "/internal/runtime",
		modulePath + "/internal/idempotency",
		modulePath + "/internal/capabilities",
	}

	for _, pkg := range guarded {
		t.Run(strings.TrimPrefix(pkg, modulePath+"/"), func(t *testing.T) {
			deps := transitiveModuleImports(t, root, pkg)
			paths := make([]string, 0, len(deps))
			for dep := range deps {
				paths = append(paths, dep)
			}
			sort.Strings(paths)
			for _, dep := range paths {
				for _, forbidden := range forbiddenPrefixes {
					if dep == forbidden || strings.HasPrefix(dep, forbidden+"/") {
						t.Errorf("%s must not depend on %s (transitively), but it does", pkg, dep)
					}
				}
			}
		})
	}
}

// TestIdempotencyIsDependencyFree proves the shared idempotency package imports
// nothing from this module, so it can be consumed from either the runtime or the
// durability side without an import cycle.
func TestIdempotencyIsDependencyFree(t *testing.T) {
	root := moduleRoot(t)
	deps := transitiveModuleImports(t, root, modulePath+"/internal/idempotency")
	if len(deps) != 0 {
		paths := make([]string, 0, len(deps))
		for dep := range deps {
			paths = append(paths, dep)
		}
		sort.Strings(paths)
		t.Errorf("internal/idempotency must be dependency-free, but imports: %s", strings.Join(paths, ", "))
	}
}
