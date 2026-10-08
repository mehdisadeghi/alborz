package filters

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"git.mehdix.org/alborz"
)

// Nothing marks these scripts as ours; the main script only includes them and the reader's own.
const (
	mainScript     = "alborz"
	RulesScript    = "alborz-rules"
	ForwardScript  = "alborz-forward"
	VacationScript = "alborz-vacation"
)

// Rules run first, since one may stop delivery of what the rest would forward or answer.
var composed = []string{RulesScript, ForwardScript, VacationScript}

// ManageSieve keeps no script version, so a save compares content to notice edits made elsewhere.
func Fingerprint(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// quote writes a Sieve quoted string (RFC 5228 2.4.2).
func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func unquote(s string) string {
	return strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(s)
}

const quoted = `"((?:[^"\\]|\\.)*)"`

func requireLine(extensions []string) string {
	if len(extensions) == 0 {
		return ""
	}
	q := make([]string, len(extensions))
	for i, e := range extensions {
		q[i] = quote(e)
	}
	return "require [" + strings.Join(q, ", ") + "];\n"
}

func mainScriptFor(present []string, own string) string {
	var b strings.Builder
	b.WriteString("# Wiring, composed by Alborz: each line runs one script, the last is your own.\n")
	b.WriteString(requireLine([]string{"include"}))
	for _, name := range present {
		fmt.Fprintf(&b, "include :personal %s;\n", quote(name))
	}
	if own != "" {
		fmt.Fprintf(&b, "include :personal %s;\n", quote(own))
	}
	return b.String()
}

var includeLine = regexp.MustCompile(`^include :personal ` + quoted + `;$`)

func ownScriptIn(content string) string {
	for _, line := range strings.Split(content, "\n") {
		m := includeLine.FindStringSubmatch(strings.TrimSpace(line))
		if m != nil && !slices.Contains(composed, unquote(m[1])) {
			return unquote(m[1])
		}
	}
	return ""
}

// wire removes the main script once nothing composed is left, leaving the account as found.
func wire(c alborz.SieveClient) error {
	scripts, err := c.ListScripts()
	if err != nil {
		return err
	}
	exists := map[string]bool{}
	active := ""
	for _, s := range scripts {
		exists[s.Name] = true
		if s.Active {
			active = s.Name
		}
	}
	var present []string
	for _, name := range composed {
		if exists[name] {
			present = append(present, name)
		}
	}
	// Only while the main script is active does its include remember the reader's own.
	own := ""
	switch {
	case active != "" && active != mainScript:
		own = active
	case exists[mainScript]:
		current, err := c.GetScript(mainScript)
		if err != nil {
			return err
		}
		own = ownScriptIn(current)
	}
	if len(present) == 0 {
		if !exists[mainScript] {
			return nil
		}
		if err := c.ActivateScript(own); err != nil {
			return err
		}
		return c.DeleteScript(mainScript)
	}
	if _, err := c.PutScript(mainScript, mainScriptFor(present, own)); err != nil {
		return err
	}
	if active != mainScript {
		return c.ActivateScript(mainScript)
	}
	return nil
}

// The server says NO for a missing script and a failure alike; the listing tells them apart.
func scriptIfAny(c alborz.SieveClient, name string) (content string, exists bool, err error) {
	scripts, err := c.ListScripts()
	if err != nil {
		return "", false, err
	}
	for _, s := range scripts {
		if s.Name == name {
			content, err = c.GetScript(name)
			return content, true, err
		}
	}
	return "", false, nil
}

// loadScript reads one optional source and preserves the page's optimistic-concurrency metadata.
func loadScript[T any](c alborz.SieveClient, name string, empty T, read func(string) (T, bool)) (value T, loaded string, byHand bool, err error) {
	content, exists, err := scriptIfAny(c, name)
	if err != nil || !exists {
		return empty, "", false, err
	}
	value, written := read(content)
	return value, Fingerprint(content), !written, nil
}

// HasExtension reports whether the server lets a script use the extension.
func HasExtension(c alborz.SieveClient, name string) bool {
	return slices.ContainsFunc(c.Extensions(), func(e string) bool { return strings.EqualFold(e, name) })
}

var (
	// ErrChanged is a script that differs from the one the page loaded.
	ErrChanged = errors.New("script changed on the server")
	// ErrByHand is a composed script in a shape the page did not write.
	ErrByHand = errors.New("script edited by hand")
)

// SaveScript writes a script; with loaded, only over the content it was loaded with.
func SaveScript(c alborz.SieveClient, name, content, loaded string) (warnings string, err error) {
	if loaded != "" {
		current, err := c.GetScript(name)
		if err != nil {
			return "", err
		}
		if Fingerprint(current) != loaded {
			return "", ErrChanged
		}
	}
	return c.PutScript(name, content)
}

// rewrite fails with ErrChanged if the script differs from loaded; an empty result deletes it.
func rewrite(c alborz.SieveClient, name, loaded string, change func(current string, exists bool) (string, error)) error {
	current, exists, err := scriptIfAny(c, name)
	if err != nil {
		return err
	}
	if exists != (loaded != "") || (exists && Fingerprint(current) != loaded) {
		return ErrChanged
	}
	next, err := change(current, exists)
	if err != nil {
		return err
	}
	switch {
	case next == "" && exists:
		if err := c.DeleteScript(name); err != nil {
			return err
		}
	case next != "":
		if _, err := c.PutScript(name, next); err != nil {
			return err
		}
	}
	return wire(c)
}
