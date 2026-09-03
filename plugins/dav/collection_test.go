package dav

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
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
	listed, err := ListCollections[testProps](context.Background(), client, base, "/cal/u/", "<propfind/>")
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
	err := CreateCollection(context.Background(), base, "/cal/u/", "Work Stuff", "calendar",
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
	if strings.Join(tried, " ") != "https://dav.example/cal/u/work-stuff/ https://dav.example/cal/u/work-stuff-2/ https://dav.example/cal/u/work-stuff-3/" {
		t.Errorf("addresses tried: %v", tried)
	}

	other := errors.New("server down")
	if err := CreateCollection(context.Background(), base, "/cal/u/", "", "calendar",
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
