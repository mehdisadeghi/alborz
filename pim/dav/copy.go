package dav

import (
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"sync"

	"git.mehdix.org/alborz/pim/store"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
	"github.com/emersion/go-webdav/carddav"
)

// wire is how a kind's objects travel and how its copy is served.
type wire struct {
	ns, query, multigetName string
	handler                 func(coll string, objects []object) http.Handler
	// sync, get and ctag ask the server with the kind's client.
	sync func(ctx context.Context, client *http.Client, target *url.URL, token string) (synced, error)
	get  func(ctx context.Context, client *http.Client, target *url.URL, paths []string) ([]sent, error)
	ctag func(ctx context.Context, client *http.Client, target *url.URL) (string, error)
}

func wireFor(kind store.Kind) wire {
	if kind == store.AddressBook {
		return wire{ns: "urn:ietf:params:xml:ns:carddav", query: "addressbook-query", multigetName: "addressbook-multiget",
			handler: bookCopy, sync: bookSync, get: bookGet, ctag: bookCTag}
	}
	return wire{ns: "urn:ietf:params:xml:ns:caldav", query: "calendar-query", multigetName: "calendar-multiget",
		handler: calendarCopy, sync: calendarSync, get: calendarGet, ctag: calendarCTag}
}

func calendarSync(ctx context.Context, client *http.Client, target *url.URL, token string) (synced, error) {
	c, err := caldav.NewClient(client, target.String())
	if err != nil {
		return synced{}, err
	}
	resp, err := c.SyncCollection(ctx, target.Path, &caldav.SyncQuery{SyncToken: token})
	if err != nil {
		return synced{}, err
	}
	s := synced{token: resp.SyncToken, truncated: resp.Truncated, gone: resp.Deleted}
	for _, o := range resp.Updated {
		s.put = append(s.put, sent{path: o.Path, etag: o.ETag, data: o.Raw})
	}
	return s, nil
}

func calendarGet(ctx context.Context, client *http.Client, target *url.URL, paths []string) ([]sent, error) {
	c, err := caldav.NewClient(client, target.String())
	if err != nil {
		return nil, err
	}
	objects, err := c.MultiGetCalendar(ctx, target.Path, &caldav.CalendarMultiGet{Paths: paths})
	if err != nil {
		return nil, err
	}
	out := make([]sent, len(objects))
	for i, o := range objects {
		out[i] = sent{path: o.Path, etag: o.ETag, data: o.Raw}
	}
	return out, nil
}

func calendarCTag(ctx context.Context, client *http.Client, target *url.URL) (string, error) {
	c, err := caldav.NewClient(client, target.String())
	if err != nil {
		return "", err
	}
	cal, err := c.FindCalendar(ctx, target.Path)
	if err != nil {
		return "", err
	}
	return cal.CTag, nil
}

func bookSync(ctx context.Context, client *http.Client, target *url.URL, token string) (synced, error) {
	c, err := carddav.NewClient(client, target.String())
	if err != nil {
		return synced{}, err
	}
	resp, err := c.SyncCollection(ctx, target.Path, &carddav.SyncQuery{SyncToken: token})
	if err != nil {
		return synced{}, err
	}
	s := synced{token: resp.SyncToken, truncated: resp.Truncated, gone: resp.Deleted}
	for _, o := range resp.Updated {
		s.put = append(s.put, sent{path: o.Path, etag: o.ETag, data: o.Raw})
	}
	return s, nil
}

func bookGet(ctx context.Context, client *http.Client, target *url.URL, paths []string) ([]sent, error) {
	c, err := carddav.NewClient(client, target.String())
	if err != nil {
		return nil, err
	}
	objects, err := c.MultiGetAddressBook(ctx, target.Path, &carddav.AddressBookMultiGet{Paths: paths})
	if err != nil {
		return nil, err
	}
	out := make([]sent, len(objects))
	for i, o := range objects {
		out[i] = sent{path: o.Path, etag: o.ETag, data: o.Raw}
	}
	return out, nil
}

func bookCTag(ctx context.Context, client *http.Client, target *url.URL) (string, error) {
	c, err := carddav.NewClient(client, target.String())
	if err != nil {
		return "", err
	}
	book, err := c.FindAddressBook(ctx, target.Path)
	if err != nil {
		return "", err
	}
	return book.CTag, nil
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

// QueryCalendarObjects parses only the objects that begin every component the filter requires.
// One that does not parse goes to the filter as its bytes, as in the store.
func (b calendars) QueryCalendarObjects(_ context.Context, _ string, query *caldav.CalendarQuery) ([]caldav.CalendarObject, error) {
	br := lineReaders.Get().(*bufio.Reader)
	defer lineReaders.Put(br)
	var held []caldav.CalendarObject
	for _, o := range b.objects {
		if !beginsAll(o.Raw, query.CompFilter.Comps) {
			continue
		}
		br.Reset(bytes.NewReader(o.Raw))
		var err error
		if o.Data, err = ical.NewDecoder(br).Decode(); err != nil {
			o.Data = nil
		}
		held = append(held, o)
	}
	found, err := caldav.Filter(query, held)
	if err != nil {
		return nil, err
	}
	// go-ical refuses to encode what servers accept, such as a VEVENT without DTSTAMP.
	for i := range found {
		found[i].Data = nil
	}
	return found, nil
}

// lineReaders spare a parse its own buffer, which outweighs the object it reads.
var lineReaders = sync.Pool{New: func() any { return bufio.NewReader(nil) }}

// beginsAll says whether raw may hold every component filters require: it holds each one's BEGIN line.
func beginsAll(raw []byte, filters []caldav.CompFilter) bool {
	for _, f := range filters {
		if !f.IsNotDefined && !holdsLine(raw, "BEGIN:"+f.Name) {
			return false
		}
	}
	return true
}

// holdsLine finds want in raw as RFC 5545 3.1 reads it: unfolded, names in any case.
func holdsLine(raw []byte, want string) bool {
	for start := range raw {
		i, j := start, 0
		for j < len(want) && i < len(raw) {
			if n := foldAt(raw[i:]); n > 0 {
				i += n
				continue
			}
			if lower(raw[i]) != lower(want[j]) {
				break
			}
			i, j = i+1, j+1
		}
		if j == len(want) {
			return true
		}
	}
	return false
}

// foldAt is the length of the line fold raw starts with, or zero; a bare LF is taken for CRLF.
func foldAt(raw []byte) int {
	n := 0
	if len(raw) > 0 && raw[0] == '\r' {
		n = 1
	}
	if len(raw) > n+1 && raw[n] == '\n' && (raw[n+1] == ' ' || raw[n+1] == '\t') {
		return n + 2
	}
	return 0
}

func lower(b byte) byte {
	if 'A' <= b && b <= 'Z' {
		return b + 'a' - 'A'
	}
	return b
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
