package web

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/calendar"
	"git.mehdix.org/alborz/pim/dav"
	pimweb "git.mehdix.org/alborz/pim/web"
	"git.mehdix.org/alborz/search"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
)

// CalendarPageRenderData is the shared frame for the month and day pages.
type CalendarPageRenderData struct {
	pimweb.ListRenderData
	ListQuery string
	Time      time.Time
}

// CalendarObjectRenderData is the shared frame for event, task, and journal object pages.
type CalendarObjectRenderData = pimweb.ObjectRenderData

// UpdateCalendarObjectRenderData is the calendar specialization of the shared PIM edit frame.
type UpdateCalendarObjectRenderData = pimweb.EditRenderData[caldav.CalendarObject]

// CalendarList is the shared projection state for calendar-backed row lists.
type CalendarList[T any] struct {
	Rows      []T
	Calendars []dav.Collection
	// Items are the rows as a single object's neighbours, in the same order.
	Items   []pimweb.Item
	Query   string
	View    string
	Sorting pimweb.Sorting
	Unread  []pim.Unread
}

func assignAddedBy[T any](rows []T, addedBy func(string, string) string, identity func(T) (string, string), set func(*T, string)) {
	for i := range rows {
		account, path := identity(rows[i])
		set(&rows[i], addedBy(account, path))
	}
}

func calendarListData[T any](ctx *web.Context, title string, list CalendarList[T], pager web.Pager, filters []web.Filter, rows []web.FilterRow, form string, invitations *pimweb.RailInvitations) pimweb.ListRenderData {
	return pimweb.NewListRenderData(ctx, title, list.Calendars, list.View, list.Query, list.Sorting, pager, filters, rows, form, invitations)
}

func calendarListParts[T any](ctx *web.Context, p *plugin, list CalendarList[T], identity func(T) (string, string), setAddedBy func(*T, string)) ([]T, web.Pager, *pimweb.RailInvitations, error) {
	rows, pager, invitations, err := pimweb.ListParts(ctx, list.Unread, list.Rows, func() (*pimweb.RailInvitations, error) { return p.invitations(ctx) })
	if err != nil {
		return nil, web.Pager{}, nil, err
	}
	assignAddedBy(rows, p.DAV.AddedBy(ctx.Login.Username()), identity, setAddedBy)
	return rows, pager, invitations, nil
}

func calendarPageData(ctx *web.Context, title string, collections []dav.Collection, labels pimweb.CollectionLabels, view, query string, filters []web.Filter, rows []web.FilterRow, form string, invitations *pimweb.RailInvitations, listQuery string, when time.Time) CalendarPageRenderData {
	list := pimweb.NewListRenderData(ctx, title, collections, view, query, pimweb.Sorting{}, web.Pager{}, filters, rows, form, invitations)
	list.CollectionLabels = labels
	return CalendarPageRenderData{ListRenderData: list, ListQuery: listQuery, Time: when}
}

func calendarList[T any](ctx *web.Context, path string, params []string, valid func(string) bool,
	load func(string, search.Query, url.Values) ([]T, []dav.Collection, []pim.Unread, error), columns []pim.Column[T], tie func(T) string, item func(T) pimweb.Item,
) (CalendarList[T], error) {
	state := web.ReadListState(ctx)
	text, view, query := state.Query, state.View, state.ParsedQuery()
	if !valid(view) {
		return CalendarList[T]{}, web.NewError(http.StatusBadRequest, "no such view")
	}
	rows, calendars, unread, err := load(view, query, state.Rows(ctx.Path(path), state.Keep(params...)))
	if err != nil {
		return CalendarList[T]{}, err
	}
	sorting, err := pimweb.SortList(state, rows, columns, tie, params...)
	if err != nil {
		return CalendarList[T]{}, err
	}
	items := pimweb.ItemsFor(rows, item)
	return CalendarList[T]{Rows: rows, Items: items, Calendars: calendars, Query: text, View: view, Sorting: sorting, Unread: unread}, nil
}

// shownCalendarRows owns the common query, visibility, fanout, and unread pipeline for calendar sections.
func shownCalendarRows[T any](p *plugin, ctx *web.Context, only map[string]bool, q search.Query, star string,
	supports func([]string) bool, visibility func(*calendar.Settings) (*bool, *[]string), query func() caldav.CalendarQuery,
	first func(*ical.Calendar) *ical.Component, shown func(*ical.Component, func(string) bool, search.Query) bool,
	row func(*caldav.CalendarObject, dav.Collection) T,
) ([]T, []dav.Collection, []pim.Unread, error) {
	q, starred, err := web.StarFilter(q, star)
	if err != nil {
		return nil, nil, nil, web.NewError(http.StatusBadRequest, err.Error())
	}
	accounts, err := p.pooledCalendars(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	collections, sites, err := calendar.Visible(ctx.Request().Context(), accounts, ctx.URLAccount(), only, supports, visibility)
	if err != nil {
		return nil, nil, nil, err
	}
	queryValue := query()
	results := pim.Present(p.DAV, dav.Across(ctx.Request().Context(), sites, pim.Site[*caldav.Client].Account, func(ctx context.Context, site pim.Site[*caldav.Client]) ([]caldav.CalendarObject, error) {
		return site.Client.QueryCalendar(ctx, site.Collection.Path, &queryValue)
	}))
	rows, unread := pimweb.ProjectResults(results, func(object *caldav.CalendarObject, collection dav.Collection) (T, *pim.Unread, bool) {
		if object.Data == nil {
			failed := calendar.Unparsed(collection.Name, object)
			return *new(T), &failed, false
		}
		if !shown(first(object.Data), starred, q) {
			return *new(T), nil, false
		}
		return row(object, collection), nil, true
	})
	return rows, collections, unread, nil
}

// eachOccurrence keeps expansion errors attached to their collection while the
// caller decides how the resulting occurrences are projected into a page.
func eachOccurrence(objects []calendar.Object, expand func(calendar.Object) ([]calendar.Occurrence, error), use func(calendar.Occurrence)) (unread []pim.Unread) {
	for _, object := range objects {
		occurrences, err := expand(object)
		if err != nil {
			unread = append(unread, pim.Unread{Collection: object.Calendar, Err: err})
		}
		for _, occurrence := range occurrences {
			use(occurrence)
		}
	}
	return
}
