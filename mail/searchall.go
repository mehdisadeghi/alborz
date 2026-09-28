package mail

import (
	"fmt"
	"html/template"
	"net/http"
	"slices"
	"strings"
	"sync"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// The inbox's action set is the one offering archive, junk and trash.
const searchRole = "INBOX"

// folderTrackMax caps the folder column; long nested paths truncate.
const folderTrackMax = 16

func searchFolders(mailboxes []MailboxInfo, q Query) []string {
	named, everywhere := q.Scope()
	excluded := q.Excluded()
	var out []string
	for i := range mailboxes {
		mbox := &mailboxes[i]
		if mbox.IsInternal() || slices.ContainsFunc(excluded, func(name string) bool { return folderNamed(mbox, name) }) {
			continue
		}
		role := mbox.role()
		switch {
		case named != "":
			if !folderNamed(mbox, named) {
				continue
			}
		case !everywhere && (role == "junk" || role == "trash"):
			continue
		}
		out = append(out, mbox.Name())
	}
	return out
}

func folderNamed(mbox *MailboxInfo, named string) bool {
	role := mbox.role()
	last := mbox.Name()[strings.LastIndex(mbox.Name(), string(mbox.Delim))+1:]
	return strings.EqualFold(named, role) || strings.EqualFold(named, mbox.Name()) || strings.EqualFold(named, last) ||
		(role == "junk" && strings.EqualFold(named, "spam"))
}

func (p *plugin) handleSearch(ctx *alborz.Context) error {
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
		return ctx.Redirect(http.StatusFound, ctx.AccountPath("/mailbox/INBOX"))
	}
	settings, err := LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return err
	}

	logins := []*alborz.Login{ctx.Login}
	if ctx.Unified {
		logins = ctx.Logins()
	}
	q := ParseQuery(spec.query)
	class, bound := listingBudget(spec)
	var (
		mu   sync.Mutex
		junk = map[rowRef]bool{}
	)
	merged, err := gather(ctx, logins, spec, func(s *alborz.Login) (*listingEntry, error) {
		sb, err := p.sidebarFor(s)
		if err != nil {
			return nil, err
		}
		// One turn per folder, so the account's other pages are served between them.
		found := &listingEntry{sortSupported: true}
		for _, folder := range searchFolders(sb.mailboxes, q) {
			e, err := readOn(s, class, bound, func(c *imapclient.Client) (*listingEntry, error) {
				return fetchUnifiedAccount(c, s.Username(), folder, spec, settings, ask.window(), listFetchOptions, false)
			})(ctx.Request().Context())
			if err != nil {
				return nil, err
			}
			found.absorb(e)
		}
		mu.Lock()
		for i := range sb.mailboxes {
			if sb.mailboxes[i].role() == "junk" {
				junk[rowRef{account: s.Username(), mailbox: sb.mailboxes[i].Name()}] = true
			}
		}
		mu.Unlock()
		return found, nil
	})
	if err != nil {
		return err
	}
	msgs := cutPage(merged.msgs, ask)

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
	base := alborz.NewBaseRenderData(ctx).WithTitle(heading)
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
	data := listPage(ctx, ask, merged, msgs)
	data.IMAPBaseRenderData = *ibase
	data.Crumb = []CrumbLink{{Label: title}}
	data.Merged, data.Spanning, data.Role = true, true, searchRole
	data.FolderQuery = q.Without(queryScope)
	data.FolderTrack = template.CSS(fmt.Sprintf("%dch", min(track, folderTrackMax)))
	return ctx.Render(http.StatusOK, listTemplate(ctx), data)
}
