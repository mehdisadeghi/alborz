// Importrule holds the packages to ADR 66: only web packages and cmd import
// alborz/web, no logic package imports a web package, and no web package
// imports another feature's, save the pages of the feature it is part of.
// It reads what go list prints:
//
//	go list -f '{{.ImportPath}}{{range .Imports}} {{.}}{{end}}' ./... | go run ./internal/importrule
package main

import (
	"bufio"
	"fmt"
	"os"
	"slices"
	"strings"
)

const (
	module = "git.mehdix.org/alborz"
	core   = module + "/web"
)

// allowed are the imports the feature split has yet to remove; each goes with its step of #220.
var allowed = []string{
	"git.mehdix.org/alborz/mail git.mehdix.org/alborz/web",
	"git.mehdix.org/alborz/pim git.mehdix.org/alborz/web",
	"git.mehdix.org/alborz/pim/dav git.mehdix.org/alborz/web",
}

func isWeb(path string) bool {
	return path == core || strings.HasPrefix(path, module+"/") && strings.HasSuffix(path, "/web")
}

// breaks reports whether pkg may not import imp.
func breaks(pkg, imp string) bool {
	switch {
	case !isWeb(imp) || strings.HasPrefix(pkg, module+"/cmd/"):
		return false
	case !isWeb(pkg):
		return true
	}
	// pim/calendar/web may use pim/web: the calendar is part of pim.
	feature := strings.TrimSuffix(imp, "web")
	return imp != core && !strings.HasPrefix(pkg, feature)
}

func main() {
	var found []string
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
				found = append(found, fields[0]+" "+imp)
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
	failed := false
	for _, f := range found {
		if !slices.Contains(allowed, f) {
			pkg, imp, _ := strings.Cut(f, " ")
			fmt.Printf("import rule: %s imports %s\n", pkg, imp)
			failed = true
		}
	}
	for _, a := range allowed {
		if !slices.Contains(found, a) {
			fmt.Printf("import rule: %s is allowed but no longer there; drop it\n", a)
			failed = true
		}
	}
	if failed {
		os.Exit(1)
	}
}
