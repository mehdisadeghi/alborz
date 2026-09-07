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
	Addresses []string
	Keep      bool
}

// forwardLine matches one line of a script the page wrote itself; a
// script that holds anything else was edited by hand.
var forwardLine = regexp.MustCompile(`^redirect( :copy)? ` + quoted + `;$`)

func (f Forwarding) script() string {
	if len(f.Addresses) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Forwarding, composed on the Forwarding page.\n")
	if f.Keep {
		b.WriteString(requireLine([]string{"copy"}))
	}
	for _, a := range f.Addresses {
		if f.Keep {
			fmt.Fprintf(&b, "redirect :copy %s;\n", quote(a))
		} else {
			fmt.Fprintf(&b, "redirect %s;\n", quote(a))
		}
	}
	return b.String()
}

// readForwarding reads the page's own shape back. ok is false for a
// script written by someone else, which the page must not pretend to
// understand.
func readForwarding(content string) (f Forwarding, ok bool) {
	f.Keep = true
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "require ") {
			continue
		}
		m := forwardLine.FindStringSubmatch(line)
		if m == nil {
			return Forwarding{}, false
		}
		f.Addresses = append(f.Addresses, unquote(m[2]))
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
		for _, a := range f.Addresses {
			if a == address {
				return
			}
		}
		f.Addresses = append(f.Addresses, address)
	})
	return answer(ctx, err, "/filters/forwarding", ctx.T("notice.forwardingsaved"))
}

func handleForwardingDelete(ctx *alborz.Context) error {
	address := ctx.FormValue("address")
	err := changeForwarding(ctx, func(f *Forwarding) {
		kept := f.Addresses[:0]
		for _, a := range f.Addresses {
			if a != address {
				kept = append(kept, a)
			}
		}
		f.Addresses = kept
	})
	return answer(ctx, err, "/filters/forwarding", ctx.T("notice.forwardingsaved"))
}

func handleForwardingKeep(ctx *alborz.Context) error {
	keep := ctx.FormValue("keep") != ""
	err := changeForwarding(ctx, func(f *Forwarding) { f.Keep = keep })
	return answer(ctx, err, "/filters/forwarding", ctx.T("notice.forwardingsaved"))
}

// answer returns to a page, saying what happened: a change made
// elsewhere, a refusal, or the save.
func answer(ctx *alborz.Context, err error, path, saved string) error {
	switch {
	case errors.Is(err, errChanged{}):
		ctx.Session.Notify(alborz.Notice{Kind: alborz.NoticeWarning, Text: ctx.T("filters.changed")})
	case err != nil:
		ctx.Session.Notify(alborz.Notice{Kind: alborz.NoticeWarning, Text: err.Error()})
	default:
		ctx.Session.PutNotice(saved)
	}
	return ctx.Redirect(http.StatusFound, ctx.AccountPath(path))
}
