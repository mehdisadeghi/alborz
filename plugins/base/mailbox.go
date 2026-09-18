package alborzbase

import (
	"errors"
	"fmt"
	"html/template"
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
	"github.com/labstack/echo/v4"
)

type MailboxRenderData struct {
	IMAPBaseRenderData
	Messages                  []IMAPMessage
	PrevPage, NextPage        int
	RangeFrom, RangeTo, Total int
	Query                     string
	// TextQuery is the query widened to the whole message, offered
	// when the search reached headers only; empty otherwise.
	TextQuery string
	// Merged says the rows are not one folder's: each is named by its
	// account and folder, and the list's actions go by Role, every row's
	// account resolving its own folder. Spanning says they are not one
	// role's either - a search across folders - so each row names the
	// folder it is in.
	Merged, Spanning bool
	Role             string
	// FolderQuery is the query less its in:, for a row's folder to
	// narrow the results to itself.
	FolderQuery string
	// FolderTrack is the width the rows' folder names share, so that a
	// column of them has one edge whatever each folder is called.
	FolderTrack template.CSS
	// Outgoing says the folder holds what the reader wrote, so the
	// rows name whom it went to rather than who wrote it.
	Outgoing      bool
	Sort          string
	SortDir       string
	SortSupported bool
	// ThreadSupported says the server can group a folder into
	// conversations, which is what offers the view at all.
	ThreadSupported bool
	// Crumb is the path to this folder, account first.
	Crumb []CrumbLink
	// PreferHTML is the reader's choice of which part a row opens.
	PreferHTML bool
	// Threaded says this listing is one, so the rows carry a depth and
	// the pager counts conversations rather than messages.
	Threaded bool
	// Filters are the narrowings in force that the page does not
	// otherwise state: the crumb already names the folder, and the
	// rail names the account.
	Filters []alborz.Filter
	// FilterRows are the views the filter menu offers.
	FilterRows []alborz.FilterRow
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

// unifiedRoles are the folder roles the merged all-accounts view offers;
// each resolves per account through its special-use attributes.
var unifiedRoles = []string{"INBOX", "Drafts", "Sent", "Junk", "Trash", "Archive"}

func handleUnifiedMailbox(ctx *alborz.Context) error {
	role, err := mailboxRef(ctx)
	if err != nil {
		return err
	}
	if !slices.Contains(unifiedRoles, role) {
		return echo.NewHTTPError(http.StatusNotFound,
			fmt.Sprintf("%q is not a unified folder", role))
	}

	// "account" exists only on a merge: it is a property of the merge,
	// not of any single server's order.
	ask, err := readListAsk(ctx, "account")
	if err != nil {
		return err
	}
	if to := searchInstead(ctx, ask); to != "" {
		return ctx.Redirect(http.StatusFound, to)
	}
	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return err
	}
	spec := ask.spec

	// The first page comes from the listing cache when it can, the
	// slowest account no longer gating every click.
	window := ask.window()
	// A search costs one live IMAP round trip per account, so it is the
	// view that most wants the cache; only its key is longer.
	cacheable := ctx.Request().Method == http.MethodGet
	key := listingPage(listingView("#"+role, spec.query, spec.view, spec.sortKey, spec.sortDir), ask.page, ask.perPage)
	class, bound := listingBudget(spec)
	merged, err := gather(ctx, ctx.Sessions(), spec, func(s *alborz.Session) (*listingEntry, error) {
		user := s.Username()
		folder := func(c *imapclient.Client) (string, error) { return resolveRole(c, user, role) }
		fetch := func(c *imapclient.Client) (*listingEntry, error) {
			name, err := folder(c)
			if err != nil || name == "" {
				return nil, err
			}
			return fetchUnifiedAccount(c, user, name, spec, settings, window, cacheable)
		}
		if cacheable {
			return cachedListing(ctx, s, key, window, spec, folder, fetch)
		}
		alborz.CacheTiming(ctx.Request().Context(), "listing", false)
		return readOn(s, class, bound, fetch)(ctx.Request().Context())
	})
	if err != nil {
		return err
	}
	// Junk is one colour already; a tint there says nothing. The
	// message page keeps its warning card.
	if role != "Junk" {
		RowMarks(ctx, TrustedAuthServ(ctx, settings), merged.msgs)
	}
	title := ctx.T("aside." + strings.ToLower(role))
	if spec.view != "" {
		title = viewTitle(ctx, spec.view)
	}

	data := listPage(ctx, ask, merged, cutPage(merged.msgs, ask))
	data.IMAPBaseRenderData = IMAPBaseRenderData{
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(fmt.Sprintf(ctx.T("mailbox.allaccounts"), title)),
		// The role is the name: the rail marks the row by it and the
		// refresh form posts to it. The label is what is read.
		Mailbox:         &MailboxStatus{StatusData: &imap.StatusData{Mailbox: role}, Label: title},
		ListView:        spec.view,
		SidebarAccounts: sidebarAccounts(ctx),
	}
	data.Crumb = []CrumbLink{{Label: ctx.T("aside." + strings.ToLower(role)), URL: "/mailbox/" + role}}
	data.Outgoing = role == "Sent" || role == "Drafts"
	data.Merged, data.Role = true, role
	return ctx.Render(http.StatusOK, listTemplate(ctx), data)
}

// window is how many rows each source of a merge gives: its own
// newest, as many as the pages up to the one asked for hold, since
// any of them may be the merge's.
func (ask listAsk) window() int {
	return (ask.page + 1) * ask.perPage
}

// cutPage is the page asked for, out of the merged windows.
func cutPage(msgs []IMAPMessage, ask listAsk) []IMAPMessage {
	from := min(ask.page*ask.perPage, len(msgs))
	return msgs[from:min(from+ask.perPage, len(msgs))]
}

// listPage is what a list says of the page it shows: the rows, where
// they stand in the whole, the pager, and the ask as the toolbar echoes
// it. e is what was read: the page itself, or the merge it was cut from.
func listPage(ctx *alborz.Context, ask listAsk, e *listingEntry, rows []IMAPMessage) *MailboxRenderData {
	data := &MailboxRenderData{
		Messages:       rows,
		PrevPage:       -1,
		NextPage:       -1,
		Total:          e.total,
		Query:          ask.spec.query,
		Filters:        searchFilters(ctx),
		FilterRows:     ViewRows(ctx, ask.spec.view, true, ctx.WithoutParam("view")),
		TextQuery:      textQueryOffered(ask.spec.query, e.headersOnly),
		PerPage:        ask.perPage,
		PerPageOptions: perPageOptions(ctx.Reading().MessagesPerPage),
		Sort:           ask.spec.sortKey,
		SortDir:        map[bool]string{true: "desc", false: "asc"}[ask.spec.reverse()],
		SortSupported:  e.sortSupported,
		PreferHTML:     ctx.Reading().PreferHTML,
	}
	if len(rows) > 0 {
		data.RangeFrom = ask.page*ask.perPage + 1
		data.RangeTo = ask.page*ask.perPage + len(rows)
	}
	if ask.page > 0 {
		data.PrevPage = ask.page - 1
	}
	if ask.window() < e.total {
		data.NextPage = ask.page + 1
	}
	return data
}

// gather asks every account at once and merges what they answer under
// the order asked for. A server that did not answer costs its account's
// rows, not the page: the others are shown and the page says who is
// missing. Only when nobody answered is it the upstream page. An
// account with nothing to say answers nil.
func gather(ctx *alborz.Context, sessions []*alborz.Session, spec listingSpec, read func(*alborz.Session) (*listingEntry, error)) (*listingEntry, error) {
	var wg sync.WaitGroup
	// One span across the whole fan-out: the accounts are queried
	// concurrently, so its wall-clock is the page's IMAP time.
	imapStart := time.Now()
	errs := make([]error, len(sessions))
	asked := func(i int, s *alborz.Session) *listingEntry {
		e, err := read(s)
		if errs[i] = err; err != nil || e == nil {
			return nil
		}
		Relate(s, e.msgs)
		return e
	}
	entries := make([]*listingEntry, len(sessions))
	for i, s := range sessions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entries[i] = asked(i, s)
		}()
	}
	wg.Wait()
	alborz.AddTiming(ctx.Request().Context(), "imap", imapStart)
	// Absorbed in the accounts' order, not the order they answered in:
	// the sort below is stable, so rows of equal rank would otherwise
	// change places from one request to the next, and a page cut out of
	// them would repeat a row or lose one.
	merged := &listingEntry{sortSupported: true}
	for _, e := range entries {
		if e != nil {
			merged.absorb(e)
		}
	}
	var down []string
	for i, err := range errs {
		var upstream alborz.UpstreamError
		switch {
		case err == nil:
		case errors.As(err, &upstream):
			down = append(down, sessions[i].Username())
		default:
			return nil, err
		}
	}
	if len(down) == len(errs) {
		return nil, errs[0]
	}
	ctx.Unreachable(down)
	slices.SortStableFunc(merged.msgs, unifiedLess(spec.sortKey, spec.reverse()))
	return merged, nil
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
			return strings.Compare(strings.ToLower(envelopeName(a.Envelope.From)), strings.ToLower(envelopeName(b.Envelope.From)))
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

// fetchUnifiedAccount reads one account's newest window of a merged
// view. withSnap takes the folder's STATUS along, for an entry meant to
// be cached; the answer doubles as what a later check compares against.
func fetchUnifiedAccount(c *imapclient.Client, user, folder string, spec listingSpec, settings *Settings, window int, withSnap bool) (*listingEntry, error) {
	var snapCmd *imapclient.StatusCommand
	if withSnap {
		snapCmd = c.Status(folder, listingStatusOptions(c))
	}
	e, err := fetchRows(c, folder, spec, settings, 0, window)
	if err != nil {
		return nil, err
	}
	for j := range e.msgs {
		e.msgs[j].Account = user
	}
	if snapCmd != nil {
		if st, err := snapCmd.Wait(); err == nil {
			e.snap = st
		}
	}
	return e, nil
}

func handleGetMailbox(ctx *alborz.Context) error {
	// Reading mail is what precedes writing it, so this is where the
	// recipient suggestions are put on to warm. Nothing here waits for
	// them and nothing on this page shows them.
	gatherCorrespondents(ctx.Session)

	if ctx.Unified {
		return handleUnifiedMailbox(ctx)
	}

	mboxName, err := mailboxRef(ctx)
	if err != nil {
		return err
	}

	// thread is not a column to order by, so it is not in sortKeys, but
	// it answers the same question and travels in the same parameter.
	ask, err := readListAsk(ctx, threadSort)
	if err != nil {
		return err
	}
	if to := searchInstead(ctx, ask); to != "" {
		return ctx.Redirect(http.StatusFound, to)
	}
	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return err
	}
	ask.spec.mbox = mboxName

	// Cached pages serve immediately while stale entries renew behind them.
	cacheable := ctx.Request().Method == http.MethodGet
	// thread names one conversation by any message in it. The server
	// groups by message id, so this asks nothing of its header search.
	if raw := ctx.QueryParam("thread"); raw != "" {
		n, perr := strconv.ParseUint(raw, 10, 32)
		if perr != nil || n == 0 {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid thread")
		}
		ask.spec.thread = imap.UID(n)
	}
	page, messagesPerPage, spec := ask.page, ask.perPage, ask.spec

	key := listingView(mboxName, spec.query, spec.view, spec.sortKey, spec.sortDir)
	if spec.thread != 0 {
		key = fmt.Sprintf("%s%sthread=%d", key, listingSep, spec.thread)
	}
	key = listingPage(key, page, messagesPerPage)
	fetch := func(c *imapclient.Client) (*listingEntry, error) {
		return fetchListing(c, ctx.Session.Username(), spec, settings, page, messagesPerPage)
	}
	var e *listingEntry
	if cacheable {
		e, err = cachedListing(ctx, ctx.Session, key, messagesPerPage, spec,
			func(*imapclient.Client) (string, error) { return mboxName, nil }, fetch)
	} else {
		e, err = loadListing(ctx, ctx.Session, key, messagesPerPage, spec, fetch)
	}
	if err != nil {
		return err
	}
	sb, msgs := railFor(ctx.Session, mboxName, e.sb), e.msgs
	Relate(ctx.Session, msgs)
	if folderRole(sb.mailboxes, mboxName) != "junk" {
		RowMarks(ctx, TrustedAuthServ(ctx, settings), msgs)
	}
	// The page's bodies are fetched behind it, so the next click, on
	// any of its rows, asks the server nothing.
	if cacheable {
		session := ctx.Session
		go prefetchBodies(session, ctx.Reading().PreferHTML, mboxName, e.validity(), e.msgs)
		if spec.query == "" && spec.sortKey != threadSort && spec.thread == 0 && ask.window() < e.total {
			prefetchPage(session, settings, spec, page+1, messagesPerPage)
		}
	}

	// A row shows the address it reached only where that is worth
	// believing; the headers saying so are part of the message.
	trust := newDeliveryTrust(ctx, settings, ctx.Session.Username())
	for i := range msgs {
		msgs[i].Alias = trust.alias(&msgs[i])
	}

	ibase := assembleIMAPBase(ctx, alborz.NewBaseRenderData(ctx), mboxName, sb, spec.view)
	ibase.SidebarAccounts = sidebarAccounts(ctx)
	title := viewTitle(ctx, spec.view)
	if spec.view == "" && ibase.Mailbox != nil {
		title = ibase.Mailbox.Label
	}
	// The heading names the view, as the merged page's does; the
	// label is set afresh on every render, so nothing lingers.
	if spec.view != "" && ibase.Mailbox != nil {
		ibase.Mailbox.Label = title
	}
	ibase.BaseRenderData.WithTitle(title)

	data := listPage(ctx, ask, e, msgs)
	data.IMAPBaseRenderData = *ibase
	data.Crumb = mailboxCrumb(sb.mailboxes, mboxName, ctx.Session.Username())
	data.Outgoing = outgoingFolder(sb.mailboxes, mboxName)
	data.ThreadSupported = e.threadAlgorithm != ""
	data.Threaded = spec.sortKey == threadSort && e.threadAlgorithm != ""
	return ctx.Render(http.StatusOK, listTemplate(ctx), data)
}

// listAsk is what a list page is asked for: which page of how many
// rows, of what, in which order.
type listAsk struct {
	page, perPage int
	spec          listingSpec
}

// readListAsk reads it from the URL. also is the one order the page has
// beyond the columns.
func readListAsk(ctx *alborz.Context, also string) (listAsk, error) {
	ask := listAsk{perPage: perPage(ctx)}
	if raw := ctx.QueryParam("page"); raw != "" {
		var err error
		if ask.page, err = strconv.Atoi(raw); err != nil || ask.page < 0 {
			return ask, echo.NewHTTPError(http.StatusBadRequest, "invalid page index")
		}
	}
	view, err := readView(ctx)
	if err != nil {
		return ask, err
	}
	sortKey, sortDir, err := readSort(ctx, also)
	if err != nil {
		return ask, err
	}
	ask.spec = listingSpec{query: ctx.QueryParam("query"), view: view, sortKey: sortKey, sortDir: sortDir}
	return ask, nil
}

// searchInstead is where a folder's page sends an ask that is not one
// folder's to answer, empty when it is.
func searchInstead(ctx *alborz.Context, ask listAsk) string {
	// in: names where to look, and this page looks in one place.
	if folder, everywhere := ParseQuery(ask.spec.query).Scope(); folder != "" || everywhere {
		return ctx.AccountPath("/search?query=" + url.QueryEscape(ask.spec.query))
	}
	return ""
}

// readSort is the order a list is asked for: the column, and the
// direction as written. also is the one order the page has beyond the
// columns.
func readSort(ctx *alborz.Context, also string) (sortKey, sortDir string, err error) {
	sortKey = ctx.QueryParam("sort")
	if _, ok := sortKeys[sortKey]; !ok && sortKey != also {
		return "", "", echo.NewHTTPError(http.StatusBadRequest, "invalid sort order")
	}
	sortDir = ctx.QueryParam("dir")
	if sortDir != "" && sortDir != "asc" && sortDir != "desc" {
		return "", "", echo.NewHTTPError(http.StatusBadRequest, "invalid sort direction")
	}
	return sortKey, sortDir, nil
}

// readView is the view the URL asks for, refused when it names none
// alborz has: a view is a link in the rail, never typed.
func readView(ctx *alborz.Context) (string, error) {
	view := ctx.QueryParam("view")
	if !KnownView(view) {
		return "", echo.NewHTTPError(http.StatusBadRequest, "unknown view")
	}
	return view, nil
}

// viewTitle names a view the way the rail does.
func viewTitle(ctx *alborz.Context, view string) string {
	switch view {
	case ViewStarred:
		return ctx.T("mailbox.starred")
	case ViewUnread:
		return ctx.T("mailbox.unread")
	}
	return fmt.Sprintf(ctx.T("mailbox.starcolor"), ctx.T("color."+view))
}

// fetchListing reads one view of a folder, the sidebar's counts riding
// along on the same round trips.
func fetchListing(c *imapclient.Client, user string, spec listingSpec, settings *Settings, page, perPage int) (*listingEntry, error) {
	load, err := startSidebar(c, spec.mbox, spec.mbox, settings.Subscriptions)
	if err != nil {
		return nil, err
	}
	e, err := fetchRows(c, spec.mbox, spec, settings, page, perPage)
	if err != nil {
		return nil, err
	}
	if e.sb, err = load.finish(); err != nil {
		return nil, err
	}
	e.snap = e.sb.active.StatusData
	return e, nil
}

// fetchRows reads one page of one folder under the spec: a
// conversation, a search, a sort, or the plain list.
func fetchRows(c *imapclient.Client, folder string, spec listingSpec, settings *Settings, page, perPage int) (*listingEntry, error) {
	e := &listingEntry{perPage: perPage, page: page, sortSupported: c.Caps().Has(imap.CapSort), threadAlgorithm: ThreadAlgorithm(c)}
	// Each account's window is cut under the requested order, so the
	// merge sees the right candidates: the largest matches, and not the
	// largest of the newest. "account" is the merge's own order.
	sortKey, reverse := spec.sortKey, spec.reverse()
	if sortKey == "account" {
		sortKey, reverse = "", true
	}
	var err error
	switch {
	case spec.thread != 0 && e.threadAlgorithm != "":
		e.msgs, err = oneThread(c, folder, e.threadAlgorithm, spec.thread)
		e.total = len(e.msgs)
	case spec.sortKey == threadSort && e.threadAlgorithm != "":
		// A conversation is the unit here, so the page holds a
		// number of threads rather than a number of messages.
		criteria := &imap.SearchCriteria{}
		if spec.query != "" {
			e.headersOnly = !SearchesIndex(c, settings)
			criteria = ParseQuery(spec.query).Criteria(!e.headersOnly, time.Now())
		} else if spec.view != "" {
			criteria = ViewCriteria(spec.view)
		}
		e.msgs, e.total, err = threadMessages(c, folder, e.threadAlgorithm, criteria, page, perPage)
	case spec.query != "":
		e.headersOnly = !SearchesIndex(c, settings)
		e.msgs, e.total, err = searchMessages(c, folder, ParseQuery(spec.query).Criteria(!e.headersOnly, time.Now()), page, perPage, sortKey, reverse)
	case spec.view != "":
		e.msgs, e.total, err = searchMessages(c, folder, ViewCriteria(spec.view), page, perPage, sortKey, reverse)
	case spec.sortKey != "account" && (spec.sortKey != "" || spec.sortDir != "") && e.sortSupported:
		e.msgs, e.total, err = searchMessages(c, folder, &imap.SearchCriteria{}, page, perPage, sortKey, reverse)
	default:
		e.msgs, e.total, err = listMessages(c, folder, page, perPage)
	}
	if err != nil {
		return nil, err
	}
	return e, nil
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
		ib := assembleIMAPBase(ctx, &alborz.BaseRenderData{}, "", sb.clone(), "")
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
	name := ""
	render := func(status int, errText string) error {
		return ctx.Render(status, "new-mailbox.html", &NewMailboxRenderData{
			IMAPBaseRenderData: *ibase,
			Error:              errText,
			Name:               name,
			SelectedAccount:    selectedAccount,
			SelectedLocation:   selectedLocation,
			LocationGroups:     locationGroups,
		})
	}

	if ctx.Request().Method == http.MethodPost {
		name = ctx.FormValue("name")
		selectedLocation = ctx.FormValue("location")
		location, account := locationByKey(locationGroups, selectedLocation)
		if location == nil {
			return render(http.StatusUnprocessableEntity, ctx.T("form.destinationneeded"))
		}
		selectedAccount = account
		if name == "" {
			return render(http.StatusUnprocessableEntity, ctx.T("form.nameneeded"))
		}

		selectedSession := ctx.SessionFor(selectedAccount)
		if selectedSession == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "not signed in to that account")
		}
		fullName := location.folder(name)
		err := selectedSession.DoIMAP(func(c *imapclient.Client) error {
			return c.Create(fullName, nil).Wait()
		})

		if err != nil {
			return render(http.StatusUnprocessableEntity, err.Error())
		}

		accountChanged(selectedAccount)
		// The folder is its own list, so the reader lands in it; the
		// banner is the one every other create shows.
		ctx.Made(ctx.T("notice.foldercreated"), name, "", ctx.Request().URL.RequestURI())
		return ctx.Redirect(http.StatusFound, folderURL(ctx, selectedAccount, fullName))
	}

	return render(http.StatusOK, "")
}

// locationByKey is the location a form named, and the account it is in.
func locationByKey(groups []NewMailboxLocationGroup, key string) (*NewMailboxLocation, string) {
	for i := range groups {
		for j := range groups[i].Locations {
			if groups[i].Locations[j].Key == key {
				return &groups[i].Locations[j], groups[i].Account
			}
		}
	}
	return nil, ""
}

// folder is the full name of a folder called name under this location.
func (l *NewMailboxLocation) folder(name string) string {
	if l.Parent == "" {
		return name
	}
	return l.Parent + string(l.Delimiter) + name
}

func folderURL(ctx *alborz.Context, account, name string) string {
	destination := fmt.Sprintf("/mailbox/%s", url.PathEscape(name))
	if len(ctx.Sessions()) > 1 {
		destination += "?account=" + alborz.AddressParam(account)
	}
	return destination
}

type DeleteMailboxRenderData struct {
	IMAPBaseRenderData
	Error string
}

type EmptyMailboxRenderData struct {
	IMAPBaseRenderData
	Count int
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
			if err := c.Delete(mbox.Name()).Wait(); err != nil {
				return err
			}
			mailboxDeleted(ctx.Session.Username(), mbox.Name())
			return nil
		}); err != nil {
			return ctx.Render(http.StatusUnprocessableEntity, "delete-mailbox.html", &DeleteMailboxRenderData{
				IMAPBaseRenderData: *ibase,
				Error:              err.Error(),
			})
		}
		ctx.PutNotice(ctx.T("notice.mailboxdeleted"))
		return ctx.Redirect(http.StatusFound, ctx.AccountPath("/mailbox/INBOX"))
	}

	return ctx.Render(http.StatusOK, "delete-mailbox.html", &DeleteMailboxRenderData{
		IMAPBaseRenderData: *ibase,
	})
}

// handleRefreshMailbox drops what is cached for the account and returns
// to the page that asked. The IDLE watcher already evicts on what the
// server announces; this is for a server without IDLE, where a reader
// who knows something arrived has no other way to say so.
func handleRefreshMailbox(ctx *alborz.Context) error {
	mboxName, err := mailboxRef(ctx)
	if err != nil {
		return err
	}
	// The merged view is every account's folder of that role, so it is
	// every account that is asked again.
	if ctx.Unified {
		for _, s := range ctx.Sessions() {
			viewForgotten(s.Username(), "#"+mboxName)
		}
		return ctx.Redirect(http.StatusFound, ctx.NextOr(fmt.Sprintf("/mailbox/%s", url.PathEscape(mboxName))))
	}
	viewForgotten(ctx.Session.Username(), mboxName)
	return ctx.Redirect(http.StatusFound, ctx.NextOr(mailboxURL(ctx, mboxName)))
}

// The explicit query parameter a button carries wins over a form field,
// so the bulk move selector cannot leak into other actions.
func formOrQueryParam(ctx *alborz.Context, k string) string {
	if v := ctx.QueryParam(k); v != "" {
		return v
	}
	return ctx.FormValue(k)
}

// MoveRenderData is the chooser a move without a destination answers
// with: the selection it will act on, and the folders it may go to.
type MoveRenderData struct {
	IMAPBaseRenderData
	UIDs []imap.UID
	Next string
}

func handleMove(ctx *alborz.Context) error {
	mboxName, _, uids, err := folderSelection(ctx)
	if err != nil {
		return err
	}

	target := ctx.NextOr(mailboxURL(ctx, mboxName))
	if len(uids) == 0 {
		return nothingSelected(ctx, target)
	}

	// A menu holds actions, one per row; the destination is a choice
	// the reader states on a page, which this same route answers with.
	to := formOrQueryParam(ctx, "to")
	if to == "" {
		ibase, err := newIMAPBaseRenderData(ctx, alborz.NewBaseRenderData(ctx))
		if err != nil {
			return err
		}
		ibase.BaseRenderData.WithTitle(ctx.T("folder.moveask"))
		return ctx.Render(http.StatusOK, "move.html", &MoveRenderData{
			IMAPBaseRenderData: *ibase,
			UIDs:               uids,
			Next:               ctx.FormValue("next"),
		})
	}

	var landed []rowRef
	act := moveAct(func(*imapclient.Client, rowRef) (string, error) { return to, nil }, &landed)
	return runAct(ctx, folderRefs(ctx.Session.Username(), mboxName, uids), act, func(done int) alborz.Notice {
		// Named the way a person would: the folder by its label, linked,
		// so the moved thing is one click away.
		label := to
		if role := (&MailboxInfo{ListData: &imap.ListData{Mailbox: to}}).role(); role != "" {
			label = ctx.T("aside." + role)
		}
		fields := url.Values{"to": {mboxName}, "next": {target}}
		for _, r := range landed {
			fields.Add("uids", fmt.Sprint(r.uid))
		}
		return movedNotice(ctx, done, label, mailboxURL(ctx, to), len(landed),
			ctx.AccountPath("/message/"+url.PathEscape(to)+"/move"), fields)
	}, landOn(ctx, target))
}

// mailAct is what an action does to one folder's share of a selection:
// announce speaks to the caches before the connection is taken, and do
// runs on it.
type mailAct struct {
	announce func(key rowRef, uids []imap.UID)
	do       func(c *imapclient.Client, key rowRef, uids []imap.UID) error
}

// actFailure is a share an action did not reach; err is nil where the
// account is not signed in.
type actFailure struct {
	account string
	err     error
}

// actOn carries an action out over a selection, each account's folder
// a turn on that account's connection, and says how many messages it
// reached and whose it did not. A folder's selection is a merged one of
// one account and one folder.
func actOn(ctx *alborz.Context, refs []rowRef, act mailAct) (done int, failed []actFailure) {
	for key, uids := range grouped(refs) {
		s := ctx.SessionFor(key.account)
		if s == nil {
			failed = append(failed, actFailure{account: key.account})
			continue
		}
		if act.announce != nil {
			act.announce(key, uids)
		}
		err := s.DoIMAP(func(c *imapclient.Client) error { return act.do(c, key, uids) })
		if err != nil {
			failed = append(failed, actFailure{key.account, err})
			continue
		}
		done += len(uids)
	}
	return done, failed
}

func leaving(key rowRef, uids []imap.UID) {
	messagesLeaving(key.account, key.mailbox, uids)
}

// moveAct moves each share to the folder to names on its connection,
// or back to where an undo's reference says it came from. landed takes
// what the server says it put where (COPYUID, RFC 4315), each naming
// the folder it left, which is what an undo is made of.
func moveAct(to func(*imapclient.Client, rowRef) (string, error), landed *[]rowRef) mailAct {
	return mailAct{
		announce: leaving,
		do: func(c *imapclient.Client, key rowRef, uids []imap.UID) error {
			dest := key.origin
			if dest == "" {
				var err error
				if dest, err = to(c, key); err != nil {
					return err
				}
			}
			moved, err := moveMessages(c, key.account, key.mailbox, dest, uids)
			for _, uid := range uidNums(moved) {
				*landed = append(*landed, rowRef{account: key.account, mailbox: dest, uid: uid, origin: key.mailbox})
			}
			return err
		},
	}
}

func flagAct(op imap.StoreFlagsOp, flags []imap.Flag) mailAct {
	return mailAct{
		announce: func(key rowRef, uids []imap.UID) { flagWriteStarting(key.account, key.mailbox, uids, op, flags) },
		do: func(c *imapclient.Client, key rowRef, uids []imap.UID) error {
			return writeFlags(c, key.account, key.mailbox, uids, op, flags)
		},
	}
}

func colourAct(add, del []imap.Flag) mailAct {
	return mailAct{do: func(c *imapclient.Client, key rowRef, uids []imap.UID) error {
		return writeColour(c, key.account, key.mailbox, uids, add, del)
	}}
}

// moveMessages moves a selection of one folder and tells the caches on
// the connection turn that moved it.
func moveMessages(c *imapclient.Client, account, from, to string, uids []imap.UID) (*imapclient.MoveData, error) {
	if err := ensureMailboxSelected(c, from); err != nil {
		return nil, err
	}
	moved, err := c.Move(imap.UIDSetNum(uids...), to).Wait()
	if err != nil {
		return nil, fmt.Errorf("failed to move message: %w", err)
	}
	messagesMoved(account, from, to, uids)
	return moved, nil
}

// writeFlags stores flags on a selection of one folder and tells the
// caches on the connection turn that stored them.
func writeFlags(c *imapclient.Client, account, mailbox string, uids []imap.UID, op imap.StoreFlagsOp, flags []imap.Flag) error {
	if err := storeFlags(c, mailbox, imap.UIDSetNum(uids...), op, flags); err != nil {
		return err
	}
	messagesFlagged(account, mailbox, uids, op, flags)
	return nil
}

// writeColour sets a star's colour. A colour is a flag plus a bit
// field, so it is set and cleared in one exchange rather than by asking
// the page to spell out both.
func writeColour(c *imapclient.Client, account, mailbox string, uids []imap.UID, add, del []imap.Flag) error {
	for _, step := range []struct {
		op    imap.StoreFlagsOp
		flags []imap.Flag
	}{{imap.StoreFlagsAdd, add}, {imap.StoreFlagsDel, del}} {
		if len(step.flags) == 0 {
			continue
		}
		if err := writeFlags(c, account, mailbox, uids, step.op, step.flags); err != nil {
			return fmt.Errorf("failed to set flag colour: %w", err)
		}
	}
	return nil
}

// movedNotice says what moved where, the folder named by its label and
// linked, and offers to move it back where the server named the new
// UIDs (COPYUID, RFC 4315): landed is how many. An undo names those
// UIDs; a message moved again since is not under them any more, so the
// server moves nothing and the undo says so rather than claiming
// success. An undo offers nothing further.
func movedNotice(ctx *alborz.Context, n int, label, href string, landed int, undoPath string, undoFields url.Values) alborz.Notice {
	if ctx.FormValue("undo") != "" {
		if landed == 0 {
			return alborz.Notice{Kind: alborz.NoticeFailed, Text: ctx.T("notice.undofailed")}
		}
		return alborz.Notice{Kind: alborz.NoticeDone, Text: ctx.T("notice.undone")}
	}
	link := "<a href=\"" + template.HTMLEscapeString(href) + "\">" + template.HTMLEscapeString(label) + "</a>"
	notice := alborz.Notice{
		Kind:   alborz.NoticeDone,
		Text:   ctx.Tf("notice.movedto", n, label),
		Markup: template.HTML(ctx.Tf("notice.movedto", n, link)),
	}
	if landed > 0 {
		notice.Action = ctx.Undo(undoPath, undoFields)
	}
	return notice
}

// uidNums lists the UIDs a MOVE reported on the destination side; none
// when the server reported nothing.
func uidNums(moved *imapclient.MoveData) []imap.UID {
	if moved == nil {
		return nil
	}
	uids, ok := moved.DestUIDs.(imap.UIDSet)
	if !ok {
		return nil
	}
	nums, _ := uids.Nums()
	return nums
}

// handleEmptyMailbox expunges everything in a folder, the standard
// one-click cleanup for Junk and Trash.
func handleEmptyMailbox(ctx *alborz.Context) error {
	mboxName, err := mailboxRef(ctx)
	if err != nil {
		return err
	}

	if ctx.Request().Method == http.MethodGet {
		ibase, err := newIMAPBaseRenderData(ctx, alborz.NewBaseRenderData(ctx))
		if err != nil {
			return err
		}
		ibase.BaseRenderData.WithTitle(fmt.Sprintf(ctx.T("folder.emptytitle"), mboxName))
		count := 0
		if ibase.Mailbox.NumMessages != nil {
			count = int(*ibase.Mailbox.NumMessages)
		}
		return ctx.Render(http.StatusOK, "empty-mailbox.html", &EmptyMailboxRenderData{
			IMAPBaseRenderData: *ibase,
			Count:              count,
		})
	}

	var removed int
	err = ctx.DoIMAP(func(c *imapclient.Client) error {
		var err error
		removed, err = emptyMailbox(c, mboxName)
		// Expunge can also remove messages already marked deleted elsewhere.
		messagesRemoved(ctx.Session.Username(), mboxName)
		return err
	})
	if err != nil {
		return err
	}

	ctx.Notify(emptiedNotice(ctx, removed))
	return ctx.Redirect(http.StatusFound, ctx.NextOr(mailboxURL(ctx, mboxName)))
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
	if err := deleteMessages(c, mboxName, seq); err != nil {
		return 0, err
	}
	return n, nil
}

// emptiedNotice reports what emptying actually removed.
func emptiedNotice(ctx *alborz.Context, removed int) alborz.Notice {
	if removed == 0 {
		return alborz.Notice{Kind: alborz.NoticeWarning, Text: ctx.T("notice.alreadyempty")}
	}
	return alborz.Notice{Kind: alborz.NoticeDone, Text: ctx.Tf("notice.emptied", removed)}
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
			messagesRemoved(s.Username(), folder)
			removed += n
			return err
		})
		viewForgotten(s.Username(), "#"+role)
		if err != nil {
			failed = append(failed, s.Username()+": "+err.Error())
		}
	}
	back := ctx.NextOr("/mailbox/" + url.PathEscape(role) + "?all=1")
	if len(failed) > 0 {
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeFailed, Text: fmt.Sprintf(ctx.T("form.saverefused"), strings.Join(failed, "; "))})
		return ctx.Redirect(http.StatusFound, back)
	}

	ctx.Notify(emptiedNotice(ctx, removed))
	return ctx.Redirect(http.StatusFound, back)
}

func handleDelete(ctx *alborz.Context) error {
	mboxName, _, uids, err := folderSelection(ctx)
	if err != nil {
		return err
	}

	back := ctx.NextOr(mailboxURL(ctx, mboxName))
	if len(uids) == 0 {
		return nothingSelected(ctx, back)
	}

	var left int
	return runAct(ctx, folderRefs(ctx.Session.Username(), mboxName, uids), mailAct{
		announce: leaving,
		do: func(c *imapclient.Client, key rowRef, uids []imap.UID) error {
			defer messagesRemoved(key.account, key.mailbox)
			if err := deleteMessages(c, key.mailbox, imap.UIDSetNum(uids...)); err != nil {
				return err
			}
			left = int(c.Mailbox().NumMessages)
			return nil
		},
	}, func(done int) alborz.Notice {
		notice := alborz.Notice{Kind: alborz.NoticeDone, Text: ctx.Tf("notice.deleted", done)}
		// A whole page ticked and more behind it is a reader clearing
		// the folder one page at a time. The rest is offered, behind a
		// page that says how many, and never taken on its own.
		if done >= perPage(ctx) && left > 0 {
			notice.Action = &alborz.NoticeAction{
				Label: ctx.Tf("notice.deleteall", left),
				Path:  ctx.AccountPath("/mailbox/" + url.PathEscape(mboxName) + "/empty"),
			}
		}
		return notice
	}, landOn(ctx, back))
}

// listTemplate is the whole page, or the part of it a narrowing
// changes. Paging, sorting, the rows-per-page menu, a search and the
// rail's rows swap the list and say so; anything else - history, a page
// asked for from outside - gets the page (TODO 126).
func listTemplate(ctx *alborz.Context) string {
	if ctx.PartialFor("main") {
		return "message-list"
	}
	return "mailbox.html"
}

// StarRenderData is one message's star, which is all that changes when
// a mark is set from a list row.
type StarRenderData struct {
	alborz.BaseRenderData
	Mailbox string
	Account template.URL
	UID     imap.UID
	Flagged bool
	Color   string
	Next    string
	Label   string
}

func handleSetFlags(ctx *alborz.Context) error {
	mboxName, formParams, uids, err := folderSelection(ctx)
	if err != nil {
		return err
	}
	if len(uids) == 0 {
		return nothingSelected(ctx, ctx.NextOr(mailboxURL(ctx, mboxName)))
	}

	if color, ok := formParams["color"]; ok {
		add, del := FlagColorFlags(color[0])
		if add == nil && del == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "unknown flag colour")
		}
		// One message's star is one message's star: where the page
		// asked for that block, the whole listing does not have to be
		// built and read again to show it. The new state is what was
		// just stored, so nothing is fetched to find it out.
		back := ctx.NextOr(mailboxURL(ctx, mboxName))
		oneStar := ctx.Partial() && len(uids) == 1
		return runAct(ctx, folderRefs(ctx.Session.Username(), mboxName, uids), colourAct(add, del), func(int) alborz.Notice {
			return alborz.Notice{}
		}, func(done bool) error {
			if !oneStar || !done {
				return ctx.Redirect(http.StatusFound, back)
			}
			label := ctx.T("mailbox.flagcolor")
			if color[0] != "" {
				label = ctx.T("mailbox.flagnone")
			}
			return ctx.Render(http.StatusOK, "row-star", &StarRenderData{
				BaseRenderData: *alborz.NewBaseRenderData(ctx),
				Mailbox:        mboxName,
				Account:        template.URL("?account=" + alborz.AddressParam(ctx.Session.Username())),
				UID:            uids[0],
				Flagged:        color[0] != "",
				Color:          color[0],
				Next:           ctx.FormValue("next"),
				Label:          label,
			})
		})
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

	target := ctx.NextOr("")
	if target == "" {
		if len(uids) != 1 || (op == imap.StoreFlagsDel && len(l) == 1 && l[0] == imap.FlagSeen) {
			// Redirecting to the message view would mark the message as read again
			target = mailboxURL(ctx, mboxName)
		} else {
			target = ctx.AccountPath(fmt.Sprintf("/message/%v/%v", url.PathEscape(mboxName), uids[0]))
		}
	}
	return runAct(ctx, folderRefs(ctx.Session.Username(), mboxName, uids), flagAct(op, l), func(int) alborz.Notice {
		return flaggedNotice(ctx, formParams["uids"], flags, op, mboxName, target)
	}, landOn(ctx, target))
}

// onlySeen reports whether a store touches nothing but the read mark.
func onlySeen(flags []string) bool {
	for _, f := range flags {
		if !strings.EqualFold(f, string(imap.FlagSeen)) {
			return false
		}
	}
	return len(flags) > 0
}

// flaggedNotice says what changed and offers the opposite store for
// an add or a remove, whose inverse is exact. A set replaced flags it
// did not record, so it has no undo; nor has an undo.
func flaggedNotice(ctx *alborz.Context, uids, flags []string, op imap.StoreFlagsOp, mboxName, target string) alborz.Notice {
	if ctx.FormValue("undo") != "" {
		return alborz.Notice{Kind: alborz.NoticeDone, Text: ctx.T("notice.undone")}
	}
	// Read and unread show in the row's own weight, so they are done
	// without a word; a colour is worth an undo, since finding a star
	// set by accident means looking through the folder for it.
	if onlySeen(flags) {
		return alborz.Notice{}
	}
	notice := alborz.Notice{Kind: alborz.NoticeDone, Text: ctx.Tf("notice.flagged", len(uids))}
	inverse := map[imap.StoreFlagsOp]string{imap.StoreFlagsAdd: "remove", imap.StoreFlagsDel: "add"}[op]
	if inverse != "" {
		fields := url.Values{"uids": uids, "flags": flags, "action": {inverse}, "next": {target}}
		notice.Action = ctx.Undo(ctx.AccountPath("/message/"+url.PathEscape(mboxName)+"/flag"), fields)
	}
	return notice
}

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
func perPageOptions(chosen int) []int {
	seen := map[int]bool{}
	var out []int
	for _, n := range append(append([]int{}, perPageLadder...), chosen) {
		if n <= 0 || n > maxMessagesPerPage || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

// perPage is how many messages a page holds: what this request asks
// for, else what the reader reads by, else the default. It is the
// reader's, not the account's, so a merged page counts the same as a
// scoped one.
func perPage(ctx *alborz.Context) int {
	if raw := ctx.QueryParam("ipp"); raw != "" {
		if n, err := alborz.ReadInt(raw); err == nil &&
			n > 0 && n <= maxMessagesPerPage {
			return n
		}
	}
	if n := ctx.Reading().MessagesPerPage; n > 0 && n <= maxMessagesPerPage {
		return n
	}
	return defaultMessagesPerPage
}

// textQueryOffered is the widened query a results page offers, when the
// search reached headers only and there is a bare term to widen.
func textQueryOffered(query string, headersOnly bool) string {
	if !headersOnly {
		return ""
	}
	return ParseQuery(query).Widened()
}

// outgoingFolder says whether the folder holds the reader's own mail.
func outgoingFolder(mailboxes []MailboxInfo, name string) bool {
	role := folderRole(mailboxes, name)
	return role == "sent" || role == "drafts"
}

func folderRole(mailboxes []MailboxInfo, name string) string {
	for i := range mailboxes {
		if mailboxes[i].Name() == name {
			return mailboxes[i].role()
		}
	}
	return ""
}

// A merged view holds rows of several accounts, so a selection names
// each row by account, folder and UID. The actions are the ones a
// folder offers that make sense across accounts: archive, junk, trash
// and the inbox by role, flags, read and unread, and an export. A named
// folder does not, since one account's folders are not another's.
type rowRef struct {
	account, mailbox string
	uid              imap.UID
	// origin is the folder a moved message came from, which only an
	// undo's reference carries: a role names one folder per account,
	// and the rows of a search come from any.
	origin string
}

// String is the form parseRefs reads: what a row's checkbox carries.
func (r rowRef) String() string {
	if r.origin != "" {
		return fmt.Sprintf("%s|%s|%d|%s", r.account, r.mailbox, r.uid, r.origin)
	}
	return fmt.Sprintf("%s|%s|%d", r.account, r.mailbox, r.uid)
}

func parseRefs(values []string) ([]rowRef, error) {
	var refs []rowRef
	for _, v := range values {
		parts := strings.SplitN(v, "|", 4)
		if len(parts) < 3 {
			return nil, fmt.Errorf("not a message reference: %q", v)
		}
		uid, err := parseUid(parts[2])
		if err != nil {
			return nil, err
		}
		ref := rowRef{account: parts[0], mailbox: parts[1], uid: uid}
		if len(parts) == 4 {
			ref.origin = parts[3]
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// grouped is the selection by the connection it is acted on: one
// account, one folder.
func grouped(refs []rowRef) map[rowRef][]imap.UID {
	out := map[rowRef][]imap.UID{}
	for _, r := range refs {
		key := rowRef{account: r.account, mailbox: r.mailbox, origin: r.origin}
		out[key] = append(out[key], r.uid)
	}
	return out
}

func handleUnifiedAct(ctx *alborz.Context) error {
	role, err := url.PathUnescape(ctx.Param("role"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}
	if !slices.Contains(unifiedRoles, role) {
		return echo.NewHTTPError(http.StatusNotFound, fmt.Sprintf("%q is not a unified folder", role))
	}
	params, err := ctx.FormParams()
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}
	refs, err := mergedSelection(ctx, role, params)
	if err != nil {
		return err
	}
	back := ctx.NextOr("/mailbox/" + url.PathEscape(role) + "?all=1")
	if len(refs) == 0 {
		return nothingSelected(ctx, back)
	}
	action := formOrQueryParam(ctx, "action")
	if action == "export" {
		return streamMbox(ctx, refs, "messages", false)
	}

	// What a move put where, so that the undo can name it.
	var landed []rowRef
	to := formOrQueryParam(ctx, "to")
	var act mailAct
	switch action {
	case "move":
		act = moveAct(func(c *imapclient.Client, key rowRef) (string, error) {
			return resolveRole(c, key.account, to)
		}, &landed)
	case "flag":
		add, del := FlagColorFlags(ctx.FormValue("color"))
		if add == nil && del == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "unknown flag colour")
		}
		act = colourAct(add, del)
	case "read":
		act = flagAct(imap.StoreFlagsAdd, []imap.Flag{imap.FlagSeen})
	case "unread":
		act = flagAct(imap.StoreFlagsDel, []imap.Flag{imap.FlagSeen})
	default:
		return echo.NewHTTPError(http.StatusBadRequest, "unknown action")
	}
	each := act.do
	act.do = func(c *imapclient.Client, key rowRef, uids []imap.UID) error {
		defer accountChanged(key.account)
		return each(c, key, uids)
	}
	return runAct(ctx, refs, act, func(done int) alborz.Notice {
		switch action {
		case "move":
			// The folder is the merged one, linked, and the undo moves
			// every account's messages back to the folder each came from.
			fields := url.Values{"next": {back}}
			for _, r := range landed {
				fields.Add("refs", r.String())
			}
			return movedNotice(ctx, done, ctx.T("aside."+strings.ToLower(to)), "/mailbox/"+url.PathEscape(to)+"?all=1", len(landed),
				"/mailbox/"+url.PathEscape(to)+"/all/act?action=move&to="+url.QueryEscape(role), fields)
		case "read", "unread":
			// The rows change weight in the list the reader is looking
			// at. A line saying so is a line about something they can see.
			return alborz.Notice{}
		}
		return alborz.Notice{Kind: alborz.NoticeDone, Text: ctx.Tf("notice.changed", done)}
	}, landOn(ctx, back))
}

// nothingSelected says so on the page the action came from.
func nothingSelected(ctx *alborz.Context, back string) error {
	ctx.Notify(alborz.Notice{Kind: alborz.NoticeWarning, Text: ctx.T("notice.nomessages")})
	return ctx.Redirect(http.StatusFound, back)
}

// runAct is an action route around its action: what was refused is a
// notice naming who refused and why, never an error page, since a
// server's no is an answer; what was done is the notice done makes of
// the count; and land is where the reader goes, told whether it was
// done.
func runAct(ctx *alborz.Context, refs []rowRef, act mailAct, done func(n int) alborz.Notice, land func(done bool) error) error {
	n, failed := actOn(ctx, refs, act)
	// As for a list: only when no server answered is it the upstream
	// page, and the first failure stands for them all.
	down := 0
	for _, f := range failed {
		var upstream alborz.UpstreamError
		if errors.As(f.err, &upstream) {
			down++
		}
	}
	if down > 0 && down == len(grouped(refs)) {
		return failed[0].err
	}
	if len(failed) > 0 {
		var refused []string
		for _, f := range failed {
			if f.err == nil {
				refused = append(refused, f.account)
			} else {
				refused = append(refused, f.account+": "+f.err.Error())
			}
		}
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeFailed, Text: fmt.Sprintf(ctx.T("form.saverefused"), strings.Join(refused, "; "))})
		return land(false)
	}
	ctx.Notify(done(n))
	return land(true)
}

// landOn is the landing of a route that answers with a page.
func landOn(ctx *alborz.Context, back string) func(bool) error {
	return func(bool) error { return ctx.Redirect(http.StatusFound, back) }
}

// searchFilters names what a search narrowed the listing to. A scoped
// term - from: or to: - is named by its field rather than shown as
// syntax, because that is how the reader asked for it: by clicking a
// sender.
func searchFilters(ctx *alborz.Context) []alborz.Filter {
	var out []alborz.Filter
	if f, ok := ctx.FilterOn("view", ctx.T("filter.view")); ok {
		f.Value = viewTitle(ctx, f.Value)
		out = append(out, f)
	}
	f, ok := ctx.FilterOn("query", ctx.T("filter.search"))
	if !ok {
		return out
	}
	for _, scope := range []struct{ prefix, label string }{
		{"from:", ctx.T("filter.from")},
		{"to:", ctx.T("filter.to")},
	} {
		if rest, cut := strings.CutPrefix(f.Value, scope.prefix); cut {
			f.Label, f.Value = scope.label, rest
			break
		}
	}
	return append(out, f)
}
