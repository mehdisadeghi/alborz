package alborzbase

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

func handleSidebar(ctx *alborz.Context) error {
	base := alborz.NewBaseRenderData(ctx)
	page := ctx.QueryParam("path")
	if !strings.HasPrefix(page, "/") || strings.HasPrefix(page, "//") {
		page = "/mailbox/INBOX"
	}
	u := *base.GlobalData.URL
	u.Path = page
	q := u.Query()
	q.Del("path")
	u.RawQuery = q.Encode()
	base.GlobalData.URL = &u
	base.GlobalData.Path = strings.Split(page, "/")[1:]
	folder := ""
	parts := strings.Split(page, "/")
	if len(parts) > 2 && (parts[1] == "mailbox" || parts[1] == "message") {
		folder, _ = url.PathUnescape(parts[2])
	}
	view, err := readView(ctx)
	if err != nil {
		return err
	}
	sb, err := sidebarFor(ctx.Session)
	if err != nil {
		return err
	}
	sb = sb.clone()
	sb.active = sb.statuses[folder]
	data := assembleIMAPBase(ctx, base, folder, sb, view)
	data.SidebarAccounts = sidebarAccounts(ctx)
	return ctx.Render(http.StatusOK, "aside", data)
}

type IMAPBaseRenderData struct {
	alborz.BaseRenderData
	CategorizedMailboxes CategorizedMailboxes
	Mailboxes            []MailboxInfo
	Mailbox              *MailboxStatus
	Inbox                *MailboxStatus
	Subscriptions        map[string]*MailboxStatus
	// ListView is the rail row the page answers to when it is not a
	// folder: starred, unread, or a star colour. Not "View": the
	// message page's own View is the rendered part.
	ListView string

	// Every account's folder tree, for the multi-account aside; empty
	// with a single account.
	SidebarAccounts []AccountSidebar
}

// AllUnseen sums the accounts' unseen counts for one role of the
// merged rail, from the trees the rail already holds.
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

// Organizes mailboxes into common/uncommon categories
type CategorizedMailboxes struct {
	Common struct {
		Inbox   *MailboxDetails
		Drafts  *MailboxDetails
		Sent    *MailboxDetails
		Junk    *MailboxDetails
		Trash   *MailboxDetails
		Archive *MailboxDetails
	}
	Additional []MailboxDetails
}

func (cc *CategorizedMailboxes) Append(mi MailboxInfo, status *MailboxStatus) {
	if mi.IsInternal() {
		return
	}
	details := &MailboxDetails{
		Info:   &mi,
		Status: status,
	}
	switch mi.role() {
	case "inbox":
		cc.Common.Inbox = details
	case "drafts":
		cc.Common.Drafts = details
	case "sent":
		cc.Common.Sent = details
	case "junk":
		cc.Common.Junk = details
	case "trash":
		cc.Common.Trash = details
	case "archive":
		cc.Common.Archive = details
	default:
		cc.Additional = append(cc.Additional, *details)
	}
}

// MailboxNode is one folder in the nested sidebar tree. Details is nil
// for a parent that only exists as a path prefix.
type MailboxNode struct {
	Name     string
	Details  *MailboxDetails
	Children []MailboxNode
}

// AdditionalTree nests the custom folders by their hierarchy delimiter,
// the way desktop clients show them. Folders under INBOX belong to
// InboxTree instead.
func (cc CategorizedMailboxes) AdditionalTree() []MailboxNode {
	var out []MailboxNode
	for _, n := range cc.additionalForest() {
		if !strings.EqualFold(n.Name, "INBOX") || n.Details != nil {
			out = append(out, n)
		}
	}
	return out
}

// InboxTree returns the inbox with any INBOX-nested folders as children,
// so they hang under the real Inbox row like desktop clients show them.
func (cc CategorizedMailboxes) InboxTree() *MailboxNode {
	inbox := cc.Common.Inbox
	if inbox == nil {
		return nil
	}
	node := MailboxNode{Name: inbox.Info.Label, Details: inbox}
	for _, n := range cc.additionalForest() {
		if strings.EqualFold(n.Name, "INBOX") && n.Details == nil {
			node.Children = append(node.Children, n.Children...)
		}
	}
	return &node
}

// CommonNodes lists the special-use folders after the inbox as leaf
// nodes, so the aside renders every folder kind through one template.
func (cc CategorizedMailboxes) CommonNodes() []MailboxNode {
	var nodes []MailboxNode
	for _, d := range []*MailboxDetails{
		cc.Common.Drafts, cc.Common.Sent, cc.Common.Junk,
		cc.Common.Trash, cc.Common.Archive,
	} {
		if d != nil {
			nodes = append(nodes, MailboxNode{Name: d.Info.Label, Details: d})
		}
	}
	return nodes
}

func (cc CategorizedMailboxes) additionalForest() []MailboxNode {
	var roots []MailboxNode
	find := func(nodes *[]MailboxNode, name string) *MailboxNode {
		for i := range *nodes {
			if (*nodes)[i].Name == name {
				return &(*nodes)[i]
			}
		}
		*nodes = append(*nodes, MailboxNode{Name: name})
		return &(*nodes)[len(*nodes)-1]
	}
	for i := range cc.Additional {
		details := &cc.Additional[i]
		delim := details.Info.Delim
		if delim == 0 {
			roots = append(roots, MailboxNode{Name: details.Info.Name(), Details: details})
			continue
		}
		segments := strings.Split(details.Info.Name(), string(delim))
		nodes := &roots
		var node *MailboxNode
		for _, seg := range segments {
			node = find(nodes, seg)
			nodes = &node.Children
		}
		node.Details = details
	}
	return roots
}

// sidebar holds the mailbox listing and statuses shown in every IMAP page's
// aside, before assembly into render data.
type sidebar struct {
	mailboxes     []MailboxInfo
	statuses      map[string]*MailboxStatus
	active, inbox *MailboxStatus
}

// clone copies the mutable layers, so an assembled or cached sidebar never
// shares them with another request; the per-message wire data underneath is
// read-only and stays shared.
// adjustUnseen moves the folder's unseen count by delta. The status is
// shared with copies of the sidebar, so the folder gets its own.
func (sb *sidebar) adjustUnseen(folder string, delta int) {
	st := sb.statuses[folder]
	if st == nil || st.StatusData == nil || st.NumUnseen == nil || int(*st.NumUnseen)+delta < 0 {
		return
	}
	data := *st.StatusData
	n := uint32(int(*data.NumUnseen) + delta)
	data.NumUnseen = &n
	st.StatusData = &data
}

func (sb sidebar) clone() sidebar {
	out := sidebar{
		mailboxes: append([]MailboxInfo(nil), sb.mailboxes...),
		statuses:  make(map[string]*MailboxStatus, len(sb.statuses)),
	}
	for k, v := range sb.statuses {
		if v == nil {
			out.statuses[k] = nil
			continue
		}
		c := *v
		out.statuses[k] = &c
		if v == sb.active {
			out.active = out.statuses[k]
		}
		if v == sb.inbox {
			out.inbox = out.statuses[k]
		}
	}
	return out
}

// AccountSidebar is one account's folder tree for the mail aside.
type AccountSidebar struct {
	Account     string
	Categorized CategorizedMailboxes
}

// The rail serves its last counts while one refresh runs behind the page.
var accountSidebars = alborz.NewBackgroundMemo[sidebar](listingFreshFor)

// railFor is the account's sidebar for a page on one folder: the tree
// and counts as the memo holds them, with that folder marked active.
// A folder the memo did not count keeps the status the page fetched.
func railFor(s *alborz.Session, mboxName string, fetched sidebar) sidebar {
	sb, err := sidebarFor(s)
	if err != nil {
		return fetched
	}
	sb = sb.clone()
	if active := sb.statuses[mboxName]; active != nil {
		sb.active = active
	} else {
		sb.active = fetched.active
	}
	return sb
}

func sidebarFor(s *alborz.Session) (sidebar, error) {
	return accountSidebars.Get(s.Username(), func() (sidebar, error) {
		// Loaded before DoIMAP: on a METADATA server the settings read
		// is itself a DoIMAP, and the session lock is not reentrant.
		settings, err := LoadSettings(s.Store())
		if err != nil {
			return sidebar{}, err
		}
		var sb sidebar
		err = s.DoIMAPBackground(func(c *imapclient.Client) error {
			load, err := startSidebar(c, "", "", settings.Subscriptions)
			if err != nil {
				return err
			}
			sb, err = load.finish()
			return err
		})
		return sb, err
	})
}

// sidebarAccounts assembles every signed-in account's folder tree for the
// aside; empty with a single account, whose aside needs no sections. A
// failing account is skipped and its section reappears once its server
// answers again.
func sidebarAccounts(ctx *alborz.Context) []AccountSidebar {
	sessions := ctx.Sessions()
	if len(sessions) < 2 {
		return nil
	}
	var accounts []AccountSidebar
	for _, s := range sessions {
		sb, err := sidebarFor(s)
		if err != nil {
			ctx.Logger().Printf("sidebar for %q: %v", s.Username(), err)
			continue
		}
		ib := assembleIMAPBase(ctx, &alborz.BaseRenderData{}, "", sb.clone(), "")
		accounts = append(accounts, AccountSidebar{
			Account:     s.Username(),
			Categorized: ib.CategorizedMailboxes,
		})
	}
	return accounts
}

// countedMailboxes names the folders the sidebar shows counts for: the
// page's own, the inbox, the special-use folders, and whatever the user
// subscribed to. Counting the rest would make the server walk every folder
// on every page for numbers nothing displays.
func countedMailboxes(mailboxes []MailboxInfo, mboxName string, subs []string) []string {
	var names []string
	seen := make(map[string]bool)
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		names = append(names, name)
	}

	add("INBOX")
	add(mboxName)
	for i := range mailboxes {
		if mailboxes[i].role() != "" && !mailboxes[i].IsInternal() {
			add(mailboxes[i].Name())
		}
	}
	for _, sub := range subs {
		add(sub)
	}
	return names
}

// sidebarLoad is a sidebar whose STATUS commands are still in flight;
// finish drains them.
type sidebarLoad struct {
	sb       sidebar
	names    []string
	cmds     []*imapclient.StatusCommand
	mboxName string
}

// startSidebar issues the mailbox LIST and, pipelined behind it on the same
// connection, a SELECT of selectMbox (empty to skip); both are drained here,
// so on return the connection has selectMbox selected. The STATUS commands
// for the counted folders are then issued but not drained: the caller runs
// the page's own commands next and calls finish after, so the statuses share
// a round trip with the page work instead of costing their own. mboxName
// names the page's mailbox for status lookup; it need not be selected.
func startSidebar(c *imapclient.Client, mboxName, selectMbox string, subs []string) (*sidebarLoad, error) {
	l := &sidebarLoad{mboxName: mboxName}
	l.sb.statuses = make(map[string]*MailboxStatus)

	list := startListMailboxes(c)
	var sel *imapclient.SelectCommand
	if selectMbox != "" {
		sel = c.Select(selectMbox, nil)
	}

	var err error
	l.sb.mailboxes, err = finishListMailboxes(list)
	if err != nil {
		if sel != nil {
			sel.Wait()
		}
		return nil, err
	}
	if sel != nil {
		if _, err := sel.Wait(); err != nil {
			return nil, alborz.NotFound("notfound.folder", selectMbox)
		}
	}

	listed := make(map[string]*imap.StatusData, len(l.sb.mailboxes))
	for _, m := range l.sb.mailboxes {
		if m.Status != nil {
			listed[m.Mailbox] = m.Status
		}
	}
	l.names = countedMailboxes(l.sb.mailboxes, mboxName, subs)
	l.cmds = make([]*imapclient.StatusCommand, len(l.names))
	for i, name := range l.names {
		if st, ok := listed[name]; ok {
			l.sb.statuses[name] = &MailboxStatus{StatusData: st}
			continue
		}
		l.cmds[i] = c.Status(name, listingStatusOptions(c))
	}
	return l, nil
}

// finish drains the STATUS commands and completes the sidebar.
func (l *sidebarLoad) finish() (sidebar, error) {
	for i, cmd := range l.cmds {
		if cmd == nil {
			continue
		}
		data, err := cmd.Wait()
		if err != nil {
			// A subscription naming a folder that no longer exists must
			// not take the page down; only the page's own mailbox does.
			if l.names[i] == l.mboxName {
				return l.sb, alborz.NotFound("notfound.folder", l.mboxName)
			}
			continue
		}
		l.sb.statuses[l.names[i]] = &MailboxStatus{StatusData: data}
	}
	l.sb.inbox = l.sb.statuses["INBOX"]
	l.sb.active = l.sb.statuses[l.mboxName]
	return l.sb, nil
}

// assembleIMAPBase builds the render data from a loaded sidebar, applying
// labels, unseen counts, and the active highlight.
func assembleIMAPBase(ctx *alborz.Context, base *alborz.BaseRenderData, mboxName string, sb sidebar, view string) *IMAPBaseRenderData {
	if mboxName != "" {
		sb.statuses[mboxName] = sb.active
	}
	sb.statuses["INBOX"] = sb.inbox

	var categorized CategorizedMailboxes
	for i := range sb.mailboxes {
		if sb.active != nil && sb.mailboxes[i].Name() == sb.active.Mailbox && view == "" {
			sb.mailboxes[i].Active = true
		}
		sb.mailboxes[i].Label = sb.mailboxes[i].Name()
		if role := sb.mailboxes[i].role(); role != "" {
			sb.mailboxes[i].Label = ctx.T("aside." + role)
		}
		if sb.active != nil && sb.mailboxes[i].Name() == sb.active.Mailbox {
			sb.active.Label = sb.mailboxes[i].Label
		}
		status := sb.statuses[sb.mailboxes[i].Name()]
		if status != nil {
			sb.mailboxes[i].Unseen = int(*status.NumUnseen)
			sb.mailboxes[i].Total = int(*status.NumMessages)
		}
		categorized.Append(sb.mailboxes[i], status)
	}

	return &IMAPBaseRenderData{
		BaseRenderData:       *base,
		CategorizedMailboxes: categorized,
		Mailboxes:            sb.mailboxes,
		Inbox:                sb.inbox,
		Mailbox:              sb.active,
		Subscriptions:        sb.statuses,
		ListView:             view,
	}
}

func newIMAPBaseRenderData(ctx *alborz.Context,
	base *alborz.BaseRenderData) (*IMAPBaseRenderData, error) {

	mboxName, err := mailboxRef(ctx)
	if err != nil {
		return nil, err
	}

	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return nil, fmt.Errorf("failed to load settings: %w", err)
	}

	var sb sidebar
	err = ctx.DoIMAP(func(c *imapclient.Client) error {
		load, err := startSidebar(c, mboxName, "", settings.Subscriptions)
		if err != nil {
			return err
		}
		sb, err = load.finish()
		return err
	})
	if err != nil {
		return nil, err
	}

	// A view narrows the active mailbox; the rail highlights the view's
	// row instead of the mailbox itself.
	view, err := readView(ctx)
	if err != nil {
		return nil, err
	}
	ibase := assembleIMAPBase(ctx, base, mboxName, sb, view)
	ibase.SidebarAccounts = sidebarAccounts(ctx)
	return ibase, nil
}
