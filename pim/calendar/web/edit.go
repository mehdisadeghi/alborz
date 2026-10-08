package web

import (
	"time"

	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/calendar"
	"git.mehdix.org/alborz/pim/dav"
	pimweb "git.mehdix.org/alborz/pim/web"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
	"uuid"
)

// calendarEdit is the common state of a calendar object create or edit.
type calendarEdit struct {
	client     *caldav.Client
	groups     []dav.Group
	collection *dav.Collection
	object     *caldav.CalendarObject
	component  *ical.Component
}

// editCalendarObject opens path, or starts a component in the first writable collection.
func (p *plugin) editCalendarObject(ctx *web.Context, path, notFound, failed string, empty error, supports func([]string) bool,
	component func(*ical.Calendar) *ical.Component, fresh func() *ical.Component,
) (*calendarEdit, error) {
	e := &calendarEdit{collection: &dav.Collection{}}
	if path == "" {
		groups, err := p.writableGroups(ctx, supports)
		if err != nil {
			return nil, err
		}
		e.groups, e.component = groups, fresh()
		if len(groups) > 0 {
			e.collection = &groups[0].Collections[0]
		}
		return e, nil
	}

	object, err := p.openCalendarObject(ctx, path, notFound, failed, empty, component)
	if err != nil {
		return nil, err
	}
	if ctx.Request().Method == "POST" {
		if object.object.ETag, err = pimweb.Drawn(ctx, object.object.ETag); err != nil {
			return nil, err
		}
	}
	e.client, e.object = object.client, object.object
	e.collection, e.component = object.collection, object.component
	return e, nil
}

func (e *calendarEdit) creating() bool { return e.object == nil }

// destination resolves a create's chosen collection; an edit stays where it is.
func (e *calendarEdit) destination(ctx *web.Context, p *plugin, supports func([]string) bool) (pim.Ref[*caldav.Client], error) {
	if !e.creating() {
		return pim.Ref[*caldav.Client]{Client: e.client, Account: ctx.Login.Username()}, nil
	}
	to, err := p.destination(ctx, ctx.FormValue("calendar"), supports)
	if err != nil {
		return pim.Ref[*caldav.Client]{}, err
	}
	for _, group := range e.groups {
		if group.Account == to.Account {
			e.collection = dav.At(group.Collections, to.Path)
		}
	}
	return to, nil
}

// put writes component in its existing calendar or a new one, with current timestamps and UID.
func (e *calendarEdit) put(ctx *web.Context, to pim.Ref[*caldav.Client], component *ical.Component, at time.Time) (*ical.Calendar, error) {
	now := time.Now().UTC()
	component.Props.SetDateTime(ical.PropDateTimeStamp, now)
	component.Props.SetDateTime(ical.PropLastModified, now)
	id, _ := component.Props.Text(ical.PropUID)
	if id == "" {
		id = uuid.New().String()
		component.Props.SetText(ical.PropUID, id)
	}
	cal := calendar.NewCalendar(component)
	if !e.creating() {
		cal = e.object.Data
	}
	if !at.IsZero() {
		calendar.EnsureTimezones(cal, at)
	}
	object, err := calendar.PutObject(ctx.Request().Context(), to, id+".ics", e.object, cal)
	if err == nil {
		e.object = object
	}
	return cal, err
}
