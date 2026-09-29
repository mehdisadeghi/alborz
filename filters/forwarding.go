package filters

import (
	"fmt"
	"regexp"
	"strings"

	"git.mehdix.org/alborz"
)

// Forwarding keeps one Keep for all rules: one redirect without :copy cancels the copy.
type Forwarding struct {
	Forwards []Forward
	Keep     bool
}

// Forward is one address; one that is off stays in the script under a test that never holds.
type Forward struct {
	Address string
	Off     bool
}

// forwardLine matches only what the page writes; anything else was edited by hand.
var forwardLine = regexp.MustCompile(`^redirect( :copy)? ` + quoted + `;$`)

const forwardOff = "if false {"

func (f Forwarding) script() string {
	if len(f.Forwards) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Forwarding, composed on the Forwarding page.\n")
	if f.Keep {
		b.WriteString(requireLine([]string{"copy"}))
	}
	for _, fw := range f.Forwards {
		line := fmt.Sprintf("redirect %s;\n", quote(fw.Address))
		if f.Keep {
			line = fmt.Sprintf("redirect :copy %s;\n", quote(fw.Address))
		}
		if fw.Off {
			line = forwardOff + "\n    " + line + "}\n"
		}
		b.WriteString(line)
	}
	return b.String()
}

// readForwarding returns ok false for a script the page did not write.
func readForwarding(content string) (f Forwarding, ok bool) {
	f.Keep = true
	off := false
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "require "):
			continue
		case line == forwardOff:
			off = true
			continue
		case line == "}":
			off = false
			continue
		}
		m := forwardLine.FindStringSubmatch(line)
		if m == nil {
			return Forwarding{}, false
		}
		f.Forwards = append(f.Forwards, Forward{Address: unquote(m[2]), Off: off})
		if m[1] == "" {
			f.Keep = false
		}
	}
	return f, f.script() == content
}

// LoadForwarding reads the forwarding script; without one, forwarding keeps a copy where the server can.
func LoadForwarding(c alborz.SieveClient) (f Forwarding, loaded string, byHand bool, err error) {
	content, exists, err := scriptIfAny(c, ForwardScript)
	if err != nil || !exists {
		return Forwarding{Keep: HasExtension(c, "copy")}, "", false, err
	}
	f, ok := readForwarding(content)
	return f, Fingerprint(content), !ok, nil
}

// ChangeForwarding rewrites the forwarding as loaded; ErrByHand leaves a script the page did not write alone.
func ChangeForwarding(c alborz.SieveClient, loaded string, change func(f *Forwarding)) error {
	return rewrite(c, ForwardScript, loaded, func(current string, exists bool) (string, error) {
		f := Forwarding{Keep: HasExtension(c, "copy")}
		if exists {
			var ok bool
			if f, ok = readForwarding(current); !ok {
				return "", ErrByHand
			}
		}
		change(&f)
		return f.script(), nil
	})
}
