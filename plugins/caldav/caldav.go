package alborzcaldav

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"github.com/labstack/echo/v4"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
)

// The account's domain has no CalDAV server, or it holds no calendar;
// the HTTP layer answers 404 rather than crashing on direct URLs.
var errNoCalendar = alborz.NotFoundf("caldav: no calendar found")

type CalendarInfo struct {
	Path    string
	Name    string
	Color   string
	Visible bool
	// Only marks the collection a URL is currently narrowed to, and only
	// when it is the sole one: pressing the same link again is how a
	// reader gets back out of a view they pressed their way into.
	Only                  bool
	Writable              bool
	SupportedComponentSet []string

	// Account owning the calendar, set only in the unified view
	Account string
}

func (c CalendarInfo) SupportsTodo() bool {
	// VTODO is optional in CalDAV. When a server omits the component-set
	// property, do not promote an ordinary calendar into the Tasks UI.
	if len(c.SupportedComponentSet) == 0 {
		return false
	}
	for _, comp := range c.SupportedComponentSet {
		if strings.EqualFold(comp, "VTODO") {
			return true
		}
	}
	return false
}

func (c CalendarInfo) SupportsEvent() bool {
	// VEVENT is the conservative fallback for older CalDAV servers that
	// do not advertise supported-calendar-component-set.
	if len(c.SupportedComponentSet) == 0 {
		return true
	}
	for _, comp := range c.SupportedComponentSet {
		if strings.EqualFold(comp, "VEVENT") {
			return true
		}
	}
	return false
}

type davMultiStatus struct {
	Responses []davResponse `xml:"response"`
}

type davResponse struct {
	Href     string        `xml:"href"`
	PropStat []davPropStat `xml:"propstat"`
}

type davPropStat struct {
	Status string             `xml:"status"`
	Prop   davCollectionProps `xml:"prop"`
}

type davCollectionProps struct {
	ResourceType struct {
		Calendar *struct{} `xml:"urn:ietf:params:xml:ns:caldav calendar,omitempty"`
	} `xml:"resourcetype"`
	DisplayName   string `xml:"displayname"`
	CalendarColor string `xml:"http://apple.com/ns/ical/ calendar-color"`
	ComponentSet  struct {
		Comps []struct {
			Name string `xml:"name,attr"`
		} `xml:"urn:ietf:params:xml:ns:caldav comp"`
	} `xml:"urn:ietf:params:xml:ns:caldav supported-calendar-component-set"`
	Privileges []struct {
		Write        *struct{} `xml:"DAV: write"`
		WriteContent *struct{} `xml:"DAV: write-content"`
		Bind         *struct{} `xml:"DAV: bind"`
	} `xml:"current-user-privilege-set>privilege"`
}

func (p *plugin) httpClient(session *alborz.Session) *http.Client {
	return &http.Client{
		// A wedged DAV server fails the request instead of hanging it.
		Timeout: requestTimeout,
		Transport: p.cache.Transport(session.Username(), &webdavRoundTripper{
			upstream: http.DefaultTransport,
			session:  session,
			debug:    p.debug,
		}),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func doPropfind(ctx context.Context, client *http.Client, path string, body string) (*davMultiStatus, error) {
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	buf.WriteString(body)

	req, err := http.NewRequestWithContext(ctx, "PROPFIND", path, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Depth", "1")
	req.Header.Set("Content-Type", "text/xml; charset=\"utf-8\"")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMultiStatus {
		return nil, fmt.Errorf("expected multistatus response, got %s", resp.Status)
	}

	var ms davMultiStatus
	if err := xml.NewDecoder(resp.Body).Decode(&ms); err != nil {
		return nil, err
	}
	return &ms, nil
}

// doMkcalendar creates a calendar collection. go-webdav's client speaks
// only to existing collections, so the request is built here: a name, the
// components the collection accepts, and Apple's colour property, which
// every server that shows colours reads.
// errCollectionExists reports the address as taken: MKCALENDAR on a
// resource that is already there is 405, which is what RFC 4918 asks
// of MKCOL and what SabreDAV answers.
var errCollectionExists = errors.New("collection already exists")

func doMkcalendar(ctx context.Context, client *http.Client, path, name string, components []string, color string) error {
	var props bytes.Buffer
	fmt.Fprintf(&props, "<D:displayname>%s</D:displayname>", xmlEscape(name))
	props.WriteString(`<C:supported-calendar-component-set>`)
	for _, c := range components {
		fmt.Fprintf(&props, `<C:comp name="%s"/>`, xmlEscape(c))
	}
	props.WriteString(`</C:supported-calendar-component-set>`)
	if color != "" {
		fmt.Fprintf(&props, "<A:calendar-color>%s</A:calendar-color>", xmlEscape(color))
	}

	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	fmt.Fprintf(&buf, `<C:mkcalendar xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:A="http://apple.com/ns/ical/"><D:set><D:prop>%s</D:prop></D:set></C:mkcalendar>`, props.String())

	req, err := http.NewRequestWithContext(ctx, "MKCALENDAR", path, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/xml; charset=\"utf-8\"")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 201 is the answer; some servers report the created properties in a
	// 207 instead, which is equally a success.
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusMultiStatus:
	case http.StatusMethodNotAllowed:
		return fmt.Errorf("%s: %w", path, errCollectionExists)
	default:
		return fmt.Errorf("failed to create calendar: %s", resp.Status)
	}
	return nil
}

// doProppatch renames or recolours a collection. The display name and
// the colour are the two properties a server will let anyone change;
// the component set is protected on every server in use, which is why
// the create form asks for it once and this form does not offer it.
func doProppatch(ctx context.Context, client *http.Client, target, name, color string) error {
	var props bytes.Buffer
	if name != "" {
		fmt.Fprintf(&props, "<D:displayname>%s</D:displayname>", xmlEscape(name))
	}
	if color != "" {
		fmt.Fprintf(&props, "<A:calendar-color>%s</A:calendar-color>", xmlEscape(color))
	}
	if props.Len() == 0 {
		return nil
	}
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	fmt.Fprintf(&buf, `<D:propertyupdate xmlns:D="DAV:" xmlns:A="http://apple.com/ns/ical/"><D:set><D:prop>%s</D:prop></D:set></D:propertyupdate>`, props.String())

	req, err := http.NewRequestWithContext(ctx, "PROPPATCH", target, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/xml; charset=\"utf-8\"")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusMultiStatus &&
		resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("failed to change the collection: %s", resp.Status)
	}
	return nil
}

// doDeleteCollection removes a collection and everything in it. WebDAV
// 9.6 knows no other kind of delete: Depth is infinity and there is no
// asking. Whatever guard there is belongs on the page before this.
func doDeleteCollection(ctx context.Context, client *http.Client, target string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, target, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("failed to delete the collection: %s", resp.Status)
	}
	return nil
}

func xmlEscape(s string) string {
	var buf bytes.Buffer
	xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

// canonicalCollectionPath normalizes a collection href or stored path so
// identities compare stably regardless of server formatting. The trailing
// slash keeps requests redirect-free and prefix matches collision-safe.
func canonicalCollectionPath(href string) string {
	href = strings.TrimSpace(href)
	if u, err := url.Parse(href); err == nil {
		href = u.Path
	}
	if !strings.HasPrefix(href, "/") {
		href = "/" + href
	}
	href = path.Clean(href)
	if href != "/" {
		href += "/"
	}
	return href
}

// listCalendars fetches the calendar list with names, colors, and supported
// component sets in a single PROPFIND.
func listCalendars(ctx context.Context, client *http.Client, baseURL *url.URL, homeSet string) ([]CalendarInfo, error) {
	fullURL := baseURL.ResolveReference(&url.URL{Path: homeSet}).String()
	ms, err := doPropfind(ctx, client, fullURL, `<D:propfind xmlns:D="DAV:" xmlns:A="http://apple.com/ns/ical/" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:prop><D:resourcetype/><D:displayname/><C:supported-calendar-component-set/><D:current-user-privilege-set/><A:calendar-color/></D:prop></D:propfind>`)
	if err != nil {
		return nil, err
	}

	var infos []CalendarInfo
	for _, resp := range ms.Responses {
		href := canonicalCollectionPath(resp.Href)
		// Found and missing properties come in separate propstats.
		var isCalendar bool
		var name, color string
		var comps []string
		writable, privKnown := false, false
		for _, ps := range resp.PropStat {
			if !strings.Contains(ps.Status, "200") {
				continue
			}
			if ps.Prop.ResourceType.Calendar != nil {
				isCalendar = true
			}
			if ps.Prop.DisplayName != "" {
				name = ps.Prop.DisplayName
			}
			if c := strings.TrimSpace(ps.Prop.CalendarColor); c != "" {
				color = c
			}
			for _, comp := range ps.Prop.ComponentSet.Comps {
				comps = append(comps, comp.Name)
			}
			for _, p := range ps.Prop.Privileges {
				privKnown = true
				if p.Write != nil || p.WriteContent != nil || p.Bind != nil {
					writable = true
				}
			}
		}
		if !isCalendar {
			continue
		}
		infos = append(infos, CalendarInfo{
			Path:  href,
			Name:  name,
			Color: color,
			// Servers that report no privileges get the benefit of the doubt.
			Writable:              writable || !privKnown,
			SupportedComponentSet: comps,
		})
	}

	// Servers list collections in storage order; sort them for a stable
	// sidebar.
	sort.Slice(infos, func(i, j int) bool {
		return strings.ToLower(infos[i].Name) < strings.ToLower(infos[j].Name)
	})
	return infos, nil
}

// logDAVExchange prints one upstream DAV round trip: the query for
// REPORTs, the status, and what kind of payload came back. Response
// bodies are re-wrapped so the caller still reads them.
func logDAVExchange(l echo.Logger, req *http.Request, resp *http.Response, err error) {
	q := ""
	if req.Method == "REPORT" && req.GetBody != nil {
		if r, e := req.GetBody(); e == nil {
			b, _ := io.ReadAll(r)
			r.Close()
			q = " query=" + string(b)
		}
	}
	if err != nil {
		l.Printf("dav: %s %s error=%v%s", req.Method, req.URL.Path, err, q)
		return
	}
	b, e := io.ReadAll(resp.Body)
	resp.Body.Close()
	if e != nil {
		l.Printf("dav: %s %s status=%d read error=%v%s", req.Method, req.URL.Path, resp.StatusCode, e, q)
		return
	}
	resp.Body = io.NopCloser(bytes.NewReader(b))
	l.Printf("dav: %s %s status=%d bytes=%d events=%d todos=%d cards=%d%s",
		req.Method, req.URL.Path, resp.StatusCode, len(b),
		bytes.Count(b, []byte("BEGIN:VEVENT")), bytes.Count(b, []byte("BEGIN:VTODO")),
		bytes.Count(b, []byte("BEGIN:VCARD")), q)
}

// webdavRoundTripper handles authentication and follows redirects while
// preserving the HTTP method. Go's default client changes non-GET/HEAD
// methods to GET on 301/302 redirects, which breaks WebDAV.
type webdavRoundTripper struct {
	upstream http.RoundTripper
	session  *alborz.Session

	// Debug logger for upstream DAV traffic; nil keeps it silent.
	// Queries and status lines only, never credentials.
	debug echo.Logger
}

func (rt *webdavRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.session.SetHTTPBasicAuth(req)

	resp, err := rt.upstream.RoundTrip(req)
	if rt.debug != nil {
		logDAVExchange(rt.debug, req, resp, err)
	}
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		loc := resp.Header.Get("Location")
		if loc != "" {
			resp.Body.Close()
			return rt.followRedirect(req, loc, 10)
		}
	}

	return resp, nil
}

func (rt *webdavRoundTripper) followRedirect(orig *http.Request, location string, maxRedirects int) (*http.Response, error) {
	if maxRedirects <= 0 {
		return nil, fmt.Errorf("too many redirects")
	}

	locURL, err := orig.URL.Parse(location)
	if err != nil {
		return nil, err
	}

	var body io.ReadCloser
	if orig.GetBody != nil {
		body, err = orig.GetBody()
		if err != nil {
			return nil, err
		}
	}

	req, err := http.NewRequestWithContext(orig.Context(), orig.Method, locURL.String(), body)
	if err != nil {
		return nil, err
	}

	for k, v := range orig.Header {
		if k != "Authorization" {
			req.Header[k] = v
		}
	}

	rt.session.SetHTTPBasicAuth(req)

	resp, err := rt.upstream.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		loc := resp.Header.Get("Location")
		if loc != "" {
			resp.Body.Close()
			return rt.followRedirect(req, loc, maxRedirects-1)
		}
	}

	return resp, nil
}

func newClient(u *url.URL, httpClient *http.Client) (*caldav.Client, error) {
	c, err := caldav.NewClient(httpClient, u.String())
	if err != nil {
		return nil, fmt.Errorf("failed to create CalDAV client: %v", err)
	}

	return c, nil
}

// createCalendar adds a collection to the account's calendar home and
// forgets the cached list, so the new one appears at once.
func (p *plugin) createCalendar(ctx context.Context, session *alborz.Session, name string, components []string, color string) error {
	c, err := p.client(session)
	if err != nil {
		return err
	}
	davBase, _ := p.davURL(session)

	principal, err := c.FindCurrentUserPrincipal(ctx)
	if err != nil {
		return fmt.Errorf("failed to query CalDAV principal: %v", err)
	}
	homeSet, err := c.FindCalendarHomeSet(ctx, principal)
	if err != nil {
		return fmt.Errorf("failed to query CalDAV calendar home set: %v", err)
	}

	// A fresh path under the home set: the display name is the user's,
	// the path segment only has to be unique and legal in a URL.
	segment := url.PathEscape(strings.ToLower(strings.ReplaceAll(strings.TrimSpace(name), " ", "-")))
	if segment == "" {
		segment = "calendar"
	}

	// Two collections may share a display name but not an address, and
	// a server answers a taken one with 405. Walk to the first free
	// address rather than making the reader rename what they meant.
	home := canonicalCollectionPath(homeSet)
	client := p.httpClient(session)
	for attempt := 1; ; attempt++ {
		try := segment
		if attempt > 1 {
			try = fmt.Sprintf("%s-%d", segment, attempt)
		}
		target := davBase.ResolveReference(&url.URL{Path: home + try + "/"}).String()
		err := doMkcalendar(ctx, client, target, name, components, color)
		if err == nil {
			break
		}
		if !errors.Is(err, errCollectionExists) || attempt == 20 {
			return err
		}
	}
	p.calendars.Forget(session.Username())
	return nil
}

func (p *plugin) clientWithCalendars(ctx context.Context, session *alborz.Session) (*caldav.Client, []CalendarInfo, error) {
	c, err := p.client(session)
	if err != nil {
		return nil, nil, err
	}
	davBase, _ := p.davURL(session)

	// Principal, home set, and calendar list are three sequential round
	// trips answering which calendars the account has, so they are found
	// once per user rather than on every page. The load outlives the
	// request that starts it: a second page waiting on it must not be
	// failed by the first one's reader going away.
	infos, err := p.calendars.Get(session.Username(), func() ([]CalendarInfo, error) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), discoveryTimeout)
		defer cancel()

		principal, err := c.FindCurrentUserPrincipal(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to query CalDAV principal: %v", err)
		}

		calendarHomeSet, err := c.FindCalendarHomeSet(ctx, principal)
		if err != nil {
			return nil, fmt.Errorf("failed to query CalDAV calendar home set: %v", err)
		}

		infos, err := listCalendars(ctx, p.httpClient(session), davBase, calendarHomeSet)
		if err != nil {
			return nil, fmt.Errorf("failed to find calendars: %v", err)
		}
		return infos, nil
	})
	if err != nil {
		return nil, nil, err
	}
	if len(infos) == 0 {
		return nil, nil, errNoCalendar
	}

	return c, infos, nil
}

func (p *plugin) clientWithCalendar(ctx context.Context, session *alborz.Session) (*caldav.Client, *CalendarInfo, error) {
	c, calendars, err := p.clientWithCalendars(ctx, session)
	if err != nil {
		return nil, nil, err
	}
	return c, &calendars[0], nil
}

type CalendarObject struct {
	*caldav.CalendarObject

	// Account owning the object, set only in the unified view
	Account string
}

// Alarm is a reminder an object carries (RFC 5545 3.6.6). Nothing here
// acts on one - Alborz is request-driven and has nothing that wakes up -
// but an alarm set in another client is part of the event, and showing
// it is the difference between "no reminder" and "a reminder this page
// will not tell you about".
type Alarm struct {
	At time.Time
	// Action is what the alarm asks for: DISPLAY, EMAIL, AUDIO. Apple
	// writes NONE for an event it deliberately leaves silent.
	Action string
}

// Alarms are the moments the object asks to be reminded at, resolved
// against the component they hang on: a trigger is usually an offset
// from the start, sometimes from the end, and occasionally an instant.
func (ao CalendarObject) Alarms() []Alarm {
	var comp *ical.Component
	for _, child := range ao.Data.Children {
		if child.Name == ical.CompEvent || child.Name == ical.CompToDo {
			comp = child
			break
		}
	}
	if comp == nil {
		return nil
	}
	start, _ := comp.Props.DateTime(ical.PropDateTimeStart, nil)
	end, _ := comp.Props.DateTime(ical.PropDateTimeEnd, nil)

	var out []Alarm
	for _, child := range comp.Children {
		if child.Name != ical.CompAlarm {
			continue
		}
		prop := child.Props.Get(ical.PropTrigger)
		if prop == nil {
			continue
		}
		action, _ := child.Props.Text(ical.PropAction)
		var at time.Time
		if prop.ValueType() == ical.ValueDateTime {
			at, _ = prop.DateTime(nil)
		} else if offset, err := prop.Duration(); err == nil {
			base := start
			if prop.Params.Get(ical.ParamRelated) == "END" && !end.IsZero() {
				base = end
			}
			if !base.IsZero() {
				at = base.Add(offset)
			}
		}
		if at.IsZero() {
			continue
		}
		out = append(out, Alarm{At: at, Action: action})
	}
	return out
}

// Occurrence is one drawing of an event: a component of an object plus
// the moment it falls on. A recurring event has many and the component
// names only the first, so a day holds occurrences rather than objects.
//
// Two shapes arrive. A server honouring the expand request returns one
// component per instance (RFC 4791 9.6.5); one that ignores it returns
// the master carrying its rule. Both are drawn, which is what lets a
// recurring event made in another client appear here.
type Occurrence struct {
	CalendarObject
	Event *ical.Event
	Start time.Time
	End   time.Time
}

// AllDay reports whether this occurrence occupies whole days, read from
// its own component rather than the object's first.
func (o Occurrence) AllDay() bool {
	prop := o.Event.Props.Get(ical.PropDateTimeStart)
	return prop != nil && prop.ValueType() == ical.ValueDate
}

// Summary is what the row says.
func (o Occurrence) Summary() string {
	summary, _ := o.Event.Props.Text(ical.PropSummary)
	return summary
}

// occurrences lists every instance of an object that begins before end
// and ends after start, in the display timezone.
func occurrences(obj CalendarObject, loc *time.Location, start, end time.Time) []Occurrence {
	var out []Occurrence
	for i := range obj.Data.Events() {
		event := &obj.Data.Events()[i]
		first, _ := event.DateTimeStart(nil)
		last, _ := event.DateTimeEnd(nil)
		span := last.Sub(first)
		if span < 0 {
			span = 0
		}

		// A rule still on the component means the server did not expand
		// it, so it is expanded here over the window being drawn.
		set, err := event.RecurrenceSet(loc)
		if err != nil || set == nil {
			out = append(out, Occurrence{CalendarObject: obj, Event: event, Start: first, End: last})
			continue
		}
		for _, at := range set.Between(start.Add(-span), end, true) {
			out = append(out, Occurrence{
				CalendarObject: obj, Event: event,
				Start: at, End: at.Add(span),
			})
		}
	}
	return out
}

func newCalendarObjectList(cos []caldav.CalendarObject) []CalendarObject {
	l := make([]CalendarObject, len(cos))
	for i := range cos {
		l[i] = CalendarObject{CalendarObject: &cos[i]}
	}
	return l
}

func (ao CalendarObject) URL() string {
	return "/calendar/" + url.PathEscape(ao.Path)
}

// AllDay reports a start given as a bare date, iCalendar's way of saying
// the event occupies whole days instead of a span of clock time. Such an
// event has no start time to show, and a formatted one would be the
// timezone's midnight, not a fact about the event.
func (ao CalendarObject) AllDay() bool {
	events := ao.Data.Events()
	if len(events) == 0 {
		return false
	}
	prop := events[0].Props.Get(ical.PropDateTimeStart)
	return prop != nil && prop.ValueType() == ical.ValueDate
}

type TaskObject struct {
	*caldav.CalendarObject

	// Account owning the object, set only in the unified view
	Account string
}

func newTaskObjectList(cos []caldav.CalendarObject) []TaskObject {
	l := make([]TaskObject, len(cos))
	for i := range cos {
		l[i] = TaskObject{CalendarObject: &cos[i]}
	}
	return l
}

func (t TaskObject) URL() string {
	return "/tasks/" + url.PathEscape(t.Path)
}

// accountCalendars is one signed-in account's calendar set.
type accountCalendars struct {
	account   string
	session   *alborz.Session
	client    *caldav.Client
	calendars []CalendarInfo
}

// pooledCalendars resolves every signed-in account that has CalDAV:
// calendar pages are always pooled across accounts. An account that
// fails is logged and skipped, so one flaky server does not take the
// page down; the error surfaces only when no account answered.
func (p *plugin) pooledCalendars(ctx *alborz.Context) ([]accountCalendars, error) {
	var accounts []accountCalendars
	var lastErr error
	for _, s := range ctx.Sessions() {
		if _, ok := p.davURL(s); !ok {
			continue
		}
		c, calendars, err := p.clientWithCalendars(ctx.Request().Context(), s)
		if err != nil {
			lastErr = err
			ctx.Logger().Printf("caldav: skipping %q in the pooled view: %v", s.Username(), err)
			continue
		}
		infos := make([]CalendarInfo, len(calendars))
		for i, cal := range calendars {
			infos[i] = cal
			infos[i].Account = s.Username()
		}
		accounts = append(accounts, accountCalendars{
			account:   s.Username(),
			session:   s,
			client:    c,
			calendars: infos,
		})
	}
	if len(accounts) == 0 {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, errNoCalendar
	}
	return accounts, nil
}

// querySite is one visible calendar to query, bound to its account's client.
type querySite struct {
	account string
	client  *caldav.Client
	path    string
	name    string
}

// querySites runs the calendar query against every site, a few at a time,
// and tags each result with its owning account.
func querySites(ctx *alborz.Context, sites []querySite, query *caldav.CalendarQuery) ([]CalendarObject, error) {
	type result struct {
		site   querySite
		events []caldav.CalendarObject
		err    error
	}
	reqCtx := ctx.Request().Context()
	results := make(chan result, len(sites))
	sem := make(chan struct{}, maxCalendarQueryConcurrency)
	for _, site := range sites {
		go func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			events, err := site.client.QueryCalendar(reqCtx, site.path, query)
			results <- result{site: site, events: events, err: err}
		}()
	}
	var events []CalendarObject
	for range sites {
		r := <-results
		if r.err != nil {
			return nil, fmt.Errorf("failed to query calendar %s: %v", r.site.name, r.err)
		}
		for i := range r.events {
			events = append(events, CalendarObject{CalendarObject: &r.events[i], Account: r.site.account})
		}
	}
	return events, nil
}

// CalendarGroup is one account's calendars, for create-form selects.
type CalendarGroup struct {
	Account     string
	Collections []CalendarInfo
}

// writableGroups lists every account's writable calendars of one kind,
// so a create form can offer any account as the destination.
func (p *plugin) writableGroups(ctx *alborz.Context, supports func(CalendarInfo) bool) ([]CalendarGroup, error) {
	accounts, err := p.pooledCalendars(ctx)
	if err != nil {
		return nil, err
	}
	var groups []CalendarGroup
	for _, acc := range accounts {
		var cals []CalendarInfo
		for _, cal := range acc.calendars {
			if cal.Writable && supports(cal) {
				cals = append(cals, cal)
			}
		}
		if len(cals) > 0 {
			groups = append(groups, CalendarGroup{Account: acc.account, Collections: cals})
		}
	}
	return groups, nil
}

// resolveCreateCalendar turns a create form's "account|path" choice into
// that account's client and the bare path, refusing unknown targets.
func (p *plugin) resolveCreateCalendar(ctx *alborz.Context, value string, supports func(CalendarInfo) bool) (*caldav.Client, string, string, error) {
	acct, calPath, ok := strings.Cut(value, "|")
	session := ctx.SessionFor(acct)
	if !ok || session == nil {
		return nil, "", "", echo.NewHTTPError(http.StatusBadRequest, "unknown calendar")
	}
	c, cals, err := p.clientWithCalendars(ctx.Request().Context(), session)
	if err != nil {
		return nil, "", "", err
	}
	for _, cal := range cals {
		if cal.Writable && supports(cal) && cal.Path == calPath {
			return c, calPath, acct, nil
		}
	}
	return nil, "", "", echo.NewHTTPError(http.StatusBadRequest, "unknown calendar")
}
