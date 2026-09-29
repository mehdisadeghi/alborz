package alborz_test

import (
	"fmt"
	"html"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func put(t *testing.T, base, path, contentType, body string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(smokeUser, smokePass)
	req.Header.Set("Content-Type", contentType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT %s answered %s", path, resp.Status)
	}
}

func dav(t *testing.T, method, url string, header map[string]string, body string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(smokeUser, smokePass)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(answer)
}

func event(uid, summary string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\nBEGIN:VEVENT\r\nUID:" + uid +
		"\r\nDTSTAMP:20260901T080000Z\r\nDTSTART:20260915T100000Z\r\nDTEND:20260915T110000Z\r\nSUMMARY:" +
		summary + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
}

func createCalendar(t *testing.T, base string, c *http.Client, name, id string) string {
	t.Helper()
	form := url.Values{"account": {smokeUser}, "place": {"here"}, "name": {name}, "color": {"#22aa55"}}
	if resp := postForm(t, c, base+"/calendars/create", form); resp.StatusCode != http.StatusFound {
		t.Fatalf("creating the calendar answered %s", resp.Status)
	}
	return base + "/alborz/dav/calendars/" + smokeUser + "/" + id + "/"
}

// A DELETE carrying If-Match must match the object's tag (RFC 9110 13.1.1).
func TestADeleteWithAStaleTagKeepsTheObject(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := login(t, base)
	cal := createCalendar(t, base, c, "Plans", "plans")
	put(t, base, strings.TrimPrefix(cal+"dentist.ics", base), "text/calendar", event("dentist", "Dentist"))

	if resp, _ := dav(t, http.MethodDelete, cal+"dentist.ics", map[string]string{"If-Match": `"stale"`}, ""); resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("a delete with a stale tag answered %s", resp.Status)
	}
	resp, _ := dav(t, http.MethodGet, cal+"dentist.ics", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the object is gone after a refused delete: %s", resp.Status)
	}
	if resp, _ := dav(t, http.MethodDelete, cal+"dentist.ics", map[string]string{"If-Match": resp.Header.Get("ETag")}, ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("a delete with the object's own tag answered %s", resp.Status)
	}
	if resp, _ := dav(t, http.MethodGet, cal+"dentist.ics", nil, ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("the deleted object is still there: %s", resp.Status)
	}
}

// One UID per calendar (RFC 4791 5.3.2.1).
func TestASecondObjectWithATakenUIDIsRefused(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := login(t, base)
	cal := createCalendar(t, base, c, "Work", "work")
	put(t, base, strings.TrimPrefix(cal+"review.ics", base), "text/calendar", event("review", "Review"))

	resp, _ := dav(t, http.MethodPut, cal+"copy.ics", map[string]string{"Content-Type": "text/calendar"}, event("review", "Review again"))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("a second object with a taken UID answered %s", resp.Status)
	}
	if resp, _ := dav(t, http.MethodGet, cal+"copy.ics", nil, ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("the refused object was written anyway: %s", resp.Status)
	}
	if resp, _ := dav(t, http.MethodPut, cal+"review.ics", map[string]string{"Content-Type": "text/calendar"}, event("review", "Review, moved")); resp.StatusCode/100 != 2 {
		t.Fatalf("rewriting the object that holds the UID answered %s", resp.Status)
	}
	if _, kept := dav(t, http.MethodGet, cal+"review.ics", nil, ""); !strings.Contains(kept, "Review, moved") {
		t.Errorf("the rewrite did not stick: %s", kept)
	}
}

func TestAnOpenTaskWithoutStatusIsListed(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := login(t, base)
	form := url.Values{"account": {smokeUser}, "place": {"here"}, "name": {"Chores"}, "color": {"#22aa55"}, "holds": {"tasks"}}
	if resp := postForm(t, c, base+"/calendars/create", form); resp.StatusCode != http.StatusFound {
		t.Fatalf("creating the task list answered %s", resp.Status)
	}
	put(t, base, "/alborz/dav/calendars/"+smokeUser+"/chores/water.ics", "text/calendar",
		"BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\nBEGIN:VTODO\r\n"+
			"UID:water\r\nDTSTAMP:20260901T080000Z\r\nSUMMARY:Water the ferns\r\nEND:VTODO\r\nEND:VCALENDAR\r\n")
	if page := get(t, c, base+"/tasks"); !strings.Contains(page, "Water the ferns") {
		t.Errorf("the open task without a STATUS is not on the list")
	}
}

// A list held whole pages as mail does, and a task opened from one page walks
// the whole list, not the page it was on.
func TestATaskListPagesAndItsTasksWalkIt(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := login(t, base)
	form := url.Values{"account": {smokeUser}, "place": {"here"}, "name": {"Chores"}, "color": {"#22aa55"}, "holds": {"tasks"}}
	if resp := postForm(t, c, base+"/calendars/create", form); resp.StatusCode != http.StatusFound {
		t.Fatalf("creating the task list answered %s", resp.Status)
	}
	for i := 1; i <= 5; i++ {
		put(t, base, fmt.Sprintf("/alborz/dav/calendars/%s/chores/t%d.ics", smokeUser, i), "text/calendar",
			fmt.Sprintf("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\nBEGIN:VTODO\r\n"+
				"UID:t%d\r\nDTSTAMP:20260901T080000Z\r\nSUMMARY:Chore %d\r\nEND:VTODO\r\nEND:VCALENDAR\r\n", i, i))
	}
	list := base + "/tasks?sort=summary&ipp=2"
	shown := func(page string) []string {
		return regexp.MustCompile(`>(Chore \d)</a>`).FindAllString(page, -1)
	}
	second := get(t, c, list+"&page=1")
	if got := shown(second); !slices.Equal(got, []string{">Chore 3</a>", ">Chore 4</a>"}) {
		t.Fatalf("the second page shows %v, want chores 3 and 4", got)
	}
	if !strings.Contains(second, "3–4 of 5") {
		t.Error("the second page does not say it holds 3–4 of 5")
	}
	if got := pagerSteps(t, second); !slices.Equal(got, []string{"", "2"}) {
		t.Errorf("the second page steps to pages %q, want the first and the third", got)
	}
	if got := shown(get(t, c, list+"&page=9")); !slices.Equal(got, []string{">Chore 5</a>"}) {
		t.Errorf("a page past the end shows %v, want the last page", got)
	}
	if resp, err := c.Get(list + "&page=x"); err != nil || resp.StatusCode != http.StatusBadRequest {
		t.Errorf("an unreadable page answered %v %v, want 400", resp.Status, err)
	}

	link := regexp.MustCompile(`<a href="([^"]+)" dir="auto"[^>]*>Chore 4</a>`).FindStringSubmatch(second)
	if link == nil {
		t.Fatal("the second page has no link to chore 4")
	}
	task := get(t, c, base+html.UnescapeString(link[1]))
	next := regexp.MustCompile(`<a class="icon-button" href="([^"]+)"[^>]*data-key="j"`).FindStringSubmatch(task)
	if next == nil {
		t.Fatal("chore 4 has no way to the next task")
	}
	if !strings.Contains(get(t, c, base+html.UnescapeString(next[1])), "Chore 5") {
		t.Error("the task after chore 4, on the next page, is not chore 5")
	}
	if !strings.Contains(task, ">4 of 5<") {
		t.Error("chore 4 does not say it is 4 of 5")
	}
}

// A VCALENDAR holds one component at least (RFC 5545 3.6), so no file is written.
func TestAMonthWithNoEventsExportsNothing(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := login(t, base)
	createCalendar(t, base, c, "Plans", "plans")
	stay := *c
	stay.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := stay.Get(base + "/calendar/export?month=2026-09")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/calendar?month=2026-09" {
		t.Fatalf("an empty month's export answered %s to %q, want the month again", resp.Status, resp.Header.Get("Location"))
	}
}

// Its own store answers for a rule alborz writes, and alborz expands it (ADR 10).
func TestACalendarOnAlborzDrawsItsSolarHijriRule(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := login(t, base)
	coll := hereAt + strings.TrimPrefix(createCalendar(t, base, c, "Plans", "plans"), base)
	postForm(t, c, base+"/settings", url.Values{"calendar": {"shcal"}, "secondary": {""}})
	form := url.Values{"summary": {"Nowruz"}, "calendar": {smokeUser + "|" + coll},
		"start": {"1405/01/01 10:00"}, "start-calendar": {"shcal"},
		"end": {"1405/01/01 11:00"}, "end-calendar": {"shcal"}, "repeat": {"yearly"}}
	if resp := postForm(t, c, base+"/calendar/create", form); resp.StatusCode != http.StatusFound {
		t.Fatalf("creating the yearly event answered %s", resp.Status)
	}
	if page := get(t, c, base+"/calendar?month=1406-01"); !strings.Contains(page, "Nowruz") {
		t.Errorf("the next year's Farvardin does not show the event")
	}
	if page := get(t, c, base+"/calendar?month=1406-02"); strings.Contains(page, "Nowruz") {
		t.Errorf("a month without an instance shows the event")
	}
}

// A client must not store RSCALE where the server does not advertise the set;
// an empty set lets any scale be tried (RFC 7529 10).
func TestASolarHijriRepeatNeedsTheServerToListItsScale(t *testing.T) {
	const refused = "does not say it can keep a repeat"
	personal := davHome + "personal/"
	// onStub is personal as pages hold it, on the stub a subtest started.
	var onStub string
	nowruz := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\nBEGIN:VEVENT\r\nUID:nowruz\r\n" +
		"DTSTAMP:20260101T080000Z\r\nDTSTART:20260321T100000Z\r\nDTEND:20260321T110000Z\r\n" +
		"RRULE:RSCALE=PERSIAN;FREQ=YEARLY;SKIP=BACKWARD\r\nSUMMARY:Nowruz\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	start := func(t *testing.T, rscales []string) (string, *http.Client, *davStub) {
		stub := &davStub{cals: map[string]davCal{personal: {"Personal", "#3366cc"}},
			refuse: map[string]bool{}, keep: map[string]bool{}, gone: map[string]bool{},
			rscales: rscales, objects: map[string]string{personal + "nowruz.ics": nowruz}}
		srv := httptest.NewServer(stub)
		t.Cleanup(srv.Close)
		onStub = at(srv.URL) + personal
		base := serveAlborz(t, []string{
			"test.local=imap+insecure://" + startIMAP(t),
			"test.local=caldav+insecure://" + strings.TrimPrefix(srv.URL, "http://") + "/dav",
		}, false)
		return base, login(t, base), stub
	}
	create := func(t *testing.T, base string, c *http.Client) (*http.Response, string) {
		postForm(t, c, base+"/settings", url.Values{"calendar": {"shcal"}, "secondary": {""}})
		return postBody(t, c, base+"/calendar/create", url.Values{"summary": {"Nowruz"}, "calendar": {smokeUser + "|" + onStub},
			"start": {"1405/01/01 10:00"}, "start-calendar": {"shcal"},
			"end": {"1405/01/01 11:00"}, "end-calendar": {"shcal"}, "repeat": {"yearly"}}, nil)
	}

	t.Run("the form, without the set", func(t *testing.T) {
		base, c, stub := start(t, nil)
		resp, body := create(t, base, c)
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(html.UnescapeString(body), refused) {
			t.Fatalf("saving answered %s, want the form refused with the reason", resp.Status)
		}
		if len(stub.puts) != 0 {
			t.Errorf("the refused event was written to the server")
		}
	})
	t.Run("the form, with an empty set", func(t *testing.T) {
		base, c, stub := start(t, []string{})
		resp, _ := create(t, base, c)
		if len(stub.puts) != 1 || !strings.Contains(stub.puts[0], "RSCALE=PERSIAN") {
			t.Fatalf("saving answered %s and wrote %q, want the Solar Hijri rule", resp.Status, stub.puts)
		}
	})
	t.Run("an import", func(t *testing.T) {
		base, c, stub := start(t, nil)
		var body strings.Builder
		w := multipart.NewWriter(&body)
		w.WriteField("collection", smokeUser+"|"+onStub)
		part, err := w.CreateFormFile("file", "nowruz.ics")
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(part, strings.Replace(nowruz, "UID:nowruz", "UID:imported", 1))
		w.Close()
		resp, err := c.Post(base+"/calendar/import", w.FormDataContentType(), strings.NewReader(body.String()))
		if err != nil {
			t.Fatal(err)
		}
		page, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(html.UnescapeString(string(page)), refused) {
			t.Fatalf("the import answered %s, want the form refused with the reason", resp.Status)
		}
		if len(stub.puts) != 0 {
			t.Errorf("the refused import was written to the server")
		}
	})
	t.Run("a task move", func(t *testing.T) {
		base, c, stub := start(t, nil)
		form := url.Values{"account": {smokeUser}, "place": {"here"}, "name": {"Chores"}, "color": {"#22aa55"}, "holds": {"tasks"}}
		if resp := postForm(t, c, base+"/calendars/create", form); resp.StatusCode != http.StatusFound {
			t.Fatalf("creating the task list answered %s", resp.Status)
		}
		task := "/alborz/dav/calendars/" + smokeUser + "/chores/water.ics"
		put(t, base, task, "text/calendar", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\nBEGIN:VTODO\r\n"+
			"UID:water\r\nDTSTAMP:20260101T080000Z\r\nDTSTART:20260321T100000Z\r\n"+
			"RRULE:RSCALE=PERSIAN;FREQ=YEARLY;SKIP=BACKWARD\r\nSUMMARY:Water the ferns\r\nEND:VTODO\r\nEND:VCALENDAR\r\n")
		if resp := postForm(t, c, base+"/tasks/move", url.Values{"paths": {smokeUser + "|" + hereAt + task}, "to": {smokeUser + "|" + onStub}}); resp.StatusCode != http.StatusFound {
			t.Fatalf("the move answered %s", resp.Status)
		}
		if page := get(t, c, base+"/tasks"); !strings.Contains(html.UnescapeString(page), refused) {
			t.Errorf("the refused move left no reason on the page")
		}
		if len(stub.puts) != 0 {
			t.Errorf("the refused task was written to the server")
		}
		if resp, _ := dav(t, http.MethodGet, base+task, nil, ""); resp.StatusCode != http.StatusOK {
			t.Errorf("the task left its list: %s", resp.Status)
		}
	})
	t.Run("a copy", func(t *testing.T) {
		base, c, stub := start(t, nil)
		plans := strings.TrimPrefix(createCalendar(t, base, c, "Plans", "plans"), base)
		put(t, base, plans+"nowruz.ics", "text/calendar", nowruz)
		resp, body := postBody(t, c, base+"/calendar/"+url.PathEscape(hereAt+plans+"nowruz.ics")+"/copy",
			url.Values{"to": {smokeUser + "|" + onStub}}, nil)
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(html.UnescapeString(body), refused) {
			t.Fatalf("the copy answered %s, want the form refused with the reason", resp.Status)
		}
		if len(stub.puts) != 0 {
			t.Errorf("the refused copy was written to the server")
		}
	})
	t.Run("an edit of a rule already there", func(t *testing.T) {
		base, c, stub := start(t, nil)
		form := url.Values{"summary": {"Nowruz at home"}, "start": {"2026-03-21T10:00"}, "start-calendar": {"gregorian"},
			"end": {"2026-03-21T11:00"}, "end-calendar": {"gregorian"}, "repeat": {"custom"}}
		resp := postForm(t, c, base+"/calendar/"+url.PathEscape(onStub+"nowruz.ics")+"/update", form)
		if resp.StatusCode != http.StatusFound || len(stub.puts) != 1 || !strings.Contains(stub.puts[0], "RSCALE=PERSIAN") {
			t.Fatalf("the edit answered %s and wrote %q, want the rule kept", resp.Status, stub.puts)
		}
	})
}

// A move is a copy and a delete; a list that keeps its task must not leave it in two lists.
func TestAMoveTheSourceRefusesLeavesTheTaskWhereItWas(t *testing.T) {
	personal := davHome + "personal/"
	stub := &davStub{cals: map[string]davCal{personal: {"Personal", "#3366cc"}},
		refuse: map[string]bool{personal + "water.ics": true}, keep: map[string]bool{}, gone: map[string]bool{},
		objects: map[string]string{personal + "water.ics": todo("water", "Water the ferns")}}
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)
	base := startAlborzDav(t, startIMAP(t), srv.URL)
	c := login(t, base)
	form := url.Values{"account": {smokeUser}, "place": {"here"}, "name": {"Chores"}, "color": {"#22aa55"}, "holds": {"tasks"}}
	if resp := postForm(t, c, base+"/calendars/create", form); resp.StatusCode != http.StatusFound {
		t.Fatalf("creating the task list answered %s", resp.Status)
	}
	chores := "/alborz/dav/calendars/" + smokeUser + "/chores/"
	move := url.Values{"paths": {smokeUser + "|" + at(srv.URL) + personal + "water.ics"}, "to": {smokeUser + "|" + hereAt + chores}}
	if resp := postForm(t, c, base+"/tasks/move", move); resp.StatusCode != http.StatusFound {
		t.Fatalf("the move answered %s", resp.Status)
	}
	if page := get(t, c, base+"/tasks"); !strings.Contains(html.UnescapeString(page), "The change was not saved.") {
		t.Errorf("the refused move left no reason on the page")
	}
	if resp, _ := dav(t, http.MethodGet, base+chores+"water.ics", nil, ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("the refused move left a copy in Chores: %s", resp.Status)
	}
}

// A server refusing the write is an answer for the reader, not an error page.
func TestAnImportTheServerRefusesSetsANotice(t *testing.T) {
	const refused = "The change was not saved. The server said:"
	personal, work, friends, family := davHome+"personal/", davHome+"work/", davBooks+"friends/", davBooks+"family/"
	card := "BEGIN:VCARD\r\nVERSION:4.0\r\nUID:ada\r\nFN:Ada\r\nEND:VCARD\r\n"
	stub := &davStub{cals: map[string]davCal{personal: {"Personal", "#3366cc"}, work: {"Work", "#3366cc"}}, hangup: map[string]bool{work: true}, books: map[string]davCal{friends: {"Friends", ""}, family: {"Family", ""}},
		refuse: map[string]bool{personal: true, friends: true, family + "bob.vcf": true}, keep: map[string]bool{}, gone: map[string]bool{}}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	imapAddr := startIMAP(t)
	host := strings.TrimPrefix(srv.URL, "http://")
	base := serveAlborz(t, []string{"test.local=imap+insecure://" + imapAddr,
		"test.local=caldav+insecure://" + host + "/dav", "test.local=carddav+insecure://" + host + "/dav"}, false)
	c := login(t, base)
	personal, work, friends, family = at(srv.URL)+personal, at(srv.URL)+work, at(srv.URL)+friends, at(srv.URL)+family
	attached := func(mime, name, body string) string {
		return appendMessage(t, imapAddr, "INBOX", "From: eve@example.org\r\nTo: "+smokeUser+"\r\nSubject: "+name+"\r\n"+
			"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=b1\r\n\r\n"+
			"--b1\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nAttached.\r\n"+
			"--b1\r\nContent-Type: "+mime+"; charset=utf-8\r\nContent-Disposition: attachment; filename=\""+name+"\"\r\n\r\n"+
			body+"--b1--\r\n")
	}
	upload := func(u string, fields url.Values, name, body string) *http.Response {
		var form strings.Builder
		w := multipart.NewWriter(&form)
		for k := range fields {
			w.WriteField(k, fields.Get(k))
		}
		part, err := w.CreateFormFile("file", name)
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(part, body)
		w.Close()
		stay := *c
		stay.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		resp, err := stay.Post(u, w.FormDataContentType(), strings.NewReader(form.String()))
		if err != nil {
			t.Fatal(err)
		}
		page, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		resp.Body = io.NopCloser(strings.NewReader(string(page)))
		return resp
	}
	for _, tc := range []struct {
		name string
		post func() *http.Response
	}{
		{"a file into a calendar", func() *http.Response {
			return upload(base+"/calendars/"+url.PathEscape(personal)+"/import", nil, "plan.ics", event("plan", "Plan"))
		}},
		{"a file into an address book", func() *http.Response {
			return upload(base+"/address-books/"+url.PathEscape(friends)+"/import", nil, "ada.vcf", card)
		}},
		{"a calendar attachment", func() *http.Response {
			uid := attached("text/calendar", "plan.ics", event("plan", "Plan"))
			return postForm(t, c, base+"/calendar/from-attachment", url.Values{"mbox": {"INBOX"}, "uid": {uid}, "part": {"2"}, "calendar": {smokeUser + "|" + personal}})
		}},
		{"a card attachment", func() *http.Response {
			uid := attached("text/vcard", "ada.vcf", card)
			return postForm(t, c, base+"/contacts/from-message", url.Values{"mbox": {"INBOX"}, "uid": {uid}, "part": {"2"}, "addressbook": {smokeUser + "|" + friends}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := tc.post()
			if resp.StatusCode != http.StatusFound {
				t.Fatalf("the refused import answered %s, want a redirect", resp.Status)
			}
			if page := get(t, c, base+resp.Header.Get("Location")); !strings.Contains(page, refused) || !strings.Contains(page, "read-only collection") {
				t.Errorf("the page after the refused import does not give the server's reason")
			}
		})
	}
	const unreadable = "The file could not be read:"
	t.Run("an unreadable file on the import page", func(t *testing.T) {
		resp := upload(base+"/calendar/import", url.Values{"collection": {smokeUser + "|" + personal}}, "plan.ics",
			"BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:plan\r\nSUMMARY\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
		page, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("the unreadable file answered %s, want 422", resp.Status)
		}
		if !strings.Contains(string(page), unreadable) || !strings.Contains(string(page), "missing colon") {
			t.Errorf("the import page does not say the file could not be read, and why")
		}
	})
	for name, ics := range map[string]string{
		"a parameter the line ends in":   "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:plan\r\nX;A=b\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
		"a file cut off inside an event": "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:plan\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			resp := upload(base+"/calendar/import", url.Values{"collection": {smokeUser + "|" + personal}}, "plan.ics", ics)
			page, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(page), unreadable) {
				t.Errorf("the file answered %s without saying it could not be read, want 422", resp.Status)
			}
		})
	}
	t.Run("an unreadable file into an address book", func(t *testing.T) {
		resp := upload(base+"/address-books/"+url.PathEscape(friends)+"/import", nil, "ada.vcf", "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Ada\r\n")
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("the unreadable file answered %s, want a redirect", resp.Status)
		}
		if page := get(t, c, base+resp.Header.Get("Location")); !strings.Contains(page, unreadable) || !strings.Contains(page, "no END field") {
			t.Errorf("the page after the unreadable file does not say it could not be read, and why")
		}
	})
	t.Run("a file the server refuses partway", func(t *testing.T) {
		resp := upload(base+"/address-books/"+url.PathEscape(family)+"/import", nil, "family.vcf",
			card+"BEGIN:VCARD\r\nVERSION:4.0\r\nUID:bob\r\nFN:Bob\r\nEND:VCARD\r\n")
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("the partly refused import answered %s, want a redirect", resp.Status)
		}
		if page := get(t, c, base+resp.Header.Get("Location")); !strings.Contains(page, "1 of 2 saved; the server refused the rest:") || !strings.Contains(page, "read-only collection") {
			t.Errorf("the page after the partly refused import does not say how much was saved, and why the rest was not")
		}
		if !strings.Contains(strings.Join(stub.puts, ""), "UID:ada") {
			t.Errorf("the card before the refusal was not written")
		}
	})
	t.Run("a file over the limit on the import page", func(t *testing.T) {
		resp := upload(base+"/calendar/import", url.Values{"collection": {smokeUser + "|" + personal}}, "big.ics", strings.Repeat("x", 16<<20+1))
		page, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("the file over the limit answered %s, want 422", resp.Status)
		}
		if !strings.Contains(string(page), "larger than 16 MB") {
			t.Errorf("the import page does not state the limit")
		}
	})
	t.Run("a server that does not answer the import page", func(t *testing.T) {
		resp := upload(base+"/calendar/import", url.Values{"collection": {smokeUser + "|" + work}}, "plan.ics", event("plan", "Plan"))
		if resp.StatusCode != http.StatusInternalServerError {
			t.Errorf("the import no server answered gave %s, want 500", resp.Status)
		}
	})
}

// endless reads one byte forever, counting what was taken.
type endless struct {
	b     byte
	taken *atomic.Int64
}

func (e endless) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = e.b
	}
	e.taken.Add(int64(len(p)))
	return len(p), nil
}

// An upload past its limit is answered while it is still being sent: the server took a fraction of it.
func TestAnUploadPastTheLimitIsRefusedUnread(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := login(t, base)
	cal := strings.TrimPrefix(createCalendar(t, base, c, "Plans", "plans"), base)
	var head strings.Builder
	w := multipart.NewWriter(&head)
	w.WriteField("collection", smokeUser+"|"+cal)
	w.CreateFormFile("file", "big.ics")
	// Eight times the limit; a server that read it whole would take all of it.
	const size = 128 << 20
	var taken atomic.Int64
	body := io.MultiReader(strings.NewReader(head.String()), io.LimitReader(endless{'x', &taken}, size))
	resp, err := c.Post(base+"/calendar/import", w.FormDataContentType(), body)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(page), "larger than 16 MB") {
		t.Errorf("the upload past the limit answered %s, without the limit on the form", resp.Status)
	}
	if n := taken.Load(); n > size/4 {
		t.Errorf("the server took %d MB of %d before answering", n>>20, size>>20)
	}
}

// An EXDATE takes one year out of a Solar Hijri rule, in the store's query and on the page.
func TestAnExceptionHidesOneSolarHijriYear(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := login(t, base)
	coll := strings.TrimPrefix(createCalendar(t, base, c, "Plans", "plans"), base)
	postForm(t, c, base+"/settings", url.Values{"calendar": {"shcal"}, "secondary": {""}})
	// 1 Farvardin 1405, 1406 and 1407 are 2026-03-21, 2027-03-21 and 2028-03-20.
	put(t, base, coll+"nowruz.ics", "text/calendar", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\n"+
		"BEGIN:VEVENT\r\nUID:nowruz\r\nDTSTAMP:20260101T080000Z\r\nDTSTART:20260321T100000Z\r\nDTEND:20260321T110000Z\r\n"+
		"RRULE:RSCALE=PERSIAN;FREQ=YEARLY;SKIP=BACKWARD\r\nEXDATE:20270321T100000Z\r\nSUMMARY:Nowruz\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	if page := get(t, c, base+"/calendar?month=1406-01"); strings.Contains(page, "Nowruz") {
		t.Errorf("the excepted year still shows the event")
	}
	if page := get(t, c, base+"/calendar?month=1407-01"); !strings.Contains(page, "Nowruz") {
		t.Errorf("the year after the exception does not show the event")
	}
}

// An address book query with no filter asks for every card (RFC 6352 10.5).
func TestABookOnAlborzListsItsCards(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := login(t, base)
	form := url.Values{"account": {smokeUser}, "place": {"here"}, "name": {"People"}}
	if resp := postForm(t, c, base+"/address-books/create", form); resp.StatusCode != http.StatusFound {
		t.Fatalf("creating the address book answered %s", resp.Status)
	}
	put(t, base, "/alborz/dav/addressbooks/"+smokeUser+"/people/parvin.vcf", "text/vcard",
		"BEGIN:VCARD\r\nVERSION:3.0\r\nUID:parvin\r\nFN:Parvin Etesami\r\nN:Etesami;Parvin;;;\r\nEND:VCARD\r\n")
	if page := get(t, c, base+"/contacts"); !strings.Contains(page, "Parvin Etesami") {
		t.Errorf("the card in the book on alborz is not on the list")
	}
}

type walked struct {
	name          string
	list, objects string
	views         []string
	sorts         []string
	edit          string
	collField     string
	collection    string
	collName      string
	ext, mime     string
	object        func(uid, title string) string
	titleField    string
	destField     string
	form          url.Values
	bulkExport    string
	listExport    string
	exported      string
}

func todo(uid, summary string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\nBEGIN:VTODO\r\nUID:" + uid +
		"\r\nDTSTAMP:20260901T080000Z\r\nSUMMARY:" + summary + "\r\nEND:VTODO\r\nEND:VCALENDAR\r\n"
}

func card(uid, name string) string {
	return "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:" + uid + "\r\nFN:" + name + "\r\nN:" + name + ";;;;\r\nEND:VCARD\r\n"
}

func eventToday(uid, summary string) string {
	day := time.Now().UTC().Format("20060102")
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\nBEGIN:VEVENT\r\nUID:" + uid +
		"\r\nDTSTAMP:20260901T080000Z\r\nDTSTART:" + day + "T100000Z\r\nDTEND:" + day + "T110000Z\r\nSUMMARY:" +
		summary + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
}

func walkedKinds() []walked {
	noon := time.Now().UTC().Format("2006-01-02") + "T12:00"
	return []walked{
		{name: "event", list: "/calendar", objects: "/calendar/",
			views: []string{"/calendar?view=list&span=month", "/calendar?view=list&span=month&group=day", "/calendar/date?date=" + time.Now().UTC().Format("2006-01-02")}, edit: "/update", collField: "cal",
			collection: "/alborz/dav/calendars/" + smokeUser + "/plans/", collName: "Plans", ext: ".ics", mime: "text/calendar", object: eventToday,
			titleField: "summary", destField: "calendar", form: url.Values{"start": {noon}, "end": {noon}, "start-calendar": {"gregorian"}, "end-calendar": {"gregorian"}},
			listExport: "/calendar/export", exported: "BEGIN:VEVENT"},
		{name: "task", list: "/tasks", objects: "/tasks/", edit: "/edit", collField: "cal",
			sorts:      []string{"summary", "status", "priority", "starred", "account", "calendar", "due", "added"},
			collection: "/alborz/dav/calendars/" + smokeUser + "/chores/", collName: "Chores", ext: ".ics", mime: "text/calendar", object: todo,
			titleField: "summary", destField: "calendar", form: url.Values{},
			bulkExport: "/tasks/export", exported: "BEGIN:VTODO"},
		{name: "contact", list: "/contacts", objects: "/contacts/", edit: "/edit", collField: "book",
			sorts:      []string{"name", "starred", "email", "phone", "account", "book", "changed"},
			collection: "/alborz/dav/addressbooks/" + smokeUser + "/people/", collName: "People", ext: ".vcf", mime: "text/vcard", object: card,
			titleField: "fn", destField: "addressbook", form: url.Values{},
			bulkExport: "/contacts/export", exported: "BEGIN:VCARD"},
	}
}

func withQuery(address, query string) string {
	if strings.Contains(address, "?") {
		return address + "&" + query
	}
	return address + "?" + query
}

func postBody(t *testing.T, c *http.Client, u string, form url.Values, header map[string]string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	stay := *c
	stay.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := stay.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", u, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(body)
}

// A template reads fields only as it renders, so a status alone proves nothing; a marker is looked for.
func TestEveryDAVObjectRouteAnswers(t *testing.T) {
	for _, scope := range []string{"", "?account=" + smokeUser} {
		t.Run("scope="+scope, func(t *testing.T) {
			base := startAlborz(t, startIMAP(t))
			c := login(t, base)
			postForm(t, c, base+"/login", url.Values{"username": {smokeUser2}, "password": {smokePass}})
			for _, made := range []struct {
				route string
				form  url.Values
			}{
				{"/calendars/create", url.Values{"name": {"Plans"}, "color": {"#22aa55"}}},
				{"/calendars/create", url.Values{"name": {"Chores"}, "color": {"#22aa55"}, "holds": {"tasks"}}},
				{"/calendars/create", url.Values{"name": {"Errands"}, "color": {"#22aa55"}, "holds": {"tasks"}}},
				{"/address-books/create", url.Values{"name": {"People"}}},
			} {
				if !strings.Contains(get(t, c, base+made.route+scope), `name="place"`) {
					t.Errorf("%s asks for no place", made.route)
				}
				made.form.Set("account", smokeUser)
				made.form.Set("place", "here")
				if resp := postForm(t, c, base+made.route, made.form); resp.StatusCode != http.StatusFound {
					t.Fatalf("creating %s answered %s", made.form.Get("name"), resp.Status)
				}
			}
			for _, k := range walkedKinds() {
				t.Run(k.name, func(t *testing.T) { walk(t, base, c, k, scope) })
			}
		})
	}
}

func walk(t *testing.T, base string, c *http.Client, k walked, scope string) {
	acct := "?account=" + smokeUser
	list := base + k.list + scope
	next := k.list + scope
	found := func(what, page, marker string) {
		t.Helper()
		if !strings.Contains(page, marker) {
			t.Errorf("%s: no %q on the page", what, marker)
		}
	}
	moved := func(what string, resp *http.Response) {
		t.Helper()
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("%s answered %s", what, resp.Status)
		}
	}
	// held is the collection as pages hold it; put and dav reach it as any client does.
	held := hereAt + k.collection
	object := func(uid string) string { return base + k.objects + url.PathEscape(held+uid+k.ext) }
	ref := func(uid string) string { return smokeUser + "|" + held + uid + k.ext }
	uids := []string{"first", "second", "third"}
	for _, uid := range uids {
		put(t, base, k.collection+uid+k.ext, k.mime, k.object(uid, "Walked "+uid))
	}
	page := get(t, c, list)
	for _, uid := range uids {
		found("the list", page, "Walked "+uid)
	}
	for i, key := range k.sorts {
		sorted := get(t, c, withQuery(list, "sort="+key+"&dir=desc"))
		found("the list by "+key, sorted, "Walked first")
		if first, third := strings.Index(sorted, "Walked first"), strings.Index(sorted, "Walked third"); third > first {
			t.Errorf("sorted by %s, descending, the list still reads first to third", key)
		}
		if i == 0 && !strings.Contains(sorted, `aria-sort="descending"`) {
			t.Errorf("sorted by %s, no column header says so", key)
		}
	}
	if len(k.sorts) > 0 {
		resp, err := c.Get(withQuery(list, "sort=nothing"))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("an order the list does not have answered %s", resp.Status)
		}
	}
	for _, view := range k.views {
		found(view, get(t, c, base+view+strings.Replace(scope, "?", "&", 1)), "Walked first")
	}

	found("the create form", get(t, c, base+k.objects+"create"+scope), `name="`+k.titleField+`"`)
	form := url.Values{k.titleField: {"Walked made"}, k.destField: {smokeUser + "|" + held}}
	for field, values := range k.form {
		form[field] = values
	}
	form.Set(k.destField, smokeUser+"|"+hereAt+"/alborz/dav/nowhere/")
	if resp := postForm(t, c, base+k.objects+"create"+scope, form); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("a create into a collection the account has not answered %s", resp.Status)
	}
	form.Set(k.destField, smokeUser+"|"+held)
	moved("the create form", postForm(t, c, base+k.objects+"create"+scope, form))
	found("the list after a create", get(t, c, list), "Walked made")

	found("the object's page", get(t, c, object("first")+acct), "Walked first")
	found("the edit form", get(t, c, object("first")+k.edit+acct), "Walked first")
	form.Set(k.titleField, "Walked again")
	moved("the edit form", postForm(t, c, object("first")+k.edit+acct, form))
	found("the object's page after an edit", get(t, c, object("first")+acct), "Walked again")
	found("the raw object", get(t, c, object("first")+"/raw"+acct), "Walked again")
	saved, err := c.Get(object("first") + "/raw" + acct + "&save=1")
	if err != nil {
		t.Fatal(err)
	}
	saved.Body.Close()
	if got := saved.Header.Get("Content-Disposition"); got != "attachment; filename=first"+k.ext || !strings.HasPrefix(saved.Header.Get("Content-Type"), k.mime) {
		t.Errorf("the saved object came as %q, %q", got, saved.Header.Get("Content-Type"))
	}

	moved("the star", postForm(t, c, object("first")+"/color"+acct, url.Values{"color": {"red"}, "next": {next}}))
	found("the starred object's page", get(t, c, object("first")+acct), `data-current="red"`)
	starred, piece := postBody(t, c, object("first")+"/color"+acct, url.Values{"color": {"blue"}, "next": {next}}, map[string]string{"HX-Request": "true"})
	if starred.StatusCode != http.StatusOK {
		t.Fatalf("the star, asked for as a piece, answered %s", starred.Status)
	}
	found("the star's piece", piece, `data-current="blue"`)

	moved("the note", postForm(t, c, object("first")+"/note"+acct, url.Values{"note": {"Walked note"}, "next": {next}}))
	found("the object's page after a note", get(t, c, object("first")+acct), "Walked note")

	switch k.name {
	case "task":
		all := k.list + "?view=all" + strings.Replace(scope, "?", "&", 1)
		marked, row := postBody(t, c, object("first")+"/complete"+acct, url.Values{"next": {all}}, map[string]string{"HX-Request": "true"})
		if marked.StatusCode != http.StatusOK {
			t.Fatalf("completing one, asked for as a piece, answered %s", marked.Status)
		}
		found("the completed task's row", row, "task-row list-row completed")
		moved("reopening one", postForm(t, c, object("first")+"/complete"+acct, url.Values{"next": {all}, "reopen": {"1"}}))
		if page := get(t, c, base+all); strings.Contains(page, "task-row list-row completed") {
			t.Errorf("a task is still done after it was reopened")
		}
		moved("completing the checked", postForm(t, c, base+"/tasks/complete", url.Values{"paths": {ref("first"), ref("second")}, "next": {all}}))
		page := get(t, c, base+all)
		if got := strings.Count(page, "task-row list-row completed"); got != 2 {
			t.Errorf("%d tasks are done after completing two", got)
		}
		moved("undoing the completion", postForm(t, c, base+"/tasks/complete", url.Values{"paths": {ref("first"), ref("second")}, "next": {all}, "undo": {"1"}}))
		if page := get(t, c, base+all); strings.Contains(page, "task-row list-row completed") {
			t.Errorf("a task is still done after the undo")
		}
		where := get(t, c, base+"/tasks/move?"+url.Values{"paths": {ref("first")}, "next": {next}}.Encode())
		found("the move page", where, `name="to"`)
		found("the move page", where, "Chores")
		moved("moving to the list it is in", postForm(t, c, base+"/tasks/move", url.Values{"paths": {ref("first")}, "to": {smokeUser + "|" + held}, "next": {next}}))
		errands := strings.Replace(held, "/chores/", "/errands/", 1)
		put(t, base, k.collection+"carried"+k.ext, k.mime, k.object("carried", "Walked carried"))
		moved("moving the checked", postForm(t, c, base+"/tasks/move", url.Values{"paths": {ref("carried")}, "to": {smokeUser + "|" + errands}, "next": {next}}))
		found("the moved task", get(t, c, base+k.objects+url.PathEscape(errands+"carried"+k.ext)+"/raw"+acct), "Walked carried")
		if resp, _ := dav(t, http.MethodGet, base+k.collection+"carried"+k.ext, nil, ""); resp.StatusCode != http.StatusNotFound {
			t.Errorf("the moved task is still in the list it left: %s", resp.Status)
		}
	case "contact":
		moved("starring the checked", postForm(t, c, base+"/contacts/color", url.Values{"paths": {ref("first"), ref("second")}, "color": {"green"}, "next": {next}}))
		found("a contact starred from the list", get(t, c, object("second")+acct), `data-current="green"`)
		moved("making a group", postForm(t, c, object("first")+"/group"+acct, url.Values{"newgroup": {"Walked group"}}))
		found("the grouped contact's page", get(t, c, object("first")+acct), "Walked group")
		moved("removing the photo", postForm(t, c, object("first")+"/photo/delete"+acct, url.Values{"next": {next}}))
	}

	if k.bulkExport != "" {
		file, body := postBody(t, c, base+k.bulkExport, url.Values{"paths": {ref("first"), ref("second")}, "next": {next}}, nil)
		if file.StatusCode != http.StatusOK || strings.Count(body, k.exported) != 2 {
			t.Errorf("exporting two answered %s with %d of them", file.Status, strings.Count(body, k.exported))
		}
	}
	if k.listExport != "" {
		found("the list's export", get(t, c, base+k.listExport+scope), k.exported)
	}
	found("the import page's picker", get(t, c, base+k.list+"/import"+scope), k.collName+"</option>")

	chosen := postForm(t, c, list, url.Values{k.collField: {held}, "next": {next}})
	moved("choosing the collections", chosen)
	if got := chosen.Header.Get("Location"); got != next {
		t.Errorf("choosing the collections lands on %q, not %q", got, next)
	}
	found("the list after choosing", get(t, c, list), "Walked second")
	refreshed := postForm(t, c, base+k.list+"/refresh"+scope, nil)
	moved("the refresh", refreshed)
	if got := refreshed.Header.Get("Location"); got != next {
		t.Errorf("the refresh lands on %q, not %q", got, next)
	}

	single := postForm(t, c, object("second")+"/delete"+acct, url.Values{"next": {next}})
	moved("deleting one", single)
	if got := single.Header.Get("Location"); got != next {
		t.Errorf("deleting one lands on %q, not %q", got, next)
	}
	moved("deleting the checked", postForm(t, c, base+k.objects+"delete", url.Values{"paths": {ref("third")}, "next": {next}}))
	page = get(t, c, list)
	found("the list after the deletes", page, "Walked again")
	for _, uid := range []string{"second", "third"} {
		if strings.Contains(page, "Walked "+uid) {
			t.Errorf("%s is still listed after its delete", uid)
		}
	}
	outside := postForm(t, c, object("first")+"/delete"+acct, url.Values{"next": {"//elsewhere.example/"}})
	moved("deleting the last", outside)
	if got := outside.Header.Get("Location"); strings.Contains(got, "elsewhere") {
		t.Errorf("a delete followed a next that leaves the site: %q", got)
	}
}

// bookCards are the cards of the account's book on alborz named name, as a DAV client reads them.
func bookCards(t *testing.T, base, name string) string {
	t.Helper()
	_, list := dav(t, "PROPFIND", base+"/alborz/dav/addressbooks/"+smokeUser+"/", map[string]string{"Depth": "1", "Content-Type": "application/xml"},
		`<propfind xmlns="DAV:"><prop><displayname/></prop></propfind>`)
	href := ""
	for _, r := range strings.Split(list, "response>") {
		if strings.Contains(r, ">"+name+"<") {
			href = between(r, "href>", "<")
		}
	}
	if href == "" {
		t.Fatalf("no book named %q:\n%s", name, list)
	}
	_, cards := dav(t, "REPORT", base+href, map[string]string{"Depth": "1", "Content-Type": "application/xml"},
		`<C:addressbook-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav"><D:prop><C:address-data/></D:prop></C:addressbook-query>`)
	return cards
}

func createBook(t *testing.T, base string, c *http.Client, name string) {
	t.Helper()
	form := url.Values{"account": {smokeUser}, "place": {"here"}, "name": {name}}
	if resp := postForm(t, c, base+"/address-books/create", form); resp.StatusCode != http.StatusFound {
		t.Fatalf("creating the address book answered %s", resp.Status)
	}
}

func TestSendingKeepsNewRecipientsInPreviousRecipients(t *testing.T) {
	smtpAddr, _ := startSMTP(t)
	base := startAlborz(t, startIMAP(t), smtpAddr)
	c := login(t, base)
	createBook(t, base, c, "People")
	put(t, base, "/alborz/dav/addressbooks/"+smokeUser+"/people/known.vcf", "text/vcard",
		"BEGIN:VCARD\r\nVERSION:3.0\r\nUID:known\r\nFN:Known\r\nEMAIL:known@example.org\r\nEND:VCARD\r\n")

	sendFrom(t, c, base, "/compose", get(t, c, base+"/compose"),
		"to", "Parvin Etesami <parvin@example.org>", "cc", "known@example.org")

	cards := bookCards(t, base, "Previous recipients")
	if !strings.Contains(cards, "EMAIL:parvin@example.org") || !strings.Contains(cards, "FN:Parvin Etesami") {
		t.Errorf("the new recipient is not in Previous recipients:\n%s", cards)
	}
	if strings.Contains(cards, "known@example.org") {
		t.Errorf("a recipient another book holds was kept again:\n%s", cards)
	}
	if !strings.Contains(get(t, c, base+"/compose"), "parvin@example.org") {
		t.Errorf("compose does not suggest the previous recipient")
	}
}

func TestAddToContactsWritesTheChosenBook(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := login(t, base)
	createBook(t, base, c, "People")
	createBook(t, base, c, "Work")

	uids := messageUIDs(get(t, c, base+"/mailbox/INBOX"))
	if len(uids) == 0 {
		t.Fatal("no seeded messages")
	}
	page := get(t, c, base+"/message/INBOX/"+uids[0])
	dialog := between(page, `<dialog id="add-contact"`, "</dialog>")
	address := html.UnescapeString(between(dialog, `<option value="`, `"`))
	book := ""
	for _, v := range strings.Split(dialog, `<option value="`)[1:] {
		if v = html.UnescapeString(v[:strings.IndexByte(v, '"')]); strings.HasSuffix(v, "/work/") {
			book = v
		}
	}
	if address == "" || book == "" {
		t.Fatalf("the message page offers no address or no Work book:\n%s", dialog)
	}

	form := url.Values{"address": {address}, "addressbook": {book}, "next": {"/message/INBOX/" + uids[0]}}
	for range 2 {
		if resp := postForm(t, c, base+"/contacts/add", form); resp.StatusCode != http.StatusFound {
			t.Fatalf("adding the address answered %s", resp.Status)
		}
	}
	parsed, err := mail.ParseAddress(address)
	if err != nil {
		t.Fatal(err)
	}
	if cards := bookCards(t, base, "Work"); strings.Count(cards, "EMAIL:"+parsed.Address) != 1 {
		t.Errorf("Work does not hold %s once:\n%s", parsed.Address, cards)
	}
	if cards := bookCards(t, base, "People"); strings.Contains(cards, parsed.Address) {
		t.Errorf("a book not chosen got the card:\n%s", cards)
	}
}

// Ticks, links and rows name a collection by its path, and an account's settings may be shared by
// every alborz it signs in to; each alborz's "Personal" is its own (ADR 28).
func TestTwoAlborzKeepTheirCollectionsApart(t *testing.T) {
	imapAddr := startIMAP(t)
	var held []string
	for _, host := range []string{"a.example", "b.example"} {
		base := serveAlborzAt(t, func(string) []string { return []string{"test.local=imap+insecure://" + imapAddr} }, true,
			&url.URL{Scheme: "https", Host: host})
		c := login(t, base)
		createCalendar(t, base, c, "Personal", "personal")
		path := "/@" + host + "/alborz/dav/calendars/" + smokeUser + "/personal/"
		if !listed(t, c, base)[path] {
			t.Fatalf("%s does not name its Personal by %s", host, path)
		}
		held = append(held, path)
	}
	if held[0] == held[1] {
		t.Errorf("both name their Personal %s", held[0])
	}
}

// A domain's DAV server that is another alborz is a server like any, not this one (ADR 28).
func TestADomainServedByAnotherAlborzListsItsCollections(t *testing.T) {
	imapAddr := startIMAP(t)
	other := startAlborz(t, imapAddr)
	createCalendar(t, other, login(t, other), "Remote", "remote")
	base := startAlborzWith(t, []string{"test.local=imap+insecure://" + imapAddr,
		"test.local=caldav+insecure://" + strings.TrimPrefix(other, "http://") + "/alborz/dav/"})
	c := login(t, base)
	createCalendar(t, base, c, "Local", "local")
	there := at(other) + "/alborz/dav/calendars/" + smokeUser + "/"
	here := hereAt + "/alborz/dav/calendars/" + smokeUser + "/"
	got := listed(t, c, base)
	if !got[there+"remote/"] || !got[here+"local/"] || len(got) != 2 {
		t.Errorf("listed %v, want the other alborz's Remote and this one's Local", got)
	}
}

// A domain whose DAV server is this alborz is this alborz, not a second server beside it.
func TestADomainServedByThisAlborzListsItsCollectionsOnce(t *testing.T) {
	imapAddr := startIMAP(t)
	base := serveAlborzAt(t, func(self string) []string {
		return []string{"test.local=imap+insecure://" + imapAddr, "test.local=caldav+insecure://" + self + "/alborz/dav/"}
	}, false, nil)
	c := login(t, base)
	createCalendar(t, base, c, "Local", "local")
	here := hereAt + "/alborz/dav/calendars/" + smokeUser + "/local/"
	if got := listed(t, c, base); !got[here] || len(got) != 1 {
		t.Errorf("listed %v, want only this alborz's Local", got)
	}
}

// listed are the calendars the calendar page links to.
func listed(t *testing.T, c *http.Client, base string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`href="/calendars/(%2F[^"?/]+)`).FindAllStringSubmatch(get(t, c, base+"/calendar"), -1) {
		p, err := url.PathUnescape(m[1])
		if err != nil {
			t.Fatal(err)
		}
		out[p] = true
	}
	return out
}
