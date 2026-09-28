package filters

import (
	"errors"
	"fmt"
	"net/http"
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

type ForwardingRenderData struct {
	alborz.BaseRenderData
	Forwarding
	Loaded string
	// ByHand says the script exists in a shape the page did not write.
	ByHand bool
	Script string
	// CanKeep and CanWire say the server supports :copy and include.
	CanKeep bool
	CanWire bool
	Address string
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
	return ctx.Render(http.StatusOK, "filters/forwarding", data)
}

func handleForwardingCreate(ctx *alborz.Context) error {
	data, err := forwardingData(ctx)
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "filters/forwarding-create", data)
}

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
		data.Address = address
		data.Refused(fmt.Sprintf(ctx.T("filters.notanaddress"), address))
		return ctx.Render(http.StatusUnprocessableEntity, "filters/forwarding-create", data)
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
