package alpscaldav

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"git.sr.ht/~migadu/alps"
	"github.com/emersion/go-webdav/caldav"
)

var errNoCalendar = fmt.Errorf("caldav: no calendar found")

type CalendarInfo struct {
	Path    string
	Name    string
	Visible bool
}

// webdavRoundTripper handles authentication and follows redirects while
// preserving the HTTP method. Go's default client changes non-GET/HEAD
// methods to GET on 301/302 redirects, which breaks WebDAV.
type webdavRoundTripper struct {
	upstream http.RoundTripper
	session  *alps.Session
}

func (rt *webdavRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.session.SetHTTPBasicAuth(req)

	resp, err := rt.upstream.RoundTrip(req)
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

func newClient(u *url.URL, session *alps.Session) (*caldav.Client, error) {
	rt := &webdavRoundTripper{
		upstream: http.DefaultTransport,
		session:  session,
	}
	httpClient := &http.Client{
		Transport: rt,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	c, err := caldav.NewClient(httpClient, u.String())
	if err != nil {
		return nil, fmt.Errorf("failed to create CalDAV client: %v", err)
	}

	return c, nil
}

func (p *plugin) clientWithCalendars(ctx context.Context, session *alps.Session) (*caldav.Client, []CalendarInfo, error) {
	c, err := newClient(p.url, session)
	if err != nil {
		return nil, nil, err
	}

	principal, err := c.FindCurrentUserPrincipal(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to query CalDAV principal: %v", err)
	}

	calendarHomeSet, err := c.FindCalendarHomeSet(ctx, principal)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to query CalDAV calendar home set: %v", err)
	}

	calendars, err := c.FindCalendars(ctx, calendarHomeSet)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to find calendars: %v", err)
	}
	if len(calendars) == 0 {
		return nil, nil, errNoCalendar
	}

	infos := make([]CalendarInfo, len(calendars))
	for i, cal := range calendars {
		infos[i] = CalendarInfo{
			Path: cal.Path,
			Name: cal.Name,
		}
	}

	// Servers list collections in storage order; sort them for a stable
	// sidebar.
	sort.Slice(infos, func(i, j int) bool {
		return strings.ToLower(infos[i].Name) < strings.ToLower(infos[j].Name)
	})
	return c, infos, nil
}

func (p *plugin) clientWithCalendar(ctx context.Context, session *alps.Session) (*caldav.Client, *CalendarInfo, error) {
	c, calendars, err := p.clientWithCalendars(ctx, session)
	if err != nil {
		return nil, nil, err
	}
	return c, &calendars[0], nil
}

type CalendarObject struct {
	*caldav.CalendarObject
}

func newCalendarObjectList(cos []caldav.CalendarObject) []CalendarObject {
	l := make([]CalendarObject, len(cos))
	for i := range cos {
		l[i] = CalendarObject{&cos[i]}
	}
	return l
}

func (ao CalendarObject) URL() string {
	return "/calendar/" + url.PathEscape(ao.Path)
}
