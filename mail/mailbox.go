package mail

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail/search"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/labstack/echo/v4"
)

type MailboxRenderData struct {
	IMAPBaseRenderData
	Messages []IMAPMessage
	Pager    web.Pager
	Total    int
	Query    string
	// TextQuery is the query widened to the whole message, when the search reached headers only.
	TextQuery string
	// Merged says the rows span accounts, and Spanning that they span folders too.
	Merged, Spanning bool
	Role             string
	// FolderQuery is the query less its in:, for narrowing to a row's folder.
	FolderQuery string
	// Everywhere is the query asked of every folder; empty when it already is.
	Everywhere string
	// FolderTrack is the width the rows' folder names share, so they keep one edge.
	FolderTrack template.CSS
	// Outgoing says the folder holds what the reader wrote, so rows name recipients.
	Outgoing      bool
	Sort          string
	SortDir       string
	SortSupported bool
	// ThreadSupported says the server can group a folder into conversations.
	ThreadSupported bool
	// Crumb is the path to this folder, account first.
	Crumb []CrumbLink
	// PreferHTML is the reader's choice of which part a row opens.
	PreferHTML bool
	// Threaded says the rows carry a depth and the pager counts conversations.
	Threaded bool
	// Filters are the narrowings in force that the crumb and the rail do not state.
	Filters []web.Filter
	// FilterRows are the views the filter menu offers.
	FilterRows []web.FilterRow
}

type MailboxDetails struct {
	Info   *MailboxInfo
	Status *MailboxStatus
}

var unifiedRoles = []string{"INBOX", "Drafts", "Sent", "Junk", "Trash", "Archive"}

// A merged folder is a role in every account, or a folder by name in the accounts that hold it.
func (p *plugin) handleUnifiedMailbox(ctx *web.Context) error {
	role, err := mailboxRef(ctx)
	if err != nil {
		return err
	}

	// account is a property of the merge, not of any single server's order.
	ask, err := readListAsk(ctx, "account")
	if err != nil {
		return err
	}
	if to := searchInstead(ctx, ask); to != "" {
		return ctx.Redirect(http.StatusFound, to)
	}
	settings, err := LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return err
	}
	spec := ask.spec
	key := listingView("#"+role, spec.query, spec.view, spec.sortKey, spec.sortDir)
	class, bound := listingBudget(spec)
	// A search costs a round trip per account, so it most wants the cache.
	merged, found, err := p.mergedWindow(ctx, role, ask, settings, ctx.Request().Method == http.MethodGet)
	if err != nil {
		return err
	}
	if !found {
		return alborz.NotFound("notfound.folder", role)
	}
	page, cut := cutPage(merged, ask.page)
	rows, err := p.wholePage(ctx, role, key, cut, class, bound)
	if err != nil {
		return err
	}
	named := role
	if slices.Contains(unifiedRoles, role) {
		named = ctx.T("aside." + strings.ToLower(role))
	}
	title := named
	if spec.view != "" {
		title = viewTitle(ctx, spec.view)
	}

	data := listPage(ctx, ask.spec, page, merged, rows)
	data.IMAPBaseRenderData = IMAPBaseRenderData{
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(fmt.Sprintf(ctx.T("mailbox.allaccounts"), title)),
		// The role is the name the rail marks and the refresh form posts to; Label is what is read.
		Mailbox:         &MailboxStatus{StatusData: &imap.StatusData{Mailbox: role}, Label: title},
		ListView:        spec.view,
		SidebarAccounts: p.sidebarAccounts(ctx),
	}
	data.Crumb = []CrumbLink{{Label: named, URL: mailboxPageURL(role).String()}}
	data.Outgoing = role == "Sent" || role == "Drafts"
	data.Merged, data.Role = true, role
	return ctx.Render(http.StatusOK, listTemplate(ctx), data)
}

// mergedWindow is a merged folder's rows up to the page asked, merged as its
// page is; found is false where every account answered without the folder.
func (p *plugin) mergedWindow(ctx *web.Context, role string, ask listAsk, settings *Settings, cacheable bool) (merged *listingEntry, found bool, err error) {
	spec := ask.spec
	key := listingView("#"+role, spec.query, spec.view, spec.sortKey, spec.sortDir)
	class, bound := listingBudget(spec)
	// Every account that answered lacking the folder is a folder that is not there.
	var holding, silent atomic.Int32
	merged, err = gather(ctx, ctx.Logins(), spec, func(s *alborz.Login) (e *listingEntry, err error) {
		defer func() {
			if e != nil {
				holding.Add(1)
			}
			if err != nil {
				silent.Add(1)
			}
		}()
		user := s.Username()
		size := ask.held(p.listings.heldSize(user, key))
		fields := mergeFetchOptions
		if size == ask.page.Size {
			fields = listFetchOptions
		}
		folder := func(c *imapclient.Client) (string, error) { return p.resolveRole(c, user, role) }
		fetch := func(c *imapclient.Client) (*listingEntry, error) {
			name, err := folder(c)
			if err != nil || name == "" {
				return nil, err
			}
			return p.fetchUnifiedAccount(c, user, name, spec, settings, size, fields, cacheable)
		}
		if cacheable {
			return p.cachedListing(ctx, s, key, size, spec, folder, fetch)
		}
		alborz.CacheTiming(ctx.Request().Context(), "listing", false)
		return readOn(s, class, bound, fetch)(ctx.Request().Context())
	})
	if err != nil {
		return nil, false, err
	}
	return merged, holding.Load() > 0 || silent.Load() > 0, nil
}

// Rounded up to a power of two so a walk through n pages refetches log n times (ADR 46).
func (ask listAsk) held(have int) int {
	pages := 1
	for pages < ask.page.Number+1 {
		pages *= 2
	}
	return max(pages*ask.page.Size, have)
}

// A row that could not be fetched is left off rather than shown without its marks.
func (p *plugin) wholePage(ctx *web.Context, role, key string, page []IMAPMessage, class alborz.IMAPClass, bound time.Duration) ([]IMAPMessage, error) {
	missing := map[string][]imap.UID{}
	for _, m := range page {
		if m.BodyStructure == nil {
			missing[m.Account] = append(missing[m.Account], m.UID)
		}
	}
	if len(missing) == 0 {
		return page, nil
	}
	fetched, err := gather(ctx, ctx.Logins(), listingSpec{}, func(s *alborz.Login) (*listingEntry, error) {
		user, uids := s.Username(), missing[s.Username()]
		if len(uids) == 0 {
			return nil, nil
		}
		return readOn(s, class, bound, func(c *imapclient.Client) (*listingEntry, error) {
			name, err := p.resolveRole(c, user, role)
			if err != nil || name == "" {
				return nil, err
			}
			rows, err := wholeRows(c, name, uids)
			if err != nil {
				return nil, err
			}
			for i := range rows {
				rows[i].Account = user
			}
			p.listings.fill(user, key, rows)
			return &listingEntry{msgs: rows}, nil
		})(ctx.Request().Context())
	})
	if err != nil {
		return nil, err
	}
	type row struct {
		account string
		uid     imap.UID
	}
	whole := make(map[row]IMAPMessage, len(fetched.msgs))
	for _, m := range fetched.msgs {
		whole[row{m.Account, m.UID}] = m
	}
	rows := make([]IMAPMessage, 0, len(page))
	for _, m := range page {
		if m.BodyStructure == nil {
			var ok bool
			if m, ok = whole[row{m.Account, m.UID}]; !ok {
				continue
			}
		}
		rows = append(rows, m)
	}
	return rows, nil
}

// The page is cut from a merged window, which holds every row up to it.
func cutPage(merged *listingEntry, page web.Page) (web.Page, []IMAPMessage) {
	page = page.Within(merged.total)
	from, to := page.Rows(len(merged.msgs))
	return page, merged.msgs[from:to]
}

func listPage(ctx *web.Context, spec listingSpec, page web.Page, e *listingEntry, rows []IMAPMessage) *MailboxRenderData {
	return &MailboxRenderData{
		Messages:      rows,
		Pager:         web.NewPager(ctx.Request().URL, page, e.total, web.PerPageChoices(ctx)),
		Total:         e.total,
		Query:         spec.query,
		Everywhere:    everywhereQuery(spec.query),
		Filters:       searchFilters(ctx),
		FilterRows:    web.ViewRows(ctx, spec.view, true, ctx.WithoutParam("view")),
		TextQuery:     textQueryOffered(spec.query, e.headersOnly),
		Sort:          spec.sortKey,
		SortDir:       map[bool]string{true: "desc", false: "asc"}[spec.reverse()],
		SortSupported: e.sortSupported,
		PreferHTML:    ctx.Reading().PreferHTML,
	}
}

// A silent server costs its account's rows, not the page; only when nobody answers is it an error.
func gather(ctx *web.Context, logins []*alborz.Login, spec listingSpec, read func(*alborz.Login) (*listingEntry, error)) (*listingEntry, error) {
	var wg sync.WaitGroup
	// One span for the whole fan-out: the accounts are queried concurrently.
	imapStart := time.Now()
	errs := make([]error, len(logins))
	asked := func(i int, s *alborz.Login) *listingEntry {
		e, err := read(s)
		if errs[i] = err; err != nil || e == nil {
			return nil
		}
		return e
	}
	entries := make([]*listingEntry, len(logins))
	for i, s := range logins {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entries[i] = asked(i, s)
		}()
	}
	wg.Wait()
	alborz.AddTiming(ctx.Request().Context(), "imap", imapStart)
	// Absorbed in the accounts' order, so the stable sort keeps equal rows in place across requests.
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
			ctx.Logger().Printf("merged view without %q: %v", logins[i].Username(), err)
			down = append(down, logins[i].Username())
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

func (p *plugin) fetchUnifiedAccount(c *imapclient.Client, user, folder string, spec listingSpec, settings *Settings, window int, fields rowFields, withSnap bool) (*listingEntry, error) {
	var snapCmd *imapclient.StatusCommand
	if withSnap {
		snapCmd = c.Status(folder, listingStatusOptions(c))
	}
	e, err := p.fetchRows(c, user, folder, spec, settings, web.Page{Size: window}, fields)
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

func (p *plugin) handleGetMailbox(ctx *web.Context) error {
	if ctx.Unified {
		return p.handleUnifiedMailbox(ctx)
	}

	mboxName, err := mailboxRef(ctx)
	if err != nil {
		return err
	}

	// thread is not a sort column, but travels in the same parameter.
	ask, err := readListAsk(ctx, threadSort)
	if err != nil {
		return err
	}
	if to := searchInstead(ctx, ask); to != "" {
		return ctx.Redirect(http.StatusFound, to)
	}
	settings, err := LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return err
	}
	ask.spec.mbox = mboxName

	cacheable := ctx.Request().Method == http.MethodGet
	if raw := ctx.QueryParam("thread"); raw != "" {
		n, perr := strconv.ParseUint(raw, 10, 32)
		if perr != nil || n == 0 {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid thread")
		}
		ask.spec.thread = imap.UID(n)
	}
	spec := ask.spec

	key := listingView(mboxName, spec.query, spec.view, spec.sortKey, spec.sortDir)
	if spec.thread != 0 {
		key = fmt.Sprintf("%s%sthread=%d", key, listingSep, spec.thread)
	}
	key = listingPage(key, ask.page.Number, ask.page.Size)
	fetch := func(c *imapclient.Client) (*listingEntry, error) {
		return p.fetchListing(c, ctx.Login.Username(), spec, settings, ask.page)
	}
	var e *listingEntry
	if cacheable {
		e, err = p.cachedListing(ctx, ctx.Login, key, ask.page.Size, spec,
			func(*imapclient.Client) (string, error) { return mboxName, nil }, fetch)
	} else {
		e, err = p.loadListing(ctx, ctx.Login, key, ask.page.Size, spec, fetch)
	}
	if err != nil {
		return err
	}
	sb, msgs := p.railFor(ctx.Login, mboxName, e.sb), e.msgs
	page := web.Page{Number: e.page, Size: e.perPage}
	// Bodies are fetched behind the page, so the next click asks the server nothing.
	if cacheable {
		login := ctx.Login
		p.prefetchBodies(login, ctx.Reading().PreferHTML, mboxName, e.validity(), e.msgs)
		if spec.sortKey != threadSort && spec.thread == 0 && page.Window() < e.total {
			p.prefetchPage(login, settings, spec, web.Page{Number: page.Number + 1, Size: page.Size})
		}
	}

	trust := p.newDeliveryTrust(ctx, settings, ctx.Login.Username())
	for i := range msgs {
		msgs[i].Alias = trust.alias(&msgs[i])
	}

	ibase := assembleIMAPBase(ctx, web.NewBaseRenderData(ctx), mboxName, sb, spec.view)
	ibase.SidebarAccounts = p.sidebarAccounts(ctx)
	title := viewTitle(ctx, spec.view)
	if spec.view == "" && ibase.Mailbox != nil {
		title = ibase.Mailbox.Label
	}
	if spec.view != "" && ibase.Mailbox != nil {
		ibase.Mailbox.Label = title
	}
	ibase.BaseRenderData.WithTitle(title)

	data := listPage(ctx, spec, page, e, msgs)
	data.IMAPBaseRenderData = *ibase
	data.Crumb = mailboxCrumb(sb.mailboxes, mboxName, ctx.Login.Username())
	data.Outgoing = outgoingFolder(sb.mailboxes, mboxName)
	data.ThreadSupported = e.threadAlgorithm != ""
	data.Threaded = spec.sortKey == threadSort && e.threadAlgorithm != ""
	return ctx.Render(http.StatusOK, listTemplate(ctx), data)
}

// A star view keeps the narrowing and order already in force.
func starViewPath(ctx *web.Context, view string) string {
	kept := url.Values{"view": {view}}
	for _, name := range []string{"query", "sort", "dir", "ipp"} {
		if value := ctx.QueryParam(name); value != "" {
			kept.Set(name, value)
		}
	}
	return ctx.AccountPath("/search?" + web.Query(kept))
}

// A search leaves junk and trash out until it is asked for them.
func everywhereQuery(query string) string {
	if query == "" {
		return ""
	}
	q := search.Parse(query)
	if _, everywhere := q.Scope(); everywhere {
		return ""
	}
	return strings.TrimSpace(q.Without(search.In) + " " + search.In + ":" + search.Everywhere)
}

type listAsk struct {
	page web.Page
	spec listingSpec
}

// also is the one order the page has beyond the columns.
func readListAsk(ctx *web.Context, also string) (listAsk, error) {
	return readListAskAt(ctx, ctx.QueryParams(), also)
}

// readListAskAt reads a list's address, the request's or the one an object was opened from.
func readListAskAt(ctx *web.Context, q url.Values, also string) (listAsk, error) {
	var ask listAsk
	var err error
	if ask.page, err = ctx.ListPageAt(q); err != nil {
		return ask, err
	}
	view, err := readView(q)
	if err != nil {
		return ask, err
	}
	sortKey, sortDir, err := readSort(q, also)
	if err != nil {
		return ask, err
	}
	ask.spec = listingSpec{query: q.Get("query"), view: view, sortKey: sortKey, sortDir: sortDir}
	return ask, nil
}

func searchInstead(ctx *web.Context, ask listAsk) string {
	if folder, everywhere := search.Parse(ask.spec.query).Scope(); folder != "" || everywhere {
		return ctx.AccountPath("/search?query=" + web.QueryValue(ask.spec.query))
	}
	// A star is on the message, not in the folder, so its view is the search page's fan-out.
	if StarView(ask.spec.view) {
		return starViewPath(ctx, ask.spec.view)
	}
	return ""
}

// also is the one order the page has beyond the columns.
func readSort(q url.Values, also string) (sortKey, sortDir string, err error) {
	sortKey = q.Get("sort")
	if _, ok := sortKeys[sortKey]; !ok && sortKey != also {
		return "", "", echo.NewHTTPError(http.StatusBadRequest, "invalid sort order")
	}
	sortDir = q.Get("dir")
	if sortDir != "" && sortDir != "asc" && sortDir != "desc" {
		return "", "", echo.NewHTTPError(http.StatusBadRequest, "invalid sort direction")
	}
	return sortKey, sortDir, nil
}

// An order the server cannot sort by is no order at all here.
func listOrder(ctx *web.Context) (sortKey string, reverse bool) {
	sortKey = ctx.QueryParam("sort")
	if _, ok := sortKeys[sortKey]; !ok || sortKey == threadSort {
		return "", true
	}
	reverse = sortKeys[sortKey].descends
	if dir := ctx.QueryParam("dir"); dir != "" {
		reverse = dir == "desc"
	}
	return sortKey, reverse
}

// A view is a link in the rail, never typed, so an unknown one is refused.
func readView(q url.Values) (string, error) {
	view := q.Get("view")
	if !KnownView(view) {
		return "", echo.NewHTTPError(http.StatusBadRequest, "unknown view")
	}
	return view, nil
}

func viewTitle(ctx *web.Context, view string) string {
	switch view {
	case web.ViewStarred:
		return ctx.T("mailbox.starred")
	case web.ViewUnread:
		return ctx.T("mailbox.unread")
	}
	return fmt.Sprintf(ctx.T("mailbox.starcolor"), ctx.T("color."+view))
}

func (p *plugin) fetchListing(c *imapclient.Client, user string, spec listingSpec, settings *Settings, page web.Page) (*listingEntry, error) {
	load, err := startSidebar(c, spec.mbox, spec.mbox, settings.Subscriptions)
	if err != nil {
		return nil, err
	}
	e, err := p.fetchRows(c, user, spec.mbox, spec, settings, page, listFetchOptions)
	if err != nil {
		return nil, err
	}
	if e.sb, err = load.finish(); err != nil {
		return nil, err
	}
	e.snap = e.sb.active.StatusData
	return e, nil
}

func (p *plugin) fetchRows(c *imapclient.Client, user, folder string, spec listingSpec, settings *Settings, page web.Page, fields rowFields) (*listingEntry, error) {
	e := &listingEntry{perPage: page.Size, sortSupported: c.Caps().Has(imap.CapSort), threadAlgorithm: ThreadAlgorithm(c)}
	// Each window is cut under the requested order, so the merge sees the true candidates.
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
		e.headersOnly = spec.query != "" && !SearchesIndex(c, settings)
		if err = ensureMailboxSelected(c, folder); err != nil {
			return nil, err
		}
		criteria := listCriteria(c, settings, spec.query, spec.view)
		var o messageOrder
		o, err = p.listings.order(user, orderView(folder, spec.query, spec.view, threadSort, true), func() (messageOrder, error) {
			return threadOrder(c, e.threadAlgorithm, criteria)
		})
		if err == nil {
			e.total = len(o.starts)
			e.msgs, err = threadPage(c, folder, o, page)
		}
	case spec.query != "" || spec.view != "" || spec.sortKey != "account" && (spec.sortKey != "" || spec.sortDir != ""):
		e.headersOnly = spec.query != "" && !SearchesIndex(c, settings)
		if err = ensureMailboxSelected(c, folder); err != nil {
			return nil, err
		}
		criteria := listCriteria(c, settings, spec.query, spec.view)
		var o messageOrder
		o, err = p.listings.order(user, orderView(folder, spec.query, spec.view, sortKey, reverse), func() (messageOrder, error) {
			uids, err := viewOrder(c, search.Parse(spec.query), criteria, sortKey, reverse)
			return messageOrder{uids: uids}, err
		})
		if err == nil {
			e.total = len(o.uids)
			e.msgs, err = orderedPage(c, folder, o.uids, page, fields)
		}
	default:
		e.msgs, e.total, err = listMessages(c, folder, page, fields)
	}
	if err != nil {
		return nil, err
	}
	e.page = page.Within(e.total).Number
	return e, nil
}

type NewMailboxRenderData struct {
	IMAPBaseRenderData
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

func (p *plugin) newMailboxLocationGroups(ctx *web.Context) []NewMailboxLocationGroup {
	var groups []NewMailboxLocationGroup
	key := 0
	for _, login := range ctx.Logins() {
		sb, err := p.sidebarFor(login)
		if err != nil {
			ctx.Logger().Printf("folder locations for %q: %v", login.Username(), err)
			continue
		}
		ib := assembleIMAPBase(ctx, &web.BaseRenderData{}, "", sb.clone(), "")
		delimiter := rune('/')
		for _, mailbox := range ib.Mailboxes {
			if mailbox.Delim != 0 {
				delimiter = mailbox.Delim
				break
			}
		}
		group := NewMailboxLocationGroup{Account: login.Username()}
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

func (p *plugin) handleNewMailbox(ctx *web.Context) error {
	ibase, err := p.newIMAPBaseRenderData(ctx, web.NewBaseRenderData(ctx))
	if err != nil {
		return err
	}
	ibase.BaseRenderData.WithTitle(ctx.T("folder.create"))
	selectedAccount := ctx.Login.Username()
	if ctx.URLAccount() != "" {
		selectedAccount = ctx.URLAccount()
	}
	locationGroups := p.newMailboxLocationGroups(ctx)
	selectedLocation := ""
	for _, group := range locationGroups {
		if group.Account == selectedAccount && len(group.Locations) > 0 {
			selectedLocation = group.Locations[0].Key
			break
		}
	}
	name := ""
	render := func(status int, errText string) error {
		ibase.BaseRenderData.Refused(errText)
		return ctx.Render(status, "mail/folder-create", &NewMailboxRenderData{
			IMAPBaseRenderData: *ibase,
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

		selectedLogin := ctx.LoginFor(selectedAccount)
		if selectedLogin == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "not signed in to that account")
		}
		fullName := location.folder(name)
		err := selectedLogin.DoIMAP(func(c *imapclient.Client) error {
			return c.Create(fullName, nil).Wait()
		})

		if err != nil {
			return render(http.StatusUnprocessableEntity, err.Error())
		}

		p.mailboxCreated(selectedAccount)
		ctx.Made(ctx.T("notice.foldercreated"), name, "", ctx.Request().URL.RequestURI())
		return ctx.Redirect(http.StatusFound, folderURL(ctx, selectedAccount, fullName))
	}

	return render(http.StatusOK, "")
}

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

func (l *NewMailboxLocation) folder(name string) string {
	if l.Parent == "" {
		return name
	}
	return l.Parent + string(l.Delimiter) + name
}

func folderURL(ctx *web.Context, account, name string) string {
	destination := fmt.Sprintf("/mailbox/%s", url.PathEscape(name))
	if len(ctx.Logins()) > 1 {
		destination += AccountQuery(account)
	}
	return destination
}

type DeleteMailboxRenderData struct {
	IMAPBaseRenderData
}

// RenameMailboxRenderData is the page that renames one folder.
type RenameMailboxRenderData struct {
	IMAPBaseRenderData
	Name string
}

type EmptyMailboxRenderData struct {
	IMAPBaseRenderData
	Count int
}

func (p *plugin) handleDeleteMailbox(ctx *web.Context) error {
	ibase, err := p.newIMAPBaseRenderData(ctx, web.NewBaseRenderData(ctx))
	if err != nil {
		return err
	}

	mbox := ibase.Mailbox
	ibase.BaseRenderData.WithTitle(fmt.Sprintf(ctx.T("folder.deletetitle"), mbox.Name()))

	if ctx.Request().Method == http.MethodPost {
		// Some deletions are refused; claiming success would send the reader looking for a folder still there.
		if err := ctx.DoIMAP(func(c *imapclient.Client) error {
			if err := c.Delete(mbox.Name()).Wait(); err != nil {
				return err
			}
			p.mailboxDeleted(ctx.Login.Username(), mbox.Name())
			return nil
		}); err != nil {
			ibase.BaseRenderData.Refused(err.Error())
			return ctx.Render(http.StatusUnprocessableEntity, "mail/folder-delete", &DeleteMailboxRenderData{
				IMAPBaseRenderData: *ibase,
			})
		}
		ctx.PutNotice(ctx.T("notice.mailboxdeleted"))
		return ctx.Redirect(http.StatusFound, ctx.AccountPath("/mailbox/INBOX"))
	}

	return ctx.Render(http.StatusOK, "mail/folder-delete", &DeleteMailboxRenderData{
		IMAPBaseRenderData: *ibase,
	})
}

// The form names the folder, not its path, so a rename cannot move it under another.
func (p *plugin) handleRenameMailbox(ctx *web.Context) error {
	ibase, err := p.newIMAPBaseRenderData(ctx, web.NewBaseRenderData(ctx))
	if err != nil {
		return err
	}
	mbox := ibase.Mailbox
	ibase.BaseRenderData.WithTitle(fmt.Sprintf(ctx.T("folder.renametitle"), mbox.Label))
	name := mbox.Label
	render := func(status int, errText string) error {
		ibase.BaseRenderData.Refused(errText)
		return ctx.Render(status, "mail/folder-rename", &RenameMailboxRenderData{
			IMAPBaseRenderData: *ibase,
			Name:               name,
		})
	}
	if ctx.Request().Method != http.MethodPost {
		return render(http.StatusOK, "")
	}
	name = strings.TrimSpace(ctx.FormValue("name"))
	if name == "" {
		return render(http.StatusUnprocessableEntity, ctx.T("form.nameneeded"))
	}
	old := mbox.Name()
	renamed := name
	for _, info := range ibase.Mailboxes {
		if info.Name() == old && info.Delim != 0 {
			if at := strings.LastIndex(old, string(info.Delim)); at >= 0 {
				renamed = old[:at+1] + name
			}
			break
		}
	}
	if renamed == old {
		return ctx.Redirect(http.StatusFound, folderURL(ctx, ctx.Login.Username(), old))
	}
	if err := ctx.DoIMAP(func(c *imapclient.Client) error {
		return c.Rename(old, renamed, nil).Wait()
	}); err != nil {
		return render(http.StatusUnprocessableEntity, err.Error())
	}
	p.mailboxDeleted(ctx.Login.Username(), old)
	ctx.PutNotice(fmt.Sprintf(ctx.T("notice.folderrenamed"), name))
	return ctx.Redirect(http.StatusFound, folderURL(ctx, ctx.Login.Username(), renamed))
}

// For a server without IDLE, a reader who knows mail arrived has no other way to say so.
func (p *plugin) handleRefreshMailbox(ctx *web.Context) error {
	mboxName, err := mailboxRef(ctx)
	if err != nil {
		return err
	}
	ctx.Quiet()
	if ctx.Unified {
		for _, s := range ctx.Logins() {
			p.viewForgotten(s.Username(), "#"+mboxName)
		}
		return ctx.Redirect(http.StatusFound, ctx.NextOr(fmt.Sprintf("/mailbox/%s", url.PathEscape(mboxName))))
	}
	p.viewForgotten(ctx.Login.Username(), mboxName)
	return ctx.Redirect(http.StatusFound, ctx.NextOr(mailboxURL(ctx, mboxName)))
}

// A button's query parameter wins over a form field, so the bulk move selector cannot leak into other actions.
func formOrQueryParam(ctx *web.Context, k string) string {
	if v := ctx.QueryParam(k); v != "" {
		return v
	}
	return ctx.FormValue(k)
}

// MoveRenderData is the chooser a move without a destination answers with.
type MoveRenderData struct {
	IMAPBaseRenderData
	UIDs []imap.UID
	Next string
}

func (p *plugin) handleMove(ctx *web.Context) error {
	mboxName, _, uids, err := folderSelection(ctx)
	if err != nil {
		return err
	}

	target := ctx.NextOr(mailboxURL(ctx, mboxName))
	if len(uids) == 0 {
		return nothingSelected(ctx, target)
	}

	// A menu holds actions; the destination is chosen on a page this route answers.
	to := formOrQueryParam(ctx, "to")
	if to == "" {
		ibase, err := p.newIMAPBaseRenderData(ctx, web.NewBaseRenderData(ctx))
		if err != nil {
			return err
		}
		ibase.BaseRenderData.WithTitle(ctx.T("folder.moveask"))
		return ctx.Render(http.StatusOK, "mail/move", &MoveRenderData{
			IMAPBaseRenderData: *ibase,
			UIDs:               uids,
			Next:               ctx.FormValue("next"),
		})
	}

	var landed []rowRef
	act := p.moveAct(func(*imapclient.Client, rowRef) (string, error) { return to, nil }, &landed)
	return runAct(ctx, folderRefs(ctx.Login.Username(), mboxName, uids), act, func(done int) web.Notice {
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

type mailAct struct {
	announce func(key rowRef, uids []imap.UID)
	do       func(c *imapclient.Client, key rowRef, uids []imap.UID) error
}

// err is nil where the account is not signed in.
type actFailure struct {
	account string
	err     error
}

func actOn(ctx *web.Context, refs []rowRef, act mailAct) (done int, failed []actFailure) {
	for key, uids := range grouped(refs) {
		s := ctx.LoginFor(key.account)
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

func (p *plugin) leaving(key rowRef, uids []imap.UID) {
	p.messagesLeaving(key.account, key.mailbox, uids)
}

// landed takes what the server says it put where (COPYUID, RFC 4315), for an undo.
func (p *plugin) moveAct(to func(*imapclient.Client, rowRef) (string, error), landed *[]rowRef) mailAct {
	return mailAct{
		announce: p.leaving,
		do: func(c *imapclient.Client, key rowRef, uids []imap.UID) error {
			dest := key.origin
			if dest == "" {
				var err error
				if dest, err = to(c, key); err != nil {
					return err
				}
			}
			moved, err := p.moveMessages(c, key.account, key.mailbox, dest, uids)
			for _, uid := range uidNums(moved) {
				*landed = append(*landed, rowRef{account: key.account, mailbox: dest, uid: uid, origin: key.mailbox})
			}
			return err
		},
	}
}

func (p *plugin) flagAct(op imap.StoreFlagsOp, flags []imap.Flag) mailAct {
	return mailAct{
		announce: func(key rowRef, uids []imap.UID) { p.flagWriteStarting(key.account, key.mailbox, uids, op, flags) },
		do: func(c *imapclient.Client, key rowRef, uids []imap.UID) error {
			return p.writeFlags(c, key.account, key.mailbox, uids, op, flags)
		},
	}
}

func (p *plugin) colourAct(add, del []imap.Flag) mailAct {
	return mailAct{do: func(c *imapclient.Client, key rowRef, uids []imap.UID) error {
		return p.writeColour(c, key.account, key.mailbox, uids, add, del)
	}}
}

func (p *plugin) moveMessages(c *imapclient.Client, account, from, to string, uids []imap.UID) (*imapclient.MoveData, error) {
	if err := ensureMailboxSelected(c, from); err != nil {
		return nil, err
	}
	moved, err := c.Move(imap.UIDSetNum(uids...), to).Wait()
	if err != nil {
		return nil, fmt.Errorf("failed to move message: %w", err)
	}
	p.messagesMoved(account, from, to, uids)
	return moved, nil
}

func (p *plugin) writeFlags(c *imapclient.Client, account, mailbox string, uids []imap.UID, op imap.StoreFlagsOp, flags []imap.Flag) error {
	if err := storeFlags(c, mailbox, imap.UIDSetNum(uids...), op, flags); err != nil {
		return err
	}
	p.messagesFlagged(account, mailbox, uids, op, flags)
	return nil
}

func (p *plugin) writeColour(c *imapclient.Client, account, mailbox string, uids []imap.UID, add, del []imap.Flag) error {
	for _, step := range []struct {
		op    imap.StoreFlagsOp
		flags []imap.Flag
	}{{imap.StoreFlagsAdd, add}, {imap.StoreFlagsDel, del}} {
		if len(step.flags) == 0 {
			continue
		}
		if err := p.writeFlags(c, account, mailbox, uids, step.op, step.flags); err != nil {
			return fmt.Errorf("failed to set flag colour: %w", err)
		}
	}
	return nil
}

// An undo names the new UIDs, so a message moved since is left alone and the undo says so.
func movedNotice(ctx *web.Context, n int, label, href string, landed int, undoPath string, undoFields url.Values) web.Notice {
	if ctx.FormValue("undo") != "" {
		if landed == 0 {
			return web.Notice{Kind: web.NoticeFailed, Text: ctx.T("notice.undofailed")}
		}
		return web.Notice{Kind: web.NoticeDone, Text: ctx.T("notice.undone")}
	}
	link := "<a href=\"" + template.HTMLEscapeString(href) + "\">" + template.HTMLEscapeString(label) + "</a>"
	notice := web.Notice{
		Kind:   web.NoticeDone,
		Text:   ctx.Tf("notice.movedto", n, label),
		Markup: template.HTML(ctx.Tf("notice.movedto", n, link)),
	}
	if landed > 0 {
		notice.Actions = []*web.NoticeAction{ctx.Undo(undoPath, undoFields)}
	}
	return notice
}

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

func (p *plugin) handleEmptyMailbox(ctx *web.Context) error {
	mboxName, err := mailboxRef(ctx)
	if err != nil {
		return err
	}

	if ctx.Request().Method == http.MethodGet {
		ibase, err := p.newIMAPBaseRenderData(ctx, web.NewBaseRenderData(ctx))
		if err != nil {
			return err
		}
		ibase.BaseRenderData.WithTitle(fmt.Sprintf(ctx.T("folder.emptytitle"), mboxName))
		count := 0
		if ibase.Mailbox.NumMessages != nil {
			count = int(*ibase.Mailbox.NumMessages)
		}
		return ctx.Render(http.StatusOK, "mail/folder-empty", &EmptyMailboxRenderData{
			IMAPBaseRenderData: *ibase,
			Count:              count,
		})
	}

	var removed int
	err = ctx.DoIMAP(func(c *imapclient.Client) error {
		var err error
		removed, err = emptyMailbox(c, mboxName)
		// Expunge can also remove messages already marked deleted elsewhere.
		p.messagesRemoved(ctx.Login.Username(), mboxName)
		return err
	})
	if err != nil {
		return err
	}

	ctx.Notify(emptiedNotice(ctx, removed))
	return ctx.Redirect(http.StatusFound, ctx.NextOr(mailboxURL(ctx, mboxName)))
}

// The count keeps a caller from saying "deleted" over an already empty folder.
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
	if err := storeFlags(c, mboxName, seq, imap.StoreFlagsAdd, []imap.Flag{imap.FlagDeleted}); err != nil {
		return 0, fmt.Errorf("failed to add deleted flag: %w", err)
	}
	if err := c.Expunge().Close(); err != nil {
		return 0, fmt.Errorf("failed to expunge mailbox: %w", err)
	}
	return n, nil
}

func emptiedNotice(ctx *web.Context, removed int) web.Notice {
	if removed == 0 {
		return web.Notice{Kind: web.NoticeWarning, Text: ctx.T("notice.alreadyempty")}
	}
	return web.Notice{Kind: web.NoticeDone, Text: ctx.Tf("notice.emptied", removed)}
}

func (p *plugin) handleEmptyAllMailbox(ctx *web.Context) error {
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
	for _, s := range ctx.Logins() {
		err := s.DoIMAP(func(c *imapclient.Client) error {
			folder, err := p.resolveRole(c, s.Username(), role)
			if err != nil {
				return err
			}
			n, err := emptyMailbox(c, folder)
			p.messagesRemoved(s.Username(), folder)
			removed += n
			return err
		})
		p.viewForgotten(s.Username(), "#"+role)
		if err != nil {
			failed = append(failed, s.Username()+": "+err.Error())
		}
	}
	back := ctx.NextOr("/mailbox/" + url.PathEscape(role) + "?all=1")
	if len(failed) > 0 {
		ctx.Notify(web.Notice{Kind: web.NoticeFailed, Text: fmt.Sprintf(ctx.T("form.saverefused"), strings.Join(failed, "; "))})
		return ctx.Redirect(http.StatusFound, back)
	}

	ctx.Notify(emptiedNotice(ctx, removed))
	return ctx.Redirect(http.StatusFound, back)
}

func (p *plugin) handleDelete(ctx *web.Context) error {
	mboxName, _, uids, err := folderSelection(ctx)
	if err != nil {
		return err
	}

	back := ctx.NextOr(mailboxURL(ctx, mboxName))
	if len(uids) == 0 {
		return nothingSelected(ctx, back)
	}

	var left int
	return runAct(ctx, folderRefs(ctx.Login.Username(), mboxName, uids), mailAct{
		announce: p.leaving,
		do: func(c *imapclient.Client, key rowRef, uids []imap.UID) error {
			defer p.messagesRemoved(key.account, key.mailbox)
			if err := deleteMessages(c, key.mailbox, imap.UIDSetNum(uids...)); err != nil {
				return err
			}
			left = int(c.Mailbox().NumMessages)
			return nil
		},
	}, func(done int) web.Notice {
		notice := web.Notice{Kind: web.NoticeDone, Text: ctx.Tf("notice.deleted", done)}
		// A whole page ticked with more behind: the rest is offered on a page, never taken on its own.
		if done >= web.PerPage(ctx) && left > 0 {
			notice.Actions = []*web.NoticeAction{{
				Label: ctx.Tf("notice.deleteall", left),
				Path:  ctx.AccountPath("/mailbox/" + url.PathEscape(mboxName) + "/empty"),
			}}
		}
		return notice
	}, landOn(ctx, back))
}

// Anything but a narrowing gets the whole page (TODO 126).
func listTemplate(ctx *web.Context) string {
	if ctx.PartialFor("main") {
		return "message-list"
	}
	return "mail/mailbox"
}

// StarRenderData is one message's star, re-rendered when a mark is set from a row.
type StarRenderData struct {
	web.BaseRenderData
	Mailbox string
	Account template.URL
	UID     imap.UID
	Flagged bool
	Color   string
	Next    string
	// G is what the star's words are read from: the page it is in, or this answer alone.
	G interface{ T(string) string }
}

func (p *plugin) handleSetFlags(ctx *web.Context) error {
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
		// A single star renders from what was just stored, without rebuilding the listing.
		// A star the list is narrowed to, once changed, takes the row out of the list.
		back := ctx.NextOr(mailboxURL(ctx, mboxName))
		oneStar := ctx.Partial() && len(uids) == 1
		leaves := oneStar && web.StarLeaves(ctx.FormValue("next"), color[0])
		return runAct(ctx, folderRefs(ctx.Login.Username(), mboxName, uids), p.colourAct(add, del), func(int) web.Notice {
			if !leaves {
				return web.Notice{}
			}
			action := "/message/" + url.PathEscape(mboxName) + "/flag?account=" + web.QueryValue(ctx.Login.Username())
			return web.StarNotice(ctx, color[0], action, url.Values{
				"uids": {fmt.Sprint(uids[0])}, "color": {ctx.FormValue("was")}, "next": {back}})
		}, func(done bool) error {
			switch {
			case !oneStar:
				return ctx.Redirect(http.StatusFound, back)
			case leaves || !done:
				return ctx.Relocate(back)
			}
			star := &StarRenderData{
				BaseRenderData: *web.NewBaseRenderData(ctx),
				Mailbox:        mboxName,
				Account:        template.URL(AccountQuery(ctx.Login.Username())),
				UID:            uids[0],
				Flagged:        color[0] != "",
				Color:          color[0],
				Next:           ctx.FormValue("next"),
			}
			star.G = star
			return ctx.Render(http.StatusOK, "row-star", star)
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
	return runAct(ctx, folderRefs(ctx.Login.Username(), mboxName, uids), p.flagAct(op, l), func(int) web.Notice {
		return flaggedNotice(ctx, formParams["uids"], flags, op, mboxName, target)
	}, landOn(ctx, target))
}

func onlySeen(flags []string) bool {
	for _, f := range flags {
		if !strings.EqualFold(f, string(imap.FlagSeen)) {
			return false
		}
	}
	return len(flags) > 0
}

// Only an add or a remove has an exact inverse to undo.
func flaggedNotice(ctx *web.Context, uids, flags []string, op imap.StoreFlagsOp, mboxName, target string) web.Notice {
	if ctx.FormValue("undo") != "" {
		return web.Notice{Kind: web.NoticeDone, Text: ctx.T("notice.undone")}
	}
	// Read and unread show in the row's weight; a colour is worth an undo.
	if onlySeen(flags) {
		return web.Notice{}
	}
	notice := web.Notice{Kind: web.NoticeDone, Text: ctx.Tf("notice.flagged", len(uids))}
	inverse := map[imap.StoreFlagsOp]string{imap.StoreFlagsAdd: "remove", imap.StoreFlagsDel: "add"}[op]
	if inverse != "" {
		fields := url.Values{"uids": uids, "flags": flags, "action": {inverse}, "next": {target}}
		notice.Actions = []*web.NoticeAction{ctx.Undo(ctx.AccountPath("/message/"+url.PathEscape(mboxName)+"/flag"), fields)}
	}
	return notice
}

func textQueryOffered(query string, headersOnly bool) string {
	if !headersOnly {
		return ""
	}
	return search.Parse(query).Widened()
}

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

// A merged view spans accounts, so a selection names each row by account, folder and UID.
type rowRef struct {
	account, mailbox string
	uid              imap.UID
	// origin is the folder a moved message came from, carried only by an undo's reference.
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

func grouped(refs []rowRef) map[rowRef][]imap.UID {
	out := map[rowRef][]imap.UID{}
	for _, r := range refs {
		key := rowRef{account: r.account, mailbox: r.mailbox, origin: r.origin}
		out[key] = append(out[key], r.uid)
	}
	return out
}

func (p *plugin) handleUnifiedAct(ctx *web.Context) error {
	role, err := url.PathUnescape(ctx.Param("role"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}
	params, err := ctx.FormParams()
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}
	refs, err := p.mergedSelection(ctx, role, params)
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
		act = p.moveAct(func(c *imapclient.Client, key rowRef) (string, error) {
			return p.resolveRole(c, key.account, to)
		}, &landed)
	case "flag":
		add, del := FlagColorFlags(ctx.FormValue("color"))
		if add == nil && del == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "unknown flag colour")
		}
		act = p.colourAct(add, del)
	case "read":
		act = p.flagAct(imap.StoreFlagsAdd, []imap.Flag{imap.FlagSeen})
	case "unread":
		act = p.flagAct(imap.StoreFlagsDel, []imap.Flag{imap.FlagSeen})
	default:
		return echo.NewHTTPError(http.StatusBadRequest, "unknown action")
	}
	each := act.do
	act.do = func(c *imapclient.Client, key rowRef, uids []imap.UID) error {
		defer p.accountChanged(key.account)
		return each(c, key, uids)
	}
	return runAct(ctx, refs, act, func(done int) web.Notice {
		switch action {
		case "move":
			fields := url.Values{"next": {back}}
			for _, r := range landed {
				fields.Add("refs", r.String())
			}
			return movedNotice(ctx, done, ctx.T("aside."+strings.ToLower(to)), "/mailbox/"+url.PathEscape(to)+"?all=1", len(landed),
				"/mailbox/"+url.PathEscape(to)+"/all/act?action=move&to="+web.QueryValue(role), fields)
		case "read", "unread":
			// The rows change weight in view, so no line is needed.
			return web.Notice{}
		}
		return web.Notice{Kind: web.NoticeDone, Text: ctx.Tf("notice.changed", done)}
	}, landOn(ctx, back))
}

func nothingSelected(ctx *web.Context, back string) error {
	ctx.Notify(web.Notice{Kind: web.NoticeWarning, Text: ctx.T("notice.nomessages")})
	return ctx.Redirect(http.StatusFound, back)
}

// A server's no is a notice naming who refused, never an error page.
func runAct(ctx *web.Context, refs []rowRef, act mailAct, done func(n int) web.Notice, land func(done bool) error) error {
	n, failed := actOn(ctx, refs, act)
	// Only when no server answered is it the upstream page; the first failure stands for all.
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
		ctx.Notify(web.Notice{Kind: web.NoticeFailed, Text: fmt.Sprintf(ctx.T("form.saverefused"), strings.Join(refused, "; "))})
		return land(false)
	}
	ctx.Notify(done(n))
	return land(true)
}

func landOn(ctx *web.Context, back string) func(bool) error {
	return func(bool) error { return ctx.Redirect(http.StatusFound, back) }
}

// A scoped term is named by its field, as the reader asked for it by clicking a sender.
func searchFilters(ctx *web.Context) []web.Filter {
	var out []web.Filter
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
