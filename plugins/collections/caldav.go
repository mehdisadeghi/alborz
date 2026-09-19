package collections

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
)

// calendars is the store as go-webdav's CalDAV server asks for it.
type calendars struct{ *Store }

func (b calendars) CurrentUserPrincipal(ctx context.Context) (string, error) {
	return principalPath(userOf(ctx)), nil
}

func (b calendars) CalendarHomeSetPath(ctx context.Context) (string, error) {
	return HomePath(Calendar, userOf(ctx)), nil
}

func calendarOf(viewer string, c Collection, may access) (*caldav.Calendar, error) {
	cal := &caldav.Calendar{
		Path:                  PathOf(viewer, c),
		Name:                  c.Name,
		Description:           c.Description,
		Color:                 c.Color,
		SupportedComponentSet: c.Components,
		ReadOnly:              may < write,
		CTag:                  strconv.FormatUint(c.CTag, 10),
		DeadProperties:        deadOf(c),
	}
	if len(c.Timezone) == 0 {
		return cal, nil
	}
	var err error
	cal.Timezone, err = ical.NewDecoder(bytes.NewReader(c.Timezone)).Decode()
	return cal, err
}

func (b calendars) ListCalendars(ctx context.Context) ([]caldav.Calendar, error) {
	visible, err := b.Visible(userOf(ctx), Calendar, time.Now())
	if err != nil {
		return nil, err
	}
	out := make([]caldav.Calendar, len(visible))
	for i, c := range visible {
		cal, err := calendarOf(userOf(ctx), c.Collection, c.may)
		if err != nil {
			return nil, err
		}
		out[i] = *cal
	}
	return out, nil
}

func (b calendars) GetCalendar(ctx context.Context, p string) (*caldav.Calendar, error) {
	_, c, may, err := b.reach(ctx, p, Calendar)
	if err != nil {
		return nil, err
	}
	return calendarOf(userOf(ctx), *c, may)
}

// zoneOf is a calendar's timezone as it is kept. A calendar with
// nothing in it is how go-webdav says the timezone was removed.
func zoneOf(zone *ical.Calendar) ([]byte, error) {
	if zone == nil || len(zone.Children) == 0 {
		return nil, nil
	}
	var out bytes.Buffer
	err := ical.NewEncoder(&out).Encode(zone)
	return out.Bytes(), err
}

// defaultComponents is what a calendar made without saying takes.
var defaultComponents = []string{ical.CompEvent, ical.CompToDo}

// storedComponents are what a calendar may be made to take: the
// components a calendar object resource holds (RFC 4791 4.1), less
// VFREEBUSY, which nothing here reads.
var storedComponents = []string{ical.CompEvent, ical.CompToDo, ical.CompJournal}

func (b calendars) CreateCalendar(ctx context.Context, cal *caldav.Calendar) error {
	components := cal.SupportedComponentSet
	if len(components) == 0 {
		components = defaultComponents
	}
	for _, comp := range components {
		if !slices.Contains(storedComponents, comp) {
			return httpError(http.StatusForbidden, fmt.Errorf("a calendar holds %s, not %q", strings.Join(storedComponents, ", "), comp))
		}
	}
	zone, err := zoneOf(cal.Timezone)
	if err != nil {
		return httpError(http.StatusBadRequest, err)
	}
	c := Collection{
		Kind: Calendar, Components: components, Name: cal.Name, Color: cal.Color,
		Description: cal.Description, Timezone: zone,
	}
	keepDead(&c, nil, cal.DeadProperties)
	return b.create(ctx, cal.Path, c)
}

func (b calendars) UpdateCalendar(ctx context.Context, p string, update *caldav.CalendarUpdate) error {
	zone, err := zoneOf(update.Timezone)
	if err != nil {
		return httpError(http.StatusBadRequest, err)
	}
	return b.update(ctx, p, Calendar, func(c *Collection) {
		set(&c.Name, update.Name)
		set(&c.Color, update.Color)
		set(&c.Description, update.Description)
		if update.Timezone != nil {
			c.Timezone = zone
		}
		keepDead(c, update.RemovedDeadProperties, update.DeadProperties)
	})
}

// calendarObject is an object as go-webdav asks for it: with its octets
// as they are kept when any of it is wanted, so the tag names what a
// read answers, and with nothing when only the tag is - listing a
// calendar's tags parses no object.
func calendarObject(p string, o Object, req *caldav.CalendarCompRequest) caldav.CalendarObject {
	co := caldav.CalendarObject{Path: p, ModTime: o.Modified, ContentLength: int64(len(o.Data)), ETag: o.ETag}
	if !req.IsEmpty() {
		co.Raw = o.Data
	}
	return co
}

func (b calendars) GetCalendarObject(ctx context.Context, p string, req *caldav.CalendarCompRequest) (*caldav.CalendarObject, error) {
	o, err := b.object(ctx, p, Calendar)
	if err != nil {
		return nil, err
	}
	co := calendarObject(p, *o, req)
	return &co, nil
}

func (b calendars) ListCalendarObjects(ctx context.Context, p string, req *caldav.CalendarCompRequest) ([]caldav.CalendarObject, error) {
	objects, err := b.objects(ctx, p, Calendar)
	if err != nil {
		return nil, err
	}
	out := make([]caldav.CalendarObject, len(objects))
	for i, o := range objects {
		out[i] = calendarObject(path.Join(p, o.Name), o, req)
	}
	return out, nil
}

// QueryCalendarObjects parses, which a filter cannot do without.
func (b calendars) QueryCalendarObjects(ctx context.Context, p string, query *caldav.CalendarQuery) ([]caldav.CalendarObject, error) {
	objects, err := b.objects(ctx, p, Calendar)
	if err != nil {
		return nil, err
	}
	all := make([]caldav.CalendarObject, len(objects))
	for i, o := range objects {
		all[i] = calendarObject(path.Join(p, o.Name), o, &query.CompRequest)
		if all[i].Data, err = ical.NewDecoder(bytes.NewReader(o.Data)).Decode(); err != nil {
			return nil, err
		}
	}
	return caldav.Filter(query, all)
}

func (b calendars) PutCalendarObject(ctx context.Context, p string, cal *ical.Calendar, opts *caldav.PutCalendarObjectOptions) (*caldav.CalendarObject, error) {
	component, _, err := caldav.ValidateCalendarObject(cal)
	if err != nil {
		return nil, caldav.NewPreconditionError(caldav.PreconditionValidCalendarObjectResource)
	}
	o, err := b.write(ctx, p, Calendar, opts.Raw, opts.IfMatch, opts.IfNoneMatch, func(c Collection) error {
		if !slices.Contains(c.Components, component) {
			return caldav.NewPreconditionError(caldav.PreconditionSupportedCalendarComponent)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// What a PUT answers with is the tag, the time and the path.
	return &caldav.CalendarObject{Path: p, ModTime: o.Modified, ContentLength: int64(len(o.Data)), ETag: o.ETag}, nil
}

// conditions reads a write's If-Match and If-None-Match (RFC 7232 3.1,
// 3.2): the versions the writer means to replace, and those it will
// not. "*" is any version at all, so If-Match: * is off when nothing is
// held: an edit of what another device deleted does not bring it back.
// If-Match compares strongly and If-None-Match weakly (RFC 7232 2.3.2).
func conditions(ifMatch, ifNoneMatch webdav.ConditionalMatch) Unless {
	return func(held string) error {
		must, matched, err := ifMatch.MatchETag(held)
		if err != nil {
			return httpError(http.StatusBadRequest, err)
		}
		mustNot, clashed, err := ifNoneMatch.MatchETagWeak(held)
		if err != nil {
			return httpError(http.StatusBadRequest, err)
		}
		if must && !matched || mustNot && clashed {
			return ErrPrecondition
		}
		return nil
	}
}

func (b calendars) DeleteCalendarObject(ctx context.Context, p string) error {
	return b.remove(ctx, p, Calendar)
}
