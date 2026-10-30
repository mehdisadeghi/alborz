package alborz

import (
	"os/exec"
	"path"
	"strings"
	"testing"
)

// TestImportRule enforces ADR 66 rule 2: only web packages, commands and
// test support import alborz/web; logic never imports a web package; a web
// package imports no other feature's pages but its parent's, or, for an
// add-on, the page it extends (ADR 82).
func TestImportRule(t *testing.T) {
	out, err := exec.Command("go", "list", "-f", "{{.ImportPath}}{{range .Imports}} {{.}}{{end}}", "./...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	const module = "git.mehdix.org/alborz"
	core := module + "/web"
	isWeb := func(p string) bool { return p != core && strings.HasPrefix(p, module+"/") && path.Base(p) == "web" }
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		pkg, imports := fields[0], fields[1:]
		rel := strings.TrimPrefix(pkg, module+"/")
		mayUseCore := pkg == core || isWeb(pkg) || strings.HasPrefix(rel, "cmd/") || strings.HasSuffix(pkg, "test")
		for _, imp := range imports {
			switch {
			case imp == core && !mayUseCore:
				t.Errorf("%s imports %s", rel, core)
			case isWeb(imp) && !isWeb(pkg) && !strings.HasPrefix(rel, "cmd/") && !strings.HasSuffix(pkg, "test"):
				t.Errorf("logic package %s imports %s", rel, imp)
			case isWeb(imp) && isWeb(pkg) && !strings.HasPrefix(rel, "addons/") && !strings.HasPrefix(path.Dir(pkg)+"/", path.Dir(imp)+"/"):
				t.Errorf("%s imports another feature's pages %s", rel, imp)
			}
		}
	}
}
