package alborz_test

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"git.mehdix.org/alborz"
	_ "git.mehdix.org/alborz/plugins/caldav"
	"github.com/fernet/fernet-go"
	"github.com/labstack/echo/v4"
)

// davCal is one calendar the stub lists.
type davCal struct{ name, color string }

// davStub answers the CalDAV a collection's create and delete need:
// principal and home-set discovery, a home-set listing, MKCALENDAR and
// DELETE. A path in refuse answers DELETE with 403; one in keep answers
// 204 but stays listed, as some servers do.
type davStub struct {
	cals   map[string]davCal
	refuse map[string]bool
	keep   map[string]bool
	gone   map[string]bool
}

const davHome = "/dav/calendars/u/"

func (s *davStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	switch r.Method {
	case "OPTIONS":
		w.Header().Set("DAV", "1, 3, calendar-access")
		w.WriteHeader(http.StatusOK)
	case "PROPFIND":
		s.propfind(w, r, string(body))
	case "MKCALENDAR":
		s.mkcalendar(w, r, string(body))
	case "DELETE":
		s.deleteColl(w, r)
	case "REPORT":
		// A calendar-query on a collection holding no objects.
		writeMS(w)
	default:
		w.WriteHeader(http.StatusOK)
	}
}

func writeMS(w http.ResponseWriter, responses ...string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusMultiStatus)
	io.WriteString(w, `<?xml version="1.0"?><multistatus xmlns="DAV:" `+
		`xmlns:C="urn:ietf:params:xml:ns:caldav" `+
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

func (s *davStub) propfind(w http.ResponseWriter, r *http.Request, body string) {
	switch {
	case strings.Contains(body, "current-user-principal"):
		writeMS(w, propResponse(r.URL.Path,
			"<current-user-principal><href>/dav/principals/u/</href></current-user-principal>"))
	case strings.Contains(body, "calendar-home-set"):
		writeMS(w, propResponse(r.URL.Path,
			"<C:calendar-home-set><href>"+davHome+"</href></C:calendar-home-set>"))
	case strings.Contains(body, "getctag"):
		writeMS(w, propResponse(r.URL.Path, "<CS:getctag>v1</CS:getctag>"))
	default:
		var resps []string
		paths := make([]string, 0, len(s.cals))
		for p := range s.cals {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			if s.gone[p] {
				continue
			}
			c := s.cals[p]
			resps = append(resps, propResponse(p,
				"<resourcetype><collection/><C:calendar/></resourcetype>"+
					"<displayname>"+c.name+"</displayname>"+
					"<AP:calendar-color>"+c.color+"</AP:calendar-color>"+
					"<C:supported-calendar-component-set><C:comp name=\"VEVENT\"/>"+
					"<C:comp name=\"VTODO\"/></C:supported-calendar-component-set>"+
					"<current-user-privilege-set><privilege><write/></privilege>"+
					"</current-user-privilege-set>"))
		}
		writeMS(w, resps...)
	}
}

func (s *davStub) mkcalendar(w http.ResponseWriter, r *http.Request, body string) {
	p := r.URL.Path
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	if _, ok := s.cals[p]; ok && !s.gone[p] {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	name := "New"
	if i := strings.Index(body, "<D:displayname>"); i >= 0 {
		name = body[i+len("<D:displayname>"):]
		name = name[:strings.Index(name, "</D:displayname>")]
	}
	s.cals[p] = davCal{name: name, color: "#888888"}
	delete(s.gone, p)
	w.WriteHeader(http.StatusCreated)
}

func (s *davStub) deleteColl(w http.ResponseWriter, r *http.Request) {
	if s.refuse[r.URL.Path] {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if !s.keep[r.URL.Path] {
		s.gone[r.URL.Path] = true
	}
	w.WriteHeader(http.StatusNoContent)
}

// startAlborzDav starts alborz over the IMAP rig and a CalDAV stub.
func startAlborzDav(t *testing.T, imapAddr, davURL string) string {
	t.Helper()
	key := fernet.MustDecodeKeys("YLZFnivEgqo-9cIJcqU6wOS7LhhCrXtgxRvYHoQ6NmA=")[0]
	e := echo.New()
	e.HideBanner, e.HidePort = true, true
	if _, err := alborz.New(e, &alborz.Options{
		Upstreams: []string{
			"test.local=imap+insecure://" + imapAddr,
			"test.local=caldav+insecure://" + strings.TrimPrefix(davURL, "http://") + "/dav",
		},
		Theme:      "alborz",
		ThemesPath: "./themes",
		LoginKey:   key,
	}); err != nil {
		t.Fatalf("start alborz: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go e.Server.Serve(ln)
	t.Cleanup(func() { e.Close() })
	return "http://" + ln.Addr().String()
}

const personalDelete = "/calendars/%2Fdav%2Fcalendars%2Fu%2Fpersonal%2F/delete"
const personalPage = "/calendars/%2Fdav%2Fcalendars%2Fu%2Fpersonal%2F"

// A delete is reported by what the server did, not by its status line:
// a refusal and a 2xx that kept the collection both leave it on its
// page with a message; only a collection that is gone is confirmed.
func TestCollectionDeleteReportsItsOutcome(t *testing.T) {
	personal := davHome + "personal/"
	stub := &davStub{
		cals:   map[string]davCal{personal: {"Personal", "#3366cc"}},
		refuse: map[string]bool{personal: true},
		keep:   map[string]bool{},
		gone:   map[string]bool{},
	}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	base := startAlborzDav(t, startIMAP(t), srv.URL)
	c := login(t, base)

	if resp := postForm(t, c, base+personalDelete, nil); resp.StatusCode != http.StatusFound {
		t.Fatalf("a refused delete answered %s, want a redirect not a 500", resp.Status)
	}
	if page := get(t, c, base+personalPage); !strings.Contains(page, "“Personal” could not be deleted.") {
		t.Errorf("the refused delete left no message on the page")
	}

	stub.refuse = map[string]bool{}
	stub.keep = map[string]bool{personal: true}
	if resp := postForm(t, c, base+personalDelete, nil); resp.StatusCode != http.StatusFound {
		t.Fatalf("delete: %s", resp.Status)
	}
	if page := get(t, c, base+personalPage); !strings.Contains(page, "kept “Personal”") {
		t.Errorf("a delete the server accepted but ignored was reported as done")
	}

	stub.keep = map[string]bool{}
	if resp := postForm(t, c, base+personalDelete, nil); resp.StatusCode != http.StatusFound {
		t.Fatalf("delete: %s", resp.Status)
	}
	if page := get(t, c, base+"/calendar"); !strings.Contains(page, "“Personal” was deleted.") {
		t.Errorf("a completed delete was not confirmed")
	}
}
