package alborz

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var (
	adrTitle  = regexp.MustCompile(`^# ADR ([1-9][0-9]*): \S`)
	adrStatus = regexp.MustCompile(`^Status: (proposed|accepted|superseded by ADR [1-9][0-9]*)\b`)
	adrPath   = regexp.MustCompile("`([A-Za-z0-9_.-]+/[^`\\s]*)`")
	adrLine   = regexp.MustCompile(`(:[0-9][0-9,-]*|#.*)$`)
)

// TestADRs holds every ADR to docs/adr/README.md and AGENTS.md: its title
// matches its file number, line 3 is the Status line, it has the four
// sections in order, and every repo path its References names exists.
func TestADRs(t *testing.T) {
	files, err := filepath.Glob("docs/adr/[0-9][0-9][0-9][0-9]-*.md")
	if err != nil || len(files) == 0 {
		t.Fatalf("no ADRs: %v", err)
	}
	for _, name := range files {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(raw), "\n")
		number, _ := strconv.Atoi(filepath.Base(name)[:4])
		if m := adrTitle.FindStringSubmatch(lines[0]); m == nil || m[1] != strconv.Itoa(number) {
			t.Errorf("%s: line 1 is not `# ADR %d: Title`", name, number)
		}
		if len(lines) < 3 || lines[1] != "" || !adrStatus.MatchString(lines[2]) {
			t.Errorf("%s: line 3 is not a Status line", name)
		}
		var sections []string
		refs := -1
		for i, line := range lines {
			if strings.HasPrefix(line, "## ") {
				sections = append(sections, line)
				if line == "## References" {
					refs = i
				}
			}
		}
		if strings.Join(sections, "|") != "## Context|## Decision|## Consequences|## References" {
			t.Errorf("%s: sections are %q", name, sections)
		}
		if refs < 0 {
			continue
		}
		for _, m := range adrPath.FindAllStringSubmatch(strings.Join(lines[refs:], "\n"), -1) {
			if !adrPathExists(m[1]) {
				t.Errorf("%s: References names missing path %s", name, m[1])
			}
		}
	}
}

// adrPathExists reports whether a referenced path names a file, a directory,
// a glob match, or a Go identifier in a package directory (pim/web.Sources).
func adrPathExists(ref string) bool {
	ref = strings.TrimRight(adrLine.ReplaceAllString(ref, ""), ".,;:)")
	if first, _, _ := strings.Cut(ref, "/"); strings.Contains(ref, "://") || strings.Contains(first, ".") && first != ".local" {
		return true // a module path or URL host, outside this repo
	}
	if strings.ContainsAny(ref, "*?[") {
		matches, _ := filepath.Glob(ref)
		return len(matches) > 0
	}
	if _, err := os.Stat(ref); err == nil {
		return true
	}
	if dot := strings.LastIndex(ref, "."); dot > strings.LastIndex(ref, "/") {
		if _, err := os.Stat(ref[:dot]); err == nil {
			return true
		}
	}
	return false
}
