package mail

import (
	"context"
	"strings"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// CategorizedMailboxes sorts mailboxes into common and additional ones.
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
	// Hidden counts folders past railFolderLimit left out for not being subscribed.
	Hidden int
}

// railFolderLimit caps unsubscribed folders on the rail; more run off a laptop's screen.
const railFolderLimit = 25

func (cc *CategorizedMailboxes) trim() {
	if len(cc.Additional) <= railFolderLimit {
		return
	}
	kept := cc.Additional[:0]
	for _, d := range cc.Additional {
		if d.Status != nil {
			kept = append(kept, d)
		}
	}
	cc.Hidden = len(cc.Additional) - len(kept)
	cc.Additional = kept
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

// MailboxNode is one folder in the nested sidebar tree; Details is nil for a path-only parent.
type MailboxNode struct {
	Name     string
	Details  *MailboxDetails
	Children []MailboxNode
}

// AdditionalTree nests the custom folders; those under INBOX belong to InboxTree.
func (cc CategorizedMailboxes) AdditionalTree() []MailboxNode {
	var out []MailboxNode
	for _, n := range cc.additionalForest() {
		if !strings.EqualFold(n.Name, "INBOX") || n.Details != nil {
			out = append(out, n)
		}
	}
	return out
}

// InboxTree returns the inbox with its nested folders as children.
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

// CommonNodes lists the special-use folders after the inbox as leaf nodes.
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

type sidebar struct {
	mailboxes     []MailboxInfo
	statuses      map[string]*MailboxStatus
	active, inbox *MailboxStatus
}

// The status is shared with copies of the sidebar, so the folder gets its own.
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

func (p *plugin) railFor(s *alborz.Login, mboxName string, fetched sidebar) sidebar {
	sb, err := p.sidebarFor(s)
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

func (p *plugin) sidebarFor(s *alborz.Login) (sidebar, error) {
	return p.accountSidebars.Get(s.Username(), func() (sidebar, error) {
		// Loaded before DoIMAP: on a METADATA server the settings read is itself a DoIMAP, and the lock is not reentrant.
		settings, err := LoadSettings(context.Background(), s.Store())
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

// Counting every folder would make the server walk them all on every page.
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

type sidebarLoad struct {
	sb       sidebar
	names    []string
	cmds     []*imapclient.StatusCommand
	mboxName string
}

// STATUS commands stay in flight so they share a round trip with the page's own; finish drains them.
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

func (l *sidebarLoad) finish() (sidebar, error) {
	for i, cmd := range l.cmds {
		if cmd == nil {
			continue
		}
		data, err := cmd.Wait()
		if err != nil {
			// A subscription to a vanished folder must not take the page down.
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

type MailboxDetails struct {
	Info   *MailboxInfo
	Status *MailboxStatus
}
