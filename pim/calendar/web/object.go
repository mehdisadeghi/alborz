package web

import (
	"fmt"
	"net/http"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim/calendar"
	"git.mehdix.org/alborz/pim/dav"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
)

type calendarObjectPage struct {
	client     *caldav.Client
	collection *dav.Collection
	object     *caldav.CalendarObject
	component  *ical.Component
}

func (p *plugin) openCalendarObject(ctx *web.Context, path, missing, failed string, empty error, first func(*ical.Calendar) *ical.Component) (*calendarObjectPage, error) {
	client, collections, err := p.Open(ctx.Request().Context(), ctx.Login)
	if err != nil {
		return nil, err
	}
	object, err := calendar.GetObject(ctx.Request().Context(), client, path)
	if code, _ := webdav.HTTPErrorCode(err); code == http.StatusNotFound {
		return nil, alborz.NotFound(missing)
	} else if err != nil {
		return nil, fmt.Errorf("%s: %w", failed, err)
	}
	collection := dav.Holding(collections, "", object.Path)
	if collection == nil {
		return nil, alborz.NotFound(missing)
	}
	if object == nil || object.Data == nil {
		return nil, fmt.Errorf("%w: %s", alborz.ErrUnreadable, path)
	}
	component := first(object.Data)
	if component == nil {
		return nil, empty
	}
	return &calendarObjectPage{client: client, collection: collection, object: object, component: component}, nil
}
