package web

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/calendar"
	"git.mehdix.org/alborz/pim/dav"
	pimweb "git.mehdix.org/alborz/pim/web"
	"git.mehdix.org/alborz/search"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
)

type JournalRenderData struct {
	web.BaseRenderData
	Calendars  []dav.Collection
	Entries    []EntryRow
	View       string
	Filters    []web.Filter
	FilterRows []web.FilterRow
	Query      string
	Sorting    pimweb.Sorting
	Pager      web.Pager
	// SelectForm is the form a row's checkbox belongs to; empty where rows offer none.
	SelectForm string

	Invitations *pimweb.RailInvitations
}

// EntryRow is one row of the journal.
type EntryRow struct {
	Path, Account string
	Calendar      dav.Collection
	Summary       string
	// Date is DTSTART, the day the entry is for.
	Date time.Time
	// Added is CREATED; zero where the writer sent none.
	Added time.Time
	Star  string
	// AddedBy names who added an entry, where not the row's own account.
	AddedBy string
	// Href is the entry's page with the list carried along.
	Href string
}

var journalListParams = []string{"account", "cal", "query", "view", "category", "sort", "dir", "page", "ipp"}

// journalForm is the journal's selection form.
const journalForm = "journal-form"

// A journal reads newest first.
var entryColumns = []pim.Column[EntryRow]{
	{Key: "date", Desc: true, Value: func(row EntryRow) string { return pim.When(row.Date) }},
	{Key: "summary", Value: func(row EntryRow) string { return strings.ToLower(row.Summary) }},
	{Key: alborz.ViewStarred, Value: func(row EntryRow) string { return pim.StarredFirst(row.Star) }},
	{Key: "account", Value: func(row EntryRow) string { return strings.ToLower(row.Account) }},
	{Key: "calendar", Value: func(row EntryRow) string { return strings.ToLower(row.Calendar.Name) }},
	{Key: "added", Value: func(row EntryRow) string { return pim.When(row.Added) }},
}

func entryTie(row EntryRow) string {
	return strings.ToLower(row.Account + "\x00" + row.Calendar.Name + "\x00" + row.Summary)
}

// JournalList is the list page's rows in order, their journals, and what shaped them.
type JournalList struct {
	Rows      []EntryRow
	Calendars []dav.Collection
	// Items are the rows as a single entry's neighbours, in the same order.
	Items   []pimweb.Item
	Query   string
	View    string
	Sorting pimweb.Sorting
	Unread  []pim.Unread
}

func (p *plugin) journalList(ctx *web.Context) (JournalList, error) {
	text := ctx.QueryParam("query")
	view := ctx.QueryParam("view")
	if !validStarView(view) {
		return JournalList{}, web.NewError(http.StatusBadRequest, "no such view")
	}
	params := pimweb.RowParams(ctx, ctx.Path("calendar.journal"), pimweb.ListParams(ctx, journalListParams...))
	rows, calendars, unread, err := p.shownEntries(ctx, pimweb.Only(ctx, "cal"), view, ctx.QueryParam("category"), search.Parse(text), params)
	if err != nil {
		return JournalList{}, err
	}
	sorting, err := pimweb.Sort(ctx, rows, entryColumns, entryTie, journalListParams...)
	if err != nil {
		return JournalList{}, err
	}
	items := make([]pimweb.Item, len(rows))
	for i, row := range rows {
		items[i] = pimweb.Item{Path: row.Path, URL: row.Href}
	}
	return JournalList{Rows: rows, Items: items, Calendars: calendars, Query: text, View: view, Sorting: sorting, Unread: unread}, nil
}

// shownEntries are the rows of the entries a view, a category and a query find in the journals in the
// request's scope, and every journal for the rail, with the journals and entries it could not read;
// params go into each row's link.
func (p *plugin) shownEntries(ctx *web.Context, only map[string]bool, view, category string, q search.Query, params url.Values) ([]EntryRow, []dav.Collection, []pim.Unread, error) {
	q, starred, err := web.StarFilter(q, view)
	if err != nil {
		return nil, nil, nil, web.NewError(http.StatusBadRequest, err.Error())
	}
	accounts, err := p.pooledCalendars(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	calendars, sites, err := calendar.Visible(ctx.Request().Context(), accounts, ctx.URLAccount(), only, calendar.SupportsJournal, calendar.JournalVisibility)
	if err != nil {
		return nil, nil, nil, err
	}
	loc := web.UserLocation(ctx)
	query := calendar.JournalQuery()
	var rows []EntryRow
	var unread []pim.Unread
	for _, result := range pim.Present(p.DAV, dav.Across(ctx.Request().Context(), sites, pim.Site[*caldav.Client].Account, func(ctx context.Context, site pim.Site[*caldav.Client]) ([]caldav.CalendarObject, error) {
		return site.Client.QueryCalendar(ctx, site.Collection.Path, &query)
	})) {
		if result.Err != nil {
			unread = append(unread, pim.Unread{Collection: result.Site.Collection.Name, Err: result.Err})
			continue
		}
		for _, obj := range result.Value {
			if obj.Data == nil {
				unread = append(unread, calendar.Unparsed(result.Site.Collection.Name, &obj))
				continue
			}
			if entryShown(calendar.FirstJournal(obj.Data), starred, category, q) {
				rows = append(rows, entryRow(ctx, &obj, result.Site.Collection, loc, params))
			}
		}
	}
	return rows, calendars, unread, nil
}

// entryShown says whether a journal entry answers its stars, is filed under category where one is
// named, and has a query's words in its title, text or categories.
func entryShown(journal *ical.Component, starred func(string) bool, category string, q search.Query) bool {
	if journal == nil || !starred(calendar.ComponentColor(journal)) {
		return false
	}
	if category != "" && !slices.Contains(calendar.Categories(journal), category) {
		return false
	}
	summary, _ := journal.Props.Text(ical.PropSummary)
	return pim.Matches(q, summary, calendar.JournalText(journal), strings.Join(calendar.Categories(journal), "\n"))
}

func entryRow(ctx *web.Context, obj *caldav.CalendarObject, cal dav.Collection, loc *time.Location, params url.Values) EntryRow {
	journal := calendar.FirstJournal(obj.Data)
	summary, _ := journal.Props.Text(ical.PropSummary)
	date, _ := journal.Props.DateTime(ical.PropDateTimeStart, loc)
	added, _ := journal.Props.DateTime(ical.PropCreated, loc)
	return EntryRow{
		Path:     obj.Path,
		Account:  cal.Account,
		Calendar: cal,
		Summary:  summary,
		Date:     date,
		Added:    added,
		Star:     calendar.ComponentColor(journal),
		Href:     pimweb.ObjectURL(ctx, "calendar.entry", obj.Path, cal.Account, params),
	}
}

func journalFilters(ctx *web.Context, calendars []dav.Collection) []web.Filter {
	out := calendarFilters(ctx, calendars)
	if f, ok := ctx.FilterOn("category", ctx.T("filter.category")); ok {
		out = append(out, f)
	}
	return out
}

func (p *plugin) journal(ctx *web.Context) error {
	list, err := p.journalList(ctx)
	if err != nil {
		return err
	}
	pimweb.WarnUnread(ctx, list.Unread)
	rows, pager, err := web.Paginate(ctx, list.Rows)
	if err != nil {
		return err
	}
	addedBy := p.DAV.AddedBy(ctx.Login.Username())
	for i := range rows {
		rows[i].AddedBy = addedBy(rows[i].Calendar.Account, rows[i].Path)
	}
	invitations, err := p.invitations(ctx)
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "journal/list", &JournalRenderData{
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("title.journal")),
		View:           list.View,
		Filters:        journalFilters(ctx, list.Calendars),
		FilterRows:     web.ViewRows(ctx, list.View, false, ctx.WithoutParam("view")),
		Calendars:      list.Calendars,
		Entries:        rows,
		Pager:          pager,
		Query:          list.Query,
		Sorting:        list.Sorting,
		SelectForm:     journalForm,
		Invitations:    invitations,
	})
}
