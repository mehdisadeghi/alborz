package web

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
	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/mail/imapsearch"
	"git.mehdix.org/alborz/search"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-imap/v2"
)

type MailboxRenderData struct {
	IMAPBaseRenderData
	Messages []mail.IMAPMessage
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
	// SelectForm is the form a row's checkbox belongs to; empty where rows offer none.
	SelectForm string
	// ListPath is the list a row's sender narrows; empty for the page's own.
	ListPath string
}

// A merged folder is a role in every account, or a folder by name in the accounts that hold it.
func (p *plugin) handleUnifiedMailbox(ctx *web.Context) error {
	role := mailboxRef(ctx)

	// account is a property of the merge, not of any single server's order.
	ask, settings, to, err := mailboxAsk(ctx, "account")
	if err != nil {
		return err
	}
	if to != "" {
		return ctx.FoundRedirect(to)
	}
	fed, err := merging(ctx, ctx.Logins(), role)
	if err != nil {
		return err
	}
	// Every account left out of a role is an empty merged folder, not a missing one.
	list, found, down := &mail.Listing{Page: ask.Page}, true, []string(nil)
	if len(fed) > 0 {
		// A search costs a round trip per account, so it most wants the cache.
		list, found, down, err = p.Merged(ctx.Request().Context(), fed, settings, role, ask, ctx.Request().Method == http.MethodGet)
		if err != nil {
			return err
		}
	}
	ctx.Unreachable(down)
	if !found {
		return alborz.NotFound("notfound.folder", role)
	}
	named := role
	if slices.Contains(mail.Roles, role) {
		named = ctx.T("aside." + strings.ToLower(role))
	}
	title := named
	if ask.View != "" {
		title = viewTitle(ctx, ask.View)
	}

	data := listPage(ctx, ask, list)
	data.IMAPBaseRenderData = IMAPBaseRenderData{
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(fmt.Sprintf(ctx.T("mailbox.allaccounts"), title)),
		// The role is the name the rail marks and the refresh form posts to; Label is what is read.
		Mailbox:         &mail.MailboxStatus{StatusData: &imap.StatusData{Mailbox: role}, Label: title},
		ListView:        ask.View,
		SidebarAccounts: p.sidebarAccounts(ctx),
	}
	data.GlobalData.HasRail = true
	data.Crumb = []CrumbLink{{Label: named, URL: ctx.Path("mail.mailbox", role)}}
	data.Outgoing = role == "Sent" || role == "Drafts"
	data.Merged, data.Role = true, role
	return ctx.Render(http.StatusOK, "mail/mailbox", data)
}

// merging is the accounts whose folder for role feeds the merged folder; only a role can be left out (ADR 80).
func merging(ctx *web.Context, logins []*alborz.Login, role string) ([]*alborz.Login, error) {
	var fed []*alborz.Login
	for _, s := range logins {
		settings, err := mail.LoadSettings(ctx.Request().Context(), s.Store())
		// A silent server keeps its place, and the merge names it as not answering (ADR 1).
		var upstream alborz.UpstreamError
		if err != nil && !errors.As(err, &upstream) {
			return nil, err
		}
		if err != nil || settings.Merges(role) {
			fed = append(fed, s)
		}
	}
	return fed, nil
}

func listPage(ctx *web.Context, ask mail.ListAsk, l *mail.Listing) *MailboxRenderData {
	return &MailboxRenderData{
		Messages:      l.Rows,
		Pager:         web.NewPager(ctx.Request().URL, l.Page, l.Total, web.PerPageChoices(ctx)),
		Total:         l.Total,
		Query:         ask.Query,
		Everywhere:    mail.Everywhere(ask.Query),
		Filters:       searchFilters(ctx),
		FilterRows:    web.ViewRows(ctx, ask.View, true, ctx.WithoutParam("view")),
		TextQuery:     mail.TextQuery(ask.Query, l.HeadersOnly),
		Sort:          ask.Sort,
		SortDir:       map[bool]string{true: "desc", false: "asc"}[ask.Descending()],
		SortSupported: l.SortSupported,
		PreferHTML:    ctx.Reading().PreferHTML,
		SelectForm:    "messages-form",
	}
}

func (p *plugin) handleGetMailbox(ctx *web.Context) error {
	if ctx.Unified {
		return p.handleUnifiedMailbox(ctx)
	}

	mboxName := mailboxRef(ctx)

	// thread is not a sort column, but travels in the same parameter.
	ask, settings, to, err := mailboxAsk(ctx, mail.SortThread)
	if err != nil {
		return err
	}
	if to != "" {
		return ctx.FoundRedirect(to)
	}
	if raw := ctx.QueryParam("thread"); raw != "" {
		n, perr := strconv.ParseUint(raw, 10, 32)
		if perr != nil || n == 0 {
			return web.NewError(http.StatusBadRequest, "invalid thread")
		}
		ask.Thread = imap.UID(n)
	}
	list, err := p.Folder(ctx.Request().Context(), ctx.Login, settings, mboxName, ask, ctx.Request().Method == http.MethodGet, ctx.Reading().PreferHTML)
	if err != nil {
		return err
	}
	sb := list.Rail

	ibase := assembleIMAPBase(ctx, web.NewBaseRenderData(ctx), mboxName, sb, ask.View)
	ibase.SidebarAccounts = p.sidebarAccounts(ctx)
	title := viewTitle(ctx, ask.View)
	if ask.View == "" && ibase.Mailbox != nil {
		title = ibase.Mailbox.Label
	}
	if ask.View != "" && ibase.Mailbox != nil {
		ibase.Mailbox.Label = title
	}
	ibase.WithTitle(title)

	data := listPage(ctx, ask, list)
	data.IMAPBaseRenderData = *ibase
	data.Crumb = mailboxCrumb(sb.Mailboxes, mboxName, ctx.Login.Username(), func(name string) string { return ctx.Path("mail.mailbox", name) })
	data.Outgoing = mail.Outgoing(sb.Mailboxes, mboxName)
	data.ThreadSupported = list.ThreadAlgorithm != ""
	data.Threaded = ask.Sort == mail.SortThread && list.ThreadAlgorithm != ""
	return ctx.Render(http.StatusOK, "mail/mailbox", data)
}

func mailboxAsk(ctx *web.Context, also string) (mail.ListAsk, *mail.Settings, string, error) {
	ask, err := readListAskAt(ctx, ctx.QueryParams(), also)
	if err != nil {
		return mail.ListAsk{}, nil, "", err
	}
	to := searchInstead(ctx, ask)
	if to != "" {
		return ask, nil, to, nil
	}
	settings, err := mail.LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	return ask, settings, "", err
}

// A star view keeps the narrowing and order already in force.
func starViewPath(ctx *web.Context, view string) string {
	kept := url.Values{"view": {view}}
	for _, name := range []string{"query", "sort", "dir", "ipp"} {
		if value := ctx.QueryParam(name); value != "" {
			kept.Set(name, value)
		}
	}
	return ctx.AccountPath(web.QueryPath(ctx.Path("mail.search"), kept))
}

// readListAskAt reads a list's address, the request's or the one an object was opened from.
func readListAskAt(ctx *web.Context, q url.Values, also string) (mail.ListAsk, error) {
	state := web.ListStateFrom(q)
	page, err := state.Page(ctx)
	if err != nil {
		return mail.ListAsk{}, err
	}
	view, err := readViewValue(state.View)
	if err != nil {
		return mail.ListAsk{}, err
	}
	if err := readStars(q.Get("query"), view); err != nil {
		return mail.ListAsk{}, err
	}
	sortKey, sortDir, err := readSortValues(state.Sort, state.Direction, also)
	if err != nil {
		return mail.ListAsk{}, err
	}
	return mail.ListAsk{Page: page, Query: state.Query, View: view, Sort: sortKey, Dir: sortDir}, nil
}

func searchInstead(ctx *web.Context, ask mail.ListAsk) string {
	if folder, everywhere := imapsearch.Scope(search.Parse(ask.Query)); folder != "" || everywhere {
		return ctx.AccountPath(ctx.Path("mail.search") + "?query=" + web.QueryValue(ask.Query))
	}
	// A star is on the message, not in the folder, so its view is the search page's fan-out.
	if mail.StarView(ask.View) {
		return starViewPath(ctx, ask.View)
	}
	return ""
}

// also is the one order the page has beyond the columns.
func readSort(q url.Values, also string) (sortKey, sortDir string, err error) {
	state := web.ListStateFrom(q)
	return readSortValues(state.Sort, state.Direction, also)
}

func readSortValues(sortKey, sortDir, also string) (string, string, error) {
	if !mail.KnownSort(sortKey) && sortKey != also {
		return "", "", web.NewError(http.StatusBadRequest, "invalid sort order")
	}
	if sortDir != "" && sortDir != "asc" && sortDir != "desc" {
		return "", "", web.NewError(http.StatusBadRequest, "invalid sort direction")
	}
	return sortKey, sortDir, nil
}

// A view is a link in the rail, never typed, so an unknown one is refused.
func readView(q url.Values) (string, error) {
	return readViewValue(web.ListStateFrom(q).View)
}

func readViewValue(view string) (string, error) {
	if !mail.KnownView(view) {
		return "", web.NewError(http.StatusBadRequest, "unknown view")
	}
	return view, nil
}

// readStars refuses a query whose star terms and view name two colours, as every section's search does;
// the search itself is listCriteria's.
func readStars(query, view string) error {
	if !mail.StarView(view) {
		view = ""
	}
	if _, _, err := web.StarFilter(search.Parse(query), view); err != nil {
		return web.NewError(http.StatusBadRequest, err.Error())
	}
	return nil
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

func (p *plugin) newMailboxRenderData(ctx *web.Context, title string) (*NewMailboxRenderData, error) {
	ibase, err := p.newIMAPBaseRenderData(ctx, web.NewBaseRenderData(ctx))
	if err != nil {
		return nil, err
	}
	ibase.WithTitle(title)
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
	return &NewMailboxRenderData{
		IMAPBaseRenderData: *ibase,
		SelectedAccount:    selectedAccount,
		SelectedLocation:   selectedLocation,
		LocationGroups:     locationGroups,
	}, nil
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
		sb, err := p.Rail(login)
		if err != nil {
			ctx.Logger().Printf("folder locations for %q: %v", login.Username(), err)
			continue
		}
		ib := assembleIMAPBase(ctx, &web.BaseRenderData{}, "", sb.Clone(), "")
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
	data, err := p.newMailboxRenderData(ctx, ctx.T("folder.create"))
	if err != nil {
		return err
	}
	name := data.Name
	render := func(status int, errText string) error {
		data.Name = name
		data.Refused(errText)
		return ctx.Render(status, "mail/folder-create", data)
	}

	if ctx.Request().Method == http.MethodPost {
		name = ctx.FormValue("name")
		data.SelectedLocation = ctx.FormValue("location")
		location, account := locationByKey(data.LocationGroups, data.SelectedLocation)
		if location == nil {
			return render(http.StatusUnprocessableEntity, ctx.T("form.destinationneeded"))
		}
		data.SelectedAccount = account
		if name == "" {
			return render(http.StatusUnprocessableEntity, ctx.T("form.nameneeded"))
		}

		selectedLogin := ctx.LoginFor(data.SelectedAccount)
		if selectedLogin == nil {
			return web.NewError(http.StatusBadRequest, "not signed in to that account")
		}
		fullName := location.folder(name)
		if err := p.CreateFolder(selectedLogin, fullName); err != nil {
			return render(http.StatusUnprocessableEntity, err.Error())
		}
		ctx.Made(ctx.T("notice.foldercreated"), name, "", ctx.Request().URL.RequestURI())
		return ctx.FoundRedirect(folderURL(ctx, data.SelectedAccount, fullName))
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
	ibase.WithTitle(fmt.Sprintf(ctx.T("folder.deletetitle"), mbox.Name()))
	render := func(status int) error {
		return ctx.Render(status, "mail/folder-delete", &DeleteMailboxRenderData{IMAPBaseRenderData: *ibase})
	}

	if ctx.Request().Method == http.MethodPost {
		// Some deletions are refused; claiming success would send the reader looking for a folder still there.
		if err := p.DeleteFolder(ctx.Request().Context(), ctx.Login, mbox.Name()); err != nil {
			ibase.Refused(err.Error())
			return render(http.StatusUnprocessableEntity)
		}
		return ctx.NoticeRedirect(ctx.T("notice.mailboxdeleted"), ctx.URL("mail.mailbox", "INBOX"))
	}

	return render(http.StatusOK)
}

// The form names the folder, not its path, so a rename cannot move it under another.
func (p *plugin) handleRenameMailbox(ctx *web.Context) error {
	ibase, err := p.newIMAPBaseRenderData(ctx, web.NewBaseRenderData(ctx))
	if err != nil {
		return err
	}
	mbox := ibase.Mailbox
	ibase.WithTitle(fmt.Sprintf(ctx.T("folder.renametitle"), mbox.Label))
	name := mbox.Label
	render := func(status int, errText string) error {
		ibase.Refused(errText)
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
		return ctx.FoundRedirect(folderURL(ctx, ctx.Login.Username(), old))
	}
	if err := p.RenameFolder(ctx.Request().Context(), ctx.Login, old, renamed); err != nil {
		return render(http.StatusUnprocessableEntity, err.Error())
	}
	return ctx.NoticeRedirect(fmt.Sprintf(ctx.T("notice.folderrenamed"), name), folderURL(ctx, ctx.Login.Username(), renamed))
}

// For a server without IDLE, a reader who knows mail arrived has no other way to say so.
func (p *plugin) handleRefreshMailbox(ctx *web.Context) error {
	mboxName := mailboxRef(ctx)
	if ctx.Unified {
		for _, s := range ctx.Logins() {
			p.ReloadRole(s.Username(), mboxName)
		}
		return ctx.QuietRedirect(ctx.NextOr(ctx.Path("mail.mailbox", mboxName)))
	}
	p.Reload(ctx.Login.Username(), mboxName)
	return ctx.QuietRedirect(ctx.NextOr(ctx.URL("mail.mailbox", mboxName)))
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
	mboxName, _, uids, refs, back, err := selectedMessages(ctx)
	if err != nil {
		return err
	}

	// A menu holds actions; the destination is chosen on a page this route answers.
	to := formOrQueryParam(ctx, "to")
	if to == "" {
		ibase, err := p.newIMAPBaseRenderData(ctx, web.NewBaseRenderData(ctx))
		if err != nil {
			return err
		}
		ibase.WithTitle(ctx.T("folder.moveask"))
		return ctx.Render(http.StatusOK, "mail/move", &MoveRenderData{
			IMAPBaseRenderData: *ibase,
			UIDs:               uids,
			Next:               ctx.FormValue("next"),
		})
	}

	var landed []mail.Ref
	return runAct(ctx, refs, p.Move(to, &landed), func(done int) web.Notice {
		label := to
		if role := (&mail.MailboxInfo{ListData: &imap.ListData{Mailbox: to}}).Role(); role != "" {
			label = ctx.T("aside." + role)
		}
		fields := url.Values{"to": {mboxName}, "next": {back}}
		for _, r := range landed {
			fields.Add("uids", fmt.Sprint(r.UID))
		}
		return movedNotice(ctx, done, label, ctx.URL("mail.mailbox", to), len(landed),
			ctx.URL("mail.messages.move", to), fields)
	}, landOn(ctx, back))
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
	mboxName := mailboxRef(ctx)

	if ctx.Request().Method == http.MethodGet {
		ibase, err := p.newIMAPBaseRenderData(ctx, web.NewBaseRenderData(ctx))
		if err != nil {
			return err
		}
		ibase.WithTitle(fmt.Sprintf(ctx.T("folder.emptytitle"), mboxName))
		count := 0
		if ibase.Mailbox.NumMessages != nil {
			count = int(*ibase.Mailbox.NumMessages)
		}
		return ctx.Render(http.StatusOK, "mail/folder-empty", &EmptyMailboxRenderData{
			IMAPBaseRenderData: *ibase,
			Count:              count,
		})
	}

	removed, err := p.Empty(ctx.Request().Context(), ctx.Login, mboxName)
	if err != nil {
		return err
	}

	return ctx.NotifyRedirect(emptiedNotice(ctx, removed), ctx.NextOr(ctx.URL("mail.mailbox", mboxName)))
}

func emptiedNotice(ctx *web.Context, removed int) web.Notice {
	if removed == 0 {
		return web.Notice{Kind: web.NoticeWarning, Text: ctx.T("notice.alreadyempty")}
	}
	return web.Notice{Kind: web.NoticeDone, Text: ctx.Tf("notice.emptied", removed)}
}

func (p *plugin) handleEmptyAllMailbox(ctx *web.Context) error {
	role := ctx.Param("role")
	if !slices.Contains(mail.Roles, role) {
		return alborz.NotFound("notfound.folder", role)
	}

	fed, err := merging(ctx, ctx.Logins(), role)
	if err != nil {
		return err
	}
	var failed []string
	var removed int
	for _, s := range fed {
		n, err := p.EmptyRole(s, role)
		removed += n
		if err != nil {
			failed = append(failed, s.Username()+": "+err.Error())
		}
	}
	back := ctx.NextOr(ctx.Path("mail.mailbox", role) + "?all=1")
	if len(failed) > 0 {
		return ctx.NotifyRedirect(web.Notice{Kind: web.NoticeFailed, Text: fmt.Sprintf(ctx.T("form.saverefused"), strings.Join(failed, "; "))}, back)
	}

	return ctx.NotifyRedirect(emptiedNotice(ctx, removed), back)
}

func (p *plugin) handleDelete(ctx *web.Context) error {
	mboxName, _, _, refs, back, err := selectedMessages(ctx)
	if err != nil {
		return err
	}

	var left int
	return runAct(ctx, refs, p.Delete(&left), func(done int) web.Notice {
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
	mboxName, formParams, uids, refs, back, err := selectedMessages(ctx)
	if err != nil {
		return err
	}

	if color, ok := formParams["color"]; ok {
		add, del := mail.FlagColorFlags(color[0])
		if add == nil && del == nil {
			return web.NewError(http.StatusBadRequest, "unknown flag colour")
		}
		// A single star renders from what was just stored, without rebuilding the listing.
		// A star the list is narrowed to, once changed, takes the row out of the list.
		oneStar := ctx.Fragment() && !ctx.PartialFor("main") && len(uids) == 1
		leaves := oneStar && web.StarLeaves(ctx.FormValue("next"), color[0])
		return runAct(ctx, refs, p.Colour(add, del), func(int) web.Notice {
			if ctx.FormValue("undo") != "" {
				return web.Notice{Kind: web.NoticeDone, Text: ctx.T("notice.undone")}
			}
			if !leaves {
				return web.Notice{}
			}
			action := web.AccountURL(ctx.Path("mail.messages.flag", mboxName), ctx.Login.Username())
			return web.StarNotice(ctx, color[0], action, url.Values{
				"uids": {fmt.Sprint(uids[0])}, "color": {ctx.FormValue("was")}, "next": {back}})
		}, func(done bool) error {
			switch {
			case !oneStar:
				return ctx.FoundRedirect(back)
			case leaves || !done:
				return ctx.Redraw(back)
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
			target = ctx.URL("mail.mailbox", mboxName)
		} else {
			target = ctx.URL("mail.message", mboxName, uids[0])
		}
	}
	return runAct(ctx, refs, p.Flag(op, l), func(int) web.Notice {
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
	params, err := formParams(ctx)
	if err != nil {
		return err
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
	var landed []mail.Ref
	to := formOrQueryParam(ctx, "to")
	var act mail.Act
	switch action {
	case "move":
		act = p.MoveToRole(to, &landed)
	case "flag":
		add, del := mail.FlagColorFlags(ctx.FormValue("color"))
		if add == nil && del == nil {
			return web.NewError(http.StatusBadRequest, "unknown flag colour")
		}
		act = p.Colour(add, del)
	case "read":
		act = p.Flag(imap.StoreFlagsAdd, []imap.Flag{imap.FlagSeen})
	case "unread":
		act = p.Flag(imap.StoreFlagsDel, []imap.Flag{imap.FlagSeen})
	default:
		return web.NewError(http.StatusBadRequest, "unknown action")
	}
	return runAct(ctx, refs, p.Changing(act), func(done int) web.Notice {
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
	return ctx.NotifyRedirect(web.Notice{Kind: web.NoticeWarning, Text: ctx.T("notice.nomessages")}, back)
}

// A server's no is a notice naming who refused, never an error page.
func runAct(ctx *web.Context, refs []mail.Ref, act mail.Act, done func(n int) web.Notice, land func(done bool) error) error {
	// Only when no server answered is it the upstream page.
	n, failed, err := mail.ActOn(ctx.Logins(), refs, act)
	if err != nil {
		return err
	}
	if len(failed) > 0 {
		var refused []string
		for _, f := range failed {
			if f.Err == nil {
				refused = append(refused, f.Account)
			} else {
				refused = append(refused, f.Account+": "+f.Err.Error())
			}
		}
		ctx.Notify(web.Notice{Kind: web.NoticeFailed, Text: fmt.Sprintf(ctx.T("form.saverefused"), strings.Join(refused, "; "))})
		return land(false)
	}
	ctx.Notify(done(n))
	return land(true)
}

func landOn(ctx *web.Context, back string) func(bool) error {
	return func(bool) error { return ctx.FoundRedirect(back) }
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
