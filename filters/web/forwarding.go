package web

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/filters"
	"git.mehdix.org/alborz/web"
)

type ForwardingRenderData struct {
	sievePageData
	filters.Forwarding
	// CanKeep says the server supports :copy.
	CanKeep bool
	Address string
}

func forwardingData(ctx *web.Context) (*ForwardingRenderData, error) {
	data := &ForwardingRenderData{
		sievePageData: sievePage(ctx, ctx.T("filters.forwarding"), filters.ForwardScript),
	}
	err := ctx.DoSieve(func(c alborz.SieveClient) error {
		data.CanKeep = filters.HasExtension(c, "copy")
		data.CanWire = filters.HasExtension(c, "include")
		var err error
		data.Forwarding, data.Loaded, data.ByHand, err = filters.LoadForwarding(c)
		return err
	})
	return data, err
}

func handleForwarding(ctx *web.Context) error {
	return renderForwarding(ctx, "filters/forwarding")
}

func renderForwarding(ctx *web.Context, page string) error {
	data, err := forwardingData(ctx)
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, page, data)
}

func handleForwardingCreate(ctx *web.Context) error {
	return renderForwarding(ctx, "filters/forwarding-create")
}

func changeForwarding(ctx *web.Context, change func(f *filters.Forwarding)) error {
	return ctx.DoSieve(func(c alborz.SieveClient) error {
		return filters.ChangeForwarding(c, ctx.FormValue("loaded"), change)
	})
}

func handleForwardingAdd(ctx *web.Context) error {
	address := strings.TrimSpace(ctx.FormValue("address"))
	if !strings.Contains(address, "@") {
		data, err := forwardingData(ctx)
		if err != nil {
			return err
		}
		data.Address = address
		return web.RenderRefused(ctx, "filters/forwarding-create", data, fmt.Sprintf(ctx.T("filters.notanaddress"), address))
	}
	return answer(ctx, changeForwarding(ctx, func(f *filters.Forwarding) {
		for _, fw := range f.Forwards {
			if fw.Address == address {
				return
			}
		}
		f.Forwards = append(f.Forwards, filters.Forward{Address: address})
	}), "filters.forwarding", ctx.T("notice.forwardingsaved"))
}

func handleForwardingDelete(ctx *web.Context) error {
	address := ctx.FormValue("address")
	return answer(ctx, changeForwarding(ctx, func(f *filters.Forwarding) {
		kept := f.Forwards[:0]
		for _, fw := range f.Forwards {
			if fw.Address != address {
				kept = append(kept, fw)
			}
		}
		f.Forwards = kept
	}), "filters.forwarding", ctx.T("notice.forwardingsaved"))
}

func handleForwardingToggle(ctx *web.Context) error {
	address := ctx.FormValue("address")
	return answer(ctx, changeForwarding(ctx, func(f *filters.Forwarding) {
		for i, fw := range f.Forwards {
			if fw.Address == address {
				f.Forwards[i].Off = !fw.Off
			}
		}
	}), "filters.forwarding", ctx.T("notice.forwardingsaved"))
}

func handleForwardingKeep(ctx *web.Context) error {
	keep := ctx.FormValue("keep") != ""
	return answer(ctx, changeForwarding(ctx, func(f *filters.Forwarding) { f.Keep = keep }), "filters.forwarding", ctx.T("notice.forwardingsaved"))
}

func answer(ctx *web.Context, err error, route, saved string) error {
	switch {
	case errors.Is(err, filters.ErrChanged):
		ctx.Notify(web.Notice{Kind: web.NoticeWarning, Text: ctx.T("filters.changed")})
	case errors.Is(err, filters.ErrByHand):
		ctx.Notify(web.Notice{Kind: web.NoticeWarning, Text: ctx.T("filters.byhand")})
	case err != nil:
		ctx.Notify(web.Notice{Kind: web.NoticeWarning, Text: err.Error()})
	default:
		ctx.PutNotice(saved)
	}
	return ctx.FoundRedirect(ctx.NextOr(ctx.URL(route)))
}
