package alborzbase

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

// searchRole is the role a results page acts through. Its rows come
// from many folders, and the actions of a merged list go by role -
// archive, junk, trash - with each row's account resolving its own
// folder, which is exactly what rows from anywhere need. The inbox's
// set of actions is the one that offers all three.
const searchRole = "INBOX"

// folderTrackMax bounds the folder names' track: a nested folder's full
// path can be longer than the sender it sits beside, and truncates.
const folderTrackMax = 16

// searchFolders are the folders of one account a query reaches. IMAP
// has no search across folders, so each is asked in turn. in: names
// one, by role or by name; without it Junk and Trash are left out, as
// they are in Gmail, until in:anywhere asks for them.
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

// folderNamed says whether in: means this folder: its role, its full
// name or the last step of it.
func folderNamed(mbox *MailboxInfo, named string) bool {
	role := mbox.role()
	last := mbox.Name()[strings.LastIndex(mbox.Name(), string(mbox.Delim))+1:]
	return strings.EqualFold(named, role) || strings.EqualFold(named, mbox.Name()) || strings.EqualFold(named, last) ||
		(role == "junk" && strings.EqualFold(named, "spam"))
}

// handleSearch answers a query over every folder of the accounts in
// scope: the URL's account, or all of them. Each folder gives its own
// newest window and the page is cut from the merge, the way the merged
// view cuts its page from the accounts.
func handleSearch(ctx *alborz.Context) error {
	ask, err := readListAsk(ctx, "account")
	if err != nil {
		return err
	}
	ask.spec.query = strings.TrimSpace(ask.spec.query)
	// The page answers two asks with one fan-out: a query, and a star
	// view, which is a query with a name and no words.
	if !StarView(ask.spec.view) {
		ask.spec.view = ""
	}
	spec := ask.spec
	if spec.query == "" && spec.view == "" {
		return ctx.Redirect(http.StatusFound, ctx.AccountPath("/mailbox/INBOX"))
	}
	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return err
	}

	sessions := []*alborz.Session{ctx.Session}
	if ctx.Unified {
		sessions = ctx.Sessions()
	}
	q := ParseQuery(spec.query)
	class, bound := listingBudget(spec)
	var (
		mu   sync.Mutex
		junk = map[rowRef]bool{}
	)
	merged, err := gather(ctx, sessions, spec, func(s *alborz.Session) (*listingEntry, error) {
		sb, err := sidebarFor(s)
		if err != nil {
			return nil, err
		}
		// A folder is a turn on the connection with the budget of
		// one: the account's other pages are served between two, and
		// an account of many folders is slow rather than cut off as
		// unreachable. What an account that fails had found so far is
		// left out whole, its count with its rows.
		found := &listingEntry{sortSupported: true}
		for _, folder := range searchFolders(sb.mailboxes, q) {
			e, err := readOn(s, class, bound, func(c *imapclient.Client) (*listingEntry, error) {
				return fetchUnifiedAccount(c, s.Username(), folder, spec, settings, ask.window(), false)
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
		// One account's rail is its folder tree, which the merged rail
		// builds for itself from the accounts.
		sb, err := sidebarFor(ctx.Session)
		if err != nil {
			return err
		}
		ibase = assembleIMAPBase(ctx, base, "", sb.clone(), spec.view)
	}
	ibase.SidebarAccounts = sidebarAccounts(ctx)
	// No name: the rail marks the folder the page shows, and this page
	// shows none.
	ibase.Mailbox = &MailboxStatus{StatusData: &imap.StatusData{}, Label: title}
	data := listPage(ctx, ask, merged, msgs)
	data.IMAPBaseRenderData = *ibase
	data.Crumb = []CrumbLink{{Label: title}}
	data.Merged, data.Spanning, data.Role = true, true, searchRole
	data.FolderQuery = q.Without(queryScope)
	data.FolderTrack = template.CSS(fmt.Sprintf("%dch", min(track, folderTrackMax)))
	return ctx.Render(http.StatusOK, listTemplate(ctx), data)
}
