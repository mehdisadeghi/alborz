package alborzcarddav

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
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav/carddav"
)

// The account's domain has no CardDAV server, or it holds no book;
// the HTTP layer answers 404 rather than crashing on direct URLs.
var errNoAddressBook = alborz.NotFoundf("carddav: no address book found")

// A collection is already there under that path; the form says so
// rather than reporting a bare 405.
var errCollectionExists = errors.New("a collection of that name is already there")

type AddressBookInfo struct {
	Path    string
	Name    string
	Color   string
	Visible bool
	// Only marks the collection a URL is currently narrowed to, and only
	// when it is the sole one: pressing the same link again is how a
	// reader gets back out of a view they pressed their way into.
	Only     bool
	Writable bool

	// Account owning the book, set only in the unified view
	Account string
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
		AddressBook *struct{} `xml:"urn:ietf:params:xml:ns:carddav addressbook,omitempty"`
	} `xml:"resourcetype"`
	DisplayName      string `xml:"displayname"`
	AddressBookColor string `xml:"http://inf-it.com/ns/ab/ addressbook-color"`
	Privileges       []struct {
		Write        *struct{} `xml:"DAV: write"`
		WriteContent *struct{} `xml:"DAV: write-content"`
		Bind         *struct{} `xml:"DAV: bind"`
	} `xml:"current-user-privilege-set>privilege"`
}

func (p *plugin) httpClient(session *alborz.Session) *http.Client {
	jar := p.jar(session)
	return &http.Client{
		// A wedged DAV server fails the request instead of hanging it.
		Timeout: requestTimeout,
		Transport: p.cache.Transport(session.Username(), &webdavRoundTripper{
			upstream: http.DefaultTransport,
			session:  session,
			debug:    p.debug,
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

// listAddressBooks fetches the address book list with names and colors in a
// single PROPFIND.
func listAddressBooks(ctx context.Context, client *http.Client, baseURL *url.URL, homeSet string) ([]AddressBookInfo, error) {
	fullURL := baseURL.ResolveReference(&url.URL{Path: homeSet}).String()
	ms, err := doPropfind(ctx, client, fullURL, `<D:propfind xmlns:D="DAV:" xmlns:I="http://inf-it.com/ns/ab/"><D:prop><D:resourcetype/><D:displayname/><I:addressbook-color/><D:current-user-privilege-set/></D:prop></D:propfind>`)
	if err != nil {
		return nil, err
	}

	var infos []AddressBookInfo
	for _, resp := range ms.Responses {
		href := canonicalCollectionPath(resp.Href)
		// Found and missing properties come in separate propstats.
		var isAddressBook bool
		var name, color string
		writable, privKnown := false, false
		for _, ps := range resp.PropStat {
			if !strings.Contains(ps.Status, "200") {
				continue
			}
			if ps.Prop.ResourceType.AddressBook != nil {
				isAddressBook = true
			}
			if ps.Prop.DisplayName != "" {
				name = ps.Prop.DisplayName
			}
			if c := strings.TrimSpace(ps.Prop.AddressBookColor); c != "" {
				color = c
			}
			for _, p := range ps.Prop.Privileges {
				privKnown = true
				if p.Write != nil || p.WriteContent != nil || p.Bind != nil {
					writable = true
				}
			}
		}
		if !isAddressBook {
			continue
		}
		infos = append(infos, AddressBookInfo{
			Path:  href,
			Name:  name,
			Color: color,
			// Servers that report no privileges get the benefit of the doubt.
			Writable: writable || !privKnown,
		})
	}

	// Servers list collections in storage order; sort them for a stable
	// sidebar.
	sort.Slice(infos, func(i, j int) bool {
		return strings.ToLower(infos[i].Name) < strings.ToLower(infos[j].Name)
	})
	return infos, nil
}

// doMkcol makes an address book. RFC 5689 extended MKCOL is the way to
// make one with its properties in the same round trip; a server that
// refuses it leaves the collection unmade rather than half made.
func doMkcol(ctx context.Context, client *http.Client, target, name, color string) error {
	var props bytes.Buffer
	props.WriteString(`<D:resourcetype><D:collection/><A:addressbook/></D:resourcetype>`)
	fmt.Fprintf(&props, "<D:displayname>%s</D:displayname>", xmlEscape(name))
	if color != "" {
		fmt.Fprintf(&props, "<I:addressbook-color>%s</I:addressbook-color>", xmlEscape(color))
	}
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	fmt.Fprintf(&buf, `<D:mkcol xmlns:D="DAV:" xmlns:A="urn:ietf:params:xml:ns:carddav" xmlns:I="http://inf-it.com/ns/ab/"><D:set><D:prop>%s</D:prop></D:set></D:mkcol>`, props.String())

	req, err := http.NewRequestWithContext(ctx, "MKCOL", target, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/xml; charset=\"utf-8\"")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusMultiStatus:
	case http.StatusMethodNotAllowed:
		return fmt.Errorf("%s: %w", target, errCollectionExists)
	default:
		return fmt.Errorf("failed to create the address book: %s", resp.Status)
	}
	return nil
}

// doProppatch renames or recolours a collection: the two properties a
// server lets anyone change. The resource type is not among them, which
// is why the create form asks what it is once and this one does not.
func doProppatch(ctx context.Context, client *http.Client, target, name, color string) error {
	var props bytes.Buffer
	if name != "" {
		fmt.Fprintf(&props, "<D:displayname>%s</D:displayname>", xmlEscape(name))
	}
	if color != "" {
		fmt.Fprintf(&props, "<I:addressbook-color>%s</I:addressbook-color>", xmlEscape(color))
	}
	if props.Len() == 0 {
		return nil
	}
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	fmt.Fprintf(&buf, `<D:propertyupdate xmlns:D="DAV:" xmlns:I="http://inf-it.com/ns/ab/"><D:set><D:prop>%s</D:prop></D:set></D:propertyupdate>`, props.String())

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

func newClient(u *url.URL, httpClient *http.Client) (*carddav.Client, error) {
	return carddav.NewClient(httpClient, u.String())
}

type AddressObject struct {
	*carddav.AddressObject

	// Account owning the contact, set only in the unified view
	Account string
}

func (ao AddressObject) URL() string {
	return "/contacts/" + url.PathEscape(ao.Path)
}

func (ao AddressObject) DisplayName() string {
	if fn := ao.Card.PreferredValue("FN"); fn != "" {
		return fn
	}
	if n := ao.Card.Name(); n != nil {
		parts := []string{n.GivenName, n.AdditionalName, n.FamilyName}
		var nonEmpty []string
		for _, p := range parts {
			if p != "" {
				nonEmpty = append(nonEmpty, p)
			}
		}
		if len(nonEmpty) > 0 {
			return strings.Join(nonEmpty, " ")
		}
	}
	for _, field := range []string{vcard.FieldNickname, vcard.FieldOrganization, vcard.FieldEmail, vcard.FieldTelephone} {
		if v := ao.Card.PreferredValue(field); v != "" {
			return v
		}
	}
	return ao.Path
}

func (ao AddressObject) PhotoURL() string {
	return ao.Card.PreferredValue("PHOTO")
}

// createAddressBook makes a book on the account's own server, walking
// to the first free address: two collections may share a display name
// but not a path, and a server answers a taken one with 405.
func (p *plugin) createAddressBook(ctx context.Context, session *alborz.Session, name, color string) error {
	c, err := p.client(session)
	if err != nil {
		return err
	}
	davBase, _ := p.davURL(session)

	principal, err := c.FindCurrentUserPrincipal(ctx)
	if err != nil {
		return fmt.Errorf("failed to query CardDAV principal: %v", err)
	}
	homeSet, err := c.FindAddressBookHomeSet(ctx, principal)
	if err != nil {
		return fmt.Errorf("failed to query CardDAV address book home set: %v", err)
	}

	segment := url.PathEscape(strings.ToLower(strings.ReplaceAll(strings.TrimSpace(name), " ", "-")))
	if segment == "" {
		segment = "contacts"
	}
	home := canonicalCollectionPath(homeSet)
	client := p.httpClient(session)
	for attempt := 1; ; attempt++ {
		try := segment
		if attempt > 1 {
			try = fmt.Sprintf("%s-%d", segment, attempt)
		}
		target := davBase.ResolveReference(&url.URL{Path: home + try + "/"}).String()
		err := doMkcol(ctx, client, target, name, color)
		if err == nil {
			break
		}
		if !errors.Is(err, errCollectionExists) || attempt == 20 {
			return err
		}
	}
	p.books.Forget(session.Username())
	return nil
}

// Modified is when the card last changed, for the list to show. There
// is no matching Added: a vCard has no property for when it was made,
// and the only universal answer is WebDAV's DAV:creationdate, which the
// object REPORT does not ask for.
func (ao AddressObject) Modified() time.Time {
	return cardModified(ao.AddressObject)
}
