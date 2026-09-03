package alborzbase

import (
	"fmt"
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
			return render(http.StatusUnprocessableEntity, ctx.T("form.destinationneeded"))
		}
		if name == "" {
			return render(http.StatusUnprocessableEntity, ctx.T("form.nameneeded"))
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
			return render(http.StatusUnprocessableEntity, err.Error())
		}

		listings.evictAll(selectedAccount)
		destination := fmt.Sprintf("/mailbox/%s", url.PathEscape(fullName))
		if len(ctx.Sessions()) > 1 {
			destination += "?account=" + alborz.AddressParam(selectedAccount)
		}
		return ctx.Redirect(http.StatusFound, destination)
	}

	return render(http.StatusOK, "")
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
			return ctx.Render(http.StatusUnprocessableEntity, "delete-mailbox.html", &DeleteMailboxRenderData{
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

// handleRefreshMailbox drops what is cached for the account and returns
// to the page that asked. The IDLE watcher already evicts on what the
// server announces; this is for a server without IDLE, where a reader
// who knows something arrived has no other way to say so.
func handleRefreshMailbox(ctx *alborz.Context) error {
	mboxName, err := mailboxRef(ctx)
	if err != nil {
		return err
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
		ctx.Session.PutNotice(ctx.T("notice.nomessages"))
		return ctx.Redirect(http.StatusFound, mailboxURL(ctx, mboxName))
	}

	to := formOrQueryParam(ctx, "to")
	if to == "" {
		ctx.Session.PutNotice(ctx.T("notice.nodestination"))
		return ctx.Redirect(http.StatusFound, mailboxURL(ctx, mboxName))
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
	return ctx.Redirect(http.StatusFound, ctx.NextOr(mailboxURL(ctx, mboxName)))
}

// handleEmptyMailbox expunges everything in a folder, the standard
// one-click cleanup for Junk and Trash.
func handleEmptyMailbox(ctx *alborz.Context) error {
	mboxName, err := mailboxRef(ctx)
	if err != nil {
		return err
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
		ctx.Session.PutNotice(ctx.T("notice.nomessages"))
		return ctx.Redirect(http.StatusFound, mailboxURL(ctx, mboxName))
	}

	err = ctx.DoIMAP(func(c *imapclient.Client) error {
		return deleteMessages(c, mboxName, imap.UIDSetNum(uids...))
	})
	if err != nil {
		return err
	}

	listings.evict(ctx.Session.Username(), mboxName)
	ctx.Session.PutNotice(ctx.Tf("notice.deleted", len(uids)))
	return ctx.Redirect(http.StatusFound, ctx.NextOr(mailboxURL(ctx, mboxName)))
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

	if next := ctx.NextOr(""); next != "" {
		return ctx.Redirect(http.StatusFound, next)
	}
	if len(uids) != 1 || (op == imap.StoreFlagsDel && len(l) == 1 && l[0] == imap.FlagSeen) {
		// Redirecting to the message view would mark the message as read again
		return ctx.Redirect(http.StatusFound, mailboxURL(ctx, mboxName))
	}
	return ctx.Redirect(http.StatusFound, ctx.AccountPath(fmt.Sprintf("/message/%v/%v", url.PathEscape(mboxName), uids[0])))
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
