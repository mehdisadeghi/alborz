package collections

import (
	"bytes"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/emersion/go-ical"
)

// publicPrefix is where published calendars are read, by anyone holding
// the secret in the address; publicExt is what a client expects a feed's
// address to end in.
const (
	publicPrefix = Prefix + "/public/"
	publicExt    = ".ics"
)

// prodID names alborz as the writer of a feed it assembles.
const prodID = "-//mehdix.org//Alborz//EN"

// PublicPath is the address a secret publishes its calendar at.
func PublicPath(secret string) string { return publicPrefix + secret + publicExt }

// IsPublic says whether a path is a published calendar's, which is
// answered without asking who is reading.
func IsPublic(p string) bool { return strings.HasPrefix(p, publicPrefix) }

// ServePublic answers a published calendar as one iCalendar stream, the
// shape every client's "subscribe" reads.
func (s *Store) ServePublic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	secret, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, publicPrefix), publicExt)
	c, err := s.ByPublic(secret)
	if !ok || errors.Is(err, ErrNotFound) || (err == nil && c.Kind != Calendar) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		serveError(w, err)
		return
	}
	// The ctag moves with every write, so it names the feed's state.
	etag := strconv.Quote(strconv.FormatUint(c.CTag, 10))
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	feed, err := s.feed(*c)
	if err != nil {
		serveError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Write(feed)
}

// classPublic is the access classification a component has when it
// names none (RFC 5545 3.8.1.3).
const classPublic = "PUBLIC"

// guarded says whether an object is not for anyone holding the address:
// one whose writer classed any part of it as other than public. The
// object goes whole, since an occurrence moved on its own tells of the
// series it belongs to.
func guarded(cal *ical.Calendar) bool {
	for _, child := range cal.Children {
		if class := child.Props.Get(ical.PropClass); class != nil && !strings.EqualFold(class.Value, classPublic) {
			return true
		}
	}
	return false
}

// anonymous is a component without what names other people or is the
// writer's own: who was invited and by whom, and the alarms.
func anonymous(comp *ical.Component) *ical.Component {
	out := &ical.Component{Name: comp.Name, Props: ical.Props{}}
	for name, props := range comp.Props {
		if name != ical.PropAttendee && name != ical.PropOrganizer {
			out.Props[name] = props
		}
	}
	for _, child := range comp.Children {
		if child.Name != ical.CompAlarm {
			out.Children = append(out.Children, child)
		}
	}
	return out
}

// feed is the calendar's objects as one: every public component with
// nobody named in it, and each timezone once.
func (s *Store) feed(c Collection) ([]byte, error) {
	objects, err := s.Objects(c.Ref())
	if err != nil {
		return nil, err
	}
	feed := ical.NewCalendar()
	feed.Props.SetText(ical.PropVersion, "2.0")
	feed.Props.SetText(ical.PropProductID, prodID)
	feed.Props.SetText(ical.PropName, c.Name)
	// What clients older than RFC 7986's NAME read; they take it bare.
	name := ical.NewProp("X-WR-CALNAME")
	name.SetText(c.Name)
	name.Params.Del(ical.ParamValue)
	feed.Props.Set(name)
	zones := map[string]bool{}
	for _, o := range objects {
		cal, err := ical.NewDecoder(bytes.NewReader(o.Data)).Decode()
		if err != nil {
			return nil, err
		}
		if guarded(cal) {
			continue
		}
		for _, child := range cal.Children {
			if child.Name == ical.CompTimezone {
				id, _ := child.Props.Text(ical.PropTimezoneID)
				if zones[id] {
					continue
				}
				zones[id] = true
			}
			feed.Children = append(feed.Children, anonymous(child))
		}
	}
	var out bytes.Buffer
	if len(feed.Children) == 0 {
		// go-ical refuses a calendar with nothing in it, which a feed
		// may well be.
		out.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:" + prodID + "\r\nEND:VCALENDAR\r\n")
		return out.Bytes(), nil
	}
	err = ical.NewEncoder(&out).Encode(feed)
	return out.Bytes(), err
}
