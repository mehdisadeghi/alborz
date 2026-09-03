package alborzbase

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"mime"
	"net"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"
	"github.com/emersion/go-smtp"
	"github.com/labstack/echo/v4"
	"jaytaylor.com/html2text"
)

func registerRoutes(p *alborz.GoPlugin) {
	p.GET("/", func(ctx *alborz.Context) error {
		return ctx.Redirect(http.StatusFound, "/mailbox/INBOX")
	})

	p.GET("/mailbox/:mbox", handleGetMailbox)
	p.POST("/mailbox/:mbox/empty", handleEmptyMailbox)
	p.POST("/mailbox/:role/empty-all", handleEmptyAllMailbox)
	p.POST("/mailbox/:mbox", handleGetMailbox)

	p.GET("/new-mailbox", handleNewMailbox)
	p.POST("/new-mailbox", handleNewMailbox)

	p.GET("/delete-mailbox/:mbox", handleDeleteMailbox)
	p.POST("/delete-mailbox/:mbox", handleDeleteMailbox)

	p.GET("/message/:mbox/:uid", func(ctx *alborz.Context) error {
		return handleGetPart(ctx, false)
	})
	p.GET("/message/:mbox/:uid/raw", func(ctx *alborz.Context) error {
		return handleGetPart(ctx, true)
	})
	p.GET("/message/:mbox/:uid/eml", handleDownloadMessage)
	p.POST("/message/:mbox/:uid/invite", handleInvitationReply)
	p.POST("/mailbox/:mbox/refresh", handleRefreshMailbox)
	p.POST("/message/:mbox/export", handleExportMbox)

	p.GET("/login", handleLogin)
	p.POST("/login", handleLogin)
	p.POST("/switch", handleSwitch)

	p.GET("/logout", handleLogout)
	p.POST("/logout", handleLogout)

	p.GET("/compose", handleComposeNew)
	p.POST("/compose", handleComposeNew)

	p.POST("/compose/attachment", handleComposeAttachment)
	p.POST("/compose/attachment/:uuid/remove", handleCancelAttachment)

	p.GET("/message/:mbox/:uid/reply", handleReply)
	p.POST("/message/:mbox/:uid/reply", handleReply)

	p.GET("/message/:mbox/:uid/forward", handleForward)
	p.POST("/message/:mbox/:uid/forward", handleForward)
	p.POST("/message/:mbox/forward", handleForwardSelection)
	p.POST("/message/:mbox/:uid/unsubscribe", handleUnsubscribe)
	p.GET("/message/:mbox/forward", handleForwardAttached)

	p.GET("/message/:mbox/:uid/edit", handleEdit)
	p.POST("/message/:mbox/:uid/edit", handleEdit)

	p.POST("/message/:mbox/move", handleMove)

	p.POST("/message/:mbox/delete", handleDelete)

	p.POST("/message/:mbox/flag", handleSetFlags)

	p.GET("/settings", handleSettings)
	p.POST("/settings", handleSettings)
	p.GET("/signatures", handleSignatures)
	p.POST("/signatures", handleSignatures)
	p.POST("/signatures/delete", handleSignatureDelete)
	p.POST("/signatures/default", handleSignatureDefault)
	p.GET("/settings/browser", handleBrowserSettings)
	p.POST("/settings/browser", handleBrowserSettings)
	p.POST("/language", handleLanguage)
}

type IMAPBaseRenderData struct {
	alborz.BaseRenderData
	CategorizedMailboxes CategorizedMailboxes
	Mailboxes            []MailboxInfo
	Mailbox              *MailboxStatus
	Inbox                *MailboxStatus
	Subscriptions        map[string]*MailboxStatus
	Starred              bool

	// Every account's folder tree, for the multi-account aside; empty
	// with a single account.
	SidebarAccounts []AccountSidebar
}

// AllInboxUnseen sums the accounts' inbox unseen counts for the
// All-inboxes row.
func (d *IMAPBaseRenderData) AllInboxUnseen() int {
	sum := 0
	for _, acc := range d.SidebarAccounts {
		sum += acc.InboxUnseen
	}
	return sum
}

type MailboxRenderData struct {
	IMAPBaseRenderData
	Messages                  []IMAPMessage
	PrevPage, NextPage        int
	RangeFrom, RangeTo, Total int
	Query                     string
	Sort                      string
	SortDir                   string
	SortSupported             bool
	// ThreadSupported says the server can group a folder into
	// conversations, which is what offers the view at all.
	ThreadSupported bool
	// Crumb is the path to this folder, account first.
	Crumb []CrumbLink
	// PreferHTML is the account's choice of which part a row opens.
	PreferHTML bool
	// Threaded says this listing is one, so the rows carry a depth and
	// the pager counts conversations rather than messages.
	Threaded bool
	// PerPage is the count in force, and PerPageOptions the ladder the
	// toolbar offers. The reader's own preference is always among them,
	// so choosing it is how they get back to it.
	PerPage        int
	PerPageOptions []int
}

type MailboxDetails struct {
	Info   *MailboxInfo
	Status *MailboxStatus
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
	InboxUnseen int
}

// accountSidebars holds each account's raw sidebar so the aside renders
// without waiting on every account's server; reloads run in the
// background once an entry goes stale.
var accountSidebars = alborz.NewBackgroundMemo[sidebar](listingFreshFor)

func sidebarFor(s *alborz.Session) (sidebar, error) {
	return accountSidebars.Get(s.Username(), func() (sidebar, error) {
		// Loaded before DoIMAP: on a METADATA server the settings read
		// is itself a DoIMAP, and the session lock is not reentrant.
		settings, err := LoadSettings(s.Store())
		if err != nil {
			return sidebar{}, err
		}
		var sb sidebar
		err = s.DoIMAP(func(c *imapclient.Client) error {
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
		ib := assembleIMAPBase(ctx, &alborz.BaseRenderData{}, "", sb.clone(), false)
		unseen := 0
		if ib.Inbox != nil && ib.Inbox.NumUnseen != nil {
			unseen = int(*ib.Inbox.NumUnseen)
		}
		accounts = append(accounts, AccountSidebar{
			Account:     s.Username(),
			Categorized: ib.CategorizedMailboxes,
			InboxUnseen: unseen,
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
			return nil, alborz.NotFoundf("folder %q does not exist", selectMbox)
		}
	}

	l.names = countedMailboxes(l.sb.mailboxes, mboxName, subs)
	l.cmds = make([]*imapclient.StatusCommand, len(l.names))
	for i, name := range l.names {
		l.cmds[i] = c.Status(name, listingStatusOptions(c))
	}
	return l, nil
}

// finish drains the STATUS commands and completes the sidebar.
func (l *sidebarLoad) finish() (sidebar, error) {
	for i, cmd := range l.cmds {
		data, err := cmd.Wait()
		if err != nil {
			// A subscription naming a folder that no longer exists must
			// not take the page down; only the page's own mailbox does.
			if l.names[i] == l.mboxName {
				return l.sb, alborz.NotFoundf("folder %q does not exist", l.mboxName)
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
func assembleIMAPBase(ctx *alborz.Context, base *alborz.BaseRenderData, mboxName string, sb sidebar, starred bool) *IMAPBaseRenderData {
	if mboxName != "" {
		sb.statuses[mboxName] = sb.active
	}
	sb.statuses["INBOX"] = sb.inbox

	var categorized CategorizedMailboxes
	for i := range sb.mailboxes {
		if sb.active != nil && sb.mailboxes[i].Name() == sb.active.Mailbox && !starred {
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
		Starred:              starred,
	}
}

func newIMAPBaseRenderData(ctx *alborz.Context,
	base *alborz.BaseRenderData) (*IMAPBaseRenderData, error) {

	mboxName, err := url.PathUnescape(ctx.Param("mbox"))
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusBadRequest, err)
	}

	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return nil, fmt.Errorf("failed to load settings: %v", err)
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

	// The starred view filters the active mailbox; highlight the Starred
	// sidebar entry instead of the mailbox itself.
	starred := ctx.QueryParam("starred") == "1"
	ibase := assembleIMAPBase(ctx, base, mboxName, sb, starred)
	ibase.SidebarAccounts = sidebarAccounts(ctx)
	return ibase, nil
}

// unifiedRoles are the folder roles the merged all-accounts view offers;
// each resolves per account through its special-use attributes.
var unifiedRoles = []string{"INBOX", "Drafts", "Sent", "Junk", "Trash", "Archive"}

func handleUnifiedMailbox(ctx *alborz.Context) error {
	role, err := url.PathUnescape(ctx.Param("mbox"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}
	if !slices.Contains(unifiedRoles, role) {
		return echo.NewHTTPError(http.StatusNotFound,
			fmt.Sprintf("%q is not a unified folder", role))
	}

	page := 0
	if pageStr := ctx.QueryParam("page"); pageStr != "" {
		if page, err = strconv.Atoi(pageStr); err != nil || page < 0 {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid page index")
		}
	}
	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return err
	}
	messagesPerPage := perPage(ctx, settings)
	query := ctx.QueryParam("query")
	starred := ctx.QueryParam("starred") == "1"

	sortKey := ctx.QueryParam("sort")
	// "account" exists only here: it is a property of the merge, not of
	// any single server's order.
	if _, ok := sortKeys[sortKey]; !ok && sortKey != "account" {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid sort order")
	}
	sortDir := ctx.QueryParam("dir")
	if sortDir != "" && sortDir != "asc" && sortDir != "desc" {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid sort direction")
	}
	reverse := sortKeys[sortKey].descends
	if sortDir != "" {
		reverse = sortDir == "desc"
	}

	// Each account contributes its own newest window; after the merge the
	// requested page is cut from the combined order. The first page comes
	// from the listing cache when it can, the slowest account no longer
	// gating every click.
	window := (page + 1) * messagesPerPage
	// A search costs one live IMAP round trip per account, so it is the
	// view that most wants the cache; only its key is longer.
	cacheable := ctx.Request().Method == http.MethodGet && page == 0
	view := listingView("#"+role, query, starred, sortKey, sortDir)
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		msgs     []IMAPMessage
		total    int
		sortable = true
	)
	// One span across the whole fan-out: the accounts are queried
	// concurrently, so its wall-clock is the page's IMAP time.
	imapStart := time.Now()
	errs := make([]error, len(ctx.Sessions()))
	for i, s := range ctx.Sessions() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			user := s.Username()
			merge := func(accountMsgs []IMAPMessage, accountTotal int) {
				mu.Lock()
				msgs = append(msgs, accountMsgs...)
				total += accountTotal
				mu.Unlock()
			}
			var cached *listingEntry
			if cacheable {
				e, state := listings.lookup(user, view, messagesPerPage)
				if state == listingFresh {
					merge(e.msgs, e.total)
					return
				}
				if state == listingStale {
					cached = e
				}
			}
			errs[i] = s.DoIMAP(func(c *imapclient.Client) error {
				folder, err := resolveRole(c, user, role)
				if err != nil || folder == "" {
					return err
				}

				// A stale entry earns reuse when a STATUS shows nothing
				// changed; on change the answer doubles as the fresh
				// entry's snapshot.
				var snap *imap.StatusData
				var snapCmd *imapclient.StatusCommand
				if cached != nil {
					st, err := c.Status(folder, listingStatusOptions(c)).Wait()
					if err == nil {
						if statusUnchanged(cached.snap, st) {
							listings.refresh(user, view)
							merge(cached.msgs, cached.total)
							return nil
						}
						snap = st
					}
				} else if cacheable {
					snapCmd = c.Status(folder, listingStatusOptions(c))
				}

				if !c.Caps().Has(imap.CapSort) {
					mu.Lock()
					sortable = false
					mu.Unlock()
				}
				var accountMsgs []IMAPMessage
				var accountTotal int
				switch {
				case query != "":
					accountMsgs, accountTotal, err = searchMessages(c, folder, PrepareSearch(query, settings.SearchHeadersOnly), 0, window, "", true)
				case starred:
					criteria := &imap.SearchCriteria{Flag: []imap.Flag{imap.FlagFlagged}}
					accountMsgs, accountTotal, err = searchMessages(c, folder, criteria, 0, window, "", true)
				case sortKey != "account" && (sortKey != "" || sortDir != "") && c.Caps().Has(imap.CapSort):
					// Each account's window is cut under the requested
					// order, so the merge sees the right candidates.
					accountMsgs, accountTotal, err = searchMessages(c, folder, &imap.SearchCriteria{}, 0, window, sortKey, reverse)
				default:
					accountMsgs, accountTotal, err = listMessages(c, folder, 0, window)
				}
				if err != nil {
					return err
				}
				for j := range accountMsgs {
					accountMsgs[j].Account = user
				}
				if snapCmd != nil {
					if st, err := snapCmd.Wait(); err == nil {
						snap = st
					}
				}
				if cacheable && snap != nil {
					listings.store(user, view, &listingEntry{
						msgs:    accountMsgs,
						total:   accountTotal,
						perPage: messagesPerPage,
						snap:    snap,
					})
				}
				merge(accountMsgs, accountTotal)
				return nil
			})
		}()
	}
	wg.Wait()
	alborz.AddTiming(ctx.Request().Context(), "imap", imapStart)
	for _, err := range errs {
		if err != nil {
			return err
		}
	}

	slices.SortStableFunc(msgs, unifiedLess(sortKey, reverse))
	from := page * messagesPerPage
	to := from + messagesPerPage
	if from > len(msgs) {
		from = len(msgs)
	}
	if to > len(msgs) {
		to = len(msgs)
	}
	msgs = msgs[from:to]

	prevPage, nextPage := -1, -1
	if page > 0 {
		prevPage = page - 1
	}
	if (page+1)*messagesPerPage < total {
		nextPage = page + 1
	}
	title := ctx.T("aside." + strings.ToLower(role))
	if starred {
		title = ctx.T("mailbox.starred")
	}

	return ctx.Render(http.StatusOK, "mailbox.html", &MailboxRenderData{
		IMAPBaseRenderData: IMAPBaseRenderData{
			BaseRenderData:  *alborz.NewBaseRenderData(ctx).WithTitle(fmt.Sprintf(ctx.T("mailbox.allaccounts"), title)),
			Mailbox:         &MailboxStatus{StatusData: &imap.StatusData{Mailbox: title}, Label: title},
			Starred:         starred,
			SidebarAccounts: sidebarAccounts(ctx),
		},
		Messages:       msgs,
		PrevPage:       prevPage,
		NextPage:       nextPage,
		RangeFrom:      from + 1,
		RangeTo:        to,
		Total:          total,
		Query:          query,
		PerPage:        messagesPerPage,
		PerPageOptions: perPageOptions(settings),
		Sort:           sortKey,
		SortDir:        map[bool]string{true: "desc", false: "asc"}[reverse],
		SortSupported:  sortable,
	})
}

// unifiedLess merges the accounts' windows under the same order each
// window was cut with; date breaks ties so equal keys stay stable.
func unifiedLess(sortKey string, reverse bool) func(a, b IMAPMessage) int {
	byDate := func(a, b IMAPMessage) int {
		return b.Date().Compare(a.Date())
	}
	var cmp func(a, b IMAPMessage) int
	switch sortKey {
	case "from":
		cmp = func(a, b IMAPMessage) int {
			return strings.Compare(strings.ToLower(senderLabel(a)), strings.ToLower(senderLabel(b)))
		}
	case "subject":
		cmp = func(a, b IMAPMessage) int {
			return strings.Compare(strings.ToLower(a.Envelope.Subject), strings.ToLower(b.Envelope.Subject))
		}
	case "size":
		cmp = func(a, b IMAPMessage) int {
			switch {
			case a.RFC822Size < b.RFC822Size:
				return -1
			case a.RFC822Size > b.RFC822Size:
				return 1
			}
			return 0
		}
	case "account":
		cmp = func(a, b IMAPMessage) int {
			return strings.Compare(strings.ToLower(a.Account), strings.ToLower(b.Account))
		}
	case "starred":
		cmp = func(a, b IMAPMessage) int {
			af, bf := a.HasFlag(imap.FlagFlagged), b.HasFlag(imap.FlagFlagged)
			if af == bf {
				return 0
			}
			if af {
				return -1
			}
			return 1
		}
	default:
		if reverse {
			return func(a, b IMAPMessage) int { return byDate(a, b) }
		}
		return func(a, b IMAPMessage) int { return -byDate(a, b) }
	}
	return func(a, b IMAPMessage) int {
		c := cmp(a, b)
		if reverse {
			c = -c
		}
		if c != 0 {
			return c
		}
		return byDate(a, b)
	}
}

func senderLabel(m IMAPMessage) string {
	for _, a := range m.Envelope.From {
		if a.Name != "" {
			return a.Name
		}
		return a.Addr()
	}
	return ""
}

func handleGetMailbox(ctx *alborz.Context) error {
	// Reading mail is what precedes writing it, so this is where the
	// recipient suggestions are put on to warm. Nothing here waits for
	// them and nothing on this page shows them.
	gatherCorrespondents(ctx.Session)

	if ctx.Unified {
		return handleUnifiedMailbox(ctx)
	}

	mboxName, err := url.PathUnescape(ctx.Param("mbox"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	page := 0
	if pageStr := ctx.QueryParam("page"); pageStr != "" {
		if page, err = strconv.Atoi(pageStr); err != nil || page < 0 {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid page index")
		}
	}

	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return err
	}
	messagesPerPage := perPage(ctx, settings)

	query := ctx.QueryParam("query")
	starred := ctx.QueryParam("starred") == "1"

	sortKey := ctx.QueryParam("sort")
	// thread is not a column to order by, so it is not in sortKeys, but
	// it answers the same question and travels in the same parameter.
	if _, ok := sortKeys[sortKey]; !ok && sortKey != threadSort {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid sort order")
	}
	sortDir := ctx.QueryParam("dir")
	if sortDir != "" && sortDir != "asc" && sortDir != "desc" {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid sort direction")
	}
	reverse := sortKeys[sortKey].descends
	if sortDir != "" {
		reverse = sortDir == "desc"
	}

	// The default view of a folder is served from the listing cache when
	// possible: a fresh entry renders without touching the server, a stale
	// one after a single STATUS confirming nothing changed. Everything
	// else pays one round trip for LIST plus SELECT and one for the query,
	// with the sidebar's STATUS responses riding along.
	cacheable := ctx.Request().Method == http.MethodGet && page == 0
	// thread names one conversation by any message in it. The server
	// groups by message id, so this asks nothing of its header search.
	var threadUID imap.UID
	if raw := ctx.QueryParam("thread"); raw != "" {
		n, perr := strconv.ParseUint(raw, 10, 32)
		if perr != nil || n == 0 {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid thread")
		}
		threadUID = imap.UID(n)
	}

	view := listingView(mboxName, query, starred, sortKey, sortDir)
	if threadUID != 0 {
		view = fmt.Sprintf("%s%sthread=%d", view, listingSep, threadUID)
	}
	user := ctx.Session.Username()

	var (
		sb              sidebar
		msgs            []IMAPMessage
		total           int
		sortSupported   bool
		threadAlgorithm imap.ThreadAlgorithm
		served          bool
	)
	if cacheable {
		if e, state := listings.lookup(user, view, messagesPerPage); state == listingFresh {
			sb, msgs, total, sortSupported, threadAlgorithm, served = e.sb, e.msgs, e.total, e.sortSupported, e.threadAlgorithm, true
		} else if state == listingStale {
			var st *imap.StatusData
			err := ctx.DoIMAP(func(c *imapclient.Client) error {
				var err error
				st, err = c.Status(mboxName, listingStatusOptions(c)).Wait()
				return err
			})
			if err == nil && statusUnchanged(e.snap, st) {
				listings.refresh(user, view)
				sb, msgs, total, sortSupported, threadAlgorithm, served = e.sb, e.msgs, e.total, e.sortSupported, e.threadAlgorithm, true
			}
		}
	}

	if !served {
		err = ctx.DoIMAP(func(c *imapclient.Client) error {
			load, err := startSidebar(c, mboxName, mboxName, settings.Subscriptions)
			if err != nil {
				return err
			}
			sortSupported = c.Caps().Has(imap.CapSort)
			threadAlgorithm = ThreadAlgorithm(c)
			switch {
			case threadUID != 0 && threadAlgorithm != "":
				msgs, err = oneThread(c, mboxName, threadAlgorithm, threadUID)
				total = len(msgs)
			case sortKey == threadSort && threadAlgorithm != "":
				// A conversation is the unit here, so the page holds a
				// number of threads rather than a number of messages.
				criteria := &imap.SearchCriteria{}
				if query != "" {
					criteria = PrepareSearch(query, settings.SearchHeadersOnly)
				} else if starred {
					criteria = &imap.SearchCriteria{Flag: []imap.Flag{imap.FlagFlagged}}
				}
				msgs, total, err = threadMessages(c, mboxName, threadAlgorithm, criteria, page, messagesPerPage)
			case query != "":
				msgs, total, err = searchMessages(c, mboxName, PrepareSearch(query, settings.SearchHeadersOnly), page, messagesPerPage, sortKey, reverse)
			case starred:
				criteria := &imap.SearchCriteria{Flag: []imap.Flag{imap.FlagFlagged}}
				msgs, total, err = searchMessages(c, mboxName, criteria, page, messagesPerPage, sortKey, reverse)
			case (sortKey != "" || sortDir != "") && sortSupported:
				msgs, total, err = searchMessages(c, mboxName, &imap.SearchCriteria{}, page, messagesPerPage, sortKey, reverse)
			default:
				msgs, total, err = listMessages(c, mboxName, page, messagesPerPage)
			}
			if err != nil {
				return err
			}
			sb, err = load.finish()
			return err
		})
		if err != nil {
			return err
		}
		if cacheable {
			listings.store(user, view, &listingEntry{
				sb:              sb,
				msgs:            msgs,
				total:           total,
				perPage:         messagesPerPage,
				sortSupported:   sortSupported,
				threadAlgorithm: threadAlgorithm,
				snap:            sb.active.StatusData,
			})
		}
	}

	// A row shows the address it reached only where that is worth
	// believing; the headers saying so are part of the message.
	trust := newDeliveryTrust(ctx, settings, ctx.Session.Username())
	for i := range msgs {
		msgs[i].Alias = trust.alias(&msgs[i])
	}

	ibase := assembleIMAPBase(ctx, alborz.NewBaseRenderData(ctx), mboxName, sb, starred)
	ibase.SidebarAccounts = sidebarAccounts(ctx)
	title := ctx.T("mailbox.starred")
	if !starred && ibase.Mailbox != nil {
		title = ibase.Mailbox.Label
	}
	ibase.BaseRenderData.WithTitle(title)

	prevPage, nextPage := -1, -1
	if page > 0 {
		prevPage = page - 1
	}
	if (page+1)*messagesPerPage < total {
		nextPage = page + 1
	}

	rangeFrom, rangeTo := 0, 0
	if len(msgs) > 0 {
		rangeFrom = page*messagesPerPage + 1
		rangeTo = page*messagesPerPage + len(msgs)
	}

	return ctx.Render(http.StatusOK, "mailbox.html", &MailboxRenderData{
		IMAPBaseRenderData: *ibase,
		Messages:           msgs,
		PrevPage:           prevPage,
		NextPage:           nextPage,
		RangeFrom:          rangeFrom,
		RangeTo:            rangeTo,
		Total:              total,
		Query:              query,
		PerPage:            messagesPerPage,
		PerPageOptions:     perPageOptions(settings),
		Sort:               sortKey,
		SortDir:            map[bool]string{true: "desc", false: "asc"}[reverse],
		SortSupported:      sortSupported,
		ThreadSupported:    threadAlgorithm != "",
		Crumb:              mailboxCrumb(sb.mailboxes, mboxName, ctx.Session.Username()),
		PreferHTML:         settings.PreferHTML,
		Threaded:           sortKey == threadSort && threadAlgorithm != "",
	})
}

type NewMailboxRenderData struct {
	IMAPBaseRenderData
	Error            string
	Name             string
	SelectedAccount  string
	SelectedLocation string
	LocationGroups   []NewMailboxLocationGroup
}

type NewMailboxLocation struct {
	Key       string
	Parent    string
	Label     string
	Delimiter rune
}

type NewMailboxLocationGroup struct {
	Account   string
	Locations []NewMailboxLocation
}

func newMailboxLocationGroups(ctx *alborz.Context) []NewMailboxLocationGroup {
	var groups []NewMailboxLocationGroup
	key := 0
	for _, session := range ctx.Sessions() {
		sb, err := sidebarFor(session)
		if err != nil {
			ctx.Logger().Printf("folder locations for %q: %v", session.Username(), err)
			continue
		}
		ib := assembleIMAPBase(ctx, &alborz.BaseRenderData{}, "", sb.clone(), false)
		delimiter := rune('/')
		for _, mailbox := range ib.Mailboxes {
			if mailbox.Delim != 0 {
				delimiter = mailbox.Delim
				break
			}
		}
		group := NewMailboxLocationGroup{Account: session.Username()}
		group.Locations = append(group.Locations, NewMailboxLocation{
			Key: fmt.Sprint(key), Label: ctx.T("folder.toplevel"), Delimiter: delimiter,
		})
		key++
		for _, mailbox := range ib.Mailboxes {
			if strings.HasPrefix(mailbox.Name(), ".") || mailbox.Delim == 0 || mailbox.HasAttr(string(imap.MailboxAttrNoInferiors)) {
				continue
			}
			group.Locations = append(group.Locations, NewMailboxLocation{
				Key:       fmt.Sprint(key),
				Parent:    mailbox.Name(),
				Label:     mailbox.Name(),
				Delimiter: mailbox.Delim,
			})
			key++
		}
		groups = append(groups, group)
	}
	return groups
}

func handleNewMailbox(ctx *alborz.Context) error {
	ibase, err := newIMAPBaseRenderData(ctx, alborz.NewBaseRenderData(ctx))
	if err != nil {
		return err
	}
	ibase.BaseRenderData.WithTitle(ctx.T("folder.create"))
	selectedAccount := ctx.Session.Username()
	if ctx.URLAccount() != "" {
		selectedAccount = ctx.URLAccount()
	}
	locationGroups := newMailboxLocationGroups(ctx)
	selectedLocation := ""
	for _, group := range locationGroups {
		if group.Account == selectedAccount && len(group.Locations) > 0 {
			selectedLocation = group.Locations[0].Key
			break
		}
	}

	if ctx.Request().Method == http.MethodPost {
		name := ctx.FormValue("name")
		selectedLocation = ctx.FormValue("location")
		var location *NewMailboxLocation
		for i := range locationGroups {
			for j := range locationGroups[i].Locations {
				if locationGroups[i].Locations[j].Key == selectedLocation {
					selectedAccount = locationGroups[i].Account
					location = &locationGroups[i].Locations[j]
				}
			}
		}
		if location == nil {
			return ctx.Render(http.StatusUnprocessableEntity, "new-mailbox.html", &NewMailboxRenderData{
				IMAPBaseRenderData: *ibase,
				Error:              ctx.T("form.destinationneeded"),
				Name:               name,
				SelectedAccount:    selectedAccount,
				SelectedLocation:   selectedLocation,
				LocationGroups:     locationGroups,
			})
		}
		if name == "" {
			return ctx.Render(http.StatusUnprocessableEntity, "new-mailbox.html", &NewMailboxRenderData{
				IMAPBaseRenderData: *ibase,
				Error:              ctx.T("form.nameneeded"),
				Name:               name,
				SelectedAccount:    selectedAccount,
				SelectedLocation:   selectedLocation,
				LocationGroups:     locationGroups,
			})
		}

		selectedSession := ctx.SessionFor(selectedAccount)
		if selectedSession == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "not signed in to that account")
		}
		fullName := name
		if location.Parent != "" {
			fullName = location.Parent + string(location.Delimiter) + name
		}
		err := selectedSession.DoIMAP(func(c *imapclient.Client) error {
			return c.Create(fullName, nil).Wait()
		})

		if err != nil {
			return ctx.Render(http.StatusUnprocessableEntity, "new-mailbox.html", &NewMailboxRenderData{
				IMAPBaseRenderData: *ibase,
				Error:              err.Error(),
				Name:               name,
				SelectedAccount:    selectedAccount,
				SelectedLocation:   selectedLocation,
				LocationGroups:     locationGroups,
			})
		}

		listings.evictAll(selectedAccount)
		destination := fmt.Sprintf("/mailbox/%s", url.PathEscape(fullName))
		if len(ctx.Sessions()) > 1 {
			destination += "?account=" + alborz.AddressParam(selectedAccount)
		}
		return ctx.Redirect(http.StatusFound, destination)
	}

	return ctx.Render(http.StatusOK, "new-mailbox.html", &NewMailboxRenderData{
		IMAPBaseRenderData: *ibase,
		Error:              "",
		SelectedAccount:    selectedAccount,
		SelectedLocation:   selectedLocation,
		LocationGroups:     locationGroups,
	})
}

type DeleteMailboxRenderData struct {
	IMAPBaseRenderData
	Error string
}

func handleDeleteMailbox(ctx *alborz.Context) error {
	ibase, err := newIMAPBaseRenderData(ctx, alborz.NewBaseRenderData(ctx))
	if err != nil {
		return err
	}

	mbox := ibase.Mailbox
	ibase.BaseRenderData.WithTitle(fmt.Sprintf(ctx.T("folder.deletetitle"), mbox.Name()))

	if ctx.Request().Method == http.MethodPost {
		// A server refuses some deletions - a special-use folder, one
		// with children. Saying it was deleted anyway sends the user
		// looking for a folder that is still there.
		if err := ctx.DoIMAP(func(c *imapclient.Client) error {
			return c.Delete(mbox.Name()).Wait()
		}); err != nil {
			return ctx.Render(http.StatusOK, "delete-mailbox.html", &DeleteMailboxRenderData{
				IMAPBaseRenderData: *ibase,
				Error:              err.Error(),
			})
		}
		listings.evictAll(ctx.Session.Username())
		ctx.Session.PutNotice(ctx.T("notice.mailboxdeleted"))
		return ctx.Redirect(http.StatusFound, ctx.AccountPath("/mailbox/INBOX"))
	}

	return ctx.Render(http.StatusOK, "delete-mailbox.html", &DeleteMailboxRenderData{
		IMAPBaseRenderData: *ibase,
	})
}

func handleLogin(ctx *alborz.Context) error {
	username := ctx.FormValue("username")
	password := ctx.FormValue("password")
	remember := ctx.FormValue("remember-me")
	add := ctx.QueryParam("add") == "1"

	renderData := struct {
		alborz.BaseRenderData
		CanRememberMe bool
		Add           bool
	}{
		BaseRenderData: *alborz.NewBaseRenderData(ctx),
		CanRememberMe:  ctx.Server.Options.LoginKey != nil,
		Add:            add,
	}
	if add {
		renderData.BaseRenderData.WithTitle(ctx.T("login.add"))
	} else {
		renderData.BaseRenderData.WithTitle(ctx.T("login.short"))
	}

	// The remembered credentials would re-login the accounts the user
	// already has, not add a new one.
	if username == "" && password == "" && !add && ctx.RestoreRememberedAccounts() {
		return loginRedirect(ctx)
	}

	if username != "" && password != "" {
		s, err := ctx.Server.Sessions.Put(username, password)
		if err != nil {
			ctx.Logger().Printf("Login failed for %q: %v", username, err)
			if _, ok := err.(alborz.AuthError); ok {
				renderData.BaseRenderData.GlobalData.Notice = ctx.T("notice.loginfailed")
				return ctx.Render(http.StatusUnauthorized, "login.html", &renderData)
			}
			var domainErr alborz.UnknownDomainError
			if errors.As(err, &domainErr) {
				renderData.BaseRenderData.GlobalData.Notice = fmt.Sprintf(ctx.T("notice.loginerror"), domainErr.Error())
				return ctx.Render(http.StatusUnauthorized, "login.html", &renderData)
			}
			var netErr *net.OpError
			if errors.As(err, &netErr) {
				renderData.BaseRenderData.GlobalData.Notice = fmt.Sprintf(ctx.T("notice.loginerror"), netErr.Err)
				return ctx.Render(http.StatusServiceUnavailable, "login.html", &renderData)
			}
			return fmt.Errorf("failed to put connection in pool: %v", err)
		}
		ctx.AddAccount(s)
		// Follow the account from here on, so mail that arrives while
		// nobody is looking is noticed rather than waited for.
		Watch(s, ctx.Logger())

		if remember == "on" {
			ctx.SetLoginToken(username, password)
		}

		return loginRedirect(ctx)
	}

	return ctx.Render(http.StatusOK, "login.html", &renderData)
}

// loginRedirect honors the next parameter after a successful login.
func loginRedirect(ctx *alborz.Context) error {
	// A second leading slash or backslash would make the target
	// scheme-relative, redirecting off-site.
	if path := ctx.QueryParam("next"); path != "" && path[0] == '/' && path != "/login" &&
		!strings.HasPrefix(path, "//") && !strings.HasPrefix(path, "/\\") {
		return ctx.Redirect(http.StatusFound, path)
	}
	return ctx.Redirect(http.StatusFound, "/mailbox/INBOX")
}

func handleLogout(ctx *alborz.Context) error {
	username := ctx.Session.Username()
	if ctx.Request().Method == http.MethodPost && ctx.FormValue("account") != "" {
		username = ctx.FormValue("account")
	}
	target := ctx.SessionFor(username)
	if target == nil {
		return echo.NewHTTPError(http.StatusBadRequest, "not signed in to that account")
	}
	wasCurrent := target == ctx.DefaultSession
	listings.evictAll(username)
	ctx.Server.ForgetAccount(username)
	if next := ctx.LogoutAccount(username); next != nil {
		if wasCurrent {
			next.PutNotice(fmt.Sprintf(ctx.T("notice.signedinas"), next.Username()))
		}
		return ctx.Redirect(http.StatusFound, "/mailbox/INBOX")
	}
	return ctx.Redirect(http.StatusFound, "/login")
}

// switchDestination keeps a scope change on the page it was made from.
// Pooled sections exist under every scope; the account-scoped ones only
// outside the merged view. Deeper paths name an object of the account
// being left, so they return to the inbox, as does a missing or foreign
// next. The query goes with the old scope and is dropped.
func switchDestination(next string, unified bool) string {
	u, err := url.Parse(next)
	if err != nil || !strings.HasPrefix(u.Path, "/") || u.Host != "" {
		return "/mailbox/INBOX"
	}
	switch u.Path {
	case "/calendar", "/contacts", "/tasks":
		return u.Path
	case "/filters", "/settings":
		if !unified {
			return u.Path
		}
	}
	return "/mailbox/INBOX"
}

func handleSwitch(ctx *alborz.Context) error {
	unified := ctx.FormValue("account") == "unified"
	destination := switchDestination(ctx.FormValue("next"), unified)
	if unified {
		ctx.SetUnified(true)
		return ctx.Redirect(http.StatusFound, destination)
	}
	ctx.SetUnified(false)
	if !ctx.SwitchAccount(ctx.FormValue("account")) {
		ctx.Session.PutNotice(ctx.T("notice.expired"))
		return ctx.Redirect(http.StatusFound, "/login?add=1")
	}
	return ctx.Redirect(http.StatusFound, destination)
}

type MessageRenderData struct {
	IMAPBaseRenderData
	Message     *IMAPMessage
	Part        *IMAPPartNode
	View        interface{}
	MailboxPage int
	Flags       map[imap.Flag]bool

	// Invitation is the scheduling request this message carries, nil when
	// it carries none.
	Invitation *Invitation

	// AuthResults is what the receiving server said about the sender's
	// domain, nil when no trusted server reported or none is named.
	AuthResults *AuthResults

	// Unsubscribe is where the unsubscribe control sends the reader. It
	// is the list's own page when the list gave one, and otherwise our
	// compose form with the identity the message was delivered to
	// already chosen: a list keyed on an alias does not recognise a
	// request from the account behind it.
	Unsubscribe string

	// InReplyTo is the message this one answers, when it is in the same
	// folder. It is nil when the server would not search headers.
	InReplyTo *ThreadNeighbour

	// Answers are the messages answering this one, found only for mail
	// being read in Sent: elsewhere the answer is already in the folder.
	Answers []ThreadNeighbour

	// ThreadSupported says the server can group the folder into
	// conversations, which is the only thing that makes the link to
	// them worth offering.
	ThreadSupported bool

	// Crumb is the path to the folder this message is in, account first.
	Crumb []CrumbLink
	// PreferHTML is the account's choice of which part the tabs open.
	PreferHTML bool

	// DeliveredTo names the address the message was delivered to when
	// that is not the account it landed in - the alias, in other words.
	DeliveredTo string

	// ForwardedBy names the mailbox that passed the message on, proved
	// by the SPF check our own server made. Empty unless it is certain.
	ForwardedBy string

	// Neighbors in the view the message was opened from; nil when absent.
	NewerURL *url.URL
	OlderURL *url.URL
	Position int
	Total    int
	Query    string

	// Signature is what the page says about the message's authenticity,
	// as chrome outside the body frame. Its zero value says nothing,
	// which is what an unsigned message deserves.
	Signature Verification
}

// handleRefreshMailbox drops what is cached for the account and returns
// to the page that asked. Alborz learns of new mail only when a page is
// loaded, and even then serves a listing up to listingFreshFor old
// without asking - so a reader who knows something arrived has no way to
// say so. This is that way. It is not a substitute for being told: see
// the note on IDLE and the "there is new mail" notice.
func handleRefreshMailbox(ctx *alborz.Context) error {
	mboxName, err := url.PathUnescape(ctx.Param("mbox"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}
	listings.evict(ctx.Session.Username(), mboxName)
	return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.AccountPath(
		"/mailbox/"+url.PathEscape(mboxName))))
}

// handleInvitationReply answers a meeting request by mail, which is
// what the organizer's client is waiting for (RFC 6047). The invitation
// is re-read from the message rather than taken from the form: what is
// answered has to be what was sent.
func handleInvitationReply(ctx *alborz.Context) error {
	mboxName, uid, err := parseMboxAndUid(ctx.Param("mbox"), ctx.Param("uid"))
	if err != nil {
		return err
	}

	status := strings.ToUpper(ctx.FormValue("status"))
	switch status {
	case partAccepted, partDeclined, partTentative:
	default:
		return echo.NewHTTPError(http.StatusBadRequest, "unknown answer")
	}

	var msg *IMAPMessage
	if err := ctx.Session.DoIMAP(func(c *imapclient.Client) error {
		var err error
		msg, _, err = getMessagePart(c, mboxName, uid, nil)
		return err
	}); err != nil {
		return err
	}
	inv := messageInvitation(ctx, msg, mboxName, uid)
	if inv == nil {
		return echo.NewHTTPError(http.StatusBadRequest, "this message carries no invitation")
	}

	reply, err := invitationReply(ctx, inv, status)
	if err != nil {
		return err
	}
	reply.Mailer = alborz.BrandName
	if v := ctx.Server.Options.Version; v != "" {
		reply.Mailer += "/" + v
	}
	if err := ctx.DoSMTP(func(c *smtp.Client) error {
		return sendMessage(c, reply)
	}); err != nil {
		return fmt.Errorf("failed to send the answer: %v", err)
	}

	ctx.Session.PutNotice(ctx.T("invite.answered"))
	return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.AccountPath(
		fmt.Sprintf("/message/%s/%v", url.PathEscape(mboxName), uid))))
}

// handleDownloadMessage hands over the bytes the server holds. It is
// what makes a message filable, forwardable to somebody else's tooling,
// and checkable against what was actually stored, none of which a
// rendered page can do.
func handleDownloadMessage(ctx *alborz.Context) error {
	mboxName, uid, err := parseMboxAndUid(ctx.Param("mbox"), ctx.Param("uid"))
	if err != nil {
		return err
	}

	var raw []byte
	var env *imap.Envelope
	if err := ctx.Session.DoIMAP(func(c *imapclient.Client) error {
		var err error
		raw, env, err = fetchRawMessage(c, mboxName, uid)
		return err
	}); err != nil {
		return err
	}

	subject := ""
	if env != nil {
		subject = env.Subject
	}
	ctx.Response().Header().Set("Content-Disposition",
		downloadName(subject, fmt.Sprintf("%v", uid), ".eml"))
	return ctx.Blob(http.StatusOK, "message/rfc822", raw)
}

// handleExportMbox writes the selected messages as one mbox, which is
// the container every mail client reads and the only one that holds
// more than a single message without inventing a layout.
//
// It streams: a folder does not fit in memory, and the reader should
// see the file start rather than a spinner.
func handleExportMbox(ctx *alborz.Context) error {
	mboxName, err := url.PathUnescape(ctx.Param("mbox"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}
	params, err := ctx.FormParams()
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}
	uids, err := parseUidList(params["uids"])
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}
	if len(uids) == 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "no messages selected")
	}

	// The header is written only once a message is actually in hand.
	// Committing the response first meant that a fetch which failed had
	// the error page rendered into a body already claiming to be an
	// mbox, and the reader was handed an .mbox file full of HTML.
	res := ctx.Response()
	started := false
	return ctx.Session.DoIMAP(func(c *imapclient.Client) error {
		for _, uid := range uids {
			raw, env, err := fetchRawMessage(c, mboxName, uid)
			if err != nil {
				if started {
					// Too late to say so in the response: stop, leave
					// the file short, and put the reason in the log
					// rather than in the download.
					ctx.Logger().Printf("export %q uid %v: %v", mboxName, uid, err)
					return nil
				}
				return fmt.Errorf("export %q uid %v: %w", mboxName, uid, err)
			}
			if !started {
				res.Header().Set("Content-Disposition", downloadName(mboxName, "messages", ".mbox"))
				res.Header().Set("Content-Type", "application/mbox")
				res.WriteHeader(http.StatusOK)
				started = true
			}
			if err := writeMbox(res, raw, env); err != nil {
				ctx.Logger().Printf("export %q uid %v: %v", mboxName, uid, err)
				return nil
			}
			res.Flush()
		}
		return nil
	})
}

// mboxSeparator opens each message in an mbox file. The address and the
// date are the envelope sender and delivery time in the original
// format's own shape; nothing reads them, but a file without them is
// not an mbox.
func mboxSeparator(env *imap.Envelope) string {
	from := "alborz"
	if env != nil && len(env.From) > 0 {
		if addr := env.From[0].Addr(); addr != "" {
			from = addr
		}
	}
	when := time.Now()
	if env != nil && !env.Date.IsZero() {
		when = env.Date
	}
	return "From " + from + " " + when.UTC().Format(time.ANSIC) + "\r\n"
}

// writeMbox appends one message in mboxrd form. A line that would be
// read as the next message's separator is quoted with a ">", and one
// already quoted gains another, which is what makes the escaping
// reversible.
func writeMbox(w io.Writer, raw []byte, env *imap.Envelope) error {
	if _, err := io.WriteString(w, mboxSeparator(env)); err != nil {
		return err
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), maxMboxLine)
	for scanner.Scan() {
		line := scanner.Bytes()
		trimmed := bytes.TrimLeft(line, ">")
		if bytes.HasPrefix(trimmed, []byte("From ")) {
			if _, err := w.Write([]byte(">")); err != nil {
				return err
			}
		}
		if _, err := w.Write(line); err != nil {
			return err
		}
		if _, err := io.WriteString(w, "\r\n"); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\r\n")
	return err
}

// downloadName offers a filename a reader will recognise, in both the
// plain and the encoded form RFC 6266 asks for: the plain one is
// stripped to ASCII for clients that read only that.
func downloadName(name, fallback, ext string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r < 0x20, r == '"', r == '\\', r == '/', r == 0x7f:
			return -1
		}
		return r
	}, name)
	cleaned = strings.TrimSpace(cleaned)
	if len([]rune(cleaned)) > maxDownloadName {
		cleaned = string([]rune(cleaned)[:maxDownloadName])
	}
	if cleaned == "" {
		cleaned = fallback
	}
	cleaned += ext
	ascii := strings.Map(func(r rune) rune {
		if r > 0x7f {
			return '_'
		}
		return r
	}, cleaned)
	return mime.FormatMediaType("attachment", map[string]string{"filename": ascii}) +
		"; filename*=UTF-8''" + url.PathEscape(cleaned)
}

func handleGetPart(ctx *alborz.Context, raw bool) error {
	mboxName, uid, err := parseMboxAndUid(ctx.Param("mbox"), ctx.Param("uid"))
	if err != nil {
		return err
	}

	partPath, err := parsePartPath(ctx.QueryParam("part"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return err
	}
	messagesPerPage := perPage(ctx, settings)

	query := ctx.QueryParam("query")
	starred := ctx.QueryParam("starred") == "1"
	var criteria *imap.SearchCriteria
	if query != "" {
		criteria = PrepareSearch(query, settings.SearchHeadersOnly)
	} else if starred {
		criteria = &imap.SearchCriteria{Flag: []imap.Flag{imap.FlagFlagged}}
	}

	// The rendered view needs the sidebar; its mailbox LIST is issued with
	// the message's SELECT pipelined behind it, and its STATUS responses
	// ride along with the message fetch. A raw download skips the sidebar
	// entirely and only selects to fetch.
	var (
		sb                  sidebar
		msg                 *IMAPMessage
		part                *message.Entity
		selected            *imapclient.SelectedMailbox
		newerUID, olderUID  imap.UID
		position, totalMsgs int
		signature           Verification
		authResults         *AuthResults
		inReplyTo           *ThreadNeighbour
		answers             []ThreadNeighbour
		threadAlgorithm     imap.ThreadAlgorithm
	)
	err = ctx.DoIMAP(func(c *imapclient.Client) error {
		var load *sidebarLoad
		var err error
		if !raw {
			if load, err = startSidebar(c, mboxName, mboxName, settings.Subscriptions); err != nil {
				return err
			}
		}
		if msg, part, err = getMessagePart(c, mboxName, uid, partPath); err != nil {
			return err
		}
		selected = c.Mailbox()
		if !raw {
			if newerUID, olderUID, position, totalMsgs, err = messageNeighbors(c, msg.SeqNum, criteria); err != nil {
				return err
			}
			threadAlgorithm = ThreadAlgorithm(c)
			sent := ""
			for i := range load.sb.mailboxes {
				if load.sb.mailboxes[i].role() == "sent" {
					sent = load.sb.mailboxes[i].Name()
					break
				}
			}
			// What this message answers. A server that will not search
			// headers simply shows the message alone.
			if parent, terr := threadParent(c, mboxName, sent, msg); terr == nil {
				inReplyTo = parent
			} else {
				ctx.Logger().Printf("thread lookup failed: %v", terr)
			}
			// Reading your own message, the useful direction is forward.
			if sent != "" && mboxName == sent {
				if found, aerr := threadAnswers(c, mboxName, msg); aerr == nil {
					answers = found
				} else {
					ctx.Logger().Printf("answer lookup failed: %v", aerr)
				}
			}
			// Whether the message is from who it says, on the same
			// connection that has the mailbox open. A message nobody
			// signed costs nothing here: signedParts walks a structure
			// already in hand and finds nothing.
			if msg.BodyStructure != nil {
				signature = verifySignature(c, mboxName, uid, msg.BodyStructure,
					messageRootHeader(msg), envelopeSender(msg.Envelope))
				// What our own server made of SPF, DKIM and DMARC when
				// it took delivery. Read from the same header set, and
				// only from the instance the trusted server wrote.
				authResults = readAuthResults(messageRootHeader(msg), settings.TrustedAuthServ)
			}
			sb, err = load.finish()
		}
		return err
	})
	if err != nil {
		return err
	}
	// The body fetch marked the message read, so the cached listing for
	// this folder no longer tells the truth.
	listings.evict(ctx.Session.Username(), mboxName)

	// A link naming no part, like Newer and Older, opens the part the
	// mailbox rows would link to; the bare envelope has no viewer.
	if !raw && ctx.QueryParam("part") == "" {
		preferred := msg.PreferredPart(settings.PreferHTML)
		if preferred != nil && len(preferred.Path) > 0 {
			q := ctx.Request().URL.Query()
			q.Set("part", preferred.PathString())
			return ctx.Redirect(http.StatusFound, msg.URL().String()+"?"+q.Encode())
		}
	}

	if len(partPath) > 0 && msg.PartByPath(partPath) == nil {
		return echo.NewHTTPError(http.StatusNotFound,
			fmt.Sprintf("message %v has no part %v", uid, ctx.QueryParam("part")))
	}

	mimeType, _, err := part.Header.ContentType()
	if err != nil {
		return fmt.Errorf("failed to parse part Content-Type: %v", err)
	}
	if len(partPath) == 0 {
		if ctx.QueryParam("plain") == "1" {
			mimeType = "text/plain"
		} else {
			mimeType = "message/rfc822"
		}
	}

	if raw {
		ctx.Response().Header().Set("Content-Type", mimeType)

		disp, dispParams, _ := part.Header.ContentDisposition()
		filename := dispParams["filename"]
		if len(partPath) == 0 {
			filename = msg.Envelope.Subject + ".eml"
		}

		// TODO: set Content-Length if possible

		// Be careful not to serve types like text/html as inline
		if !strings.EqualFold(mimeType, "text/plain") || strings.EqualFold(disp, "attachment") {
			dispParams := make(map[string]string)
			if filename != "" {
				dispParams["filename"] = filename
			}
			disp := mime.FormatMediaType("attachment", dispParams)
			ctx.Response().Header().Set("Content-Disposition", disp)
		}

		if len(partPath) == 0 {
			return part.WriteTo(ctx.Response())
		}
		return ctx.Stream(http.StatusOK, mimeType, part.Body)
	}

	view, err := viewMessagePart(ctx, msg, part)
	if err == ErrViewUnsupported {
		view = nil
	}

	flags := make(map[imap.Flag]bool)
	for _, f := range selected.PermanentFlags {
		if f == imap.FlagWildcard {
			continue
		}
		flags[f] = msg.HasFlag(f)
	}

	trust := newDeliveryTrust(ctx, settings, ctx.Session.Username())
	ibase := assembleIMAPBase(ctx, alborz.NewBaseRenderData(ctx), mboxName, sb, starred)
	ibase.SidebarAccounts = sidebarAccounts(ctx)
	ibase.BaseRenderData.WithTitle(msg.Envelope.Subject)
	mbox := ibase.Mailbox

	return ctx.Render(http.StatusOK, "message.html", &MessageRenderData{
		IMAPBaseRenderData: *ibase,
		Message:            msg,
		Part:               msg.PartByPath(partPath),
		View:               view,
		MailboxPage:        int(*mbox.NumMessages-msg.SeqNum) / messagesPerPage,
		Flags:              flags,
		NewerURL:           messageURL(mbox.Name(), newerUID),
		OlderURL:           messageURL(mbox.Name(), olderUID),
		Position:           position,
		Total:              totalMsgs,
		Query:              query,
		Signature:          signature,
		AuthResults:        authResults,
		Invitation:         messageInvitation(ctx, msg, mboxName, uid),
		InReplyTo:          inReplyTo,
		Answers:            answers,
		ThreadSupported:    threadAlgorithm != "",
		Crumb:              mailboxCrumb(sb.mailboxes, mboxName, ctx.Session.Username()),
		PreferHTML:         settings.PreferHTML,
		Unsubscribe:        unsubscribeHref(settings, trust, msg),
		DeliveredTo:        trust.alias(msg),
		ForwardedBy:        ForwardedBy(msg.rootHeader, settings.TrustedAuthServ, msg.ListID),
	})
}

// ownIdentity returns want when it names an address this account may
// write as, and "" otherwise. A link may choose between the addresses
// the reader already owns; it may not choose an address for them.
func ownIdentity(settings *Settings, trust deliveryTrust, want string) string {
	if want == "" {
		return ""
	}
	parsed, err := mail.ParseAddress(want)
	if err != nil {
		return ""
	}
	if strings.EqualFold(parsed.Address, trust.account) {
		return want
	}
	// An address at a domain this server serves may be the reader's even
	// when they have not written it down; one anywhere else cannot be.
	if trust.ours(parsed.Address) {
		return want
	}
	for _, identity := range settings.Identities {
		mine, err := mail.ParseAddress(identity)
		if err != nil {
			continue
		}
		if strings.EqualFold(mine.Address, parsed.Address) {
			return want
		}
	}
	return ""
}

// UnsubscribeExternal reports whether the unsubscribe link leaves
// alborz. A list's own page opens in a tab of its own; our compose form
// is this page going somewhere and should not.
func (d *MessageRenderData) UnsubscribeExternal() bool {
	return d.Unsubscribe != "" && !strings.HasPrefix(d.Unsubscribe, "/")
}

// unsubscribeHref is where the unsubscribe control should point. A list
// that gave an https page keeps it. A list that asks for a message gets
// one written from the identity it was addressed or delivered to, since
// a list keyed on an alias will not recognise the account behind it.
func unsubscribeHref(settings *Settings, trust deliveryTrust, msg *IMAPMessage) string {
	href := msg.ListUnsubscribe
	if href == "" || !strings.HasPrefix(href, "/compose?") {
		return href
	}
	from := writeAs(settings, trust, msg, msg.Envelope.To, msg.Envelope.Cc)
	if from == "" {
		return href
	}
	u, err := url.Parse(href)
	if err != nil {
		return href
	}
	q := u.Query()
	q.Set("from", from)
	u.RawQuery = alborz.AddressQuery(q)
	return u.String()
}

// messageInvitation reads the message's scheduling part, if it has one.
// A failure here is not a failure of the page: the part is also an
// attachment, and a message that will not parse as an invitation is
// simply not shown as one.
func messageInvitation(ctx *alborz.Context, msg *IMAPMessage, mboxName string, uid imap.UID) *Invitation {
	inv, _, err := invitationAt(ctx, msg, mboxName, uid)
	if err != nil {
		ctx.Logger().Printf("failed to read the calendar part: %v", err)
		return nil
	}
	return inv
}

// InvitationAt reads the scheduling part of one message, and the bytes
// it was written as. The calendar plugin needs both: the fields to show
// and the object to file, which is the organizer's own and not one
// rebuilt from what a page displayed.
func InvitationAt(ctx *alborz.Context, mboxName string, uid imap.UID) (*Invitation, []byte, error) {
	var msg *IMAPMessage
	if err := ctx.Session.DoIMAP(func(c *imapclient.Client) error {
		var err error
		msg, _, err = getMessagePart(c, mboxName, uid, nil)
		return err
	}); err != nil {
		return nil, nil, err
	}
	return invitationAt(ctx, msg, mboxName, uid)
}

func invitationAt(ctx *alborz.Context, msg *IMAPMessage, mboxName string, uid imap.UID) (*Invitation, []byte, error) {
	part := invitationPart(msg)
	if part == nil {
		return nil, nil, nil
	}
	var raw []byte
	err := ctx.Session.DoIMAP(func(c *imapclient.Client) error {
		_, entity, err := getMessagePart(c, mboxName, uid, part.Path)
		if err != nil {
			return err
		}
		raw, err = io.ReadAll(entity.Body)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	return readInvitation(raw, part.PathString(), ctx.Session.Username()), raw, nil
}

// AttachedMessage names a whole message the form carries, for the
// checkbox that keeps it across a round trip.
type AttachedMessage struct {
	Ref  string
	Name string
}

type ComposeRenderData struct {
	IMAPBaseRenderData
	Message *OutgoingMessage
	// Attached are whole messages carried as message/rfc822 parts,
	// which have no part path in the message being written.
	Attached []AttachedMessage
	Error    string
	// Identities offered in the From dropdown, one entry per account.
	Identities []AccountIdentities
	// Signatures the account holds, and the one under the message now.
	Signatures []Signature
	Signature  string
}

// AccountIdentities lists the addresses an account may send as: the
// account itself is implied and not repeated in Addresses.
type AccountIdentities struct {
	Account   string
	Addresses []string
}

// composeIdentities reads each signed-in account's stored identities. A
// server that cannot be reached contributes its address alone rather
// than failing the page.
func composeIdentities(ctx *alborz.Context) []AccountIdentities {
	var out []AccountIdentities
	for _, s := range ctx.Sessions() {
		entry := AccountIdentities{Account: s.Username()}
		if settings, err := LoadSettings(s.Store()); err == nil {
			entry.Addresses = settings.Identities
		}
		out = append(out, entry)
	}
	return out
}

type messagePath struct {
	Mailbox string
	Uid     imap.UID
}

// attachedMessages is the compose form's record of whole messages
// carried as attachments: "mailbox/uid", one per entry, re-fetched when
// the form comes back rather than held in memory between requests.
const attachedField = "attached_messages"

func parseAttachedRef(s string) (messagePath, error) {
	i := strings.LastIndex(s, "/")
	if i < 0 {
		return messagePath{}, fmt.Errorf("bad attached message %q", s)
	}
	var p messagePath
	var err error
	p.Mailbox, p.Uid, err = parseMboxAndUid(s[:i], s[i+1:])
	return p, err
}

// PartAttachments are the attachments that came from a part of another
// message, which is the only kind the form can carry back by path. A
// whole message has no part path and rides in Attached instead.
func (d *ComposeRenderData) PartAttachments() []*imapAttachment {
	var out []*imapAttachment
	for _, att := range d.Message.Attachments {
		if a, ok := att.(*imapAttachment); ok {
			out = append(out, a)
		}
	}
	return out
}

// attachedList names what the form should carry back, reading the
// attachments already built rather than fetching or remembering
// anything.
func attachedList(msg *OutgoingMessage) []AttachedMessage {
	var out []AttachedMessage
	for _, att := range msg.Attachments {
		if m, ok := att.(*messageAttachment); ok {
			out = append(out, AttachedMessage{Ref: attachedRef(m.Path), Name: m.Name})
		}
	}
	return out
}

func attachedRef(p messagePath) string {
	return fmt.Sprintf("%s/%v", url.PathEscape(p.Mailbox), p.Uid)
}

// attachMessages fetches each named message whole and hangs it off the
// outgoing one.
func attachMessages(ctx *alborz.Context, msg *OutgoingMessage, paths []messagePath) error {
	for _, p := range paths {
		var body []byte
		var env *imap.Envelope
		err := ctx.DoIMAP(func(c *imapclient.Client) error {
			var err error
			body, env, err = fetchRawMessage(c, p.Mailbox, p.Uid)
			return err
		})
		if err != nil {
			return err
		}
		subject := ""
		if env != nil {
			subject = env.Subject
		}
		msg.Attachments = append(msg.Attachments, &messageAttachment{
			Path: p, Name: attachmentName(subject), Body: body,
		})
	}
	return nil
}

type composeOptions struct {
	Draft   *messagePath
	Forward *messagePath
	// Attached are whole messages carried as message/rfc822 parts.
	Attached  []messagePath
	InReplyTo *messagePath
	// Signature names the one already under the body, so the page opens
	// with the right entry chosen rather than with none.
	Signature string
}

// Send message, append it to the Sent mailbox, mark the original message as
// answered. The sender is the account chosen in the From dropdown; the
// draft and the message being answered belong to the request's own
// account, whose mailbox and UID the URL named.
func submitCompose(ctx *alborz.Context, sender *alborz.Session, msg *OutgoingMessage, options *composeOptions) error {
	err := sender.DoSMTP(func(c *smtp.Client) error {
		return sendMessage(c, msg)
	})
	if err != nil {
		if _, ok := err.(alborz.AuthError); ok {
			return echo.NewHTTPError(http.StatusForbidden, err)
		}
		return fmt.Errorf("failed to send message: %v", err)
	}

	if inReplyTo := options.InReplyTo; inReplyTo != nil {
		err = ctx.DoIMAP(func(c *imapclient.Client) error {
			return markMessageAnswered(c, inReplyTo.Mailbox, inReplyTo.Uid)
		})
		if err != nil {
			return fmt.Errorf("failed to mark original message as answered: %v", err)
		}
	}

	// The row shows a forwarded mark that nothing here ever set. Unlike
	// \Answered this is a keyword, which a server is free to refuse, and
	// the message has already been sent: note the refusal, do not fail
	// the send over it.
	if forward := options.Forward; forward != nil {
		err = ctx.DoIMAP(func(c *imapclient.Client) error {
			return markMessageForwarded(c, forward.Mailbox, forward.Uid)
		})
		if err != nil {
			ctx.Logger().Printf("failed to mark original message as forwarded: %v", err)
		}
	}

	err = sender.DoIMAP(func(c *imapclient.Client) error {
		_, err := appendMessage(c, msg, "sent")
		return err
	})
	if err != nil {
		return fmt.Errorf("failed to save message to Sent mailbox: %v", err)
	}

	if draft := options.Draft; draft != nil {
		err = ctx.DoIMAP(func(c *imapclient.Client) error {
			return deleteMessage(c, draft.Mailbox, draft.Uid)
		})
		if err != nil {
			return fmt.Errorf("failed to delete draft: %v", err)
		}
	}

	listings.evictAll(ctx.Session.Username())
	listings.evictAll(sender.Username())
	ctx.Session.PutNotice(ctx.T("notice.sent"))
	return ctx.Redirect(http.StatusFound, ctx.AccountPath("/mailbox/INBOX"))
}

func handleCompose(ctx *alborz.Context, msg *OutgoingMessage, options *composeOptions) error {
	ibase, err := newIMAPBaseRenderData(ctx, alborz.NewBaseRenderData(ctx))
	if err != nil {
		return err
	}

	// Both the sent copy and the saved draft carry it, so a message says
	// what wrote it however it left here.
	msg.Mailer = alborz.BrandName
	if v := ctx.Server.Options.Version; v != "" {
		msg.Mailer += "/" + v
	}

	if msg.From == "" && strings.ContainsRune(ctx.Session.Username(), '@') {
		settings, err := LoadSettings(ctx.Session.Store())
		if err != nil {
			return err
		}
		if settings.From != "" {
			addr := mail.Address{
				Name:    settings.From,
				Address: ctx.Session.Username(),
			}
			msg.From = addr.String()
		} else {
			msg.From = ctx.Session.Username()
		}
	}

	if ctx.Request().Method == http.MethodPost {
		formParams, err := ctx.FormParams()
		if err != nil {
			return fmt.Errorf("failed to parse form: %v", err)
		}
		_, saveAsDraft := formParams["save_as_draft"]

		// The From dropdown picks the sending account and, within it, the
		// address to send as. SMTP, the Sent folder and the settings the
		// message is written under follow that account. Everything the
		// URL and the form name - the draft, the message answered, the
		// attachments - stays on the request's own account.
		sender := ctx.Session
		account, identity := splitIdentityChoice(ctx.FormValue("from_account"))
		if account != "" && account != sender.Username() {
			sender = ctx.SessionFor(account)
			if sender == nil {
				return echo.NewHTTPError(http.StatusBadRequest, "not signed in to that account")
			}
		}
		settings, err := LoadSettings(sender.Store())
		if err != nil {
			return err
		}
		msg.From = sender.Username()
		if settings.From != "" {
			msg.From = (&mail.Address{Name: settings.From, Address: sender.Username()}).String()
		}
		// An identity replaces the address, and carries its own name when
		// it names one. A stale choice falls back to the account itself.
		//
		// No Sender header. RFC 5322 3.6.2 asks for one when the author
		// and the transmitter are different parties - a secretary
		// sending for someone else - which an identity is not: it is
		// the same person under another of their own addresses. Adding
		// it prints the account address on every message and makes Mail
		// and Outlook render "account on behalf of identity", which is
		// the one thing the identity exists to avoid. DMARC aligns on
		// From, so nothing is bought by it either.
		if identity != "" && slices.Contains(settings.Identities, identity) {
			msg.From = identity
		}
		for _, field := range []struct {
			name string
			into *[]string
		}{{"to", &msg.To}, {"cc", &msg.Cc}, {"bcc", &msg.Bcc}, {"reply_to", &msg.ReplyTo}} {
			addresses, err := parseAddressList(ctx.FormValue(field.name))
			if err != nil {
				ibase.BaseRenderData.WithTitle(ctx.T("aside.compose"))
				return ctx.Render(http.StatusUnprocessableEntity, "compose.html", &ComposeRenderData{
					IMAPBaseRenderData: *ibase,
					Message:            msg,
					Attached:           attachedList(msg),
					Identities:         composeIdentities(ctx),
					Signatures:         settings.Signatures,
					Signature:          ctx.FormValue("signature"),
					Error:              ctx.T("form.recipientinvalid"),
				})
			}
			*field.into = addresses
		}
		msg.Subject = ctx.FormValue("subject")
		msg.Text = ctx.FormValue("text")

		// Choosing a signature is a submit like any other, so it works
		// with no script: the body comes back with the old one replaced
		// and everything else as it was typed.
		if _, ok := formParams["signature_apply"]; ok {
			chosen := ctx.FormValue("signature")
			sig, _ := settings.signatureNamed(chosen)
			msg.Text = withSignature(msg.Text, sig.Text)
			ibase.BaseRenderData.WithTitle(ctx.T("aside.compose"))
			return ctx.Render(http.StatusOK, "compose.html", &ComposeRenderData{
				IMAPBaseRenderData: *ibase,
				Message:            msg,
				Attached:           attachedList(msg),
				Identities:         composeIdentities(ctx),
				Signatures:         settings.Signatures,
				Signature:          sig.Name,
			})
		}
		// Both halves of this are the server's to know: the account's
		// setting, and whether the message being answered came from a
		// list. Neither is asked of the page - a form field carrying a
		// decision is one the reader's browser can lose or change, and
		// it made the feature depend on when a tab happened to load.
		sendSettings, err := LoadSettings(sender.Store())
		if err != nil {
			return err
		}
		msg.SendHTML = sendSettings.SendHTML
		if msg.SendHTML && options.InReplyTo != nil {
			toList := false
			if err := ctx.DoIMAP(func(c *imapclient.Client) error {
				id, err := messageListID(c, options.InReplyTo.Mailbox, options.InReplyTo.Uid)
				toList = id != ""
				return err
			}); err != nil {
				// Unknown is treated as a list: sending plain text to a
				// person is a smaller harm than sending an alternative
				// part to a list that refuses it.
				ctx.Logger().Printf("list check for reply: %v", err)
				toList = true
			}
			msg.SendHTML = !toList
		}
		msg.InReplyTo = ctx.FormValue("in_reply_to")
		msg.MessageID = ctx.FormValue("message_id")

		form, err := ctx.MultipartForm()
		if err != nil {
			return fmt.Errorf("failed to get multipart form: %v", err)
		}

		var attached []messagePath
		for _, s := range form.Value[attachedField] {
			p, err := parseAttachedRef(s)
			if err != nil {
				return echo.NewHTTPError(http.StatusBadRequest, err)
			}
			attached = append(attached, p)
		}
		if err := attachMessages(ctx, msg, attached); err != nil {
			return err
		}
		options.Attached = attached

		// Fetch previous attachments from original message
		var original *messagePath
		if options.Draft != nil {
			original = options.Draft
		} else if options.Forward != nil {
			original = options.Forward
		}
		if original != nil {
			for _, s := range form.Value["prev_attachments"] {
				path, err := parsePartPath(s)
				if err != nil {
					return fmt.Errorf("failed to parse original attachment path: %v", err)
				}

				var part *message.Entity
				err = ctx.DoIMAP(func(c *imapclient.Client) error {
					var err error
					_, part, err = getMessagePart(c, original.Mailbox, original.Uid, path)
					return err
				})
				if err != nil {
					return fmt.Errorf("failed to fetch attachment from original message: %v", err)
				}

				var buf bytes.Buffer
				if _, err := io.Copy(&buf, part.Body); err != nil {
					return fmt.Errorf("failed to copy attachment from original message: %v", err)
				}

				h := mail.AttachmentHeader{Header: part.Header}
				mimeType, _, _ := h.ContentType()
				filename, _ := h.Filename()
				msg.Attachments = append(msg.Attachments, &imapAttachment{
					Mailbox: original.Mailbox,
					Uid:     original.Uid,
					Node: &IMAPPartNode{
						Path:     path,
						MIMEType: mimeType,
						Filename: filename,
					},
					Body: buf.Bytes(),
				})
			}
		} else if len(form.Value["prev_attachments"]) > 0 {
			return fmt.Errorf("previous attachments specified but no original message available")
		}

		for _, fh := range form.File["attachments"] {
			msg.Attachments = append(msg.Attachments, &formAttachment{fh})
		}

		uuids := ctx.FormValue("attachment-uuids")
		for _, uuid := range strings.Split(uuids, ",") {
			if uuid == "" {
				continue
			}

			attachment := ctx.Session.PopAttachment(uuid)
			if attachment == nil {
				return fmt.Errorf("Unable to retrieve message attachment %s from session", uuid)
			}
			msg.Attachments = append(msg.Attachments,
				&formAttachment{attachment.File})
			defer attachment.Form.RemoveAll()
		}

		// A message with nowhere to go is not sent, and is not reported
		// as sent; a draft may of course be addressed later.
		if !saveAsDraft && len(msg.To) == 0 && len(msg.Cc) == 0 && len(msg.Bcc) == 0 {
			ibase.BaseRenderData.WithTitle(ctx.T("aside.compose"))
			return ctx.Render(http.StatusUnprocessableEntity, "compose.html", &ComposeRenderData{
				IMAPBaseRenderData: *ibase,
				Message:            msg,
				Attached:           attachedList(msg),
				Identities:         composeIdentities(ctx),
				Signatures:         settings.Signatures,
				Signature:          ctx.FormValue("signature"),
				Error:              ctx.T("form.recipientneeded"),
			})
		}

		if saveAsDraft {
			// A draft is what was typed, and only that. The HTML part is
			// a rendering made when the message is sent, so storing it
			// here would put a multipart/alternative in front of the
			// editor - which cannot open one - for no gain.
			msg.SendHTML = false
			var (
				drafts *MailboxInfo
				uid    imap.UID
			)
			err = ctx.DoIMAP(func(c *imapclient.Client) error {
				drafts, err = appendMessage(c, msg, "drafts")
				if err != nil {
					return err
				}

				if draft := options.Draft; draft != nil {
					if err := deleteMessage(c, draft.Mailbox, draft.Uid); err != nil {
						return err
					}
				}

				if err := ensureMailboxSelected(c, drafts.Name()); err != nil {
					return err
				}

				// TODO: use APPENDUID instead when available
				criteria := imap.SearchCriteria{
					Header: []imap.SearchCriteriaHeaderField{
						{Key: "Message-Id", Value: msg.MessageID},
					},
				}
				if data, err := c.UIDSearch(&criteria, nil).Wait(); err != nil {
					return err
				} else if uids := data.AllUIDs(); len(uids) != 1 {
					panic(fmt.Errorf("Duplicate message ID"))
				} else {
					uid = uids[0]
				}
				return nil
			})
			if err != nil {
				return fmt.Errorf("failed to save message to Draft mailbox: %v", err)
			}
			listings.evictAll(ctx.Session.Username())
			ctx.Session.PutNotice(ctx.T("notice.draftsaved"))
			return ctx.Redirect(http.StatusFound, fmt.Sprintf(
				"/message/%s/%d/edit?part=1", drafts.Mailbox, uid))
		} else {
			return submitCompose(ctx, sender, msg, options)
		}
	}

	ibase.BaseRenderData.WithTitle(ctx.T("aside.compose"))
	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return err
	}

	data := &ComposeRenderData{
		Identities:         composeIdentities(ctx),
		IMAPBaseRenderData: *ibase,
		Message:            msg,
		Attached:           attachedList(msg),
		Signatures:         settings.Signatures,
		Signature:          options.Signature,
	}
	if data.Extra == nil {
		data.Extra = make(map[string]interface{})
	}
	data.Extra["EmailSuggestions"] = gatherCorrespondents(ctx.Session)
	return ctx.Render(http.StatusOK, "compose.html", data)
}

// correspondentTTL decides when the gathered addresses count as stale
// and a background pass goes to fetch them again. They change
// only when mail is sent or arrives, and re-reading two folders on every
// compose would put a fetch in front of a blank page.
const correspondentTTL = 10 * time.Minute

// correspondentDepth is how far back the two folders are read. Recent is
// the point - an address nobody has written to in a thousand messages is
// not what the writer is reaching for.
const correspondentDepth = 200

var correspondents = alborz.NewMemo[[]string](correspondentTTL)

// gatherCorrespondents returns the addresses this account has written to
// and heard from. A contact list holds the people you decided to keep;
// this holds the people you actually exchange mail with, which is not
// the same set and is the one a recipient field is usually reaching for.
// Every client collects it - Apple Mail calls them Previous Recipients,
// Thunderbird a Collected Addresses book, Gmail Other Contacts.
//
// It never waits. Reading two folders is far too much to put in front of
// a blank compose form, so the list is built in the background and the
// form gets whatever is ready - on a cold session, nothing.
func gatherCorrespondents(s *alborz.Session) []string {
	return correspondents.Warm(s.Username(), func() ([]string, error) {
		seen := make(map[string]string)
		keep := func(addrs []imap.Address) {
			for _, a := range addrs {
				addr := a.Addr()
				if addr == "" || !strings.ContainsRune(addr, '@') {
					continue
				}
				entry := addr
				if a.Name != "" {
					entry = (&mail.Address{Name: a.Name, Address: addr}).String()
				}
				// A named form wins over a bare one for the same address.
				key := strings.ToLower(addr)
				if old, ok := seen[key]; !ok || len(entry) > len(old) {
					seen[key] = entry
				}
			}
		}

		err := s.DoIMAP(func(c *imapclient.Client) error {
			mailboxes, err := listMailboxes(c)
			if err != nil {
				return err
			}
			var categorized CategorizedMailboxes
			for i := range mailboxes {
				categorized.Append(mailboxes[i], nil)
			}
			// Sent names who you write to, the inbox who writes to you.
			read := func(name string, out bool) {
				msgs, err := recentEnvelopes(c, name, correspondentDepth)
				if err != nil {
					return
				}
				for _, msg := range msgs {
					if msg.Envelope == nil {
						continue
					}
					if out {
						keep(msg.Envelope.To)
						keep(msg.Envelope.Cc)
					} else {
						keep(msg.Envelope.From)
					}
				}
			}
			if sent := categorized.Common.Sent; sent != nil {
				read(sent.Info.Name(), true)
			}
			read("INBOX", false)
			return nil
		})
		if err != nil {
			return nil, err
		}

		out := make([]string, 0, len(seen))
		for _, entry := range seen {
			out = append(out, entry)
		}
		slices.Sort(out)
		return out, nil
	})
}

// withSignature replaces whatever sits under the last "-- " line with
// the given text, or removes it when the text is empty. The delimiter is
// RFC 3676 4.3 and must start a line, so a quoted "> -- " from the
// message being answered is not mistaken for the writer's own.
func withSignature(body, text string) string {
	if i := strings.LastIndex(body, "\n-- \n"); i >= 0 {
		body = body[:i]
	}
	if text == "" {
		return body
	}
	// Room to write above it, which is the point of starting with one.
	return body + "\n\n\n-- \n" + text
}

func handleComposeNew(ctx *alborz.Context) error {
	// Where a mailto: link arrives when the browser has been told this
	// application opens them. Only a mailto URI is followed:
	// composeFromMailto hands anything else straight back, and
	// redirecting to that would be an open redirect.
	if uri := ctx.QueryParam("mailto"); uri != "" {
		if to := composeFromMailto(uri); strings.HasPrefix(to, "/compose?") {
			return ctx.Redirect(http.StatusFound, to)
		}
		return echo.NewHTTPError(http.StatusBadRequest, "not a mailto URI")
	}

	text := ctx.QueryParam("body")
	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return nil
	}
	chosen := settings.DefaultSignature
	if sig, ok := settings.signatureNamed(chosen); ok && text == "" {
		text = withSignature(text, sig.Text)
	} else if !ok {
		chosen = ""
	}

	// These are common mailto URL query parameters
	var hdr mail.Header
	hdr.GenerateMessageID()
	mid, _ := hdr.MessageID()
	return handleCompose(ctx, &OutgoingMessage{
		From:      ownIdentity(settings, newDeliveryTrust(ctx, settings, ctx.Session.Username()), ctx.QueryParam("from")),
		To:        strings.Split(ctx.QueryParam("to"), ","),
		Subject:   ctx.QueryParam("subject"),
		MessageID: "<" + mid + ">",
		InReplyTo: ctx.QueryParam("in-reply-to"),
		Text:      text,
	}, &composeOptions{Signature: chosen})
}

func handleComposeAttachment(ctx *alborz.Context) error {
	reader, err := ctx.Request().MultipartReader()
	if err != nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "Invalid request",
		})
	}
	form, err := reader.ReadForm(32 << 20) // 32 MB
	if err != nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "Invalid request",
		})
	}

	var uuids []string
	for _, fh := range form.File["attachments"] {
		uuid, err := ctx.Session.PutAttachment(fh, form)
		if err == alborz.ErrAttachmentCacheSize {
			form.RemoveAll()
			return ctx.JSON(http.StatusBadRequest, map[string]string{
				"error": "Your attachments exceed the maximum file size. Remove some and try again.",
			})
		} else if err != nil {
			form.RemoveAll()
			ctx.Logger().Printf("PutAttachment: %v\n", err)
			return ctx.JSON(http.StatusBadRequest, map[string]string{
				"error": "failed to store attachment",
			})
		}
		uuids = append(uuids, uuid)
	}

	return ctx.JSON(http.StatusOK, &uuids)
}

func handleCancelAttachment(ctx *alborz.Context) error {
	uuid := ctx.Param("uuid")
	a := ctx.Session.PopAttachment(uuid)
	if a != nil {
		a.Form.RemoveAll()
	}
	return ctx.JSON(http.StatusOK, nil)
}

func unwrapIMAPAddressList(addrs []imap.Address) []string {
	l := make([]string, len(addrs))
	for i, addr := range addrs {
		l[i] = unwrapIMAPAddress(addr)
	}
	return l
}

func unwrapIMAPAddress(addr imap.Address) string {
	address := addr.Addr()
	if addr.Name != "" {
		address = fmt.Sprintf("%q <%s>", addr.Name, address)
	}
	return address
}

func handleReply(ctx *alborz.Context) error {
	var inReplyToPath messagePath
	var err error
	inReplyToPath.Mailbox, inReplyToPath.Uid, err = parseMboxAndUid(ctx.Param("mbox"), ctx.Param("uid"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	var msg OutgoingMessage
	if ctx.Request().Method == http.MethodGet {
		msg, err = populateMessageFromOriginalMessage(ctx, inReplyToPath)
		if err != nil {
			return err
		}
	}

	return handleCompose(ctx, &msg, &composeOptions{InReplyTo: &inReplyToPath})
}

// quotablePart fetches the part a reply or a forward should quote. A
// bare link names no part and a multipart message's root is not text,
// so the text part a reader was shown is fetched instead - without it,
// answering any message that carries an attachment was a 400.
func quotablePart(ctx *alborz.Context, path messagePath, partPath []int) (*IMAPMessage, *message.Entity, string, error) {
	var msg *IMAPMessage
	var part *message.Entity
	fetch := func(p []int) error {
		return ctx.DoIMAP(func(c *imapclient.Client) error {
			var err error
			msg, part, err = getMessagePart(c, path.Mailbox, path.Uid, p)
			return err
		})
	}
	if err := fetch(partPath); err != nil {
		return nil, nil, "", err
	}
	mimeType, _, err := part.Header.ContentType()
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to parse part Content-Type: %v", err)
	}
	if len(partPath) > 0 || !strings.HasPrefix(mimeType, "multipart/") {
		return msg, part, mimeType, nil
	}

	text := msg.TextPart()
	if text == nil {
		return nil, nil, "", echo.NewHTTPError(http.StatusBadRequest,
			fmt.Errorf("message has no text part to quote"))
	}
	if err := fetch(text.Path); err != nil {
		return nil, nil, "", err
	}
	mimeType, _, err = part.Header.ContentType()
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to parse part Content-Type: %v", err)
	}
	return msg, part, mimeType, nil
}

func populateMessageFromOriginalMessage(ctx *alborz.Context, inReplyToPath messagePath) (OutgoingMessage, error) {
	var ret OutgoingMessage

	partPath, err := parsePartPath(ctx.QueryParam("part"))
	if err != nil {
		return ret, echo.NewHTTPError(http.StatusBadRequest, err)
	}

	inReplyTo, part, mimeType, err := quotablePart(ctx, inReplyToPath, partPath)
	if err != nil {
		return ret, err
	}

	var quoted string
	switch mimeType {
	case "text/plain":
		quoted, err = quote(part.Body)
	case "text/html":
		var text string
		text, err = html2text.FromReader(part.Body, html2text.Options{})
		if err != nil {
			return ret, err
		}

		quoted, err = quote(strings.NewReader(text))
	default:
		defErr := fmt.Errorf("cannot forward %q part", mimeType)
		err = echo.NewHTTPError(http.StatusBadRequest, defErr)
	}
	if err != nil {
		return ret, err
	}

	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return ret, err
	}
	ret.Text = replyBody(ctx, inReplyTo, quoted, settings.ReplyBelowQuote)
	ret.QuoteBelow = !settings.ReplyBelowQuote

	var hdr mail.Header
	hdr.GenerateMessageID()
	mid, _ := hdr.MessageID()
	ret.MessageID = "<" + mid + ">"
	ret.InReplyTo = "<" + inReplyTo.Envelope.MessageID + ">"
	// RFC 5322 3.6.4: the chain is what was there plus what is being
	// answered, and it is the header threading actually runs on.
	ret.References = strings.TrimSpace(inReplyTo.References + " " + ret.InReplyTo)
	// Answer from the address it was sent to, when that address is one
	// of ours: a mail to an identity is answered by that identity, not
	// by the account behind it. Cc counts as well, since a list often
	// puts the identity there.
	ret.From = writeAs(settings, newDeliveryTrust(ctx, settings, ctx.Session.Username()),
		inReplyTo, inReplyTo.Envelope.To, inReplyTo.Envelope.Cc)
	replyTo := inReplyTo.Envelope.ReplyTo
	if len(replyTo) == 0 {
		replyTo = inReplyTo.Envelope.From
	}
	ret.To = unwrapIMAPAddressList(replyTo)

	// A reply to the list goes to the list and nowhere else, which is
	// what List-Post names and what the convention expects.
	if ctx.QueryParam("list") != "" && inReplyTo.ListPost != "" {
		ret.To = []string{inReplyTo.ListPost}
	} else if ctx.QueryParam("all") != "" {
		filtered := filterOutUsername(ctx.Session.Username(),
			inReplyTo.Envelope.To)
		ret.To = unwrapIMAPAddressList(append(replyTo, filtered...))
		ret.Cc = unwrapIMAPAddressList(inReplyTo.Envelope.Cc)
	}

	ret.Subject = inReplyTo.Envelope.Subject
	if !strings.HasPrefix(strings.ToLower(ret.Subject), "re:") {
		ret.Subject = "Re: " + ret.Subject
	}

	return ret, nil
}

// readAll is the body of a part as it was written.
func readAll(r io.Reader) (string, error) {
	b, err := io.ReadAll(r)
	return string(b), err
}

// forwardBody is the message being passed on, whole: a separator, the
// headers that say where it came from, then the body exactly as it
// arrived. It is not quoted - "> " in front of every line is reply
// convention, and prefixing a forward is what turned a readable message
// into a wall of chevrons. The person writes above it.
func forwardBody(ctx *alborz.Context, original *IMAPMessage, body string) string {
	g := alborz.NewBaseRenderData(ctx).GlobalData
	var b strings.Builder
	b.WriteString("\n\n")
	// Not translated: nothing parses it, and the recipient may not read
	// the sender's language. The header names below are RFC 5322's for
	// the same reason.
	b.WriteString(ctx.T("message.forwardedblock"))
	b.WriteString("\n")
	if from := original.Envelope.From; len(from) > 0 {
		fmt.Fprintf(&b, "From: %s\n", addressLine(from[0]))
	}
	fmt.Fprintf(&b, "Date: %s\n", g.FormatDate(original.Date()))
	fmt.Fprintf(&b, "Subject: %s\n", original.Envelope.Subject)
	if to := original.Envelope.To; len(to) > 0 {
		fmt.Fprintf(&b, "To: %s\n", strings.Join(unwrapIMAPAddressList(to), ", "))
	}
	b.WriteString("\n")
	b.WriteString(body)
	return b.String()
}

func addressLine(a imap.Address) string {
	if a.Name != "" {
		return fmt.Sprintf("%s <%s>", a.Name, a.Addr())
	}
	return a.Addr()
}

// replyBody puts the quote where the person writing wants to stand
// relative to it: above it, which is what most correspondents expect,
// or below it, which is what a mailing list asks for. Either way the
// quote is introduced, so a reader can see whose words they are without
// scrolling to find out.
func replyBody(ctx *alborz.Context, original *IMAPMessage, quoted string, below bool) string {
	who := ""
	if from := original.Envelope.From; len(from) > 0 {
		who = from[0].Name
		if who == "" {
			who = from[0].Addr()
		}
	}
	when := alborz.NewBaseRenderData(ctx).GlobalData.FormatDate(original.Date())
	attribution := ctx.Tf("message.wrote", when, who)
	if below {
		return attribution + "\n" + quoted + "\n"
	}
	return "\n\n" + attribution + "\n" + quoted
}

// parseIdentities reads the settings textarea: one address per line,
// blank lines ignored.
func parseIdentities(raw string) []string {
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// splitIdentityChoice reads the From dropdown's value: the account, and
// optionally the identity of that account to send as.
func splitIdentityChoice(value string) (account, identity string) {
	account, identity, _ = strings.Cut(value, "|")
	return account, identity
}

// identityAddressed names the address a reply should be written from:
// the identity the original was addressed to, if one of them was, and
// otherwise the empty string, which leaves the compose form on its own
// default. Comparison is case-insensitive - a mail addressed to
// Mx@43.yt is addressed to the identity mx@43.yt.
func identityAddressed(settings *Settings, username string, lists ...[]imap.Address) string {
	for _, list := range lists {
		for _, addr := range list {
			if strings.EqualFold(addr.Addr(), username) {
				return ""
			}
			for _, identity := range settings.Identities {
				parsed, err := mail.ParseAddress(identity)
				if err != nil {
					continue
				}
				if strings.EqualFold(parsed.Address, addr.Addr()) {
					return identity
				}
			}
		}
	}
	return ""
}

// deliveryTrust decides which of the addresses a message's delivery path
// named may be shown or written from. Nothing else may: the headers are
// part of the message, so a sender can write any of them, and an address
// that cannot be checked is not displayed at all rather than displayed
// with a caveat nobody reads.
type deliveryTrust struct {
	// account is the address the message landed in.
	account string
	// domains are the mail domains this server serves. An address at
	// one of them can be the reader's; an address anywhere else cannot,
	// whatever the header claims.
	domains []string
	// authserv is what the reader's own server calls itself, from the
	// same setting Authentication-Results is read under. Empty until
	// they name it, and the check below is skipped until they do.
	authserv string
}

func newDeliveryTrust(ctx *alborz.Context, settings *Settings, account string) deliveryTrust {
	return deliveryTrust{
		account:  account,
		domains:  ctx.Server.Domains(),
		authserv: settings.TrustedAuthServ,
	}
}

// ours reports whether an address could be one the reader receives at.
func (t deliveryTrust) ours(addr string) bool {
	at := strings.LastIndex(addr, "@")
	if at < 0 {
		return false
	}
	domain := strings.ToLower(addr[at+1:])
	if account := strings.LastIndex(t.account, "@"); account >= 0 &&
		strings.EqualFold(domain, t.account[account+1:]) {
		return true
	}
	for _, served := range t.domains {
		if strings.EqualFold(domain, served) {
			return true
		}
	}
	return false
}

// addresses returns the delivery addresses worth believing for a
// message: at one of our domains, and - once the reader has named their
// own server - written above the Received that server added, since
// everything below it was in the message before we ever saw it.
func (t deliveryTrust) addresses(msg *IMAPMessage) []string {
	account := t.account
	if msg.Account != "" {
		// Every row of the merged view belongs to a different account.
		account = msg.Account
	}
	addrs, cut := deliveryAddresses(msg.rootHeader, t.authserv)
	var out []string
	for i, addr := range addrs {
		if i >= cut || !t.ours(addr) || strings.EqualFold(addr, account) {
			continue
		}
		out = append(out, addr)
	}
	return out
}

// alias is the one address a message reached that is not the account it
// landed in, or "" when there is nothing worth showing.
func (t deliveryTrust) alias(msg *IMAPMessage) string {
	if got := t.addresses(msg); len(got) > 0 {
		return got[0]
	}
	return ""
}

// identityDelivered names the identity a message was delivered to, from
// what the delivery path wrote down rather than from To and Cc. Mail to
// an alias often names the alias nowhere else: To holds the list or the
// original recipient, and only the delivering server records where it
// actually went.
//
// Unlike identityAddressed this does not stop at the account's own
// address. A delivery chain records both - the alias it was sent to and
// the mailbox it ended in - so meeting the account is a reason to keep
// looking rather than to give up.
func identityDelivered(settings *Settings, username string, delivered []string) string {
	for _, addr := range delivered {
		if strings.EqualFold(addr, username) {
			continue
		}
		for _, identity := range settings.Identities {
			parsed, err := mail.ParseAddress(identity)
			if err != nil {
				continue
			}
			if strings.EqualFold(parsed.Address, addr) {
				return identity
			}
		}
	}
	return ""
}

// writeAs names the address a message should be answered or unsubscribed
// from: the identity it was addressed to, else the one it was delivered
// to. Empty leaves the compose form on its own default.
func writeAs(settings *Settings, trust deliveryTrust, msg *IMAPMessage, lists ...[]imap.Address) string {
	if from := identityAddressed(settings, trust.account, lists...); from != "" {
		return from
	}
	// Only what the delivery path is trusted for: an address a sender
	// wrote into a header is not one of the reader's identities.
	trusted := trust.addresses(msg)
	if from := identityDelivered(settings, trust.account, trusted); from != "" {
		return from
	}
	// An alias at one of our own domains that nobody has written down as
	// an identity is still where the message went, and a list keyed on
	// it will not answer to anything else. Offered bare, since there is
	// no name to go with it.
	if len(trusted) > 0 {
		return trusted[0]
	}
	return ""
}

func filterOutUsername(username string, addresses []imap.Address) []imap.Address {
	for i, addr := range addresses {
		if addr.Addr() == username {
			return append(addresses[:i], addresses[i+1:]...)
		}
	}
	return addresses
}

// handleUnsubscribe takes a list at its word. RFC 8058 lets a sender
// promise that one POST to the URI in List-Unsubscribe removes the
// reader, and the POST is made from here rather than from the page: the
// browser may not post across origins, and the endpoint is the list's,
// not ours. The URI is read from the message again rather than taken
// from the form, so a page cannot ask us to post anywhere it likes.
func handleUnsubscribe(ctx *alborz.Context) error {
	mboxName, uid, err := parseMboxAndUid(ctx.Param("mbox"), ctx.Param("uid"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}
	var msg *IMAPMessage
	err = ctx.DoIMAP(func(c *imapclient.Client) error {
		var err error
		msg, _, err = getMessagePart(c, mboxName, uid, nil)
		return err
	})
	if err != nil {
		return err
	}
	back := ctx.NextOr(ctx.AccountPath(fmt.Sprintf("/message/%v/%v",
		url.PathEscape(mboxName), uid)))
	if msg.OneClick == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "this message offers no one-click unsubscribe")
	}

	body := strings.NewReader("List-Unsubscribe=One-Click")
	req, err := http.NewRequestWithContext(ctx.Request().Context(),
		http.MethodPost, msg.OneClick, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := alborz.NewRemoteClient(unsubscribeTimeout)
	resp, err := client.Do(req)
	if err != nil {
		ctx.Session.PutNotice(ctx.T("notice.unsubfailed"))
		return ctx.Redirect(http.StatusFound, back)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		ctx.Logger().Printf("unsubscribe %s answered %s", msg.OneClick, resp.Status)
		ctx.Session.PutNotice(ctx.T("notice.unsubfailed"))
		return ctx.Redirect(http.StatusFound, back)
	}
	ctx.Session.PutNotice(ctx.T("notice.unsubscribed"))
	return ctx.Redirect(http.StatusFound, back)
}

// unsubscribeTimeout bounds the one request we make to somebody else's
// server on the reader's behalf.
const unsubscribeTimeout = 15 * time.Second

// handleForwardSelection is the list toolbar's Forward: the selection
// names the message, where the message page names it in the path.
// Forwarding several at once means attaching each as message/rfc822,
// which is not built yet, so it says so rather than silently forwarding
// one of them.
func handleForwardSelection(ctx *alborz.Context) error {
	mboxName, err := url.PathUnescape(ctx.Param("mbox"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}
	params, err := ctx.FormParams()
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}
	uids, err := parseUidList(params["uids"])
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}
	back := ctx.NextOr(ctx.AccountPath(fmt.Sprintf("/mailbox/%v", url.PathEscape(mboxName))))
	if len(uids) == 0 {
		return ctx.Redirect(http.StatusFound, back)
	}
	// One message is passed on inline, where the writer can trim it.
	// Several cannot be: concatenating them means nothing, so each
	// travels whole as an attachment, which is what every client that
	// offers this does.
	if len(uids) == 1 {
		return ctx.Redirect(http.StatusFound, ctx.AccountPath(
			fmt.Sprintf("/message/%v/%v/forward", url.PathEscape(mboxName), uids[0])))
	}

	refs := make([]string, len(uids))
	for i, uid := range uids {
		refs[i] = fmt.Sprintf("%v", uid)
	}
	q := url.Values{"uids": {strings.Join(refs, ",")}}
	if a := ctx.URLAccount(); a != "" {
		q.Set("account", alborz.AddressParam(a))
	}
	return ctx.Redirect(http.StatusFound, fmt.Sprintf("/message/%v/forward?%s",
		url.PathEscape(mboxName), q.Encode()))
}

// handleForwardAttached composes one message carrying several whole
// ones. It is a GET so the page can be reloaded and so compose sees the
// request it expects; the selection arrives in the query.
func handleForwardAttached(ctx *alborz.Context) error {
	mboxName, err := url.PathUnescape(ctx.Param("mbox"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}
	uids, err := parseUidList(strings.Split(ctx.QueryParam("uids"), ","))
	if err != nil || len(uids) == 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "no messages to forward")
	}
	paths := make([]messagePath, len(uids))
	for i, uid := range uids {
		paths[i] = messagePath{Mailbox: mboxName, Uid: uid}
	}
	var msg OutgoingMessage
	if ctx.Request().Method == http.MethodGet {
		if err := attachMessages(ctx, &msg, paths); err != nil {
			return err
		}
		var hdr mail.Header
		hdr.GenerateMessageID()
		mid, _ := hdr.MessageID()
		msg.MessageID = "<" + mid + ">"
		msg.Subject = ctx.Tf("message.forwardcount", len(uids))
		msg.QuoteBelow = true
	}
	return handleCompose(ctx, &msg, &composeOptions{Attached: paths})
}

func handleForward(ctx *alborz.Context) error {
	var sourcePath messagePath
	var err error
	sourcePath.Mailbox, sourcePath.Uid, err = parseMboxAndUid(ctx.Param("mbox"), ctx.Param("uid"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	var msg OutgoingMessage
	if ctx.Request().Method == http.MethodGet {
		// Populate fields from original message
		partPath, err := parsePartPath(ctx.QueryParam("part"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err)
		}

		source, part, mimeType, err := quotablePart(ctx, sourcePath, partPath)
		if err != nil {
			return err
		}

		var body string
		switch mimeType {
		case "text/plain":
			body, err = readAll(part.Body)
		case "text/html":
			// Lossy, and the only thing a plain-text composer can do
			// with HTML. Attaching the message instead keeps it whole.
			body, err = html2text.FromReader(part.Body, html2text.Options{})
		default:
			defErr := fmt.Errorf("cannot forward %q part", mimeType)
			err = echo.NewHTTPError(http.StatusBadRequest, defErr)
		}
		if err != nil {
			return err
		}
		msg.Text = forwardBody(ctx, source, body)
		msg.QuoteBelow = true

		var hdr mail.Header
		hdr.GenerateMessageID()
		mid, _ := hdr.MessageID()
		msg.MessageID = "<" + mid + ">"
		msg.Subject = source.Envelope.Subject
		if !strings.HasPrefix(strings.ToLower(msg.Subject), "fwd:") &&
			!strings.HasPrefix(strings.ToLower(msg.Subject), "fw:") {
			msg.Subject = "Fwd: " + msg.Subject
		}
		// A forward starts a thread of its own: it carried the original's
		// In-Reply-To, which made it a sibling reply to whatever that
		// message was answering, in a conversation the reader may not
		// even be in.
		msg.References = "<" + source.Envelope.MessageID + ">"

		attachments := source.Attachments()
		for i := range attachments {
			// No need to populate attachment body here, we just need the
			// metadata
			msg.Attachments = append(msg.Attachments, &imapAttachment{
				Mailbox: sourcePath.Mailbox,
				Uid:     sourcePath.Uid,
				Node:    &attachments[i],
			})
		}
	}

	return handleCompose(ctx, &msg, &composeOptions{Forward: &sourcePath})
}

func handleEdit(ctx *alborz.Context) error {
	var sourcePath messagePath
	var err error
	sourcePath.Mailbox, sourcePath.Uid, err = parseMboxAndUid(ctx.Param("mbox"), ctx.Param("uid"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	// TODO: somehow get the path to the In-Reply-To message (with a search?)

	var msg OutgoingMessage
	if ctx.Request().Method == http.MethodGet {
		// Populate fields from source message
		partPath, err := parsePartPath(ctx.QueryParam("part"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err)
		}

		source, part, mimeType, err := quotablePart(ctx, sourcePath, partPath)
		if err != nil {
			return err
		}

		if !strings.EqualFold(mimeType, "text/plain") {
			err := fmt.Errorf("cannot edit %q part", mimeType)
			return echo.NewHTTPError(http.StatusBadRequest, err)
		}

		b, err := ioutil.ReadAll(part.Body)
		if err != nil {
			return fmt.Errorf("failed to read part body: %v", err)
		}
		msg.Text = string(b)

		if len(source.Envelope.From) > 0 {
			msg.From = source.Envelope.From[0].Addr()
		}
		msg.To = unwrapIMAPAddressList(source.Envelope.To)
		msg.Subject = source.Envelope.Subject
		msg.InReplyTo = formatMsgIDList(source.Envelope.InReplyTo)
		msg.MessageID = "<" + source.Envelope.MessageID + ">"

		attachments := source.Attachments()
		for i := range attachments {
			// No need to populate attachment body here, we just need the
			// metadata
			msg.Attachments = append(msg.Attachments, &imapAttachment{
				Mailbox: sourcePath.Mailbox,
				Uid:     sourcePath.Uid,
				Node:    &attachments[i],
			})
		}
	}

	return handleCompose(ctx, &msg, &composeOptions{Draft: &sourcePath})
}

func formatMsgIDList(l []string) string {
	if len(l) == 0 {
		return ""
	}
	return "<" + strings.Join(l, ">, <") + ">"
}

// The explicit query parameter a button carries wins over a form field,
// so the bulk move selector cannot leak into other actions.
func formOrQueryParam(ctx *alborz.Context, k string) string {
	if v := ctx.QueryParam(k); v != "" {
		return v
	}
	return ctx.FormValue(k)
}

func handleMove(ctx *alborz.Context) error {
	mboxName, err := url.PathUnescape(ctx.Param("mbox"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	formParams, err := ctx.FormParams()
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}
	uids, err := parseUidList(formParams["uids"])
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	if len(uids) == 0 {
		ctx.Session.PutNotice(ctx.T("notice.nomessages"))
		return ctx.Redirect(http.StatusFound, ctx.AccountPath(fmt.Sprintf("/mailbox/%v", url.PathEscape(mboxName))))
	}

	to := formOrQueryParam(ctx, "to")
	if to == "" {
		ctx.Session.PutNotice(ctx.T("notice.nodestination"))
		return ctx.Redirect(http.StatusFound, ctx.AccountPath(fmt.Sprintf("/mailbox/%v", url.PathEscape(mboxName))))
	}

	err = ctx.DoIMAP(func(c *imapclient.Client) error {
		if err := ensureMailboxSelected(c, mboxName); err != nil {
			return err
		}

		if _, err := c.Move(imap.UIDSetNum(uids...), to).Wait(); err != nil {
			return fmt.Errorf("failed to move message: %v", err)
		}

		// TODO: get the UID of the message in the destination mailbox with UIDPLUS
		return nil
	})
	if err != nil {
		return err
	}

	listings.evict(ctx.Session.Username(), mboxName)
	listings.evict(ctx.Session.Username(), to)
	ctx.Session.PutNotice(ctx.Tf("notice.moved", len(uids)))
	return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.AccountPath(fmt.Sprintf("/mailbox/%v", url.PathEscape(mboxName)))))
}

// handleEmptyMailbox expunges everything in a folder, the standard
// one-click cleanup for Junk and Trash.
func handleEmptyMailbox(ctx *alborz.Context) error {
	mboxName, err := url.PathUnescape(ctx.Param("mbox"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	var removed int
	err = ctx.DoIMAP(func(c *imapclient.Client) error {
		var err error
		removed, err = emptyMailbox(c, mboxName)
		return err
	})
	if err != nil {
		return err
	}

	listings.evict(ctx.Session.Username(), mboxName)
	ctx.Session.PutNotice(emptiedNotice(ctx, removed))
	return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.AccountPath(fmt.Sprintf("/mailbox/%v", url.PathEscape(mboxName)))))
}

// emptyMailbox flags and expunges every message in the folder and reports
// how many there were: a caller that says "deleted" over an already empty
// folder is telling the user something that did not happen.
func emptyMailbox(c *imapclient.Client, mboxName string) (int, error) {
	if err := ensureMailboxSelected(c, mboxName); err != nil {
		return 0, err
	}
	n := int(c.Mailbox().NumMessages)
	if n == 0 {
		return 0, nil
	}
	var seq imap.SeqSet
	seq.AddRange(1, 0)
	if err := c.Store(seq, &imap.StoreFlags{
		Op:     imap.StoreFlagsAdd,
		Silent: true,
		Flags:  []imap.Flag{imap.FlagDeleted},
	}, nil).Close(); err != nil {
		return 0, fmt.Errorf("failed to add deleted flag: %v", err)
	}
	if err := c.Expunge().Close(); err != nil {
		return 0, fmt.Errorf("failed to expunge mailbox: %v", err)
	}
	return n, nil
}

// emptiedNotice reports what emptying actually removed.
func emptiedNotice(ctx *alborz.Context, removed int) string {
	if removed == 0 {
		return ctx.T("notice.alreadyempty")
	}
	return ctx.Tf("notice.emptied", removed)
}

// handleEmptyAllMailbox empties the same role folder on every signed-in
// account: the pooled Junk/Trash views act on the merge, so a single
// control clears them all.
func handleEmptyAllMailbox(ctx *alborz.Context) error {
	role, err := url.PathUnescape(ctx.Param("role"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}
	if !slices.Contains(unifiedRoles, role) {
		return echo.NewHTTPError(http.StatusNotFound,
			fmt.Sprintf("%q is not a unified folder", role))
	}

	var failed []string
	var removed int
	for _, s := range ctx.Sessions() {
		err := s.DoIMAP(func(c *imapclient.Client) error {
			folder, err := resolveRole(c, s.Username(), role)
			if err != nil {
				return err
			}
			n, err := emptyMailbox(c, folder)
			removed += n
			return err
		})
		listings.evict(s.Username(), "#"+role)
		if err != nil {
			failed = append(failed, s.Username()+": "+err.Error())
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("failed to empty %s: %s", role, strings.Join(failed, "; "))
	}

	ctx.Session.PutNotice(emptiedNotice(ctx, removed))
	return ctx.Redirect(http.StatusFound, ctx.NextOr(fmt.Sprintf("/mailbox/%s?all=1", role)))
}

func handleDelete(ctx *alborz.Context) error {
	mboxName, err := url.PathUnescape(ctx.Param("mbox"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	formParams, err := ctx.FormParams()
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}
	uids, err := parseUidList(formParams["uids"])
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	if len(uids) == 0 {
		ctx.Session.PutNotice(ctx.T("notice.nomessages"))
		return ctx.Redirect(http.StatusFound, ctx.AccountPath(fmt.Sprintf("/mailbox/%v", url.PathEscape(mboxName))))
	}

	err = ctx.DoIMAP(func(c *imapclient.Client) error {
		if err := ensureMailboxSelected(c, mboxName); err != nil {
			return err
		}

		err := c.Store(imap.UIDSetNum(uids...), &imap.StoreFlags{
			Op:     imap.StoreFlagsAdd,
			Silent: true,
			Flags:  []imap.Flag{imap.FlagDeleted},
		}, nil).Close()
		if err != nil {
			return fmt.Errorf("failed to add deleted flag: %v", err)
		}

		if err := c.Expunge().Close(); err != nil {
			return fmt.Errorf("failed to expunge mailbox: %v", err)
		}

		return nil
	})
	if err != nil {
		return err
	}

	listings.evict(ctx.Session.Username(), mboxName)
	ctx.Session.PutNotice(ctx.Tf("notice.deleted", len(uids)))
	return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.AccountPath(fmt.Sprintf("/mailbox/%v", url.PathEscape(mboxName)))))
}

func handleSetFlags(ctx *alborz.Context) error {
	mboxName, err := url.PathUnescape(ctx.Param("mbox"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	formParams, err := ctx.FormParams()
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	uids, err := parseUidList(formParams["uids"])
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	// A colour is a flag plus a bit field, so it is set and cleared in
	// one exchange rather than by asking the page to spell out both.
	if color, ok := formParams["color"]; ok {
		add, del := FlagColorFlags(color[0])
		if add == nil && del == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "unknown flag colour")
		}
		err = ctx.DoIMAP(func(c *imapclient.Client) error {
			if err := ensureMailboxSelected(c, mboxName); err != nil {
				return err
			}
			for _, step := range []struct {
				op    imap.StoreFlagsOp
				flags []imap.Flag
			}{{imap.StoreFlagsAdd, add}, {imap.StoreFlagsDel, del}} {
				if len(step.flags) == 0 {
					continue
				}
				if err := c.Store(imap.UIDSetNum(uids...), &imap.StoreFlags{
					Op: step.op, Silent: true, Flags: step.flags,
				}, nil).Close(); err != nil {
					return fmt.Errorf("failed to set flag colour: %v", err)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		listings.evict(ctx.Session.Username(), mboxName)
		return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.AccountPath(
			fmt.Sprintf("/mailbox/%v", url.PathEscape(mboxName)))))
	}

	flags, ok := formParams["flags"]
	if !ok {
		flagsStr := ctx.QueryParam("to")
		if flagsStr == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "missing 'flags' form parameter")
		}
		flags = strings.Fields(flagsStr)
	}

	actionStr := ctx.FormValue("action")
	if actionStr == "" {
		actionStr = ctx.QueryParam("action")
	}

	var op imap.StoreFlagsOp
	switch actionStr {
	case "", "set":
		op = imap.StoreFlagsSet
	case "add":
		op = imap.StoreFlagsAdd
	case "remove":
		op = imap.StoreFlagsDel
	default:
		return echo.NewHTTPError(http.StatusBadRequest, "invalid 'action' value")
	}

	l := make([]imap.Flag, len(flags))
	for i, s := range flags {
		l[i] = imap.Flag(s)
	}

	err = ctx.DoIMAP(func(c *imapclient.Client) error {
		if err := ensureMailboxSelected(c, mboxName); err != nil {
			return err
		}

		err := c.Store(imap.UIDSetNum(uids...), &imap.StoreFlags{
			Op:     op,
			Silent: true,
			Flags:  l,
		}, nil).Close()
		if err != nil {
			return fmt.Errorf("failed to set flags: %v", err)
		}

		return nil
	})
	if err != nil {
		return err
	}
	listings.evict(ctx.Session.Username(), mboxName)

	if next := ctx.NextOr(""); next != "" {
		return ctx.Redirect(http.StatusFound, next)
	}
	if len(uids) != 1 || (op == imap.StoreFlagsDel && len(l) == 1 && l[0] == imap.FlagSeen) {
		// Redirecting to the message view would mark the message as read again
		return ctx.Redirect(http.StatusFound, ctx.AccountPath(fmt.Sprintf("/mailbox/%v", url.PathEscape(mboxName))))
	}
	return ctx.Redirect(http.StatusFound, ctx.AccountPath(fmt.Sprintf("/message/%v/%v", url.PathEscape(mboxName), uids[0])))
}

const settingsKey = "base.settings"

// perPage is how many rows a listing shows: the stored preference,
// unless the URL asks for another count with ipp. That parameter answers
// for this look at the page only and is not written back - the same
// shape the sort order and the search term already have - so a link
// carrying one is a link to a longer page rather than a change to the
// reader's settings. Out-of-range asks fall back to the preference
// rather than failing: a number in a URL is not a form to validate.
// perPageLadder is what the toolbar offers besides the reader's own
// preference. Three steps, not a spinner: the question is "a few more"
// or "the lot", and a list of links needs no script to answer it.
var perPageLadder = []int{25, 50, 100}

// perPageOptions is the ladder with the reader's own preference folded
// in, sorted and without repeats, so the count in force is always one
// of the choices and picking it is how they return to it.
func perPageOptions(settings *Settings) []int {
	seen := map[int]bool{}
	var out []int
	for _, n := range append(append([]int{}, perPageLadder...), settings.MessagesPerPage) {
		if n <= 0 || n > maxMessagesPerPage || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

func perPage(ctx *alborz.Context, settings *Settings) int {
	if raw := ctx.QueryParam("ipp"); raw != "" {
		if n, err := strconv.Atoi(alborz.LatinDigits(raw)); err == nil &&
			n > 0 && n <= maxMessagesPerPage {
			return n
		}
	}
	return settings.MessagesPerPage
}

const (
	maxMessagesPerPage = 100
	maxSignature       = 2048
	// A header line may be 998 octets (RFC 5322 2.1.1); a body line in
	// the wild is longer, and a scanner that stops is a truncated file.
	maxMboxLine      = 1 << 20
	maxDownloadName  = 80
	maxSignatures    = 20
	maxSignatureName = 60
	maxFullName      = 512
)

// Signature is one of an account's sign-offs: a name to pick it by and
// the text that goes under the "-- " delimiter (RFC 3676 4.3).
type Signature struct {
	Name string
	Text string
}

type Settings struct {
	MessagesPerPage int
	// Signatures belong to the account rather than to an identity: which
	// persona an address writes as is the writer's to decide per message,
	// and a rule mapping the two would be wrong as often as it was right.
	Signatures []Signature
	// DefaultSignature names the one a new message starts with; empty
	// means none.
	DefaultSignature string
	From             string
	// Identities are the other addresses this mailbox may send as, one
	// per line, each a bare address or a "Name <address>" pair. The
	// account's own address is always offered and is not listed here.
	Identities     []string
	Subscriptions  []string
	Timezone       string
	FirstDayOfWeek int // 0 = Sunday, 1 = Monday (default)

	// TrustedAuthServ is the authserv-id of the server that takes
	// delivery for this account - what it calls itself in the
	// Authentication-Results header it writes (RFC 8601). Only that
	// server's verdict is read, because every other instance of the
	// header was written by somebody upstream, possibly the sender.
	// Empty means no verdict is shown: a guess here would be worse than
	// silence, since the whole point is knowing who wrote the line.
	TrustedAuthServ string

	// Stored negated so the zero value keeps body search on by default.
	SearchHeadersOnly bool

	// ReplyBelowQuote puts the reply after the quoted message, the way a
	// mailing list expects it, instead of before. Stored positively:
	// the zero value keeps the reply on top, which is what a mail client
	// has always done here and what most correspondents expect.
	ReplyBelowQuote bool

	// SendHTML adds an HTML part to outgoing mail that says nothing but
	// which way each paragraph runs. Off by default: mailing lists ask
	// for plain text and some refuse multipart/alternative outright, so
	// an account that talks to lists wants it off and one that writes
	// Persian to Gmail wants it on.
	SendHTML bool

	// PreferHTML opens a message at its HTML part where it has one.
	// Plain text is the default because it is the part a sender wrote
	// for reading rather than for looking at, and it carries no remote
	// content; an account whose correspondents send HTML that says
	// something the plain part does not wants the other order.
	PreferHTML bool
}

func LoadSettings(s alborz.Store) (*Settings, error) {
	settings := &Settings{
		MessagesPerPage: 50,
		FirstDayOfWeek:  1, // Monday
	}
	if err := s.Get(settingsKey, settings); err != nil && err != alborz.ErrNoStoreEntry {
		return nil, err
	}
	if key, limit := settings.check(); key != "" {
		return nil, fmt.Errorf("stored settings break %s (%d)", key, limit)
	}
	return settings, nil
}

// check reports the first rule the settings break as the key of the
// message that says so and the limit that message names; the key is
// empty when they hold.
func (s *Settings) check() (string, int) {
	switch {
	case s.MessagesPerPage <= 0 || s.MessagesPerPage > maxMessagesPerPage:
		return "form.perpage", maxMessagesPerPage
	case len(s.Signatures) > maxSignatures:
		return "form.signaturecount", maxSignatures
	case len(s.From) > maxFullName:
		return "form.namelong", maxFullName
	}
	for _, sig := range s.Signatures {
		if len(sig.Text) > maxSignature {
			return "form.signaturelong", maxSignature
		}
		if len(sig.Name) > maxSignatureName {
			return "form.signaturenamelong", maxSignatureName
		}
	}
	return "", 0
}

// signatureNamed finds a signature by name; the second result is false
// when nothing carries that name, which is what a stale choice looks
// like after the signature it named was deleted.
func (s *Settings) signatureNamed(name string) (Signature, bool) {
	for _, sig := range s.Signatures {
		if sig.Name == name {
			return sig, true
		}
	}
	return Signature{}, false
}

type SettingsRenderData struct {
	alborz.BaseRenderData
	// AuthServGuess is what this account's server appears to call itself
	// in the verdicts it writes, offered for confirmation. Empty when
	// the mail seen does not agree on one, because a close race is a
	// guess and a guess here is worse than nothing.
	AuthServGuess string
	Mailboxes     []MailboxInfo
	Settings      *Settings
	Subscriptions Subscriptions
	Secondary     string // calendar system shown beside the Gregorian one
	MaxPerPage    int
	// Servers is where this account's mail actually lives, and what
	// software is answering. A deployment is not the one the reader is
	// used to, and it is the first thing a bug report has to say.
	Servers ServerInfo
	Error   string
}

// ServerInfo is what alborz can say about the account's upstreams
// without asking for anything it does not already have a connection to.
type ServerInfo struct {
	IMAP  string
	SMTP  string
	Sieve string
	// Agent is what the IMAP server calls itself (RFC 2971 ID). Empty
	// where the server does not advertise the extension, which many do
	// not; it is the server's own claim, not alborz's.
	Agent string
	// Abilities are the capabilities that change what alborz does, so
	// the page explains its own behaviour rather than listing a
	// protocol.
	Abilities []Ability
	// Explained is the same list as prose, for the disclosure that
	// works where a tooltip does not.
	Explained []alborz.Explained
}

// Ability is one capability named for what it means to the reader, with
// a line saying what it changes: the name alone is protocol jargon.
type Ability struct {
	Label string // translation key
	Hint  string // translation key
	Have  bool
}

// abilities reports the capabilities that decide how alborz behaves.
// The raw CAPABILITY line is not shown: a reader wants to know whether
// sorting happens on the server, not that SORT=DISPLAY exists.
func abilities(c *imapclient.Client) []Ability {
	caps := c.Caps()
	return []Ability{
		{"settings.abilitysort", "settings.abilitysorthint", caps.Has(imap.CapSort)},
		{"settings.abilitythread", "settings.abilitythreadhint", caps.Has(imap.Cap("THREAD=REFERENCES"))},
		{"settings.abilitysettings", "settings.abilitysettingshint", caps.Has(imap.CapMetadata)},
		{"settings.abilityquota", "settings.abilityquotahint", caps.Has(imap.CapQuota)},
		{"settings.abilitypush", "settings.abilitypushhint", caps.Has(imap.CapIdle)},
		{"settings.abilityid", "settings.abilityidhint", caps.Has(imap.CapID)},
	}
}

// BrowserSettingsRenderData carries the choices stored in the browser
// rather than in any account.
type BrowserSettingsRenderData struct {
	alborz.BaseRenderData
	Language      string // explicit per-user choice, "" follows the browser
	Theme         string
	ColorScheme   string
	AccountColors bool
	AlignByScript bool
	TextSize      string
}

type Subscriptions []string

func (s Subscriptions) Has(sub string) bool {
	for _, cand := range s {
		if cand == sub {
			return true
		}
	}
	return false
}

// serverAgent asks the IMAP server what it is (RFC 2971). A server
// without the extension answers nothing, which is not an error: the
// page simply has one fewer thing to say. The exchange is one round trip
// on a connection already open.
func serverAgent(c *imapclient.Client) string {
	if !c.Caps().Has(imap.CapID) {
		return ""
	}
	data, err := c.ID(&imap.IDData{Name: alborz.BrandName}).Wait()
	if err != nil || data == nil || data.Name == "" {
		return ""
	}
	if data.Version == "" {
		return data.Name
	}
	return data.Name + " " + data.Version
}

// serverInfo names where the account's mail lives. The hosts come from
// the configuration or from SRV discovery; nothing is guessed.
func serverInfo(ctx *alborz.Context, agent string, abilities []Ability) ServerInfo {
	_, domain, _ := strings.Cut(ctx.Session.Username(), "@")
	up := ctx.Server.UpstreamsFor(domain)
	explained := make([]alborz.Explained, 0, len(abilities))
	for _, a := range abilities {
		explained = append(explained, alborz.Explained{
			Term: ctx.T(a.Label), Hint: ctx.T(a.Hint)})
	}
	return ServerInfo{IMAP: up.IMAP, SMTP: up.SMTP, Sieve: up.Sieve,
		Agent: agent, Abilities: abilities, Explained: explained}
}

// SignaturesRenderData is the signature page: the list, which one a new
// message starts with, and the one being written if any.
type SignaturesRenderData struct {
	IMAPBaseRenderData
	Settings *Settings
	// Editing is the signature the form holds. Empty Name means the form
	// is adding rather than changing one.
	Editing Signature
	// Was is the name the form started with, so a rename replaces rather
	// than duplicates.
	Was   string
	Error string
}

// handleSignatures keeps signatures out of the settings pane: they are
// prose a person writes, not a preference to be set. The page lists what
// exists and writes one at a time, because deleting is an action rather
// than a box to tick and then save.
func handleSignatures(ctx *alborz.Context) error {
	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return fmt.Errorf("failed to load settings: %v", err)
	}
	ibase, err := newIMAPBaseRenderData(ctx, alborz.NewBaseRenderData(ctx))
	if err != nil {
		return err
	}

	editing, was := Signature{}, ""
	if name := ctx.QueryParam("edit"); name != "" {
		if found, ok := settings.signatureNamed(name); ok {
			editing, was = found, found.Name
		}
	}

	render := func(status int, message string) error {
		ibase.BaseRenderData.WithTitle(ctx.T("settings.signatures"))
		return ctx.Render(status, "signatures.html", &SignaturesRenderData{
			IMAPBaseRenderData: *ibase,
			Settings:           settings,
			Editing:            editing,
			Was:                was,
			Error:              message,
		})
	}

	if ctx.Request().Method != http.MethodPost {
		return render(http.StatusOK, "")
	}

	name := strings.TrimSpace(ctx.FormValue("name"))
	text := strings.TrimRight(ctx.FormValue("text"), "\r\n")
	was = ctx.FormValue("was")
	editing = Signature{Name: name, Text: text}
	switch {
	case name == "":
		return render(http.StatusUnprocessableEntity, ctx.T("form.signaturename"))
	case text == "":
		return render(http.StatusUnprocessableEntity, ctx.T("form.signaturetext"))
	case len(settings.Signatures) >= maxSignatures && was == "":
		return render(http.StatusUnprocessableEntity,
			fmt.Sprintf(ctx.T("form.signaturecount"), maxSignatures))
	}

	// A rename keeps the entry's place in the list, and keeps being the
	// default if it was one.
	replaced := false
	for i := range settings.Signatures {
		if settings.Signatures[i].Name != was || was == "" {
			continue
		}
		if settings.DefaultSignature == was {
			settings.DefaultSignature = name
		}
		settings.Signatures[i] = editing
		replaced = true
	}
	if !replaced {
		if _, taken := settings.signatureNamed(name); taken {
			return render(http.StatusUnprocessableEntity, ctx.T("form.signaturetaken"))
		}
		settings.Signatures = append(settings.Signatures, editing)
	}
	if key, limit := settings.check(); key != "" {
		return render(http.StatusUnprocessableEntity, fmt.Sprintf(ctx.T(key), limit))
	}
	if err := ctx.Session.Store().Put(settingsKey, settings); err != nil {
		return fmt.Errorf("failed to save settings: %v", err)
	}
	return ctx.Redirect(http.StatusFound, ctx.AccountPath("/signatures"))
}

// handleSignatureDelete removes one. It is its own route because a
// deletion is an action taken on a thing, not a field of a form that
// saves everything at once.
func handleSignatureDelete(ctx *alborz.Context) error {
	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return err
	}
	name := ctx.FormValue("name")
	kept := settings.Signatures[:0]
	for _, sig := range settings.Signatures {
		if sig.Name != name {
			kept = append(kept, sig)
		}
	}
	settings.Signatures = kept
	if settings.DefaultSignature == name {
		settings.DefaultSignature = ""
	}
	if err := ctx.Session.Store().Put(settingsKey, settings); err != nil {
		return fmt.Errorf("failed to save settings: %v", err)
	}
	return ctx.Redirect(http.StatusFound, ctx.AccountPath("/signatures"))
}

// handleSignatureDefault sets which signature a new message starts with.
// It is a preference and saves on its own, so choosing one never depends
// on what the form below happens to hold.
func handleSignatureDefault(ctx *alborz.Context) error {
	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return err
	}
	chosen := ctx.FormValue("signature_default")
	if _, ok := settings.signatureNamed(chosen); !ok {
		chosen = ""
	}
	settings.DefaultSignature = chosen
	if err := ctx.Session.Store().Put(settingsKey, settings); err != nil {
		return fmt.Errorf("failed to save settings: %v", err)
	}
	return ctx.Redirect(http.StatusFound, ctx.AccountPath("/signatures"))
}

func handleSettings(ctx *alborz.Context) error {
	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return fmt.Errorf("failed to load settings: %v", err)
	}

	var mailboxes []MailboxInfo
	var agent string
	var abilityList []Ability
	err = ctx.DoIMAP(func(c *imapclient.Client) error {
		mailboxes, err = listMailboxes(c)
		if err != nil {
			return err
		}
		agent = serverAgent(c)
		abilityList = abilities(c)
		return nil
	})
	if err != nil {
		return err
	}
	servers := serverInfo(ctx, agent, abilityList)

	// The form answers its own invalid input, on the page it was typed
	// on. Digits are read as they were typed: a page that counts in
	// Persian digits invites them back in its number fields.
	reject := func(message string) error {
		return ctx.Render(http.StatusUnprocessableEntity, "settings.html", &SettingsRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("nav.settings")),
			Settings:       settings,
			Mailboxes:      mailboxes,
			Subscriptions:  Subscriptions(settings.Subscriptions),
			Secondary:      ctx.SecondaryCalendar(),
			MaxPerPage:     maxMessagesPerPage,
			Error:          message,
		})
	}

	if ctx.Request().Method == http.MethodPost {
		settings.MessagesPerPage, err = strconv.Atoi(alborz.LatinDigits(ctx.FormValue("messages_per_page")))
		if err != nil {
			return reject(fmt.Sprintf(ctx.T("form.perpage"), maxMessagesPerPage))
		}
		settings.From = ctx.FormValue("from")
		settings.TrustedAuthServ = strings.TrimSpace(ctx.FormValue("trusted_authserv"))
		settings.Identities = parseIdentities(ctx.FormValue("identities"))
		settings.Timezone = ctx.FormValue("timezone")
		settings.SearchHeadersOnly = ctx.FormValue("search_body") != "on"
		settings.ReplyBelowQuote = ctx.FormValue("reply_position") == "below"
		settings.PreferHTML = ctx.FormValue("prefer_html") != ""
		settings.SendHTML = ctx.FormValue("send_html") != ""
		if fdow := ctx.FormValue("first_day_of_week"); fdow != "" {
			settings.FirstDayOfWeek, err = strconv.Atoi(alborz.LatinDigits(fdow))
			if err != nil || settings.FirstDayOfWeek < 0 || settings.FirstDayOfWeek > 6 {
				return reject(ctx.T("form.firstday"))
			}
		}

		params, err := ctx.FormParams()
		if err != nil {
			return err
		}
		settings.Subscriptions = params["subscriptions"]

		if key, limit := settings.check(); key != "" {
			return reject(fmt.Sprintf(ctx.T(key), limit))
		}
		if err := ctx.Session.Store().Put(settingsKey, settings); err != nil {
			return fmt.Errorf("failed to save settings: %v", err)
		}
		if err := ctx.SetSecondaryCalendar(ctx.FormValue("secondary")); err != nil {
			return fmt.Errorf("failed to save calendar choice: %v", err)
		}

		listings.evictAll(ctx.Session.Username())
		return ctx.Redirect(http.StatusFound, ctx.AccountPath("/settings"))
	}

	// Only when nothing is set: with a value in hand there is nothing to
	// suggest, and the sample costs a fetch.
	guess := ""
	if settings.TrustedAuthServ == "" {
		guess = SuggestAuthServ(ctx)
	}
	return ctx.Render(http.StatusOK, "settings.html", &SettingsRenderData{
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("nav.settings")),
		AuthServGuess:  guess,
		Settings:       settings,
		Mailboxes:      mailboxes,
		Subscriptions:  Subscriptions(settings.Subscriptions),
		Secondary:      ctx.SecondaryCalendar(),
		MaxPerPage:     maxMessagesPerPage,
		Servers:        servers,
	})
}

// handleLanguage sets the interface language and returns to the page it
// was chosen from. It is its own route because the choice takes effect
// at once: a language behind a Save button is a language you have to
// read in the wrong one to change.
func handleLanguage(ctx *alborz.Context) error {
	ctx.SetLanguage(ctx.FormValue("language"))
	return ctx.Redirect(http.StatusFound, ctx.NextOr("/"))
}

// handleBrowserSettings serves the choices that live in the browser
// rather than in an account's store. It touches no account, so a server
// that is down cannot hold the language or the theme hostage.
func handleBrowserSettings(ctx *alborz.Context) error {
	if ctx.Request().Method == http.MethodPost {
		ctx.SetColorScheme(ctx.FormValue("color_scheme"))
		ctx.SetTheme(ctx.FormValue("theme"))
		ctx.SetLanguage(ctx.FormValue("language"))
		ctx.SetAccountColors(ctx.FormValue("account_colors") != "")
		ctx.SetAlignByScript(ctx.FormValue("align_script") != "")
		ctx.SetTextSize(ctx.FormValue("text_size"))
		return ctx.Redirect(http.StatusFound, "/settings/browser")
	}

	return ctx.Render(http.StatusOK, "settings-browser.html", &BrowserSettingsRenderData{
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("settings.forbrowser")),
		Language:       ctx.Language(),
		Theme:          ctx.Theme(),
		ColorScheme:    ctx.ColorScheme(),
		AccountColors:  ctx.AccountColors(),
		AlignByScript:  ctx.AlignByScript(),
		TextSize:       ctx.TextSize(),
	})
}
