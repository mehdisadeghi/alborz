package dav

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"slices"

	"git.mehdix.org/alborz/pim/store"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
	"github.com/emersion/go-webdav/carddav"
)

// wire is how a kind's objects travel and how its copy is served.
type wire struct {
	ns, data, query, multigetName string
	handler                       func(coll string, objects []object) http.Handler
}

func wireFor(kind store.Kind) wire {
	if kind == store.AddressBook {
		return wire{ns: "urn:ietf:params:xml:ns:carddav", data: "address-data",
			query: "addressbook-query", multigetName: "addressbook-multiget", handler: bookCopy}
	}
	return wire{ns: "urn:ietf:params:xml:ns:caldav", data: "calendar-data",
		query: "calendar-query", multigetName: "calendar-multiget", handler: calendarCopy}
}

func (w wire) answers(body []byte) bool {
	d := xml.NewDecoder(bytes.NewReader(body))
	for {
		tok, err := d.Token()
		if err != nil {
			return false
		}
		if start, ok := tok.(xml.StartElement); ok {
			return start.Name.Space == w.ns && (start.Name.Local == w.query || start.Name.Local == w.multigetName)
		}
	}
}

var errNotHeld = webdav.NewHTTPError(http.StatusNotFound, errors.New("dav: no such object in the copy"))

// calendars serves go-webdav's CalDAV server from a collection's copy; only reads reach it.
type calendars struct {
	caldav.Backend
	objects []caldav.CalendarObject
}

func calendarCopy(coll string, held []object) http.Handler {
	objects := make([]caldav.CalendarObject, len(held))
	for i, o := range held {
		objects[i] = caldav.CalendarObject{Path: coll + o.Name, ETag: o.ETag, ContentLength: int64(len(o.Data)), Raw: o.Data}
	}
	return &caldav.Handler{Backend: calendars{objects: objects}}
}

func (b calendars) GetCalendarObject(_ context.Context, p string, _ *caldav.CalendarCompRequest) (*caldav.CalendarObject, error) {
	i := slices.IndexFunc(b.objects, func(o caldav.CalendarObject) bool { return o.Path == p })
	if i < 0 {
		return nil, errNotHeld
	}
	return &b.objects[i], nil
}

func (b calendars) QueryCalendarObjects(_ context.Context, _ string, query *caldav.CalendarQuery) ([]caldav.CalendarObject, error) {
	for i := range b.objects {
		var err error
		if b.objects[i].Data, err = ical.NewDecoder(bytes.NewReader(b.objects[i].Raw)).Decode(); err != nil {
			return nil, err
		}
	}
	found, err := caldav.Filter(query, b.objects)
	if err != nil {
		return nil, err
	}
	// go-ical refuses to encode what servers accept, such as a VEVENT without DTSTAMP.
	for i := range found {
		found[i].Data = nil
	}
	return found, nil
}

// books is calendars for an address book.
type books struct {
	carddav.Backend
	objects []carddav.AddressObject
}

func bookCopy(coll string, held []object) http.Handler {
	objects := make([]carddav.AddressObject, len(held))
	for i, o := range held {
		objects[i] = carddav.AddressObject{Path: coll + o.Name, ETag: o.ETag, ContentLength: int64(len(o.Data)), Raw: o.Data}
	}
	return &carddav.Handler{Backend: books{objects: objects}}
}

func (b books) GetAddressObject(_ context.Context, p string, _ *carddav.AddressDataRequest) (*carddav.AddressObject, error) {
	i := slices.IndexFunc(b.objects, func(o carddav.AddressObject) bool { return o.Path == p })
	if i < 0 {
		return nil, errNotHeld
	}
	return &b.objects[i], nil
}

// QueryAddressObjects parses only for a filter, as the store does.
func (b books) QueryAddressObjects(_ context.Context, _ string, query *carddav.AddressBookQuery) ([]carddav.AddressObject, error) {
	if len(query.PropFilters) > 0 {
		for i := range b.objects {
			var err error
			if b.objects[i].Card, err = vcard.NewDecoder(bytes.NewReader(b.objects[i].Raw)).Decode(); err != nil {
				return nil, err
			}
		}
	}
	return carddav.Filter(query, b.objects)
}
