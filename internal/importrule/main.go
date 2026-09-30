// Importrule holds the packages to ADR 66: only web packages, cmd and test
// support (named as httptest is) import alborz/web, no logic package imports
// a web package, and no web package imports another feature's, save the
// pages of the feature it is part of.
// It reads what go list prints:
//
//	go list -f '{{.ImportPath}}{{range .Imports}} {{.}}{{end}}' ./... | go run ./internal/importrule
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

const (
	module = "git.mehdix.org/alborz"
	core   = module + "/web"
)

func isWeb(path string) bool {
	return path == core || strings.HasPrefix(path, module+"/") && strings.HasSuffix(path, "/web")
}

// breaks reports whether pkg may not import imp.
func breaks(pkg, imp string) bool {
	switch {
	case !isWeb(imp) || strings.HasPrefix(pkg, module+"/cmd/") || strings.HasSuffix(pkg, "test"):
		return false
	case !isWeb(pkg):
		return true
	}
	// pim/calendar/web may use pim/web: the calendar is part of pim.
	feature := strings.TrimSuffix(imp, "web")
	return imp != core && !strings.HasPrefix(pkg, feature)
}

func main() {
	failed := false
	packages := 0
	lines := bufio.NewScanner(os.Stdin)
	for lines.Scan() {
		fields := strings.Fields(lines.Text())
		if len(fields) == 0 {
			continue
		}
		packages++
		for _, imp := range fields[1:] {
			if breaks(fields[0], imp) {
				fmt.Printf("import rule: %s imports %s\n", fields[0], imp)
				failed = true
			}
		}
	}
	if err := lines.Err(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// A failed go list prints nothing, which would pass every package.
	if packages == 0 {
		fmt.Println("import rule: no packages listed")
		os.Exit(1)
	}
	if failed {
		os.Exit(1)
	}
}
