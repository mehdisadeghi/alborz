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
	ask.spec.query = strings.TrimSpace(ask.spec.query)
	if !StarView(ask.spec.view) {
		ask.spec.view = ""
	}
	spec := ask.spec
	if spec.query == "" && spec.view == "" {
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
	q := search.Parse(spec.query)
	merged, down, err := p.searchWindow(ctx.Request().Context(), logins, ask, settings)
	if err != nil {
		return err
	}
	ctx.Unreachable(down)
	page, msgs := cutPage(merged, ask.page)

	track := 0
	for i := range msgs {
		track = max(track, len([]rune(msgs[i].Mailbox)))
	}
	title := ctx.T("search.title")
	heading := fmt.Sprintf("%s: %s", title, spec.query)
	if spec.view != "" {
		title = ViewLabel(ctx, spec.view)
		heading = title
	}
	base := web.NewBaseRenderData(ctx).WithTitle(heading)
	ibase := &IMAPBaseRenderData{BaseRenderData: *base, ListView: spec.view}
	if !ctx.Unified {
		sb, err := p.sidebarFor(ctx.Login)
		if err != nil {
			return err
		}
		ibase = assembleIMAPBase(ctx, base, "", sb.clone(), spec.view)
	}
	ibase.SidebarAccounts = p.sidebarAccounts(ctx)
	// No name, so the rail marks no folder.
	ibase.Mailbox = &MailboxStatus{StatusData: &imap.StatusData{}, Label: title}
	data := listPage(ctx, spec, page, merged, msgs)
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
