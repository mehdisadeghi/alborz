package alborztest

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The harness must stay out of the binary: only tests may import this package.
// go list sees build tags, so a production file hidden behind one is caught too.
func TestTheHarnessIsTestOnly(t *testing.T) {
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("no path to this test")
	}
	root := filepath.Join(filepath.Dir(here), "..", "..")
	out, err := run(t, root, `{{.ImportPath}}|{{join .Imports " "}}|{{join .TestImports " "}}|{{join .XTestImports " "}}`)
	if err != nil {
		t.Fatalf("listing the module: %v\n%s", err, out)
	}
	const self = "git.mehdix.org/alborz/internal/alborztest"
	used := false
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Split(line, "|")
		if len(fields) != 4 {
			t.Fatalf("unexpected go list line %q", line)
		}
		pkg, shipped, tests, xtests := fields[0], fields[1], fields[2], fields[3]
		if strings.Contains(" "+shipped+" ", " "+self+" ") {
			t.Errorf("%s imports the test harness and would ship it", pkg)
		}
		if strings.Contains(" "+tests+" "+xtests+" ", " "+self+" ") {
			used = true
		}
	}
	if !used {
		t.Fatal("no test imports the harness, so this check proves nothing")
	}
}

func run(t *testing.T, dir, format string) (string, error) {
	t.Helper()
	cmd := exec.Command("go", "list", "-f", format, "./...")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s: %w", cmd.Args, err)
	}
	return string(out), nil
}
