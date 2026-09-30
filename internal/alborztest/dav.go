package alborztest

import (
	"encoding/xml"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
)

// The homes the DAV server names for the account.
const (
	DAVHome  = "/dav/calendars/u/"
	DAVBooks = "/dav/addressbooks/u/"
)

// DAVServer answers the CalDAV and CardDAV that listing, making, deleting and writing to collections need.
type DAVServer struct {
	*httptest.Server
	mu        sync.Mutex
	cals      map[string]string
	books     map[string]string
	refuse    map[string]bool
	hangup    map[string]bool
	failing   map[string]bool
	keep      map[string]bool
	gone      map[string]bool
	rscales   []string
	objects   map[string]string
	schedules bool
	puts      []string
}

type DAVOption func(*DAVServer)

// Calendar lists a calendar at path.
func Calendar(path, name string) DAVOption { return func(d *DAVServer) { d.cals[path] = name } }

// Book lists an address book at path.
func Book(path, name string) DAVOption { return func(d *DAVServer) { d.books[path] = name } }

// Refusing answers a DELETE of a path, and a PUT into or at it, with 403.
func Refusing(paths ...string) DAVOption { return func(d *DAVServer) { d.refuse = set(paths) } }

// Hangup closes the connection on a PUT into a collection, a server that does not answer.
func Hangup(paths ...string) DAVOption { return func(d *DAVServer) { d.hangup = set(paths) } }

// Failing answers a REPORT on a collection with 503, a server that cannot serve it for now.
func Failing(paths ...string) DAVOption { return func(d *DAVServer) { d.failing = set(paths) } }

// Keeping answers a DELETE of a collection with 204, and lists it all the same.
func Keeping(paths ...string) DAVOption { return func(d *DAVServer) { d.keep = set(paths) } }

// Gone are collections deleted elsewhere: unlisted, and 404 to a REPORT.
func Gone(paths ...string) DAVOption { return func(d *DAVServer) { d.gone = set(paths) } }

// RScales is the supported-rscale-set the calendars advertise; without it they advertise none.
func RScales(scales ...string) DAVOption {
	return func(d *DAVServer) { d.rscales = append([]string{}, scales...) }
}

// Object serves body at path.
func Object(path, body string) DAVOption { return func(d *DAVServer) { d.objects[path] = body } }

// Schedules advertises calendar-auto-schedule: the server sends invitations itself (RFC 6638 2).
func Schedules() DAVOption { return func(d *DAVServer) { d.schedules = true } }

func set(paths []string) map[string]bool {
	m := map[string]bool{}
	for _, p := range paths {
		m[p] = true
	}
	return m
}

func DAV(t *testing.T, opts ...DAVOption) *DAVServer {
	t.Helper()
	d := &DAVServer{cals: map[string]string{}, books: map[string]string{}, objects: map[string]string{},
		refuse: map[string]bool{}, hangup: map[string]bool{}, failing: map[string]bool{}, keep: map[string]bool{}, gone: map[string]bool{}}
	d.Apply(opts...)
	d.Server = httptest.NewServer(d)
	t.Cleanup(d.Close)
	return d
}

// Apply changes the server as the options say.
func (d *DAVServer) Apply(opts ...DAVOption) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, o := range opts {
		o(d)
	}
}

// Upstreams name the server for CalDAV and CardDAV.
func (d *DAVServer) Upstreams() []string {
	host := strings.TrimPrefix(d.URL, "http://")
	return []string{"caldav+insecure://" + host + "/dav", "carddav+insecure://" + host + "/dav"}
}

// At begins the paths pages hold for its collections (ADR 28).
func (d *DAVServer) At() string { return At(d.URL) }

// Puts are the bodies written to it, in order.
func (d *DAVServer) Puts() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.puts)
}

// Calendars are the calendars it holds.
func (d *DAVServer) Calendars() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for p := range d.cals {
		if !d.gone[p] {
			out = append(out, p)
		}
	}
	return out
}

func (d *DAVServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	d.mu.Lock()
	defer d.mu.Unlock()
	switch r.Method {
	case "OPTIONS":
		dav := "1, 3, calendar-access, addressbook"
		if d.schedules {
			dav = "1, 3, calendar-access, calendar-auto-schedule"
		}
		w.Header().Set("DAV", dav)
		w.WriteHeader(http.StatusOK)
	case "PROPFIND":
		if d.gone[r.URL.Path] {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		d.propfind(w, r, string(body))
	case "MKCALENDAR":
		d.mkcalendar(w, r, string(body))
	case "DELETE":
		if d.refuse[r.URL.Path] {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if !d.keep[r.URL.Path] {
			d.gone[r.URL.Path] = true
		}
		w.WriteHeader(http.StatusNoContent)
	case "REPORT":
		if d.gone[r.URL.Path] {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if d.failing[r.URL.Path] {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		d.report(w, r, string(body))
	case "PUT":
		if d.hangup[path.Dir(r.URL.Path)+"/"] {
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		if d.refuse[path.Dir(r.URL.Path)+"/"] || d.refuse[r.URL.Path] {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, "read-only collection")
			return
		}
		d.puts = append(d.puts, string(body))
		w.WriteHeader(http.StatusOK)
	case "GET":
		obj, ok := d.objects[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/calendar")
		w.Header().Set("ETag", `"1"`)
		io.WriteString(w, obj)
	default:
		w.WriteHeader(http.StatusOK)
	}
}

// report answers a REPORT on a calendar with the objects in it, or those a multiget names, and one on
// an object with that object, as a server that filters by nothing but the text it is asked to match; a
// sync-collection gets a token as well.
func (d *DAVServer) report(w http.ResponseWriter, r *http.Request, body string) {
	_, calendar := d.cals[r.URL.Path]
	_, book := d.books[r.URL.Path]
	_, object := d.objects[r.URL.Path]
	if !calendar && !book && !object {
		multistatus(w)
		return
	}
	element := `C:calendar-data`
	if _, inBook := d.books[path.Dir(r.URL.Path)+"/"]; book || inBook {
		element = `A:address-data xmlns:A="urn:ietf:params:xml:ns:carddav"`
	}
	var resps []string
	for _, p := range slices.Sorted(maps.Keys(d.objects)) {
		if p != r.URL.Path && (path.Dir(p)+"/" != r.URL.Path || strings.Contains(body, "multiget") && !strings.Contains(body, p)) {
			continue
		}
		if m := textMatch.FindStringSubmatch(body); m != nil && !strings.Contains(d.objects[p], m[1]) {
			continue
		}
		var data strings.Builder
		xml.EscapeText(&data, []byte(d.objects[p]))
		resps = append(resps, propResponse(p, `<getetag>"1"</getetag><`+element+`>`+data.String()+"</"+strings.Fields(element)[0]+">"))
	}
	if strings.Contains(body, "sync-collection") {
		resps = append(resps, "<sync-token>1</sync-token>")
	}
	multistatus(w, resps...)
}

var textMatch = regexp.MustCompile(`text-match[^>]*>([^<]*)<`)

func multistatus(w http.ResponseWriter, responses ...string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusMultiStatus)
	io.WriteString(w, `<?xml version="1.0"?><multistatus xmlns="DAV:" `+
		`xmlns:C="urn:ietf:params:xml:ns:caldav" `+
		`xmlns:CR="urn:ietf:params:xml:ns:carddav" `+
		`xmlns:CS="http://calendarserver.org/ns/" `+
		`xmlns:AP="http://apple.com/ns/ical/">`)
	for _, resp := range responses {
		io.WriteString(w, resp)
	}
	io.WriteString(w, "</multistatus>")
}

func propResponse(href, props string) string {
	return "<response><href>" + href + "</href><propstat><prop>" + props +
		"</prop><status>HTTP/1.1 200 OK</status></propstat></response>"
}

func (d *DAVServer) propfind(w http.ResponseWriter, r *http.Request, body string) {
	switch {
	case strings.Contains(body, "current-user-principal"):
		multistatus(w, propResponse(r.URL.Path,
			"<current-user-principal><href>/dav/principals/u/</href></current-user-principal>"))
	case strings.Contains(body, "calendar-home-set"):
		multistatus(w, propResponse(r.URL.Path,
			"<C:calendar-home-set><href>"+DAVHome+"</href></C:calendar-home-set>"))
	case strings.Contains(body, "addressbook-home-set"):
		multistatus(w, propResponse(r.URL.Path,
			"<CR:addressbook-home-set><href>"+DAVBooks+"</href></CR:addressbook-home-set>"))
	case strings.HasPrefix(r.URL.Path, DAVBooks):
		var resps []string
		for _, p := range slices.Sorted(maps.Keys(d.books)) {
			resps = append(resps, propResponse(p, "<resourcetype><collection/><CR:addressbook/></resourcetype>"+
				"<displayname>"+d.books[p]+"</displayname>"+
				"<current-user-privilege-set><privilege><write/></privilege></current-user-privilege-set>"))
		}
		multistatus(w, resps...)
	case r.Header.Get("Depth") == "0" && strings.Contains(body, "getctag"):
		multistatus(w, propResponse(r.URL.Path, "<CS:getctag>v1</CS:getctag>"))
	default:
		rscales := ""
		if d.rscales != nil {
			rscales = "<C:supported-rscale-set>"
			for _, scale := range d.rscales {
				rscales += "<C:supported-rscale>" + scale + "</C:supported-rscale>"
			}
			rscales += "</C:supported-rscale-set>"
		}
		var resps []string
		for _, p := range slices.Sorted(maps.Keys(d.cals)) {
			if d.gone[p] {
				continue
			}
			resps = append(resps, propResponse(p,
				"<resourcetype><collection/><C:calendar/></resourcetype>"+
					"<displayname>"+d.cals[p]+"</displayname>"+
					"<AP:calendar-color>#3366cc</AP:calendar-color>"+
					"<C:supported-calendar-component-set><C:comp name=\"VEVENT\"/>"+
					"<C:comp name=\"VTODO\"/></C:supported-calendar-component-set>"+rscales+
					"<current-user-privilege-set><privilege><write/></privilege>"+
					"</current-user-privilege-set>"))
		}
		multistatus(w, resps...)
	}
}

func (d *DAVServer) mkcalendar(w http.ResponseWriter, r *http.Request, body string) {
	p := r.URL.Path
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	if _, ok := d.cals[p]; ok && !d.gone[p] {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	name := "New"
	if i := strings.Index(body, "<D:displayname>"); i >= 0 {
		name = body[i+len("<D:displayname>"):]
		name = name[:strings.Index(name, "</D:displayname>")]
	}
	d.cals[p] = name
	delete(d.gone, p)
	w.WriteHeader(http.StatusCreated)
}
