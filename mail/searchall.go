package mail

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail/search"
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
	if !StarView(ask.View) {
		ask.View = ""
	}
	if ask.Query == "" && ask.View == "" {
		return ctx.Redirect(http.StatusFound, ctx.URL("mail.mailbox", "INBOX"))
	}
	settings, err := LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return err
	}

	logins := []*alborz.Login{ctx.Login}
	if ctx.Unified {
		logins = ctx.Logins()
	}
	q := search.Parse(ask.Query)
	list, down, err := p.Search(ctx.Request().Context(), logins, settings, ask)
	if err != nil {
		return err
	}
	ctx.Unreachable(down)

	track := 0
	for i := range list.Rows {
		track = max(track, len([]rune(list.Rows[i].Mailbox)))
	}
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
	ibase.Mailbox = &MailboxStatus{StatusData: &imap.StatusData{}, Label: title}
	data := listPage(ctx, ask, list)
	data.IMAPBaseRenderData = *ibase
	data.Crumb = []CrumbLink{{Label: title}}
	data.Merged, data.Spanning, data.Role = true, true, searchRole
	data.FolderQuery = q.Without(search.In)
	data.FolderTrack = template.CSS(fmt.Sprintf("%dch", min(track, folderTrackMax)))
	return ctx.Render(http.StatusOK, listTemplate(ctx), data)
}

// ViewLabel names a view for a page heading.
func ViewLabel(ctx *web.Context, view string) string {
	if view == alborz.ViewStarred {
		return ctx.T("mailbox.starred")
	}
	return ctx.T("color." + view)
}
