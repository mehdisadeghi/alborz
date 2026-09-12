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
	// PreferHTML is the account's choice of which part a row opens.
	PreferHTML bool
	// Threaded says this listing is one, so the rows carry a depth and
	// the pager counts conversations rather than messages.
	Threaded bool
	// Filters are the narrowings in force that the page does not
	// otherwise state: the crumb already names the folder and the
	// view, and the rail names the account.
	Filters []alborz.Filter
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
	messagesPerPage := perPage(ctx)
	query := ctx.QueryParam("query")
	view, err := readView(ctx)
	if err != nil {
		return err
	}

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
	key := listingView("#"+role, query, view, sortKey, sortDir)
	spec := listingSpec{query: query, view: view, sortKey: sortKey, sortDir: sortDir}
	bound := alborz.RoundTripTimeout
	if SearchesText(query) {
		bound = alborz.ScanTimeout
	}
	var (
		mu          sync.Mutex
		wg          sync.WaitGroup
		msgs        []IMAPMessage
		total       int
		sortable    = true
		headersOnly bool
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
			merge := func(e *listingEntry) {
				mu.Lock()
				msgs = append(msgs, e.msgs...)
				total += e.total
				headersOnly = headersOnly || e.headersOnly
				mu.Unlock()
			}
			if cacheable {
				if e, state := listings.lookup(user, key, messagesPerPage); e != nil {
					merge(e)
					if state == listingStale {
						revalidateUnified(s, settings, key, role, spec, e)
					}
					return
				}
			}
			errs[i] = s.DoIMAPWithin(bound, func(c *imapclient.Client) error {
				folder, err := resolveRole(c, user, role)
				if err != nil || folder == "" {
					return err
				}
				if !c.Caps().Has(imap.CapSort) {
					mu.Lock()
					sortable = false
					mu.Unlock()
				}
				e, err := fetchUnifiedAccount(c, user, folder, spec, settings, window, cacheable)
				if err != nil {
					return err
				}
				if cacheable && e.snap != nil {
					listings.store(user, key, e)
				}
				merge(e)
				return nil
			})
		}()
	}
	wg.Wait()
	alborz.AddTiming(ctx.Request().Context(), "imap", imapStart)
	// A server that did not answer costs its account's rows, not the
	// page: the others are shown and the page says who is missing.
	// Only when nobody answered is it the upstream page.
	var down []string
	for i, err := range errs {
		var upstream alborz.UpstreamError
		switch {
		case err == nil:
		case errors.As(err, &upstream):
			down = append(down, ctx.Sessions()[i].Username())
		default:
			return err
		}
	}
	if len(down) == len(errs) {
		return errs[0]
	}
	ctx.Unreachable(down)

	slices.SortStableFunc(msgs, unifiedLess(sortKey, reverse))
	// Junk is one colour already; a tint there says nothing. The
	// message page keeps its warning card.
	if role != "Junk" {
		RowMarks(ctx, TrustedAuthServ(ctx, settings), msgs)
	}
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
	if view != "" {
		title = viewTitle(ctx, view)
	}

	return ctx.Render(http.StatusOK, listTemplate(ctx), &MailboxRenderData{
		IMAPBaseRenderData: IMAPBaseRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(fmt.Sprintf(ctx.T("mailbox.allaccounts"), title)),
			// The role is the name: the rail marks the row by it and the
			// refresh form posts to it. The label is what is read.
			Mailbox:         &MailboxStatus{StatusData: &imap.StatusData{Mailbox: role}, Label: title},
			ListView:        view,
			SidebarAccounts: sidebarAccounts(ctx),
		},
		Messages:       msgs,
		Crumb:          viewCrumb(ctx, []CrumbLink{{Label: ctx.T("aside." + strings.ToLower(role)), URL: "/mailbox/" + role}}, role, view),
		PrevPage:       prevPage,
		NextPage:       nextPage,
		RangeFrom:      from + 1,
		RangeTo:        to,
		Total:          total,
		Query:          query,
		Filters:        searchFilters(ctx),
		TextQuery:      textQueryOffered(query, headersOnly),
		Outgoing:       role == "Sent" || role == "Drafts",
		PerPage:        messagesPerPage,
		PerPageOptions: perPageOptions(ctx.Reading().MessagesPerPage),
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
	e := &listingEntry{perPage: window}
	var err error
	switch {
	case spec.query != "":
		e.headersOnly = !SearchesIndex(c, settings)
		e.msgs, e.total, err = searchMessages(c, folder, PrepareSearch(spec.query, !e.headersOnly), 0, window, "", true)
	case spec.view != "":
		e.msgs, e.total, err = searchMessages(c, folder, ViewCriteria(spec.view), 0, window, "", true)
	case spec.sortKey != "account" && (spec.sortKey != "" || spec.sortDir != "") && c.Caps().Has(imap.CapSort):
		// Each account's window is cut under the requested order, so
		// the merge sees the right candidates.
		e.msgs, e.total, err = searchMessages(c, folder, &imap.SearchCriteria{}, 0, window, spec.sortKey, spec.reverse())
	default:
		e.msgs, e.total, err = listMessages(c, folder, 0, window)
	}
	if err != nil {
		return nil, err
	}
	for j := range e.msgs {
		e.msgs[j].Account = user
	}
	Relate(c, user, e.msgs)
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
	messagesPerPage := perPage(ctx)

	query := ctx.QueryParam("query")
	view, err := readView(ctx)
	if err != nil {
		return err
	}

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

	key := listingView(mboxName, query, view, sortKey, sortDir)
	if threadUID != 0 {
		key = fmt.Sprintf("%s%sthread=%d", key, listingSep, threadUID)
	}
	user := ctx.Session.Username()

	spec := listingSpec{mbox: mboxName, query: query, view: view, sortKey: sortKey, sortDir: sortDir, thread: threadUID}
	var e *listingEntry
	if cacheable {
		var state listingState
		if e, state = listings.lookup(user, key, messagesPerPage); e != nil &&
			state == listingStale && !(mboxName == "INBOX" && watchers.watching(user)) {
			// The page is served as it is and the server asked behind it;
			// a watched INBOX needs no asking, the watcher already heard.
			revalidateListing(ctx.Session, settings, key, spec, e)
		}
	}
	if e == nil {
		bound := alborz.RoundTripTimeout
		if SearchesText(query) {
			bound = alborz.ScanTimeout
		}
		err = ctx.DoIMAPWithin(bound, func(c *imapclient.Client) error {
			var err error
			e, err = fetchListing(c, user, spec, settings, page, messagesPerPage)
			return err
		})
		if err != nil {
			return err
		}
		if cacheable {
			listings.store(user, key, e)
		}
	}
	sb, msgs, total, sortSupported, threadAlgorithm := railFor(ctx.Session, mboxName, e.sb), e.msgs, e.total, e.sortSupported, e.threadAlgorithm
	if folderRole(sb.mailboxes, mboxName) != "junk" {
		RowMarks(ctx, TrustedAuthServ(ctx, settings), msgs)
	}
	// The page's bodies are fetched behind it, so the next click, on
	// any of its rows, asks the server nothing.
	if cacheable {
		session := ctx.Session
		go prefetchBodies(session, settings.PreferHTML, mboxName, e.msgs)
	}

	// A row shows the address it reached only where that is worth
	// believing; the headers saying so are part of the message.
	trust := newDeliveryTrust(ctx, settings, ctx.Session.Username())
	for i := range msgs {
		msgs[i].Alias = trust.alias(&msgs[i])
	}

	ibase := assembleIMAPBase(ctx, alborz.NewBaseRenderData(ctx), mboxName, sb, view)
	ibase.SidebarAccounts = sidebarAccounts(ctx)
	title := viewTitle(ctx, view)
	if view == "" && ibase.Mailbox != nil {
		title = ibase.Mailbox.Label
	}
	// The heading names the view, as the merged page's does; the
	// label is set afresh on every render, so nothing lingers.
	if view != "" && ibase.Mailbox != nil {
		ibase.Mailbox.Label = title
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

	return ctx.Render(http.StatusOK, listTemplate(ctx), &MailboxRenderData{
		IMAPBaseRenderData: *ibase,
		Messages:           msgs,
		PrevPage:           prevPage,
		NextPage:           nextPage,
		RangeFrom:          rangeFrom,
		RangeTo:            rangeTo,
		Total:              total,
		Query:              query,
		Filters:            searchFilters(ctx),
		TextQuery:          textQueryOffered(query, e.headersOnly),
		Outgoing:           outgoingFolder(sb.mailboxes, mboxName),
		PerPage:            messagesPerPage,
		PerPageOptions:     perPageOptions(ctx.Reading().MessagesPerPage),
		Sort:               sortKey,
		SortDir:            map[bool]string{true: "desc", false: "asc"}[reverse],
		SortSupported:      sortSupported,
		ThreadSupported:    threadAlgorithm != "",
		Crumb:              viewCrumb(ctx, mailboxCrumb(sb.mailboxes, mboxName, ctx.Session.Username()), mboxName, view),
		PreferHTML:         settings.PreferHTML,
		Threaded:           sortKey == threadSort && threadAlgorithm != "",
	})
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

// viewCrumb puts the view after the folder: a reader in Starred is one
// step past the inbox, and the crumb leads back to the view, not past
// it to the folder.
func viewCrumb(ctx *alborz.Context, crumb []CrumbLink, folder, view string) []CrumbLink {
	if view == "" {
		return crumb
	}
	return append(crumb, CrumbLink{Label: viewTitle(ctx, view), URL: "/mailbox/" + url.PathEscape(folder) + "?view=" + url.QueryEscape(view)})
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
	e := &listingEntry{perPage: perPage, sortSupported: c.Caps().Has(imap.CapSort), threadAlgorithm: ThreadAlgorithm(c)}
	reverse := spec.reverse()
	switch {
	case spec.thread != 0 && e.threadAlgorithm != "":
		e.msgs, err = oneThread(c, spec.mbox, e.threadAlgorithm, spec.thread)
		e.total = len(e.msgs)
	case spec.sortKey == threadSort && e.threadAlgorithm != "":
		// A conversation is the unit here, so the page holds a
		// number of threads rather than a number of messages.
		criteria := &imap.SearchCriteria{}
		if spec.query != "" {
			e.headersOnly = !SearchesIndex(c, settings)
			criteria = PrepareSearch(spec.query, !e.headersOnly)
		} else if spec.view != "" {
			criteria = ViewCriteria(spec.view)
		}
		e.msgs, e.total, err = threadMessages(c, spec.mbox, e.threadAlgorithm, criteria, page, perPage)
	case spec.query != "":
		e.headersOnly = !SearchesIndex(c, settings)
		e.msgs, e.total, err = searchMessages(c, spec.mbox, PrepareSearch(spec.query, !e.headersOnly), page, perPage, spec.sortKey, reverse)
	case spec.view != "":
		e.msgs, e.total, err = searchMessages(c, spec.mbox, ViewCriteria(spec.view), page, perPage, spec.sortKey, reverse)
	case (spec.sortKey != "" || spec.sortDir != "") && e.sortSupported:
		e.msgs, e.total, err = searchMessages(c, spec.mbox, &imap.SearchCriteria{}, page, perPage, spec.sortKey, reverse)
	default:
		e.msgs, e.total, err = listMessages(c, spec.mbox, page, perPage)
	}
	if err != nil {
		return nil, err
	}
	if e.sb, err = load.finish(); err != nil {
		return nil, err
	}
	e.snap = e.sb.active.StatusData
	Relate(c, user, e.msgs)
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

		listings.evictAll(selectedAccount)
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
			return c.Delete(mbox.Name()).Wait()
		}); err != nil {
			return ctx.Render(http.StatusUnprocessableEntity, "delete-mailbox.html", &DeleteMailboxRenderData{
				IMAPBaseRenderData: *ibase,
				Error:              err.Error(),
			})
		}
		listings.evictAll(ctx.Session.Username())
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
			listings.evict(s.Username(), "#"+mboxName)
		}
		return ctx.Redirect(http.StatusFound, ctx.NextOr(fmt.Sprintf("/mailbox/%s", url.PathEscape(mboxName))))
	}
	listings.evict(ctx.Session.Username(), mboxName)
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

func handleMove(ctx *alborz.Context) error {
	mboxName, err := mailboxRef(ctx)
	if err != nil {
		return err
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
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeWarning, Text: ctx.T("notice.nomessages")})
		return ctx.Redirect(http.StatusFound, mailboxURL(ctx, mboxName))
	}

	to := formOrQueryParam(ctx, "to")
	if to == "" {
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeWarning, Text: ctx.T("notice.nodestination")})
		return ctx.Redirect(http.StatusFound, mailboxURL(ctx, mboxName))
	}

	var moved *imapclient.MoveData
	err = ctx.DoIMAP(func(c *imapclient.Client) error {
		if err := ensureMailboxSelected(c, mboxName); err != nil {
			return err
		}
		data, err := c.Move(imap.UIDSetNum(uids...), to).Wait()
		if err != nil {
			return fmt.Errorf("failed to move message: %v", err)
		}
		moved = data
		return nil
	})
	if err != nil {
		return err
	}

	listings.evict(ctx.Session.Username(), mboxName)
	listings.evict(ctx.Session.Username(), to)
	target := ctx.NextOr(mailboxURL(ctx, mboxName))
	ctx.Notify(movedNotice(ctx, len(uids), mboxName, to, moved, target))
	return ctx.Redirect(http.StatusFound, target)
}

// movedNotice says what moved and, where the server named the new
// UIDs (COPYUID, RFC 4315), offers to move it back. An undo names those
// UIDs; a message moved again since is not under them any more, so the
// server moves nothing and the undo says so rather than claiming
// success. An undo offers nothing further.
func movedNotice(ctx *alborz.Context, n int, from, to string, moved *imapclient.MoveData, target string) alborz.Notice {
	if ctx.FormValue("undo") != "" {
		if len(uidNums(moved, false)) == 0 {
			return alborz.Notice{Kind: alborz.NoticeFailed, Text: ctx.T("notice.undofailed")}
		}
		return alborz.Notice{Kind: alborz.NoticeDone, Text: ctx.T("notice.undone")}
	}
	// Named the way a person would: the folder by its label, linked, so
	// the moved thing is one click away.
	label := to
	if role := (&MailboxInfo{ListData: &imap.ListData{Mailbox: to}}).role(); role != "" {
		label = ctx.T("aside." + role)
	}
	link := "<a href=\"" + template.HTMLEscapeString(mailboxURL(ctx, to)) + "\">" + template.HTMLEscapeString(label) + "</a>"
	notice := alborz.Notice{
		Kind:   alborz.NoticeDone,
		Text:   ctx.Tf("notice.movedto", n, label),
		Markup: template.HTML(ctx.Tf("notice.movedto", n, link)),
	}
	if back := uidNums(moved, true); len(back) > 0 {
		fields := url.Values{"to": {from}, "next": {target}}
		for _, uid := range back {
			fields.Add("uids", fmt.Sprint(uid))
		}
		notice.Action = ctx.Undo(ctx.AccountPath("/message/"+url.PathEscape(to)+"/move"), fields)
	}
	return notice
}

// uidNums lists the UIDs a MOVE reported, on the destination or the
// source side; none when the server reported nothing.
func uidNums(moved *imapclient.MoveData, dest bool) []imap.UID {
	if moved == nil {
		return nil
	}
	set := moved.SourceUIDs
	if dest {
		set = moved.DestUIDs
	}
	uids, ok := set.(imap.UIDSet)
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
		return err
	})
	if err != nil {
		return err
	}

	listings.evict(ctx.Session.Username(), mboxName)
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
			removed += n
			return err
		})
		listings.evict(s.Username(), "#"+role)
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
	mboxName, err := mailboxRef(ctx)
	if err != nil {
		return err
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
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeWarning, Text: ctx.T("notice.nomessages")})
		return ctx.Redirect(http.StatusFound, mailboxURL(ctx, mboxName))
	}

	var left int
	err = ctx.DoIMAP(func(c *imapclient.Client) error {
		if err := deleteMessages(c, mboxName, imap.UIDSetNum(uids...)); err != nil {
			return err
		}
		left = int(c.Mailbox().NumMessages)
		return nil
	})
	if err != nil {
		return err
	}
	listings.evict(ctx.Session.Username(), mboxName)
	notice := alborz.Notice{Kind: alborz.NoticeDone, Text: ctx.Tf("notice.deleted", len(uids))}
	// A whole page ticked and more behind it is a reader clearing the
	// folder one page at a time. The rest is offered, behind a page
	// that says how many, and never taken on its own.
	if len(uids) >= perPage(ctx) && left > 0 {
		notice.Action = &alborz.NoticeAction{
			Label: ctx.Tf("notice.deleteall", left),
			Path:  ctx.AccountPath("/mailbox/" + url.PathEscape(mboxName) + "/empty"),
		}
	}
	ctx.Notify(notice)
	return ctx.Redirect(http.StatusFound, ctx.NextOr(mailboxURL(ctx, mboxName)))
}

// listTemplate is the whole page, or the part of it a narrowing
// changes. Paging, sorting, the rows-per-page menu and a search swap
// the list and say so; anything else - a boosted link, history, a page
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
	mboxName, err := mailboxRef(ctx)
	if err != nil {
		return err
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
			for _, step := range []struct {
				op    imap.StoreFlagsOp
				flags []imap.Flag
			}{{imap.StoreFlagsAdd, add}, {imap.StoreFlagsDel, del}} {
				if len(step.flags) == 0 {
					continue
				}
				if err := storeFlags(c, mboxName, imap.UIDSetNum(uids...), step.op, step.flags); err != nil {
					return fmt.Errorf("failed to set flag colour: %w", err)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		listings.evict(ctx.Session.Username(), mboxName)
		bodies.evict(ctx.Session.Username(), mboxName, uids)
		// One message's star is one message's star: where the page
		// asked for that block, the whole listing does not have to be
		// built and read again to show it. The new state is what was
		// just stored, so nothing is fetched to find it out.
		if ctx.Partial() && len(uids) == 1 {
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
		}
		return ctx.Redirect(http.StatusFound, ctx.NextOr(mailboxURL(ctx, mboxName)))
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
		return storeFlags(c, mboxName, imap.UIDSetNum(uids...), op, l)
	})
	if err != nil {
		return err
	}
	listings.evict(ctx.Session.Username(), mboxName)
	bodies.evict(ctx.Session.Username(), mboxName, uids)

	target := ctx.NextOr("")
	if target == "" {
		if len(uids) != 1 || (op == imap.StoreFlagsDel && len(l) == 1 && l[0] == imap.FlagSeen) {
			// Redirecting to the message view would mark the message as read again
			target = mailboxURL(ctx, mboxName)
		} else {
			target = ctx.AccountPath(fmt.Sprintf("/message/%v/%v", url.PathEscape(mboxName), uids[0]))
		}
	}
	ctx.Notify(flaggedNotice(ctx, formParams["uids"], flags, op, mboxName, target))
	return ctx.Redirect(http.StatusFound, target)
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
	return TextQuery(query)
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
}

// String is the form parseRefs reads: what a row's checkbox carries.
func (r rowRef) String() string {
	return fmt.Sprintf("%s|%s|%d", r.account, r.mailbox, r.uid)
}

func parseRefs(values []string) ([]rowRef, error) {
	var refs []rowRef
	for _, v := range values {
		parts := strings.SplitN(v, "|", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("not a message reference: %q", v)
		}
		uid, err := parseUid(parts[2])
		if err != nil {
			return nil, err
		}
		refs = append(refs, rowRef{account: parts[0], mailbox: parts[1], uid: uid})
	}
	return refs, nil
}

// grouped is the selection by the connection it is acted on: one
// account, one folder.
func grouped(refs []rowRef) map[rowRef][]imap.UID {
	out := map[rowRef][]imap.UID{}
	for _, r := range refs {
		key := rowRef{account: r.account, mailbox: r.mailbox}
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
	refs, err := parseRefs(params["refs"])
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}
	back := ctx.NextOr("/mailbox/" + url.PathEscape(role) + "?all=1")
	if len(refs) == 0 {
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeWarning, Text: ctx.T("notice.nomessages")})
		return ctx.Redirect(http.StatusFound, back)
	}
	action := formOrQueryParam(ctx, "action")
	if action == "export" {
		return exportRefs(ctx, refs)
	}

	var failed []string
	done := 0
	// What a move put where, per account, so that the undo can name
	// it: the destination UIDs from COPYUID (RFC 4315), where the
	// server reports them.
	var landed []string
	for key, uids := range grouped(refs) {
		s := ctx.SessionFor(key.account)
		if s == nil {
			failed = append(failed, key.account)
			continue
		}
		err := s.DoIMAP(func(c *imapclient.Client) error {
			if err := ensureMailboxSelected(c, key.mailbox); err != nil {
				return err
			}
			set := imap.UIDSetNum(uids...)
			switch action {
			case "move":
				to, err := resolveRole(c, key.account, formOrQueryParam(ctx, "to"))
				if err != nil {
					return err
				}
				moved, err := c.Move(set, to).Wait()
				for _, uid := range uidNums(moved, true) {
					landed = append(landed, rowRef{account: key.account, mailbox: to, uid: uid}.String())
				}
				return err
			case "flag":
				add, del := FlagColorFlags(ctx.FormValue("color"))
				if add == nil && del == nil {
					return echo.NewHTTPError(http.StatusBadRequest, "unknown flag colour")
				}
				for _, step := range []struct {
					op    imap.StoreFlagsOp
					flags []imap.Flag
				}{{imap.StoreFlagsAdd, add}, {imap.StoreFlagsDel, del}} {
					if len(step.flags) == 0 {
						continue
					}
					if err := storeFlags(c, key.mailbox, set, step.op, step.flags); err != nil {
						return err
					}
				}
				return nil
			case "read":
				return storeFlags(c, key.mailbox, set, imap.StoreFlagsAdd, []imap.Flag{imap.FlagSeen})
			case "unread":
				return storeFlags(c, key.mailbox, set, imap.StoreFlagsDel, []imap.Flag{imap.FlagSeen})
			}
			return echo.NewHTTPError(http.StatusBadRequest, "unknown action")
		})
		listings.evictAll(key.account)
		if err != nil {
			failed = append(failed, key.account+": "+err.Error())
			continue
		}
		done += len(uids)
	}
	if len(failed) > 0 {
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeFailed, Text: fmt.Sprintf(ctx.T("form.saverefused"), strings.Join(failed, "; "))})
		return ctx.Redirect(http.StatusFound, back)
	}
	switch action {
	case "move":
		ctx.Notify(unifiedMovedNotice(ctx, done, role, formOrQueryParam(ctx, "to"), landed, back))
	case "read", "unread":
		// The rows change weight in the list the reader is looking at.
		// A line saying so is a line about something they can see.
	default:
		ctx.PutNotice(ctx.Tf("notice.changed", done))
	}
	return ctx.Redirect(http.StatusFound, back)
}

// unifiedMovedNotice is movedNotice for the merged view: the folder is
// the merged one, linked, and the undo moves every account's messages
// back to the role they came from.
func unifiedMovedNotice(ctx *alborz.Context, n int, from, to string, landed []string, target string) alborz.Notice {
	if ctx.FormValue("undo") != "" {
		if len(landed) == 0 {
			return alborz.Notice{Kind: alborz.NoticeFailed, Text: ctx.T("notice.undofailed")}
		}
		return alborz.Notice{Kind: alborz.NoticeDone, Text: ctx.T("notice.undone")}
	}
	label := ctx.T("aside." + strings.ToLower(to))
	link := "<a href=\"/mailbox/" + url.PathEscape(to) + "?all=1\">" + template.HTMLEscapeString(label) + "</a>"
	notice := alborz.Notice{
		Kind:   alborz.NoticeDone,
		Text:   ctx.Tf("notice.movedto", n, label),
		Markup: template.HTML(ctx.Tf("notice.movedto", n, link)),
	}
	if len(landed) > 0 {
		fields := url.Values{"next": {target}, "refs": landed}
		notice.Action = ctx.Undo("/mailbox/"+url.PathEscape(to)+"/all/act?action=move&to="+url.QueryEscape(from), fields)
	}
	return notice
}

// exportRefs streams a selection across accounts as one mbox, the way
// a folder's selection goes out.
func exportRefs(ctx *alborz.Context, refs []rowRef) error {
	res := ctx.Response()
	started := false
	for _, r := range refs {
		s := ctx.SessionFor(r.account)
		if s == nil {
			continue
		}
		var raw []byte
		var env *imap.Envelope
		err := s.DoIMAP(func(c *imapclient.Client) error {
			var err error
			raw, env, err = fetchRawMessage(c, r.mailbox, r.uid)
			return err
		})
		if err != nil {
			if started {
				ctx.Logger().Printf("export %s %q uid %v: %v", r.account, r.mailbox, r.uid, err)
				return nil
			}
			return err
		}
		if !started {
			res.Header().Set("Content-Disposition", downloadName("messages", "messages", ".mbox"))
			res.Header().Set("Content-Type", "application/mbox")
			res.WriteHeader(http.StatusOK)
			started = true
		}
		if err := writeMbox(res, raw, env); err != nil {
			return nil
		}
		res.Flush()
	}
	return nil
}

// searchFilters names what a search narrowed the listing to. A scoped
// term - from: or to: - is named by its field rather than shown as
// syntax, because that is how the reader asked for it: by clicking a
// sender.
func searchFilters(ctx *alborz.Context) []alborz.Filter {
	f, ok := ctx.FilterOn("query", ctx.T("filter.search"))
	if !ok {
		return nil
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
	return []alborz.Filter{f}
}
