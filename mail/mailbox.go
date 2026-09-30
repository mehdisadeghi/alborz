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

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail/search"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
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
	merged, found, down, err := p.mergedWindow(ctx.Request().Context(), ctx.Logins(), role, ask, settings, ctx.Request().Method == http.MethodGet)
	if err != nil {
		return err
	}
	ctx.Unreachable(down)
	if !found {
		return alborz.NotFound("notfound.folder", role)
	}
	page, cut := cutPage(merged, ask.page)
	rows, down, err := p.wholePage(ctx.Request().Context(), ctx.Logins(), role, key, cut, class, bound)
	if err != nil {
		return err
	}
	ctx.Unreachable(down)
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
	data.Crumb = []CrumbLink{{Label: named, URL: ctx.Path("mail.mailbox", role)}}
	data.Outgoing = role == "Sent" || role == "Drafts"
	data.Merged, data.Role = true, role
	return ctx.Render(http.StatusOK, listTemplate(ctx), data)
}

func listPage(ctx *web.Context, spec listingSpec, page alborz.Page, e *listingEntry, rows []IMAPMessage) *MailboxRenderData {
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
			return web.NewError(http.StatusBadRequest, "invalid thread")
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
		e, err = p.cachedListing(ctx.Request().Context(), ctx.Login, key, ask.page.Size, spec,
			func(*imapclient.Client) (string, error) { return mboxName, nil }, fetch)
	} else {
		e, err = p.loadListing(ctx.Request().Context(), ctx.Login, key, ask.page.Size, spec, fetch)
	}
	if err != nil {
		return err
	}
	sb, msgs := p.railFor(ctx.Login, mboxName, e.sb), e.msgs
	page := alborz.Page{Number: e.page, Size: e.perPage}
	// Bodies are fetched behind the page, so the next click asks the server nothing.
	if cacheable {
		login := ctx.Login
		p.prefetchBodies(login, ctx.Reading().PreferHTML, mboxName, e.validity(), e.msgs)
		if spec.sortKey != threadSort && spec.thread == 0 && page.Window() < e.total {
			p.prefetchPage(login, settings, spec, alborz.Page{Number: page.Number + 1, Size: page.Size})
		}
	}

	trust := p.newDeliveryTrust(ctx.Login, settings)
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
	data.Crumb = mailboxCrumb(sb.mailboxes, mboxName, ctx.Login.Username(), folderPath(ctx))
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
	return ctx.AccountPath(ctx.Path("mail.search") + "?" + web.Query(kept))
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
		return ctx.AccountPath(ctx.Path("mail.search") + "?query=" + web.QueryValue(ask.spec.query))
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
		return "", "", web.NewError(http.StatusBadRequest, "invalid sort order")
	}
	sortDir = q.Get("dir")
	if sortDir != "" && sortDir != "asc" && sortDir != "desc" {
		return "", "", web.NewError(http.StatusBadRequest, "invalid sort direction")
	}
	return sortKey, sortDir, nil
}

// A view is a link in the rail, never typed, so an unknown one is refused.
func readView(q url.Values) (string, error) {
	view := q.Get("view")
	if !KnownView(view) {
		return "", web.NewError(http.StatusBadRequest, "unknown view")
	}
	return view, nil
}

func viewTitle(ctx *web.Context, view string) string {
	switch view {
	case alborz.ViewStarred:
		return ctx.T("mailbox.starred")
	case alborz.ViewUnread:
		return ctx.T("mailbox.unread")
	}
	return fmt.Sprintf(ctx.T("mailbox.starcolor"), ctx.T("color."+view))
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
			return web.NewError(http.StatusBadRequest, "not signed in to that account")
		}
		fullName := location.folder(name)
		err := createFolder(selectedLogin, fullName)

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
	destination := ctx.Path("mail.mailbox", name)
	if len(ctx.Logins()) > 1 {
		destination += web.AccountQuery(account)
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
			return p.deleteFolder(c, ctx.Login.Username(), mbox.Name())
		}); err != nil {
			ibase.BaseRenderData.Refused(err.Error())
			return ctx.Render(http.StatusUnprocessableEntity, "mail/folder-delete", &DeleteMailboxRenderData{
				IMAPBaseRenderData: *ibase,
			})
		}
		ctx.PutNotice(ctx.T("notice.mailboxdeleted"))
		return ctx.Redirect(http.StatusFound, ctx.URL("mail.mailbox", "INBOX"))
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
		return renameFolder(c, old, renamed)
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
		return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.Path("mail.mailbox", mboxName)))
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
			ctx.URL("mail.messages.move", to), fields)
	}, landOn(ctx, target))
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
		removed, err = p.empty(c, ctx.Login.Username(), mboxName)
		return err
	})
	if err != nil {
		return err
	}

	ctx.Notify(emptiedNotice(ctx, removed))
	return ctx.Redirect(http.StatusFound, ctx.NextOr(mailboxURL(ctx, mboxName)))
}

func emptiedNotice(ctx *web.Context, removed int) web.Notice {
	if removed == 0 {
		return web.Notice{Kind: web.NoticeWarning, Text: ctx.T("notice.alreadyempty")}
	}
	return web.Notice{Kind: web.NoticeDone, Text: ctx.Tf("notice.emptied", removed)}
}

func (p *plugin) handleEmptyAllMailbox(ctx *web.Context) error {
	role := ctx.Param("role")
	if !slices.Contains(unifiedRoles, role) {
		return web.NewError(http.StatusNotFound,
			fmt.Sprintf("%q is not a unified folder", role))
	}

	var failed []string
	var removed int
	for _, s := range ctx.Logins() {
		n, err := p.emptyRole(s, role)
		removed += n
		if err != nil {
			failed = append(failed, s.Username()+": "+err.Error())
		}
	}
	back := ctx.NextOr(ctx.Path("mail.mailbox", role) + "?all=1")
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
			var err error
			left, err = p.removeMessages(c, key, uids)
			return err
		},
	}, func(done int) web.Notice {
		notice := web.Notice{Kind: web.NoticeDone, Text: ctx.Tf("notice.deleted", done)}
		// A whole page ticked with more behind: the rest is offered on a page, never taken on its own.
		if done >= web.PerPage(ctx) && left > 0 {
			notice.Actions = []*web.NoticeAction{{
				Label: ctx.Tf("notice.deleteall", left),
				Path:  ctx.URL("mail.mailbox.empty", mboxName),
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
			return web.NewError(http.StatusBadRequest, "unknown flag colour")
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
			action := ctx.Path("mail.messages.flag", mboxName) + "?account=" + web.QueryValue(ctx.Login.Username())
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
				Account:        template.URL(web.AccountQuery(ctx.Login.Username())),
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
			return web.NewError(http.StatusBadRequest, "missing 'flags' form parameter")
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
		return web.NewError(http.StatusBadRequest, "invalid 'action' value")
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
			target = ctx.URL("mail.message", mboxName, uids[0])
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
		notice.Actions = []*web.NoticeAction{ctx.Undo(ctx.URL("mail.messages.flag", mboxName), fields)}
	}
	return notice
}

func (p *plugin) handleUnifiedAct(ctx *web.Context) error {
	role := ctx.Param("role")
	params, err := ctx.FormParams()
	if err != nil {
		return web.NewError(http.StatusBadRequest, err)
	}
	refs, err := p.mergedSelection(ctx, role, params)
	if err != nil {
		return err
	}
	back := ctx.NextOr(ctx.Path("mail.mailbox", role) + "?all=1")
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
			return web.NewError(http.StatusBadRequest, "unknown flag colour")
		}
		act = p.colourAct(add, del)
	case "read":
		act = p.flagAct(imap.StoreFlagsAdd, []imap.Flag{imap.FlagSeen})
	case "unread":
		act = p.flagAct(imap.StoreFlagsDel, []imap.Flag{imap.FlagSeen})
	default:
		return web.NewError(http.StatusBadRequest, "unknown action")
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
			return movedNotice(ctx, done, ctx.T("aside."+strings.ToLower(to)), ctx.Path("mail.mailbox", to)+"?all=1", len(landed),
				ctx.Path("mail.role.act", to)+"?action=move&to="+web.QueryValue(role), fields)
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
	n, failed := actOn(ctx.Logins(), refs, act)
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
