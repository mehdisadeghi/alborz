package davcache

import (
	"encoding/xml"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// The shape Nextcloud answers a calendar-query with: prefixes declared
// on the root, which a patched-in response cannot rely on.
const nextcloudTasks = `<?xml version="1.0"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav" xmlns:oc="http://owncloud.org/ns">
 <d:response><d:href>/remote.php/dav/calendars/m/tasks/one.ics</d:href><d:propstat><d:prop><d:getetag>&quot;1&quot;</d:getetag><cal:calendar-data>BEGIN:VCALENDAR
BEGIN:VTODO
SUMMARY:one
END:VTODO
END:VCALENDAR</cal:calendar-data></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
 <d:response><d:href>/remote.php/dav/calendars/m/tasks/two%20words.ics</d:href><d:propstat><d:prop><d:getetag>&quot;2&quot;</d:getetag><cal:calendar-data>BEGIN:VCALENDAR
BEGIN:VTODO
SUMMARY:two
END:VTODO
END:VCALENDAR</cal:calendar-data></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
</d:multistatus>`

const taskQuery = `<calendar-query xmlns="urn:ietf:params:xml:ns:caldav"><filter><comp-filter name="VCALENDAR"><comp-filter name="VTODO"/></comp-filter></filter></calendar-query>`

func hrefs(t *testing.T, body []byte) map[string]string {
	t.Helper()
	var ms struct {
		Responses []struct {
			Href     string `xml:"DAV: href"`
			PropStat struct {
				Prop struct {
					Data string `xml:"urn:ietf:params:xml:ns:caldav calendar-data"`
				} `xml:"DAV: prop"`
			} `xml:"DAV: propstat"`
		} `xml:"DAV: response"`
	}
	if err := xml.Unmarshal(body, &ms); err != nil {
		t.Fatalf("patched document does not parse: %v\n%s", err, body)
	}
	out := map[string]string{}
	for _, r := range ms.Responses {
		u, _ := url.Parse(r.Href)
		out[u.Path] = r.PropStat.Prop.Data
	}
	return out
}

func TestOwnWriteIsAppliedToWhatIsHeld(t *testing.T) {
	const dir = "/remote.php/dav/calendars/m/tasks/"
	task := func(summary string) []byte {
		return []byte("BEGIN:VCALENDAR\nBEGIN:VTODO\nSUMMARY:" + summary + " <&>\nEND:VTODO\nEND:VCALENDAR")
	}
	held := func(query string) *entry {
		return &entry{status: http.StatusMultiStatus, body: []byte(nextcloudTasks), reqBody: []byte(query)}
	}

	changed := objectResponse(dir+"two words.ics", `"3"`, caldavNS, "calendar-data", task("two, edited"))
	body, ok := patchMultistatus(held(taskQuery), dir+"two words.ics", changed, task("two, edited"))
	if got := hrefs(t, body); !ok || len(got) != 2 || !strings.Contains(got[dir+"two words.ics"], "two, edited <&>") {
		t.Fatalf("edit of a held object: ok=%v %v", ok, got)
	}

	made := objectResponse(dir+"new.ics", `"9"`, caldavNS, "calendar-data", task("new"))
	body, ok = patchMultistatus(held(taskQuery), dir+"new.ics", made, task("new"))
	if got := hrefs(t, body); !ok || len(got) != 3 || !strings.Contains(got[dir+"new.ics"], "SUMMARY:new") {
		t.Fatalf("new object in a whole-collection query: ok=%v %v", ok, got)
	}

	body, ok = patchMultistatus(held(taskQuery), dir+"one.ics", nil, nil)
	if got := hrefs(t, body); !ok || len(got) != 1 || got[dir+"one.ics"] != "" {
		t.Fatalf("delete: ok=%v %v", ok, got)
	}

	ranged := strings.Replace(taskQuery, `<comp-filter name="VTODO"/>`, `<comp-filter name="VTODO"><time-range start="20260101T000000Z"/></comp-filter>`, 1)
	if _, ok := patchMultistatus(held(ranged), dir+"new.ics", made, task("new")); ok {
		t.Fatal("a new object was put into a time-range query, where it may not belong")
	}
	event := []byte("BEGIN:VCALENDAR\nBEGIN:VEVENT\nEND:VEVENT\nEND:VCALENDAR")
	if _, ok := patchMultistatus(held(taskQuery), dir+"e.ics", objectResponse(dir+"e.ics", `"1"`, caldavNS, "calendar-data", event), event); ok {
		t.Fatal("an event was put into a query for tasks")
	}
}

func TestAnEditAnswersTheObjectsOwnPage(t *testing.T) {
	const path = "/remote.php/dav/calendars/m/tasks/one.ics"
	at, _ := url.Parse("https://dav.example" + path)
	own := `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav"><d:response><d:href>` + path +
		`</d:href><d:propstat><d:prop><d:getetag>&quot;1&quot;</d:getetag><cal:calendar-data>BEGIN:VCALENDAR
BEGIN:VTODO
SUMMARY:one
END:VTODO
END:VCALENDAR</cal:calendar-data></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`
	u := newUser(DefaultPoll)
	key := cacheKey("REPORT", path, "1", []byte(taskQuery))
	u.entries[key] = &entry{status: http.StatusMultiStatus, body: []byte(own), reqBody: []byte(taskQuery), method: "REPORT", depth: "1", url: at}

	written := []byte("BEGIN:VCALENDAR\nBEGIN:VTODO\nSUMMARY:one, edited\nEND:VTODO\nEND:VCALENDAR")
	put := &http.Request{Method: http.MethodPut, URL: at, Header: http.Header{"Content-Type": {"text/calendar; charset=utf-8"}}}
	if !u.applyWrite(put, written, &http.Response{Header: http.Header{"Etag": {`"2"`}}}, nil) {
		t.Fatal("a PUT answered with an ETag was not applied")
	}
	e := u.entries[key]
	if e == nil || !strings.Contains(hrefs(t, e.body)[path], "one, edited") || !e.fetched.IsZero() {
		t.Fatalf("the object's own read does not hold what was written, due at once: %+v", e)
	}

	if !u.applyWrite(&http.Request{Method: http.MethodDelete, URL: at}, nil, &http.Response{}, nil) || u.entries[key] != nil {
		t.Fatal("a deleted object's own read is still held")
	}
}
