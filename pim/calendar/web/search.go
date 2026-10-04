package web

import (
	"cmp"
	"net/http"
	"slices"
	"strings"
	"time"

	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/calendar"
	"git.mehdix.org/alborz/pim/dav"
	pimweb "git.mehdix.org/alborz/pim/web"
	"git.mehdix.org/alborz/search"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-ical"
)

// EventSearchRenderData is the events a search finds across all time, each on the occurrence it shows.
type EventSearchRenderData struct {
	web.BaseRenderData
	Calendars   []dav.Collection
	Invitations *pimweb.RailInvitations
	Found       []calendar.Occurrence
	Query, View string
	Pager       web.Pager
	Filters     []web.Filter
	FilterRows  []web.FilterRow
	// ListQuery is carried to an event's page so it can name its neighbours; SelectForm is the rows' form.
	ListQuery  string
	SelectForm string
	// CollectionFor is a row's calendar, CollectionHref the agenda narrowed to it.
	CollectionFor  func(account, path string) dav.Collection
	CollectionHref func(account, path string) string
	// AddedBy names who added an event, where not the row's own account.
	AddedBy func(account, path string) string
	unread  []pim.Unread
}

var eventSearchParams = []string{"account", "cal", "query", "view"}

func (p *plugin) search(ctx *web.Context) error {
	view := ctx.QueryParam("view")
	if !validStarView(view) {
		return web.NewError(http.StatusBadRequest, "no such view")
	}
	text := strings.TrimSpace(ctx.QueryParam("query"))
	data, err := p.eventSearch(ctx, pimweb.Only(ctx, "cal"), view, search.Parse(text))
	if err != nil {
		return err
	}
	pimweb.WarnUnread(ctx, data.unread)
	if data.Found, data.Pager, err = web.Paginate(ctx, data.Found); err != nil {
		return err
	}
	if data.Invitations, err = p.invitations(ctx); err != nil {
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
	out := calendarFilters(ctx, nil)
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
	for _, obj := range objects {
		occurrences, err := calendar.Shown(obj, loc, now)
		if err != nil {
			unread = append(unread, pim.Unread{Collection: obj.Calendar, Err: err})
		}
		for _, oc := range occurrences {
			if eventShown(oc, starred, q) {
				found = append(found, oc)
			}
		}
	}
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
		Calendars:      calendars,
		Found:          found,
		ListQuery:      web.Query(pimweb.ListParams(ctx, eventSearchParams...)),
		CollectionFor:  collection,
		CollectionHref: href,
		AddedBy:        p.DAV.AddedBy(ctx.Login.Username()),
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
	pimweb.WarnUnread(ctx, data.unread)
	total := len(data.Found)
	data.Found = data.Found[:min(limit, total)]
	return web.Found{Rows: data, Total: total, All: ctx.SearchURL("calendar.search", q, view)}, nil
}

// foundTasks is the tasks' part in the search across sections, in the list's own order.
func (p *plugin) foundTasks(ctx *web.Context, q search.Query, view string, limit int) (web.Found, error) {
	if unasked := web.StarUnasked(q); unasked != nil {
		return web.Found{Unasked: unasked}, nil
	}
	params := pimweb.ListParams(ctx, taskListParams...)
	params.Set("from", ctx.Request().URL.RequestURI())
	rows, _, unread, err := p.shownTasks(ctx, nil, view, q, params)
	if err != nil {
		return web.Found{}, err
	}
	pimweb.WarnUnread(ctx, unread)
	if _, err := pimweb.Sort(ctx, rows, taskColumns, taskTie); err != nil {
		return web.Found{}, err
	}
	shown := rows[:min(limit, len(rows))]
	addedBy := p.DAV.AddedBy(ctx.Login.Username())
	for i := range shown {
		shown[i].AddedBy = addedBy(shown[i].Calendar.Account, shown[i].Task.Path)
	}
	return web.Found{Rows: &TasksRenderData{Tasks: shown}, Total: len(rows), All: ctx.SearchURL("calendar.tasks", q, view)}, nil
}

// foundJournal is the journal's part in the search across sections, in the list's own order.
func (p *plugin) foundJournal(ctx *web.Context, q search.Query, view string, limit int) (web.Found, error) {
	if unasked := web.StarUnasked(q); unasked != nil {
		return web.Found{Unasked: unasked}, nil
	}
	params := pimweb.ListParams(ctx, journalListParams...)
	params.Set("from", ctx.Request().URL.RequestURI())
	rows, _, unread, err := p.shownEntries(ctx, nil, view, "", q, params)
	if err != nil {
		return web.Found{}, err
	}
	pimweb.WarnUnread(ctx, unread)
	if _, err := pimweb.Sort(ctx, rows, entryColumns, entryTie); err != nil {
		return web.Found{}, err
	}
	shown := rows[:min(limit, len(rows))]
	addedBy := p.DAV.AddedBy(ctx.Login.Username())
	for i := range shown {
		shown[i].AddedBy = addedBy(shown[i].Calendar.Account, shown[i].Path)
	}
	return web.Found{Rows: &JournalRenderData{Entries: shown}, Total: len(rows), All: ctx.SearchURL("calendar.journal", q, view)}, nil
}
