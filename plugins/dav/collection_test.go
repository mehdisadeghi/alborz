package dav

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"git.mehdix.org/alborz"
)

type testProps struct {
	ResourceType struct {
		Calendar *struct{} `xml:"urn:ietf:params:xml:ns:caldav calendar,omitempty"`
	} `xml:"resourcetype"`
	DisplayName  string     `xml:"displayname"`
	PrivilegeSet Privileges `xml:"current-user-privilege-set>privilege"`
}

func (p testProps) Collection() (string, string, bool) {
	return p.DisplayName, "", p.ResourceType.Calendar != nil
}
func (p testProps) Privileges() Privileges { return p.PrivilegeSet }

type answer struct {
	status int
	body   string
}

func (a answer) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: a.status, Status: http.StatusText(a.status), Header: http.Header{},
		Body: io.NopCloser(strings.NewReader(a.body))}, nil
}

const homeListing = `<?xml version="1.0"?>
<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
 <D:response><D:href>/cal/u/</D:href>
  <D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>
 </D:response>
 <D:response><D:href>/cal/u/work/</D:href>
  <D:propstat><D:prop><D:resourcetype><D:collection/><C:calendar/></D:resourcetype><D:displayname>Work</D:displayname>
   <D:current-user-privilege-set><D:privilege><D:read/></D:privilege></D:current-user-privilege-set></D:prop>
   <D:status>HTTP/1.1 200 OK</D:status></D:propstat>
 </D:response>
 <D:response><D:href>/cal/u/Family%20Days</D:href>
  <D:propstat><D:prop><D:resourcetype><D:collection/><C:calendar/></D:resourcetype><D:displayname>Family</D:displayname></D:prop>
   <D:status>HTTP/1.1 200 OK</D:status></D:propstat>
  <D:propstat><D:prop><D:displayname/></D:prop><D:status>HTTP/1.1 404 Not Found</D:status></D:propstat>
 </D:response>
</D:multistatus>`

func TestListCollectionsReadsWhatTheServerSaid(t *testing.T) {
	client := &http.Client{Transport: answer{http.StatusMultiStatus, homeListing}}
	base, _ := url.Parse("https://dav.example/")
	listed, err := ListCollections[testProps](context.Background(), client, base, []Home{{Path: "/cal/u/"}}, "<propfind/>")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 {
		t.Fatalf("the home set itself was listed as a collection: %d listed", len(listed))
	}
	// Sorted by name, paths canonical, and only a granted write privilege
	// or a silent server makes a collection writable.
	if listed[0].Name != "Family" || listed[0].Path != "/cal/u/Family Days/" || !listed[0].Writable {
		t.Errorf("first: %+v", listed[0].Collection)
	}
	if listed[1].Name != "Work" || listed[1].Writable {
		t.Errorf("a read-only collection was offered for writing: %+v", listed[1].Collection)
	}
}

func TestCanonicalCollectionPath(t *testing.T) {
	for in, want := range map[string]string{
		"https://dav.example/cal/u/work": "/cal/u/work/",
		" /cal/u/work/ ":                 "/cal/u/work/",
		"cal/u//work/../home":            "/cal/u/home/",
		"/":                              "/",
	} {
		if got := CanonicalCollectionPath(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

func TestCreateCollectionWalksToAFreeAddress(t *testing.T) {
	base, _ := url.Parse("https://dav.example/")
	var tried []string
	made, err := CreateCollection(context.Background(), base, "/cal/u/", "Work Stuff", "calendar",
		func(_ context.Context, target string) error {
			tried = append(tried, target)
			if len(tried) < 3 {
				return ErrCollectionExists
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if made != "/cal/u/work-stuff-3/" {
		t.Errorf("the address it settled on: %q", made)
	}
	if strings.Join(tried, " ") != "https://dav.example/cal/u/work-stuff/ https://dav.example/cal/u/work-stuff-2/ https://dav.example/cal/u/work-stuff-3/" {
		t.Errorf("addresses tried: %v", tried)
	}

	other := errors.New("server down")
	if _, err := CreateCollection(context.Background(), base, "/cal/u/", "", "calendar",
		func(_ context.Context, target string) error { return other }); !errors.Is(err, other) {
		t.Errorf("a failure that is not a taken address was retried: %v", err)
	}
}

func TestCountObjectsSkipsTheCollectionItself(t *testing.T) {
	client := &http.Client{Transport: answer{http.StatusMultiStatus, `<?xml version="1.0"?>
<D:multistatus xmlns:D="DAV:">
 <D:response><D:href>/cal/u/work/</D:href><D:propstat><D:prop/><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
 <D:response><D:href>/cal/u/work/a.ics</D:href><D:propstat><D:prop/><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
 <D:response><D:href>/cal/u/work/b.ics</D:href><D:propstat><D:prop/><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
</D:multistatus>`}}
	base, _ := url.Parse("https://dav.example/")
	n, err := CountObjects(context.Background(), client, base, "/cal/u/work/")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("counted %d, want 2", n)
	}
}

func routerTo(own *httptest.Server, named http.RoundTripper) sourceRouter {
	u, _ := url.Parse(own.URL + "/dav/")
	return sourceRouter{
		sources: []Source{{ID: SourceOwn, URL: u, Named: true}},
		trusted: http.DefaultTransport,
		named:   named,
		sign:    func(req *http.Request, _ Source) error { req.SetBasicAuth("u", "secret"); return nil },
	}
}

func TestASourceIsAskedInItsOwnPathsAndAnswersInOurs(t *testing.T) {
	var asked string
	own := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		asked = r.URL.Path + " " + string(b)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		io.WriteString(w, `<D:href>/dav/cal/a.ics</D:href>`)
	}))
	defer own.Close()
	req, _ := http.NewRequest("REPORT", "http://alborz.invalid/@own/dav/cal/", strings.NewReader(`<D:href>/@own/dav/cal/a.ics</D:href>`))
	resp, err := routerTo(own, http.DefaultTransport).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	answered, _ := io.ReadAll(resp.Body)
	if want := `/dav/cal/ <D:href>/dav/cal/a.ics</D:href>`; asked != want {
		t.Errorf("the source was asked %q, want %q", asked, want)
	}
	if want := `<D:href>/@own/dav/cal/a.ics</D:href>`; string(answered) != want {
		t.Errorf("the page was answered %q, want %q", answered, want)
	}
}

func TestALoginStaysOnItsSourcesHost(t *testing.T) {
	var got []string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, "other:"+r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer other.Close()
	own := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _, signed := r.BasicAuth()
		got = append(got, fmt.Sprintf("own:%v", signed))
		http.Redirect(w, r, other.URL+"/elsewhere/", http.StatusTemporaryRedirect)
	}))
	defer own.Close()
	req, _ := http.NewRequest("PROPFIND", "http://alborz.invalid/@own/dav/", nil)
	_, err := routerTo(own, http.DefaultTransport).RoundTrip(req)
	var refused *RefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("a 401 after a redirect came back as %v, want a refusal", err)
	}
	if want := []string{"own:true", "other:"}; !slices.Equal(got, want) {
		t.Errorf("the servers saw %q, want %q", got, want)
	}
}

func TestAnAccountsServerIsNotDialledOnOurNetwork(t *testing.T) {
	reached := false
	own := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	defer own.Close()
	req, _ := http.NewRequest("PROPFIND", "http://alborz.invalid/@own/dav/", nil)
	if _, err := routerTo(own, alborz.NewRemoteTransport()).RoundTrip(req); err == nil || reached {
		t.Errorf("a loopback server was reached (%v), err %v", reached, err)
	}
}
