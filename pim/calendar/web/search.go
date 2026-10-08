package web

import (
	"cmp"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/calendar"
	pimweb "git.mehdix.org/alborz/pim/web"
	"git.mehdix.org/alborz/search"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-ical"
)

// EventSearchRenderData is the events a search finds across all time, each on the occurrence it shows.
type EventSearchRenderData struct {
	pimweb.ListRenderData
	Found []calendar.Occurrence
	// ListQuery is carried to an event's page so it can name its neighbours; SelectForm is the rows' form.
	ListQuery string
	unread    []pim.Unread
}

var eventSearchParams = []string{"account", "cal", "query", "view"}

func (p *plugin) search(ctx *web.Context) error {
	state := web.ReadListState(ctx)
	view := state.View
	if !web.ValidStarView(view) {
		return web.NewError(http.StatusBadRequest, "no such view")
	}
	text, query := state.Query, state.ParsedQuery()
	text = strings.TrimSpace(text)
	data, err := p.eventSearch(ctx, pimweb.Only(ctx, "cal"), view, query)
	if err != nil {
		return err
	}
	data.Found, data.Pager, data.Invitations, err = pimweb.ListParts(ctx, data.unread, data.Found, func() (*pimweb.RailInvitations, error) { return p.invitations(ctx) })
	if err != nil {
		return err
	}
	data.BaseRenderData = *web.NewBaseRenderData(ctx).WithTitle(ctx.T("nav.calendar") + ": " + ctx.T("search.title"))
	data.Query, data.View = text, view
	data.SelectForm = "events-form"
	data.Filters = searchFilters(ctx)
	data.FilterRows = web.ViewRows(ctx, view, false, ctx.WithoutParam("view"))
	return ctx.Render(http.StatusOK, "calendar/search", data)
}

func searchFilters(ctx *web.Context) []web.Filter {
	out := calendarFilters(ctx)
	if f, ok := ctx.FilterOn("query", ctx.T("filter.search")); ok {
		out = append([]web.Filter{f}, out...)
	}
	return out
}

// eventSearch is every event in the calendars in scope that a view and a query find, those under way
// or to come first, soonest first, then the past, latest first.
func (p *plugin) eventSearch(ctx *web.Context, only map[string]bool, view string, q search.Query) (*EventSearchRenderData, error) {
	q, starred, err := web.StarFilter(q, view)
	if err != nil {
		return nil, web.NewError(http.StatusBadRequest, err.Error())
	}
	accounts, err := p.pooledCalendars(ctx)
	if err != nil {
		return nil, err
	}
	calendars, sites, err := calendar.Visible(ctx.Request().Context(), accounts, ctx.URLAccount(), only, calendar.SupportsEvent, calendar.EventVisibility)
	if err != nil {
		return nil, err
	}
	query := calendar.SeriesQuery()
	objects, unread := p.QuerySites(ctx.Request().Context(), sites, &query)
	loc, now := web.UserLocation(ctx), time.Now()
	var found []calendar.Occurrence
	unread = append(unread, eachOccurrence(objects,
		func(obj calendar.Object) ([]calendar.Occurrence, error) { return calendar.Shown(obj, loc, now) },
		func(oc calendar.Occurrence) {
			if eventShown(oc, starred, q) {
				found = append(found, oc)
			}
		})...)
	past := func(oc calendar.Occurrence) bool { return !oc.End.After(now) && oc.Start.Before(now) }
	slices.SortStableFunc(found, func(a, b calendar.Occurrence) int {
		switch pa, pb := past(a), past(b); {
		case pa != pb && pa:
			return 1
		case pa != pb:
			return -1
		case pa:
			return b.Start.Compare(a.Start)
		}
		return cmp.Or(a.Start.Compare(b.Start), strings.Compare(a.Path, b.Path))
	})
	collection, href := pimweb.Labels(ctx, calendars, "calendar.month", "cal", map[string][]string{"view": {"list"}})
	return &EventSearchRenderData{
		ListRenderData: pimweb.ListRenderData{CollectionLabels: pimweb.NewCollectionLabels(p.DAV, ctx.Login.Username(), collection, href), Collections: calendars},
		Found:          found,
		ListQuery:      web.Query(web.ReadListState(ctx).Keep(eventSearchParams...)),
		unread:         unread,
	}, nil
}

// eventShown says whether an occurrence answers a view and a query in its summary, description or place.
func eventShown(oc calendar.Occurrence, starred func(string) bool, q search.Query) bool {
	if !starred(oc.Star()) {
		return false
	}
	description, _ := oc.Event.Props.Text(ical.PropDescription)
	location, _ := oc.Event.Props.Text(ical.PropLocation)
	return pim.Matches(q, oc.Summary(), description, location)
}

// foundEvents is the events' part in the search across sections.
func (p *plugin) foundEvents(ctx *web.Context, q search.Query, view string, limit int) (web.Found, error) {
	if unasked := web.StarUnasked(q); unasked != nil {
		return web.Found{Unasked: unasked}, nil
	}
	data, err := p.eventSearch(ctx, nil, view, q)
	if err != nil {
		return web.Found{}, err
	}
	var total int
	data.Found, total = pimweb.SearchRows(ctx, data.Found, data.unread, limit)
	return web.Found{Rows: data, Total: total, All: ctx.SearchURL("calendar.search", q, view)}, nil
}

// foundCalendar is the shared search workflow for calendar-backed row sections.
// The callbacks keep only the row loading, attribution, and render projection specific.
func foundCalendar[T any](ctx *web.Context, q search.Query, view string, limit int, params []string,
	load func(url.Values) ([]T, []pim.Unread, error), columns []pim.Column[T], tie func(T) string,
	prepare func([]T), render func([]T) web.RenderData, route string,
) (web.Found, error) {
	if unasked := web.StarUnasked(q); unasked != nil {
		return web.Found{Unasked: unasked}, nil
	}
	state := web.ReadListState(ctx)
	listParams := state.Keep(params...)
	listParams.Set("from", ctx.Request().URL.RequestURI())
	rows, unread, err := load(listParams)
	if err != nil {
		return web.Found{}, err
	}
	shown, total, err := pimweb.SearchListRows(ctx, state, rows, unread, limit, columns, tie)
	if err != nil {
		return web.Found{}, err
	}
	prepare(shown)
	return web.Found{Rows: render(shown), Total: total, All: ctx.SearchURL(route, q, view)}, nil
}

// foundTasks is the tasks' part in the search across sections, in the list's own order.
func (p *plugin) foundTasks(ctx *web.Context, q search.Query, view string, limit int) (web.Found, error) {
	return foundCalendar(ctx, q, view, limit, taskListParams,
		func(params url.Values) ([]TaskRow, []pim.Unread, error) {
			rows, _, unread, err := p.shownTasks(ctx, nil, view, q, params)
			return rows, unread, err
		}, taskColumns, taskTie,
		func(rows []TaskRow) {
			assignAddedBy(rows, p.DAV.AddedBy(ctx.Login.Username()),
				func(row TaskRow) (string, string) { return row.Calendar.Account, row.Task.Path },
				func(row *TaskRow, value string) { row.AddedBy = value })
		}, func(rows []TaskRow) web.RenderData { return &TasksRenderData{Tasks: rows} }, "calendar.tasks")
}

// foundJournal is the journal's part in the search across sections, in the list's own order.
func (p *plugin) foundJournal(ctx *web.Context, q search.Query, view string, limit int) (web.Found, error) {
	return foundCalendar(ctx, q, view, limit, journalListParams,
		func(params url.Values) ([]EntryRow, []pim.Unread, error) {
			rows, _, unread, err := p.shownEntries(ctx, nil, view, "", q, params)
			return rows, unread, err
		}, entryColumns, entryTie,
		func(rows []EntryRow) {
			assignAddedBy(rows, p.DAV.AddedBy(ctx.Login.Username()),
				func(row EntryRow) (string, string) { return row.Calendar.Account, row.Path },
				func(row *EntryRow, value string) { row.AddedBy = value })
		}, func(rows []EntryRow) web.RenderData { return &JournalRenderData{Entries: rows} }, "calendar.journal")
}
