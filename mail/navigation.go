package mail

import (
	"net/http"
	"net/url"
	"slices"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-imap/v2"
)

// mergedList is the list a message was opened from when that list spans
// accounts or folders: a merged folder or a search.
type mergedList struct {
	role   string
	search bool
	logins []*alborz.Login
	ask    listAsk
}

// mergedOrigin reads the list a message was opened from; nil where it is one folder's.
func mergedOrigin(ctx *web.Context) *mergedList {
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
	if route == "mail.search" {
		ask.spec.query = strings.TrimSpace(ask.spec.query)
		if !StarView(ask.spec.view) {
			ask.spec.view = ""
		}
		return &mergedList{search: true, logins: logins, ask: ask}
	}
	if route != "mail.mailbox" || len(logins) < 2 {
		return nil
	}
	return &mergedList{role: values["mbox"], logins: logins, ask: ask}
}

// mergedNeighbours places a message among the merged rows it was opened
// from, reading once a page further down where it is the last row read.
func (p *plugin) mergedNeighbours(ctx *web.Context, settings *Settings, m *mergedList, mailbox string, uid imap.UID) (web.Neighbours, error) {
	account := ctx.Login.Username()
	ask := m.ask
	var n web.Neighbours
	for read := range 2 {
		var e *listingEntry
		var down []string
		var err error
		if m.search {
			e, down, err = p.searchWindow(ctx.Request().Context(), m.logins, ask, settings)
		} else {
			e, _, down, err = p.mergedWindow(ctx.Request().Context(), ctx.Logins(), m.role, ask, settings, true)
		}
		if err != nil {
			return web.Neighbours{}, err
		}
		ctx.Unreachable(down)
		i := slices.IndexFunc(e.msgs, func(r IMAPMessage) bool {
			return r.Account == account && r.Mailbox == mailbox && r.UID == uid
		})
		if i < 0 {
			return web.Neighbours{}, nil
		}
		n = web.Neighbours{Position: i + 1, Total: e.total}
		if i > 0 {
			n.PrevURL = neighbourHref(ctx, e.msgs[i-1].Account, e.msgs[i-1].Mailbox, e.msgs[i-1].UID)
		}
		if i+1 < len(e.msgs) {
			n.NextURL = neighbourHref(ctx, e.msgs[i+1].Account, e.msgs[i+1].Mailbox, e.msgs[i+1].UID)
		}
		if n.NextURL != "" || len(e.msgs) >= e.total || read > 0 {
			break
		}
		ask.page.Number = len(e.msgs) / ask.page.Size
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
	sb, err := p.sidebarFor(ctx.Login)
	if err != nil {
		return err
	}
	sb = sb.clone()
	sb.active = sb.statuses[mailbox]
	data := &MessageRenderData{Query: ctx.QueryParam("query")}
	settings, err := LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return err
	}
	merged := mergedOrigin(ctx)
	list := folderList{data.Query, view, ctx.QueryParam("sort"), ctx.QueryParam("dir")}
	around, err := p.around(ctx.Request().Context(), ctx.Login, settings, &sb, mailbox, uid, list, web.PerPage(ctx), merged != nil)
	if err != nil {
		return err
	}
	data.Message, data.ThreadSupported, data.InReplyTo, data.Answers = around.msg, around.threadSupported, around.inReplyTo, around.answers
	place := around.place
	data.Neighbours = neighbours(ctx, mailbox, place)
	if merged != nil {
		if data.Neighbours, err = p.mergedNeighbours(ctx, settings, merged, mailbox, uid); err != nil {
			return err
		}
	}
	data.IMAPBaseRenderData = *assembleIMAPBase(ctx, web.NewBaseRenderData(ctx), mailbox, sb, view)
	return ctx.Render(http.StatusOK, "message-context", data)
}

func neighbours(ctx *web.Context, mboxName string, place listPlace) web.Neighbours {
	account := ctx.QueryParam("account")
	return web.Neighbours{
		PrevURL:  neighbourHref(ctx, account, mboxName, place.newer),
		NextURL:  neighbourHref(ctx, account, mboxName, place.older),
		Position: place.position, Total: place.total,
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
