package mail

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-imap/v2"
)

func (p *plugin) handleSidebar(ctx *web.Context) error {
	base := web.NewBaseRenderData(ctx)
	page := ctx.QueryParam("path")
	if !strings.HasPrefix(page, "/") || strings.HasPrefix(page, "//") {
		page = ctx.Path("mail.mailbox", "INBOX")
	}
	u := *base.GlobalData.URL
	u.Path = page
	q := u.Query()
	q.Del("path")
	u.RawQuery = web.Query(q)
	base.GlobalData.URL = &u
	base.GlobalData.Path = strings.Split(page, "/")[1:]
	folder := ""
	if at, err := url.Parse(page); err == nil {
		_, values, _ := ctx.Server.Match(at)
		folder = values["mbox"]
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
	sb.Active = sb.Statuses[folder]
	data := assembleIMAPBase(ctx, base, folder, sb, view)
	data.SidebarAccounts = p.sidebarAccounts(ctx)
	return ctx.Render(http.StatusOK, "mail-rail", data)
}

type IMAPBaseRenderData struct {
	web.BaseRenderData
	CategorizedMailboxes CategorizedMailboxes
	Mailboxes            []MailboxInfo
	Mailbox              *MailboxStatus
	Inbox                *MailboxStatus
	Subscriptions        map[string]*MailboxStatus
	// ListView is the rail row a non-folder page answers to; not "View", which the message page uses.
	ListView string

	// SidebarAccounts is every account's folder tree; empty with a single account.
	SidebarAccounts []AccountSidebar
}

// AllUnseen sums the accounts' unseen counts for one role of the merged rail.
func (d *IMAPBaseRenderData) AllUnseen(role string) int {
	sum := 0
	for _, acc := range d.SidebarAccounts {
		c := acc.Categorized.Common
		var details *MailboxDetails
		switch role {
		case "INBOX":
			details = c.Inbox
		case "Drafts":
			details = c.Drafts
		case "Sent":
			details = c.Sent
		case "Junk":
			details = c.Junk
		case "Trash":
			details = c.Trash
		case "Archive":
			details = c.Archive
		}
		if details != nil && details.Info != nil && details.Info.Unseen > 0 {
			sum += details.Info.Unseen
		}
	}
	return sum
}

// AccountSidebar is one account's folder tree for the mail aside.
type AccountSidebar struct {
	Account     string
	Categorized CategorizedMailboxes
}

func (p *plugin) sidebarAccounts(ctx *web.Context) []AccountSidebar {
	logins := ctx.Logins()
	if len(logins) < 2 {
		return nil
	}
	var accounts []AccountSidebar
	for _, s := range logins {
		sb, err := p.Rail(s)
		if err != nil {
			ctx.Logger().Printf("sidebar for %q: %v", s.Username(), err)
			continue
		}
		ib := assembleIMAPBase(ctx, &web.BaseRenderData{}, "", sb.Clone(), "")
		accounts = append(accounts, AccountSidebar{
			Account:     s.Username(),
			Categorized: ib.CategorizedMailboxes,
		})
	}
	return accounts
}

func assembleIMAPBase(ctx *web.Context, base *web.BaseRenderData, mboxName string, sb Rail, view string) *IMAPBaseRenderData {
	if mboxName != "" {
		sb.Statuses[mboxName] = sb.Active
	}
	sb.Statuses["INBOX"] = sb.Inbox

	var categorized CategorizedMailboxes
	for i := range sb.Mailboxes {
		if sb.Active != nil && sb.Mailboxes[i].Name() == sb.Active.Mailbox && view == "" {
			sb.Mailboxes[i].Active = true
		}
		sb.Mailboxes[i].Label = sb.Mailboxes[i].Name()
		if role := sb.Mailboxes[i].Role(); role != "" {
			sb.Mailboxes[i].Label = ctx.T("aside." + role)
		}
		if sb.Active != nil && sb.Mailboxes[i].Name() == sb.Active.Mailbox {
			sb.Active.Label = sb.Mailboxes[i].Label
		}
		status := sb.Statuses[sb.Mailboxes[i].Name()]
		if status != nil {
			sb.Mailboxes[i].Unseen = int(*status.NumUnseen)
			sb.Mailboxes[i].Total = int(*status.NumMessages)
		}
		categorized.Append(sb.Mailboxes[i], status)
	}
	categorized.Trim()

	return &IMAPBaseRenderData{
		BaseRenderData:       *base,
		CategorizedMailboxes: categorized,
		Mailboxes:            sb.Mailboxes,
		Inbox:                sb.Inbox,
		Mailbox:              sb.Active,
		Subscriptions:        sb.Statuses,
		ListView:             view,
	}
}

func (p *plugin) newIMAPBaseRenderData(ctx *web.Context,
	base *web.BaseRenderData) (*IMAPBaseRenderData, error) {

	mboxName, err := mailboxRef(ctx)
	if err != nil {
		return nil, err
	}

	settings, err := LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return nil, fmt.Errorf("failed to load settings: %w", err)
	}

	sb, err := LoadRail(ctx.Request().Context(), ctx.Login, settings, mboxName)
	if err != nil {
		return nil, err
	}

	view, err := readView(ctx.QueryParams())
	if err != nil {
		return nil, err
	}
	ibase := assembleIMAPBase(ctx, base, mboxName, sb, view)
	ibase.SidebarAccounts = p.sidebarAccounts(ctx)
	return ibase, nil
}

// CrumbLink is one step of the path a page shows above itself.
type CrumbLink struct {
	Label   string
	URL     string
	Address bool // reads left to right whatever the page does
}

// Steps are named by path segment: a custom folder's Label is its whole wire name. folder is a folder's address.
func mailboxCrumb(mailboxes []MailboxInfo, name, account string, folder func(string) string) []CrumbLink {
	crumb := []CrumbLink{{Label: account, URL: folder("INBOX"), Address: true}}
	if name == "" {
		return crumb
	}

	byName := make(map[string]*MailboxInfo, len(mailboxes))
	delim := rune(0)
	for i := range mailboxes {
		byName[mailboxes[i].Name()] = &mailboxes[i]
		if delim == 0 {
			delim = mailboxes[i].Delim
		}
	}

	parts := []string{name}
	if delim != 0 {
		parts = strings.Split(name, string(delim))
	}
	for i := range parts {
		step := CrumbLink{Label: parts[i]}
		path := strings.Join(parts[:i+1], string(delim))
		if mbox, ok := byName[path]; ok {
			if mbox.Role() != "" {
				step.Label = mbox.Label
			}
			if !slices.Contains(mbox.Attrs, imap.MailboxAttrNoSelect) {
				step.URL = folder(mbox.Name())
			}
		}
		crumb = append(crumb, step)
	}
	return crumb
}
