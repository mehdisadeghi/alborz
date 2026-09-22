package alborzsieve

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"git.mehdix.org/alborz"
)

// Forwarding is what the forwarding page composes: where a message goes,
// one address per rule, and whether one stays here as well. Keeping is
// one choice for all of them: a single redirect without :copy cancels
// the copy for the message, whatever the other rules say.
type Forwarding struct {
	Forwards []Forward
	Keep     bool
}

// A Forward that is off stays in the script under a test that never
// holds, so it is read back whole and any client still shows it.
type Forward struct {
	Address string
	Off     bool
}

// forwardLine matches one line of a script the page wrote itself; a
// script that holds anything else was edited by hand.
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

// readForwarding reads the page's own shape back. ok is false for a
// script written by someone else, which the page must not pretend to
// understand.
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

type ForwardingRenderData struct {
	alborz.BaseRenderData
	Forwarding
	// Loaded fingerprints the script as the page read it, empty when
	// there was none; every write checks the server still holds that.
	Loaded string
	// ByHand says the script exists but is not the page's own shape;
	// Script names it, for the link to the raw editor.
	ByHand bool
	Script string
	// CanKeep says the server can file a copy while forwarding, and
	// CanWire that it can run one script from another, without which
	// the page has no way to put its script in front of the reader's.
	CanKeep bool
	CanWire bool
	// Address is what the create form holds, and Error what it answers.
	Address string
	Error   string
	Rail    map[string][]alborz.RailRow
}

func forwardingData(ctx *alborz.Context) (*ForwardingRenderData, error) {
	data := &ForwardingRenderData{
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("filters.forwarding")),
		Script:         forwardScript,
		Rail:           rail(ctx),
	}
	err := ctx.DoSieve(func(c alborz.SieveClient) error {
		data.CanKeep = hasExtension(c, "copy")
		data.CanWire = hasExtension(c, "include")
		content, exists, err := scriptIfAny(c, forwardScript)
		if err != nil || !exists {
			data.Keep = data.CanKeep
			return err
		}
		data.Loaded = fingerprint(content)
		f, ok := readForwarding(content)
		data.Forwarding, data.ByHand = f, !ok
		return nil
	})
	return data, err
}

func handleForwarding(ctx *alborz.Context) error {
	data, err := forwardingData(ctx)
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "forwarding.html", data)
}

func handleForwardingCreate(ctx *alborz.Context) error {
	data, err := forwardingData(ctx)
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "forwarding-create.html", data)
}

// changeForwarding rewrites the script with the change applied, and
// refuses a script changed elsewhere or written by hand.
func changeForwarding(ctx *alborz.Context, change func(f *Forwarding)) error {
	loaded := ctx.FormValue("loaded")
	return ctx.DoSieve(func(c alborz.SieveClient) error {
		return rewrite(c, forwardScript, loaded, func(current string, exists bool) (string, error) {
			f := Forwarding{Keep: hasExtension(c, "copy")}
			if exists {
				var ok bool
				if f, ok = readForwarding(current); !ok {
					return "", errors.New(ctx.T("filters.byhand"))
				}
			}
			change(&f)
			return f.script(), nil
		})
	})
}

func handleForwardingAdd(ctx *alborz.Context) error {
	address := strings.TrimSpace(ctx.FormValue("address"))
	if !strings.Contains(address, "@") {
		data, err := forwardingData(ctx)
		if err != nil {
			return err
		}
		data.Address, data.Error = address, fmt.Sprintf(ctx.T("filters.notanaddress"), address)
		return ctx.Render(http.StatusUnprocessableEntity, "forwarding-create.html", data)
	}
	err := changeForwarding(ctx, func(f *Forwarding) {
		for _, fw := range f.Forwards {
			if fw.Address == address {
				return
			}
		}
		f.Forwards = append(f.Forwards, Forward{Address: address})
	})
	return answer(ctx, err, "/filters/forwarding", ctx.T("notice.forwardingsaved"))
}

func handleForwardingDelete(ctx *alborz.Context) error {
	address := ctx.FormValue("address")
	err := changeForwarding(ctx, func(f *Forwarding) {
		kept := f.Forwards[:0]
		for _, fw := range f.Forwards {
			if fw.Address != address {
				kept = append(kept, fw)
			}
		}
		f.Forwards = kept
	})
	return answer(ctx, err, "/filters/forwarding", ctx.T("notice.forwardingsaved"))
}

func handleForwardingToggle(ctx *alborz.Context) error {
	address := ctx.FormValue("address")
	err := changeForwarding(ctx, func(f *Forwarding) {
		for i, fw := range f.Forwards {
			if fw.Address == address {
				f.Forwards[i].Off = !fw.Off
			}
		}
	})
	return answer(ctx, err, "/filters/forwarding", ctx.T("notice.forwardingsaved"))
}

func handleForwardingKeep(ctx *alborz.Context) error {
	keep := ctx.FormValue("keep") != ""
	err := changeForwarding(ctx, func(f *Forwarding) { f.Keep = keep })
	return answer(ctx, err, "/filters/forwarding", ctx.T("notice.forwardingsaved"))
}

// answer returns to a page - the one the form came from, or path -
// saying what happened: a change made elsewhere, a refusal, or the save.
func answer(ctx *alborz.Context, err error, path, saved string) error {
	switch {
	case errors.Is(err, errChanged{}):
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeWarning, Text: ctx.T("filters.changed")})
	case err != nil:
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeWarning, Text: err.Error()})
	default:
		ctx.PutNotice(saved)
	}
	return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.AccountPath(path)))
}
