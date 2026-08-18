package alborzcaldav

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/emersion/go-webdav/caldav"
	"git.mehdix.org/alborz"
)

var errNoCalendar = fmt.Errorf("caldav: no calendar found")

type CalendarInfo struct {
	Path                  string
	Name                  string
	Color                 string
	Visible               bool
	Writable              bool
	SupportedComponentSet []string
}

func (c CalendarInfo) SupportsTodo() bool {
	if len(c.SupportedComponentSet) == 0 {
		return true
	}
	for _, comp := range c.SupportedComponentSet {
		if comp == "VTODO" {
			return true
		}
	}
	return false
}

func (c CalendarInfo) SupportsEvent() bool {
	if len(c.SupportedComponentSet) == 0 {
		return true
	}
	for _, comp := range c.SupportedComponentSet {
		if comp == "VEVENT" {
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
	jar := p.jar(session)
	return &http.Client{
		Transport: p.cache.Transport(session.Username(), &webdavRoundTripper{
			upstream: http.DefaultTransport,
			session:  session,
		}, jar),
		Jar: jar,
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

// webdavRoundTripper handles authentication and follows redirects while
// preserving the HTTP method. Go's default client changes non-GET/HEAD
// methods to GET on 301/302 redirects, which breaks WebDAV.
type webdavRoundTripper struct {
	upstream http.RoundTripper
	session  *alborz.Session
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

func newClient(u *url.URL, httpClient *http.Client) (*caldav.Client, error) {
	c, err := caldav.NewClient(httpClient, u.String())
	if err != nil {
		return nil, fmt.Errorf("failed to create CalDAV client: %v", err)
	}

	return c, nil
}

func (p *plugin) clientWithCalendars(ctx context.Context, session *alborz.Session) (*caldav.Client, []CalendarInfo, error) {
	c, err := p.client(session)
	if err != nil {
		return nil, nil, err
	}
	davBase, _ := p.davURL(session)

	principal, err := c.FindCurrentUserPrincipal(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to query CalDAV principal: %v", err)
	}

	calendarHomeSet, err := c.FindCalendarHomeSet(ctx, principal)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to query CalDAV calendar home set: %v", err)
	}

	infos, err := listCalendars(ctx, p.httpClient(session), davBase, calendarHomeSet)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to find calendars: %v", err)
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

type TaskObject struct {
	*caldav.CalendarObject
}

func newTaskObjectList(cos []caldav.CalendarObject) []TaskObject {
	l := make([]TaskObject, len(cos))
	for i := range cos {
		l[i] = TaskObject{&cos[i]}
	}
	return l
}

func (t TaskObject) URL() string {
	return "/tasks/" + url.PathEscape(t.Path)
}
