package web

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/mail/imapsearch"
	"git.mehdix.org/alborz/search"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-imap/v2"
)

// The inbox's action set is the one offering archive, junk and trash.
const searchRole = "INBOX"

// folderTrackMax caps the folder column; long nested paths truncate.
const folderTrackMax = 16

func (p *plugin) handleSearch(ctx *web.Context) error {
	ask, err := readListAsk(ctx, "account")
	if err != nil {
		return err
	}
	ask.Query = strings.TrimSpace(ask.Query)
	if !mail.StarView(ask.View) {
		ask.View = ""
	}
	if ask.Query == "" && ask.View == "" {
		return ctx.Redirect(http.StatusFound, ctx.URL("mail.mailbox", "INBOX"))
	}
	data, down, err := p.searchRows(ctx, ask)
	if err != nil {
		return err
	}
	ctx.Unreachable(down)

	title := ctx.T("search.title")
	heading := fmt.Sprintf("%s: %s", title, ask.Query)
	if ask.View != "" {
		title = ViewLabel(ctx, ask.View)
		heading = title
	}
	base := web.NewBaseRenderData(ctx).WithTitle(heading)
	ibase := &IMAPBaseRenderData{BaseRenderData: *base, ListView: ask.View}
	if !ctx.Unified {
		sb, err := p.Rail(ctx.Login)
		if err != nil {
			return err
		}
		ibase = assembleIMAPBase(ctx, base, "", sb.Clone(), ask.View)
	}
	ibase.SidebarAccounts = p.sidebarAccounts(ctx)
	// No name, so the rail marks no folder.
	ibase.Mailbox = &mail.MailboxStatus{StatusData: &imap.StatusData{}, Label: title}
	data.IMAPBaseRenderData = *ibase
	data.Crumb = []CrumbLink{{Label: title}}
	return ctx.Render(http.StatusOK, listTemplate(ctx), data)
}

// searchRows are a page of a search across folders as its list draws them, and the accounts whose
// servers did not answer.
func (p *plugin) searchRows(ctx *web.Context, ask mail.ListAsk) (*MailboxRenderData, []string, error) {
	settings, err := mail.LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return nil, nil, err
	}
	logins := []*alborz.Login{ctx.Login}
	if ctx.Unified {
		logins = ctx.Logins()
	}
	list, down, err := p.Search(ctx.Request().Context(), logins, settings, ask)
	if err != nil {
		return nil, nil, err
	}
	track := 0
	for i := range list.Rows {
		track = max(track, len([]rune(list.Rows[i].Mailbox)))
	}
	data := listPage(ctx, ask, list)
	data.Merged, data.Spanning, data.Role = true, true, searchRole
	data.FolderQuery = search.Parse(ask.Query).Without(imapsearch.In)
	data.FolderTrack = template.CSS(fmt.Sprintf("%dch", min(track, folderTrackMax)))
	return data, down, nil
}

// found is mail's part in the search across sections: a search of every folder.
func (p *plugin) found(ctx *web.Context, q search.Query, view string, limit int) (web.Found, error) {
	ask := mail.ListAsk{Page: alborz.Page{Size: limit}, Query: q.String(), View: view}
	data, down, err := p.searchRows(ctx, ask)
	if err != nil {
		return web.Found{}, err
	}
	data.SelectForm = ""
	return web.Found{Rows: data, Total: data.Total, All: ctx.SearchURL("mail.search", q, view), Down: down}, nil
}

// ViewLabel names a view for a page heading.
func ViewLabel(ctx *web.Context, view string) string {
	if view == alborz.ViewStarred {
		return ctx.T("mailbox.starred")
	}
	return ctx.T("color." + view)
}
