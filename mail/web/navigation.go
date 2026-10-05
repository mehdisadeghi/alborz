package web

import (
	"net/http"
	"net/url"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-imap/v2"
)

// mergedOrigin reads the list a message was opened from; nil where it is one folder's.
func mergedOrigin(ctx *web.Context) *mail.Origin {
	from := ctx.From()
	if from == "" {
		return nil
	}
	u, err := url.Parse(from)
	if err != nil {
		return nil
	}
	q := u.Query()
	logins := ctx.Logins()
	if account := q.Get("account"); account != "" {
		login := ctx.LoginFor(account)
		if login == nil {
			return nil
		}
		logins = []*alborz.Login{login}
	}
	ask, err := readListAskAt(ctx, q, "account")
	if err != nil {
		return nil
	}
	route, values, _ := ctx.Server.Match(u)
	// The search across sections shows mail's search of every folder, so a message walks that.
	if route == "mail.search" || route == "core.search" {
		ask.Query = strings.TrimSpace(ask.Query)
		if !mail.StarView(ask.View) {
			ask.View = ""
		}
		return &mail.Origin{Search: true, Logins: logins, Ask: ask}
	}
	if route != "mail.mailbox" || len(logins) < 2 {
		return nil
	}
	if logins, err = merging(ctx, logins, values["mbox"]); err != nil {
		return nil
	}
	return &mail.Origin{Role: values["mbox"], Logins: logins, Ask: ask}
}

// mergedNeighbours places a message among the merged rows it was opened from.
func (p *plugin) mergedNeighbours(ctx *web.Context, settings *mail.Settings, o *mail.Origin, mailbox string, uid imap.UID) (web.Neighbours, error) {
	prev, next, position, total, down, err := p.Beside(ctx.Request().Context(), settings, *o, ctx.Login.Username(), mailbox, uid)
	if err != nil {
		return web.Neighbours{}, err
	}
	ctx.Unreachable(down)
	if position == 0 {
		return web.Neighbours{}, nil
	}
	n := web.Neighbours{Position: position, Total: total}
	if prev != nil {
		n.PrevURL = neighbourHref(ctx, prev.Account, prev.Mailbox, prev.UID)
	}
	if next != nil {
		n.NextURL = neighbourHref(ctx, next.Account, next.Mailbox, next.UID)
	}
	return n, nil
}

func (p *plugin) handleMessageNavigation(ctx *web.Context) error {
	mailbox, uid, err := MessageRef(ctx)
	if err != nil {
		return err
	}
	view, err := readView(ctx.QueryParams())
	if err != nil {
		return err
	}
	sb, err := p.Rail(ctx.Login)
	if err != nil {
		return err
	}
	sb = sb.Clone()
	sb.Active = sb.Statuses[mailbox]
	data := &MessageRenderData{Query: ctx.QueryParam("query")}
	settings, err := mail.LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return err
	}
	merged := mergedOrigin(ctx)
	list := mail.ListAsk{Page: alborz.Page{Size: web.PerPage(ctx)}, Query: data.Query, View: view,
		Sort: ctx.QueryParam("sort"), Dir: ctx.QueryParam("dir")}
	around, err := p.Around(ctx.Request().Context(), ctx.Login, settings, &sb, mailbox, uid, list, merged != nil)
	if err != nil {
		return err
	}
	data.Message, data.ThreadSupported, data.InReplyTo, data.Answers = around.Message, around.ThreadSupported, around.InReplyTo, around.Answers
	place := around.Place
	data.Neighbours = neighbours(ctx, mailbox, place)
	if merged != nil {
		if data.Neighbours, err = p.mergedNeighbours(ctx, settings, merged, mailbox, uid); err != nil {
			return err
		}
	}
	data.IMAPBaseRenderData = *assembleIMAPBase(ctx, web.NewBaseRenderData(ctx), mailbox, sb, view)
	return ctx.Render(http.StatusOK, "message-context", data)
}

func neighbours(ctx *web.Context, mboxName string, place mail.Place) web.Neighbours {
	account := ctx.QueryParam("account")
	return web.Neighbours{
		PrevURL:  neighbourHref(ctx, account, mboxName, place.Newer),
		NextURL:  neighbourHref(ctx, account, mboxName, place.Older),
		Position: place.Position, Total: place.Total,
	}
}

// Without the narrowing and order, one step sideways lands in the plain folder.
func neighbourHref(ctx *web.Context, account, mboxName string, uid imap.UID) string {
	if uid == 0 {
		return ""
	}
	kept := url.Values{}
	for _, name := range []string{"query", "view", "sort", "dir", "ipp", "all", "thread"} {
		if value := ctx.QueryParam(name); value != "" {
			kept.Set(name, value)
		}
	}
	if account != "" {
		kept.Set("account", account)
	}
	if from := ctx.From(); from != "" {
		kept.Set("from", from)
	}
	href := ctx.Path("mail.message", mboxName, uid)
	if q := web.Query(kept); q != "" {
		href += "?" + q
	}
	return href
}
