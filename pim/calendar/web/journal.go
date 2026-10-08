package web

import (
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
	pimweb.ListRenderData
	Entries []EntryRow
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

type JournalList = CalendarList[EntryRow]

func (p *plugin) journalList(ctx *web.Context) (JournalList, error) {
	return calendarList(ctx, "calendar.journal", journalListParams, web.ValidStarView,
		func(view string, q search.Query, params url.Values) ([]EntryRow, []dav.Collection, []pim.Unread, error) {
			return p.shownEntries(ctx, pimweb.Only(ctx, "cal"), view, ctx.QueryParam("category"), q, params)
		}, entryColumns, entryTie, func(row EntryRow) pimweb.Item { return pimweb.Item{Path: row.Path, URL: row.Href} })
}

// shownEntries are the rows of the entries a view, a category and a query find in the journals in the
// request's scope, and every journal for the rail, with the journals and entries it could not read;
// params go into each row's link.
func (p *plugin) shownEntries(ctx *web.Context, only map[string]bool, view, category string, q search.Query, params url.Values) ([]EntryRow, []dav.Collection, []pim.Unread, error) {
	rows, calendars, unread, err := shownCalendarRows(p, ctx, only, q, view, calendar.SupportsJournal, calendar.JournalVisibility, calendar.JournalQuery,
		calendar.FirstJournal, func(journal *ical.Component, starred func(string) bool, q search.Query) bool {
			return entryShown(journal, starred, category, q)
		}, func(obj *caldav.CalendarObject, collection dav.Collection) EntryRow {
			return entryRow(ctx, obj, collection, web.UserLocation(ctx), params)
		})
	if err != nil {
		return nil, nil, nil, err
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

func journalFilters(ctx *web.Context) []web.Filter {
	out := calendarFilters(ctx)
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
	rows, pager, invitations, err := calendarListParts(ctx, p, list, func(row EntryRow) (string, string) { return row.Calendar.Account, row.Path }, func(row *EntryRow, value string) { row.AddedBy = value })
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "journal/list", &JournalRenderData{
		ListRenderData: calendarListData(ctx, ctx.T("title.journal"), list, pager, journalFilters(ctx), web.ViewRows(ctx, list.View, false, ctx.WithoutParam("view")), journalForm, invitations),
		Entries:        rows,
	})
}
