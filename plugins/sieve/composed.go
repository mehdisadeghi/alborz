package alborzsieve

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"git.mehdix.org/alborz"
)

// The pages compose ordinary scripts under these names and read them
// back; nothing marks them as ours on the server. The main script does
// no more than include the composed ones and the script the reader had
// active before, so a raw editor shows the whole arrangement.
const (
	mainScript     = "alborz"
	rulesScript    = "alborz-rules"
	forwardScript  = "alborz-forward"
	vacationScript = "alborz-vacation"
)

// composed lists the scripts the pages know how to write, in the order
// the main script runs them: rules first, since one may stop delivery
// of what the rest would forward or answer.
var composed = []string{rulesScript, forwardScript, vacationScript}

func isComposed(name string) bool {
	for _, c := range composed {
		if c == name {
			return true
		}
	}
	return false
}

// fingerprint names a script's content. ManageSieve keeps no version
// or date for a script, so the content itself is what a save compares
// against to notice an edit made elsewhere in the meantime.
func fingerprint(content string) string {
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

// quoted matches one Sieve quoted string, escapes included.
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

// mainScriptFor writes the main script: the composed scripts that
// exist, then the reader's own, by name.
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

// ownScriptIn names the reader's own script the main script includes:
// the include that is not one of the composed ones.
func ownScriptIn(content string) string {
	for _, line := range strings.Split(content, "\n") {
		m := includeLine.FindStringSubmatch(strings.TrimSpace(line))
		if m != nil && !isComposed(unquote(m[1])) {
			return unquote(m[1])
		}
	}
	return ""
}

// wire puts the main script in front of the reader's own and activates
// it, or takes it away again once nothing composed is left, so that
// turning every page off leaves the account as it was found.
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
	// The reader's own script is whatever is active now, which is their
	// latest choice; only when the main script is the active one does
	// its include line remember it.
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

// scriptIfAny reads a script that may not be there. A missing script
// is an answer, not a failure, and the listing is what tells the two
// apart: the server says NO for both.
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

func hasExtension(c alborz.SieveClient, name string) bool {
	for _, e := range c.Extensions() {
		if strings.EqualFold(e, name) {
			return true
		}
	}
	return false
}

// errChanged says the script the page read is not the one the server
// holds now; the page reports it and the reader starts over.
type errChanged struct{}

func (errChanged) Error() string { return "script changed on the server" }

// rewrite replaces a composed script with what change makes of the
// current one, under the same check the raw editor makes: the script
// the page read, named by loaded, must still be the one on the server.
// An empty result deletes the script. Wiring follows either way.
func rewrite(c alborz.SieveClient, name, loaded string, change func(current string, exists bool) (string, error)) error {
	current, exists, err := scriptIfAny(c, name)
	if err != nil {
		return err
	}
	if exists != (loaded != "") || (exists && fingerprint(current) != loaded) {
		return errChanged{}
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
