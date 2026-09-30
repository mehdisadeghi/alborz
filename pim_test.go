package alborz_test

import (
	"fmt"
	"html"
	"io"
	"mime/multipart"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	. "git.mehdix.org/alborz/internal/alborztest"
)

// A DELETE carrying If-Match must match the object's tag (RFC 9110 13.1.1).
func TestADeleteWithAStaleTagKeepsTheObject(t *testing.T) {
	a := Alborz(t)
	cal := a.SignIn(t, User).CreateCalendar(t, "Plans", "")
	a.Put(t, cal+"dentist.ics", "text/calendar", Event("dentist", "Dentist"))

	if resp := a.DAV(t, http.MethodDelete, cal+"dentist.ics", map[string]string{"If-Match": `"stale"`}, ""); resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("a delete with a stale tag answered %s", resp.Status)
	}
	resp := a.DAV(t, http.MethodGet, cal+"dentist.ics", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the object is gone after a refused delete: %s", resp.Status)
	}
	if resp := a.DAV(t, http.MethodDelete, cal+"dentist.ics", map[string]string{"If-Match": resp.Header.Get("ETag")}, ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("a delete with the object's own tag answered %s", resp.Status)
	}
	if resp := a.DAV(t, http.MethodGet, cal+"dentist.ics", nil, ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("the deleted object is still there: %s", resp.Status)
	}
}

// One UID per calendar (RFC 4791 5.3.2.1).
func TestASecondObjectWithATakenUIDIsRefused(t *testing.T) {
	a := Alborz(t)
	cal := a.SignIn(t, User).CreateCalendar(t, "Work", "")
	a.Put(t, cal+"review.ics", "text/calendar", Event("review", "Review"))
	ics := map[string]string{"Content-Type": "text/calendar"}

	if resp := a.DAV(t, http.MethodPut, cal+"copy.ics", ics, Event("review", "Review again")); resp.StatusCode != http.StatusConflict {
		t.Fatalf("a second object with a taken UID answered %s", resp.Status)
	}
	if resp := a.DAV(t, http.MethodGet, cal+"copy.ics", nil, ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("the refused object was written anyway: %s", resp.Status)
	}
	if resp := a.DAV(t, http.MethodPut, cal+"review.ics", ics, Event("review", "Review, moved")); resp.StatusCode/100 != 2 {
		t.Fatalf("rewriting the object that holds the UID answered %s", resp.Status)
	}
	if kept := a.DAV(t, http.MethodGet, cal+"review.ics", nil, "").Text; !strings.Contains(kept, "Review, moved") {
		t.Errorf("the rewrite did not stick: %s", kept)
	}
}

func TestAnOpenTaskWithoutStatusIsListed(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	chores := c.CreateCalendar(t, "Chores", "tasks")
	a.Put(t, chores+"water.ics", "text/calendar", Todo("water", "Water the ferns"))
	if page := c.Page(t, "calendar.tasks"); !strings.Contains(page, "Water the ferns") {
		t.Errorf("the open task without a STATUS is not on the list")
	}
}

// A list held whole pages as mail does, and a task opened from one page walks
// the whole list, not the page it was on.
func TestATaskListPagesAndItsTasksWalkIt(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	chores := c.CreateCalendar(t, "Chores", "tasks")
	for i := 1; i <= 5; i++ {
		a.Put(t, fmt.Sprintf("%st%d.ics", chores, i), "text/calendar", Todo(fmt.Sprintf("t%d", i), fmt.Sprintf("Chore %d", i)))
	}
	list := url.Values{"sort": {"summary"}, "ipp": {"2"}}
	shown := func(page string) []string {
		return regexp.MustCompile(`>(Chore \d)</a>`).FindAllString(page, -1)
	}
	second := c.Page(t, "calendar.tasks", list, url.Values{"page": {"1"}})
	if got := shown(second); !slices.Equal(got, []string{">Chore 3</a>", ">Chore 4</a>"}) {
		t.Fatalf("the second page shows %v, want chores 3 and 4", got)
	}
	if !strings.Contains(second, "3–4 of 5") {
		t.Error("the second page does not say it holds 3–4 of 5")
	}
	if got := PagerSteps(t, second); !slices.Equal(got, []string{"", "2"}) {
		t.Errorf("the second page steps to pages %q, want the first and the third", got)
	}
	if got := shown(c.Page(t, "calendar.tasks", list, url.Values{"page": {"9"}})); !slices.Equal(got, []string{">Chore 5</a>"}) {
		t.Errorf("a page past the end shows %v, want the last page", got)
	}
	if resp := c.Get(t, "calendar.tasks", list, url.Values{"page": {"x"}}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("an unreadable page answered %v, want 400", resp.Status)
	}

	link := regexp.MustCompile(`<a href="([^"]+)" dir="auto"[^>]*>Chore 4</a>`).FindStringSubmatch(second)
	if link == nil {
		t.Fatal("the second page has no link to chore 4")
	}
	task := c.Follow(t, html.UnescapeString(link[1]))
	next := regexp.MustCompile(`<a class="icon-button" href="([^"]+)"[^>]*data-key="j"`).FindStringSubmatch(task)
	if next == nil {
		t.Fatal("chore 4 has no way to the next task")
	}
	if !strings.Contains(c.Follow(t, html.UnescapeString(next[1])), "Chore 5") {
		t.Error("the task after chore 4, on the next page, is not chore 5")
	}
	if !strings.Contains(task, ">4 of 5<") {
		t.Error("chore 4 does not say it is 4 of 5")
	}
}

// A VCALENDAR holds one component at least (RFC 5545 3.6), so no file is written.
func TestAMonthWithNoEventsExportsNothing(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	c.CreateCalendar(t, "Plans", "")
	month := url.Values{"month": {"2026-09"}}
	resp := c.Get(t, "calendar.month.export", month)
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != a.Path("calendar.month", month) {
		t.Fatalf("an empty month's export answered %s to %q, want the month again", resp.Status, resp.Header.Get("Location"))
	}
}

// Its own store answers for a rule alborz writes, and alborz expands it (ADR 10).
func TestACalendarOnAlborzDrawsItsSolarHijriRule(t *testing.T) {
	c := Alborz(t).SignIn(t, User)
	coll := Here + c.CreateCalendar(t, "Plans", "")
	c.Post(t, "settings.profile", url.Values{"calendar": {"shcal"}, "secondary": {""}})
	form := url.Values{"summary": {"Nowruz"}, "calendar": {User + "|" + coll},
		"start": {"1405/01/01 10:00"}, "start-calendar": {"shcal"},
		"end": {"1405/01/01 11:00"}, "end-calendar": {"shcal"}, "repeat": {"yearly"}}
	if resp := c.Post(t, "calendar.event.create", form); resp.StatusCode != http.StatusFound {
		t.Fatalf("creating the yearly event answered %s", resp.Status)
	}
	if page := c.Page(t, "calendar.month", url.Values{"month": {"1406-01"}}); !strings.Contains(page, "Nowruz") {
		t.Errorf("the next year's Farvardin does not show the event")
	}
	if page := c.Page(t, "calendar.month", url.Values{"month": {"1406-02"}}); strings.Contains(page, "Nowruz") {
		t.Errorf("a month without an instance shows the event")
	}
}

const nowruz = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\nBEGIN:VEVENT\r\nUID:nowruz\r\n" +
	"DTSTAMP:20260101T080000Z\r\nDTSTART:20260321T100000Z\r\nDTEND:20260321T110000Z\r\n" +
	"RRULE:RSCALE=PERSIAN;FREQ=YEARLY;SKIP=BACKWARD\r\nSUMMARY:Nowruz\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

const noRScale = "does not say it can keep a repeat"

// rscaleServer serves alborz with a DAV server holding personal and a Solar Hijri rule in it; personal is
// the calendar as pages hold it.
func rscaleServer(t *testing.T, opts ...DAVOption) (a *App, c *Client, d *DAVServer, personal string) {
	t.Helper()
	d = DAV(t, append([]DAVOption{Calendar(DAVHome+"personal/", "Personal"), Object(DAVHome+"personal/nowruz.ics", nowruz)}, opts...)...)
	a = Alborz(t, NoCache(), WithDAV(d))
	return a, a.SignIn(t, User), d, d.At() + DAVHome + "personal/"
}

// A client must not store RSCALE where the server does not advertise the set;
// an empty set lets any scale be tried (RFC 7529 10). Every way onto such a server
// is refused as the form is, and an edit keeps a rule already there.
func TestASolarHijriRepeatNeedsTheServerToListItsScale(t *testing.T) {
	for _, tc := range []struct {
		name  string
		opts  []DAVOption
		saved bool
	}{
		{"the form, without the set", nil, false},
		{"the form, with an empty set", []DAVOption{RScales()}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, c, d, personal := rscaleServer(t, tc.opts...)
			c.Post(t, "settings.profile", url.Values{"calendar": {"shcal"}, "secondary": {""}})
			resp := c.Post(t, "calendar.event.create", url.Values{"summary": {"Nowruz"}, "calendar": {User + "|" + personal},
				"start": {"1405/01/01 10:00"}, "start-calendar": {"shcal"},
				"end": {"1405/01/01 11:00"}, "end-calendar": {"shcal"}, "repeat": {"yearly"}})
			if !tc.saved {
				if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(html.UnescapeString(resp.Text), noRScale) {
					t.Fatalf("saving answered %s, want the form refused with the reason", resp.Status)
				}
				if len(d.Puts()) != 0 {
					t.Errorf("the refused event was written to the server")
				}
				return
			}
			if puts := d.Puts(); len(puts) != 1 || !strings.Contains(puts[0], "RSCALE=PERSIAN") {
				t.Fatalf("saving answered %s and wrote %q, want the Solar Hijri rule", resp.Status, puts)
			}
		})
	}
	t.Run("an import", func(t *testing.T) {
		_, c, d, personal := rscaleServer(t)
		resp := c.Upload(t, "calendar.import", url.Values{"collection": {User + "|" + personal}},
			[]File{{Field: "file", Name: "nowruz.ics", Body: strings.NewReader(strings.Replace(nowruz, "UID:nowruz", "UID:imported", 1))}})
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(html.UnescapeString(resp.Text), noRScale) {
			t.Fatalf("the import answered %s, want the form refused with the reason", resp.Status)
		}
		if len(d.Puts()) != 0 {
			t.Errorf("the refused import was written to the server")
		}
	})
	t.Run("a task move", func(t *testing.T) {
		a, c, d, personal := rscaleServer(t)
		task := c.CreateCalendar(t, "Chores", "tasks") + "water.ics"
		a.Put(t, task, "text/calendar", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\nBEGIN:VTODO\r\n"+
			"UID:water\r\nDTSTAMP:20260101T080000Z\r\nDTSTART:20260321T100000Z\r\n"+
			"RRULE:RSCALE=PERSIAN;FREQ=YEARLY;SKIP=BACKWARD\r\nSUMMARY:Water the ferns\r\nEND:VTODO\r\nEND:VCALENDAR\r\n")
		if resp := c.Post(t, "calendar.tasks.move", url.Values{"paths": {User + "|" + Here + task}, "to": {User + "|" + personal}}); resp.StatusCode != http.StatusFound {
			t.Fatalf("the move answered %s", resp.Status)
		}
		if page := c.Page(t, "calendar.tasks"); !strings.Contains(html.UnescapeString(page), noRScale) {
			t.Errorf("the refused move left no reason on the page")
		}
		if len(d.Puts()) != 0 {
			t.Errorf("the refused task was written to the server")
		}
		if resp := a.DAV(t, http.MethodGet, task, nil, ""); resp.StatusCode != http.StatusOK {
			t.Errorf("the task left its list: %s", resp.Status)
		}
	})
	t.Run("a copy", func(t *testing.T) {
		a, c, d, personal := rscaleServer(t)
		plans := c.CreateCalendar(t, "Plans", "")
		a.Put(t, plans+"nowruz.ics", "text/calendar", nowruz)
		resp := c.Post(t, "calendar.event.copy", url.Values{"to": {User + "|" + personal}}, Here+plans+"nowruz.ics")
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(html.UnescapeString(resp.Text), noRScale) {
			t.Fatalf("the copy answered %s, want the form refused with the reason", resp.Status)
		}
		if len(d.Puts()) != 0 {
			t.Errorf("the refused copy was written to the server")
		}
	})
	t.Run("an edit of a rule already there", func(t *testing.T) {
		_, c, d, personal := rscaleServer(t)
		form := url.Values{"summary": {"Nowruz at home"}, "start": {"2026-03-21T10:00"}, "start-calendar": {"gregorian"},
			"end": {"2026-03-21T11:00"}, "end-calendar": {"gregorian"}, "repeat": {"custom"}}
		resp := c.Post(t, "calendar.event.update", form, personal+"nowruz.ics")
		if puts := d.Puts(); resp.StatusCode != http.StatusFound || len(puts) != 1 || !strings.Contains(puts[0], "RSCALE=PERSIAN") {
			t.Fatalf("the edit answered %s and wrote %q, want the rule kept", resp.Status, puts)
		}
	})
}

// A move is a copy and a delete; a list that keeps its task must not leave it in two lists.
func TestAMoveTheSourceRefusesLeavesTheTaskWhereItWas(t *testing.T) {
	personal := DAVHome + "personal/"
	d := DAV(t, Calendar(personal, "Personal"), Refusing(personal+"water.ics"), Object(personal+"water.ics", Todo("water", "Water the ferns")))
	a := Alborz(t, WithDAV(d))
	c := a.SignIn(t, User)
	chores := c.CreateCalendar(t, "Chores", "tasks")
	move := url.Values{"paths": {User + "|" + d.At() + personal + "water.ics"}, "to": {User + "|" + Here + chores}}
	if resp := c.Post(t, "calendar.tasks.move", move); resp.StatusCode != http.StatusFound {
		t.Fatalf("the move answered %s", resp.Status)
	}
	if page := c.Page(t, "calendar.tasks"); !strings.Contains(html.UnescapeString(page), "The change was not saved.") {
		t.Errorf("the refused move left no reason on the page")
	}
	if resp := a.DAV(t, http.MethodGet, chores+"water.ics", nil, ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("the refused move left a copy in Chores: %s", resp.Status)
	}
}

// A server refusing the write is an answer for the reader, not an error page.
func TestAnImportTheServerRefusesSetsANotice(t *testing.T) {
	const refused = "The change was not saved. The server said:"
	personal, work, friends, family := DAVHome+"personal/", DAVHome+"work/", DAVBooks+"friends/", DAVBooks+"family/"
	card := "BEGIN:VCARD\r\nVERSION:4.0\r\nUID:ada\r\nFN:Ada\r\nEND:VCARD\r\n"
	d := DAV(t, Calendar(personal, "Personal"), Calendar(work, "Work"), Hangup(work), Book(friends, "Friends"), Book(family, "Family"),
		Refusing(personal, friends, family+"bob.vcf"))
	a := Alborz(t, NoCache(), WithDAV(d))
	c := a.SignIn(t, User)
	personal, work, friends, family = d.At()+personal, d.At()+work, d.At()+friends, d.At()+family
	attached := func(mime, name, body string) string {
		return fmt.Sprint(a.IMAP.Append(t, "INBOX", "From: eve@example.org\r\nTo: "+User+"\r\nSubject: "+name+"\r\n"+
			"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=b1\r\n\r\n"+
			"--b1\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nAttached.\r\n"+
			"--b1\r\nContent-Type: "+mime+"; charset=utf-8\r\nContent-Disposition: attachment; filename=\""+name+"\"\r\n\r\n"+
			body+"--b1--\r\n"))
	}
	file := func(name, body string) []File {
		return []File{{Field: "file", Name: name, Body: strings.NewReader(body)}}
	}
	for _, tc := range []struct {
		name string
		post func() *Response
	}{
		{"a file into a calendar", func() *Response {
			return c.Upload(t, "calendar.collection.import", nil, file("plan.ics", Event("plan", "Plan")), personal)
		}},
		{"a file into an address book", func() *Response {
			return c.Upload(t, "contacts.book.import", nil, file("ada.vcf", card), friends)
		}},
		{"a calendar attachment", func() *Response {
			uid := attached("text/calendar", "plan.ics", Event("plan", "Plan"))
			return c.Post(t, "calendar.fromattachment", url.Values{"mbox": {"INBOX"}, "uid": {uid}, "part": {"2"}, "calendar": {User + "|" + personal}})
		}},
		{"a card attachment", func() *Response {
			uid := attached("text/vcard", "ada.vcf", card)
			return c.Post(t, "contacts.frommessage", url.Values{"mbox": {"INBOX"}, "uid": {uid}, "part": {"2"}, "addressbook": {User + "|" + friends}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := tc.post()
			if resp.StatusCode != http.StatusFound {
				t.Fatalf("the refused import answered %s, want a redirect", resp.Status)
			}
			if page := c.Follow(t, resp.Header.Get("Location")); !strings.Contains(page, refused) || !strings.Contains(page, "read-only collection") {
				t.Errorf("the page after the refused import does not give the server's reason")
			}
		})
	}
	const unreadable = "The file could not be read:"
	for _, tc := range []struct{ name, ics, reason string }{
		{"an unreadable file", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:plan\r\nSUMMARY\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n", "missing colon"},
		{"a parameter the line ends in", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:plan\r\nX;A=b\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n", ""},
		{"a file cut off inside an event", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:plan\r\n", ""},
	} {
		t.Run(tc.name+" on the import page", func(t *testing.T) {
			resp := c.Upload(t, "calendar.import", url.Values{"collection": {User + "|" + personal}}, file("plan.ics", tc.ics))
			if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(resp.Text, unreadable) || !strings.Contains(resp.Text, tc.reason) {
				t.Errorf("the file answered %s without saying it could not be read, and why; want 422", resp.Status)
			}
		})
	}
	t.Run("an unreadable file into an address book", func(t *testing.T) {
		resp := c.Upload(t, "contacts.book.import", nil, file("ada.vcf", "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Ada\r\n"), friends)
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("the unreadable file answered %s, want a redirect", resp.Status)
		}
		if page := c.Follow(t, resp.Header.Get("Location")); !strings.Contains(page, unreadable) || !strings.Contains(page, "no END field") {
			t.Errorf("the page after the unreadable file does not say it could not be read, and why")
		}
	})
	t.Run("a file the server refuses partway", func(t *testing.T) {
		resp := c.Upload(t, "contacts.book.import", nil, file("family.vcf", card+"BEGIN:VCARD\r\nVERSION:4.0\r\nUID:bob\r\nFN:Bob\r\nEND:VCARD\r\n"), family)
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("the partly refused import answered %s, want a redirect", resp.Status)
		}
		if page := c.Follow(t, resp.Header.Get("Location")); !strings.Contains(page, "1 of 2 saved; the server refused the rest:") || !strings.Contains(page, "read-only collection") {
			t.Errorf("the page after the partly refused import does not say how much was saved, and why the rest was not")
		}
		if !strings.Contains(strings.Join(d.Puts(), ""), "UID:ada") {
			t.Errorf("the card before the refusal was not written")
		}
	})
	t.Run("a file over the limit on the import page", func(t *testing.T) {
		resp := c.Upload(t, "calendar.import", url.Values{"collection": {User + "|" + personal}}, file("big.ics", strings.Repeat("x", 16<<20+1)))
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("the file over the limit answered %s, want 422", resp.Status)
		}
		if !strings.Contains(resp.Text, "larger than 16 MB") {
			t.Errorf("the import page does not state the limit")
		}
	})
	t.Run("a server that does not answer the import page", func(t *testing.T) {
		resp := c.Upload(t, "calendar.import", url.Values{"collection": {User + "|" + work}}, file("plan.ics", Event("plan", "Plan")))
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
	a := Alborz(t)
	c := a.SignIn(t, User)
	cal := c.CreateCalendar(t, "Plans", "")
	var head strings.Builder
	w := multipart.NewWriter(&head)
	w.WriteField("collection", User+"|"+cal)
	w.CreateFormFile("file", "big.ics")
	// Eight times the limit; a server that read it whole would take all of it.
	const size = 128 << 20
	var taken atomic.Int64
	body := io.MultiReader(strings.NewReader(head.String()), io.LimitReader(endless{'x', &taken}, size))
	req, err := http.NewRequest(http.MethodPost, a.URL+a.Path("calendar.import"), body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp := c.Do(t, req)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(resp.Text, "larger than 16 MB") {
		t.Errorf("the upload past the limit answered %s, without the limit on the form", resp.Status)
	}
	if n := taken.Load(); n > size/4 {
		t.Errorf("the server took %d MB of %d before answering", n>>20, size>>20)
	}
}

// An EXDATE takes one year out of a Solar Hijri rule, in the store's query and on the page.
func TestAnExceptionHidesOneSolarHijriYear(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	coll := c.CreateCalendar(t, "Plans", "")
	c.Post(t, "settings.profile", url.Values{"calendar": {"shcal"}, "secondary": {""}})
	// 1 Farvardin 1405, 1406 and 1407 are 2026-03-21, 2027-03-21 and 2028-03-20.
	a.Put(t, coll+"nowruz.ics", "text/calendar", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\n"+
		"BEGIN:VEVENT\r\nUID:nowruz\r\nDTSTAMP:20260101T080000Z\r\nDTSTART:20260321T100000Z\r\nDTEND:20260321T110000Z\r\n"+
		"RRULE:RSCALE=PERSIAN;FREQ=YEARLY;SKIP=BACKWARD\r\nEXDATE:20270321T100000Z\r\nSUMMARY:Nowruz\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	if page := c.Page(t, "calendar.month", url.Values{"month": {"1406-01"}}); strings.Contains(page, "Nowruz") {
		t.Errorf("the excepted year still shows the event")
	}
	if page := c.Page(t, "calendar.month", url.Values{"month": {"1407-01"}}); !strings.Contains(page, "Nowruz") {
		t.Errorf("the year after the exception does not show the event")
	}
}

// An address book query with no filter asks for every card (RFC 6352 10.5).
func TestABookOnAlborzListsItsCards(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	book := c.CreateBook(t, "People")
	a.Put(t, book+"parvin.vcf", "text/vcard",
		"BEGIN:VCARD\r\nVERSION:3.0\r\nUID:parvin\r\nFN:Parvin Etesami\r\nN:Etesami;Parvin;;;\r\nEND:VCARD\r\n")
	if page := c.Page(t, "contacts.list"); !strings.Contains(page, "Parvin Etesami") {
		t.Errorf("the card in the book on alborz is not on the list")
	}
}

// walked is a kind of DAV object, by the routes of its pages.
type walked struct {
	name                   string
	list, object, edit     string
	create, bulkDelete     string
	importPage, refresh    string
	views                  [][]any
	sorts                  []string
	collField              string
	collection             string
	collName               string
	ext, mime              string
	body                   func(uid, title string) string
	titleField             string
	destField              string
	form                   url.Values
	bulkExport, listExport string
	exported               string
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
	today := time.Now().UTC().Format("2006-01-02")
	noon := today + "T12:00"
	return []walked{
		{name: "event", list: "calendar.month", object: "calendar.event", edit: "calendar.event.update",
			create: "calendar.event.create", bulkDelete: "calendar.events.delete",
			importPage: "calendar.import", refresh: "calendar.refresh",
			views: [][]any{
				{"calendar.month", url.Values{"view": {"list"}, "span": {"month"}}},
				{"calendar.month", url.Values{"view": {"list"}, "span": {"month"}, "group": {"day"}}},
				{"calendar.day", url.Values{"date": {today}}},
			},
			collField: "cal", collection: CalendarsOf(User) + "plans/", collName: "Plans", ext: ".ics", mime: "text/calendar", body: eventToday,
			titleField: "summary", destField: "calendar", form: url.Values{"start": {noon}, "end": {noon}, "start-calendar": {"gregorian"}, "end-calendar": {"gregorian"}},
			listExport: "calendar.month.export", exported: "BEGIN:VEVENT"},
		{name: "task", list: "calendar.tasks", object: "calendar.task", edit: "calendar.task.edit",
			create: "calendar.task.create", bulkDelete: "calendar.tasks.delete",
			importPage: "calendar.tasks.import", refresh: "calendar.tasks.refresh",
			sorts:     []string{"summary", "status", "priority", "starred", "account", "calendar", "due", "added"},
			collField: "cal", collection: CalendarsOf(User) + "chores/", collName: "Chores", ext: ".ics", mime: "text/calendar", body: Todo,
			titleField: "summary", destField: "calendar", form: url.Values{},
			bulkExport: "calendar.tasks.export", exported: "BEGIN:VTODO"},
		{name: "contact", list: "contacts.list", object: "contacts.contact", edit: "contacts.contact.edit",
			create: "contacts.contact.create", bulkDelete: "contacts.delete",
			importPage: "contacts.import", refresh: "contacts.refresh",
			sorts:     []string{"name", "starred", "email", "phone", "account", "book", "changed"},
			collField: "book", collection: BooksOf(User) + "people/", collName: "People", ext: ".vcf", mime: "text/vcard", body: card,
			titleField: "fn", destField: "addressbook", form: url.Values{},
			bulkExport: "contacts.export", exported: "BEGIN:VCARD"},
	}
}

// postPiece posts form as a script does, asking for a piece of the page.
func postPiece(t *testing.T, a *App, c *Client, form url.Values, route string, args ...any) *Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, a.URL+a.Path(route, args...), strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	return c.Do(t, req)
}

// A template reads fields only as it renders, so a status alone proves nothing; a marker is looked for.
func TestEveryDAVObjectRouteAnswers(t *testing.T) {
	for _, scope := range []url.Values{nil, {"account": {User}}} {
		t.Run(fmt.Sprintf("scope=%v", scope), func(t *testing.T) {
			a := Alborz(t)
			c := a.SignIn(t, User)
			c.SignIn(t, User2)
			for _, made := range []struct {
				route string
				form  url.Values
			}{
				{"calendar.calendars.create", url.Values{"name": {"Plans"}, "color": {"#22aa55"}}},
				{"calendar.calendars.create", url.Values{"name": {"Chores"}, "color": {"#22aa55"}, "holds": {"tasks"}}},
				{"calendar.calendars.create", url.Values{"name": {"Errands"}, "color": {"#22aa55"}, "holds": {"tasks"}}},
				{"contacts.books.create", url.Values{"name": {"People"}}},
			} {
				if !strings.Contains(c.Page(t, made.route, scope), `name="place"`) {
					t.Errorf("%s asks for no place", made.route)
				}
				made.form.Set("account", User)
				made.form.Set("place", "here")
				if resp := c.Post(t, made.route, made.form); resp.StatusCode != http.StatusFound {
					t.Fatalf("creating %s answered %s", made.form.Get("name"), resp.Status)
				}
			}
			for _, k := range walkedKinds() {
				t.Run(k.name, func(t *testing.T) { walk(t, a, c, k, scope) })
			}
		})
	}
}

func walk(t *testing.T, a *App, c *Client, k walked, scope url.Values) {
	acct := url.Values{"account": {User}}
	next := a.Path(k.list, scope)
	found := func(what, page, marker string) {
		t.Helper()
		if !strings.Contains(page, marker) {
			t.Errorf("%s: no %q on the page", what, marker)
		}
	}
	moved := func(what string, resp *Response) {
		t.Helper()
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("%s answered %s", what, resp.Status)
		}
	}
	// held is the collection as pages hold it; Put and DAV reach it as any client does.
	held := Here + k.collection
	object := func(uid string) string { return held + uid + k.ext }
	ref := func(uid string) string { return User + "|" + held + uid + k.ext }
	uids := []string{"first", "second", "third"}
	for _, uid := range uids {
		a.Put(t, k.collection+uid+k.ext, k.mime, k.body(uid, "Walked "+uid))
	}
	page := c.Page(t, k.list, scope)
	for _, uid := range uids {
		found("the list", page, "Walked "+uid)
	}
	for i, key := range k.sorts {
		sorted := c.Page(t, k.list, scope, url.Values{"sort": {key}, "dir": {"desc"}})
		found("the list by "+key, sorted, "Walked first")
		if first, third := strings.Index(sorted, "Walked first"), strings.Index(sorted, "Walked third"); third > first {
			t.Errorf("sorted by %s, descending, the list still reads first to third", key)
		}
		if i == 0 && !strings.Contains(sorted, `aria-sort="descending"`) {
			t.Errorf("sorted by %s, no column header says so", key)
		}
	}
	if len(k.sorts) > 0 {
		if resp := c.Get(t, k.list, scope, url.Values{"sort": {"nothing"}}); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("an order the list does not have answered %s", resp.Status)
		}
	}
	for _, view := range k.views {
		found(fmt.Sprint(view), c.Page(t, view[0].(string), slices.Concat(view[1:], []any{scope})...), "Walked first")
	}

	found("the create form", c.Page(t, k.create, scope), `name="`+k.titleField+`"`)
	form := url.Values{k.titleField: {"Walked made"}}
	for field, values := range k.form {
		form[field] = values
	}
	form.Set(k.destField, User+"|"+Here+CalendarsOf("nowhere"))
	if resp := c.Post(t, k.create, form, scope); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("a create into a collection the account has not answered %s", resp.Status)
	}
	form.Set(k.destField, User+"|"+held)
	moved("the create form", c.Post(t, k.create, form, scope))
	found("the list after a create", c.Page(t, k.list, scope), "Walked made")

	found("the object's page", c.Page(t, k.object, object("first"), acct), "Walked first")
	found("the edit form", c.Page(t, k.edit, object("first"), acct), "Walked first")
	form.Set(k.titleField, "Walked again")
	moved("the edit form", c.Post(t, k.edit, form, object("first"), acct))
	found("the object's page after an edit", c.Page(t, k.object, object("first"), acct), "Walked again")
	found("the raw object", c.Page(t, k.object+".raw", object("first"), acct), "Walked again")
	saved := c.Get(t, k.object+".raw", object("first"), acct, url.Values{"save": {"1"}})
	if got := saved.Header.Get("Content-Disposition"); got != "attachment; filename=first"+k.ext || !strings.HasPrefix(saved.Header.Get("Content-Type"), k.mime) {
		t.Errorf("the saved object came as %q, %q", got, saved.Header.Get("Content-Type"))
	}

	moved("the star", c.Post(t, k.object+".color", url.Values{"color": {"red"}, "next": {next}}, object("first"), acct))
	found("the starred object's page", c.Page(t, k.object, object("first"), acct), `data-current="red"`)
	starred := postPiece(t, a, c, url.Values{"color": {"blue"}, "next": {next}}, k.object+".color", object("first"), acct)
	if starred.StatusCode != http.StatusOK {
		t.Fatalf("the star, asked for as a piece, answered %s", starred.Status)
	}
	found("the star's piece", starred.Text, `data-current="blue"`)

	moved("the note", c.Post(t, k.object+".note", url.Values{"note": {"Walked note"}, "next": {next}}, object("first"), acct))
	found("the object's page after a note", c.Page(t, k.object, object("first"), acct), "Walked note")

	switch k.name {
	case "task":
		all := a.Path(k.list, url.Values{"view": {"all"}}, scope)
		marked := postPiece(t, a, c, url.Values{"next": {all}}, "calendar.task.complete", object("first"), acct)
		if marked.StatusCode != http.StatusOK {
			t.Fatalf("completing one, asked for as a piece, answered %s", marked.Status)
		}
		found("the completed task's row", marked.Text, "task-row list-row completed")
		moved("reopening one", c.Post(t, "calendar.task.complete", url.Values{"next": {all}, "reopen": {"1"}}, object("first"), acct))
		if page := c.Follow(t, all); strings.Contains(page, "task-row list-row completed") {
			t.Errorf("a task is still done after it was reopened")
		}
		moved("completing the checked", c.Post(t, "calendar.tasks.complete", url.Values{"paths": {ref("first"), ref("second")}, "next": {all}}))
		if got := strings.Count(c.Follow(t, all), "task-row list-row completed"); got != 2 {
			t.Errorf("%d tasks are done after completing two", got)
		}
		moved("undoing the completion", c.Post(t, "calendar.tasks.complete", url.Values{"paths": {ref("first"), ref("second")}, "next": {all}, "undo": {"1"}}))
		if page := c.Follow(t, all); strings.Contains(page, "task-row list-row completed") {
			t.Errorf("a task is still done after the undo")
		}
		where := c.Page(t, "calendar.tasks.move", url.Values{"paths": {ref("first")}, "next": {next}})
		found("the move page", where, `name="to"`)
		found("the move page", where, "Chores")
		moved("moving to the list it is in", c.Post(t, "calendar.tasks.move", url.Values{"paths": {ref("first")}, "to": {User + "|" + held}, "next": {next}}))
		errands := strings.Replace(held, "/chores/", "/errands/", 1)
		a.Put(t, k.collection+"carried"+k.ext, k.mime, k.body("carried", "Walked carried"))
		moved("moving the checked", c.Post(t, "calendar.tasks.move", url.Values{"paths": {ref("carried")}, "to": {User + "|" + errands}, "next": {next}}))
		found("the moved task", c.Page(t, k.object+".raw", errands+"carried"+k.ext, acct), "Walked carried")
		if resp := a.DAV(t, http.MethodGet, k.collection+"carried"+k.ext, nil, ""); resp.StatusCode != http.StatusNotFound {
			t.Errorf("the moved task is still in the list it left: %s", resp.Status)
		}
	case "contact":
		moved("starring the checked", c.Post(t, "contacts.color", url.Values{"paths": {ref("first"), ref("second")}, "color": {"green"}, "next": {next}}))
		found("a contact starred from the list", c.Page(t, k.object, object("second"), acct), `data-current="green"`)
		moved("making a group", c.Post(t, "contacts.contact.group", url.Values{"newgroup": {"Walked group"}}, object("first"), acct))
		found("the grouped contact's page", c.Page(t, k.object, object("first"), acct), "Walked group")
		moved("removing the photo", c.Post(t, "contacts.contact.photo.delete", url.Values{"next": {next}}, object("first"), acct))
	}

	if k.bulkExport != "" {
		file := c.Post(t, k.bulkExport, url.Values{"paths": {ref("first"), ref("second")}, "next": {next}})
		if file.StatusCode != http.StatusOK || strings.Count(file.Text, k.exported) != 2 {
			t.Errorf("exporting two answered %s with %d of them", file.Status, strings.Count(file.Text, k.exported))
		}
	}
	if k.listExport != "" {
		found("the list's export", c.Page(t, k.listExport, scope), k.exported)
	}
	found("the import page's picker", c.Page(t, k.importPage, scope), k.collName+"</option>")

	chosen := c.Post(t, k.list, url.Values{k.collField: {held}, "next": {next}}, scope)
	moved("choosing the collections", chosen)
	if got := chosen.Header.Get("Location"); got != next {
		t.Errorf("choosing the collections lands on %q, not %q", got, next)
	}
	found("the list after choosing", c.Page(t, k.list, scope), "Walked second")
	refreshed := c.Post(t, k.refresh, nil, scope)
	moved("the refresh", refreshed)
	if got := refreshed.Header.Get("Location"); got != next {
		t.Errorf("the refresh lands on %q, not %q", got, next)
	}

	single := c.Post(t, k.object+".delete", url.Values{"next": {next}}, object("second"), acct)
	moved("deleting one", single)
	if got := single.Header.Get("Location"); got != next {
		t.Errorf("deleting one lands on %q, not %q", got, next)
	}
	moved("deleting the checked", c.Post(t, k.bulkDelete, url.Values{"paths": {ref("third")}, "next": {next}}))
	page = c.Page(t, k.list, scope)
	found("the list after the deletes", page, "Walked again")
	for _, uid := range []string{"second", "third"} {
		if strings.Contains(page, "Walked "+uid) {
			t.Errorf("%s is still listed after its delete", uid)
		}
	}
	outside := c.Post(t, k.object+".delete", url.Values{"next": {"//elsewhere.example/"}}, object("first"), acct)
	moved("deleting the last", outside)
	if got := outside.Header.Get("Location"); strings.Contains(got, "elsewhere") {
		t.Errorf("a delete followed a next that leaves the site: %q", got)
	}
}

// bookCards are the cards of the account's book on alborz named name, as a DAV client reads them.
func bookCards(t *testing.T, a *App, name string) string {
	t.Helper()
	list := a.DAV(t, "PROPFIND", BooksOf(User), map[string]string{"Depth": "1", "Content-Type": "application/xml"},
		`<propfind xmlns="DAV:"><prop><displayname/></prop></propfind>`).Text
	href := ""
	for _, r := range strings.Split(list, "response>") {
		if strings.Contains(r, ">"+name+"<") {
			href = Between(r, "href>", "<")
		}
	}
	if href == "" {
		t.Fatalf("no book named %q:\n%s", name, list)
	}
	return a.DAV(t, "REPORT", href, map[string]string{"Depth": "1", "Content-Type": "application/xml"},
		`<C:addressbook-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav"><D:prop><C:address-data/></D:prop></C:addressbook-query>`).Text
}

func TestSendingKeepsNewRecipientsInPreviousRecipients(t *testing.T) {
	a := Alborz(t, WithSMTP(SMTP(t)))
	c := a.SignIn(t, User)
	book := c.CreateBook(t, "People")
	a.Put(t, book+"known.vcf", "text/vcard",
		"BEGIN:VCARD\r\nVERSION:3.0\r\nUID:known\r\nFN:Known\r\nEMAIL:known@example.org\r\nEND:VCARD\r\n")

	sendFrom(t, c, c.Page(t, "mail.compose"),
		url.Values{"to": {"Parvin Etesami <parvin@example.org>"}, "cc": {"known@example.org"}}, "mail.compose")

	cards := bookCards(t, a, "Previous recipients")
	if !strings.Contains(cards, "EMAIL:parvin@example.org") || !strings.Contains(cards, "FN:Parvin Etesami") {
		t.Errorf("the new recipient is not in Previous recipients:\n%s", cards)
	}
	if strings.Contains(cards, "known@example.org") {
		t.Errorf("a recipient another book holds was kept again:\n%s", cards)
	}
	if !strings.Contains(c.Page(t, "mail.compose"), "parvin@example.org") {
		t.Errorf("compose does not suggest the previous recipient")
	}
}

func TestAddToContactsWritesTheChosenBook(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	c.CreateBook(t, "People")
	c.CreateBook(t, "Work")

	uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX"))
	if len(uids) == 0 {
		t.Fatal("no seeded messages")
	}
	dialog := Between(c.Page(t, "mail.message", "INBOX", uids[0]), `<dialog id="add-contact"`, "</dialog>")
	address := html.UnescapeString(Between(dialog, `<option value="`, `"`))
	book := ""
	for _, v := range strings.Split(dialog, `<option value="`)[1:] {
		if v = html.UnescapeString(v[:strings.IndexByte(v, '"')]); strings.HasSuffix(v, "/work/") {
			book = v
		}
	}
	if address == "" || book == "" {
		t.Fatalf("the message page offers no address or no Work book:\n%s", dialog)
	}

	form := url.Values{"address": {address}, "addressbook": {book}, "next": {a.Path("mail.message", "INBOX", uids[0])}}
	for range 2 {
		if resp := c.Post(t, "contacts.add", form); resp.StatusCode != http.StatusFound {
			t.Fatalf("adding the address answered %s", resp.Status)
		}
	}
	parsed, err := mail.ParseAddress(address)
	if err != nil {
		t.Fatal(err)
	}
	if cards := bookCards(t, a, "Work"); strings.Count(cards, "EMAIL:"+parsed.Address) != 1 {
		t.Errorf("Work does not hold %s once:\n%s", parsed.Address, cards)
	}
	if cards := bookCards(t, a, "People"); strings.Contains(cards, parsed.Address) {
		t.Errorf("a book not chosen got the card:\n%s", cards)
	}
}

// Ticks, links and rows name a collection by its path, and an account's settings may be shared by
// every alborz it signs in to; each alborz's "Personal" is its own (ADR 28).
func TestTwoAlborzKeepTheirCollectionsApart(t *testing.T) {
	imap := IMAP(t)
	var held []string
	for _, host := range []string{"a.example", "b.example"} {
		c := Alborz(t, WithIMAP(imap), PublicURL("https://"+host)).SignIn(t, User)
		path := "/@" + host + c.CreateCalendar(t, "Personal", "")
		if !c.Listed(t)[path] {
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
	imap := IMAP(t)
	other := Alborz(t, WithIMAP(imap))
	remote := At(other.URL) + other.SignIn(t, User).CreateCalendar(t, "Remote", "")
	c := Alborz(t, WithIMAP(imap), other.DAVUpstream()).SignIn(t, User)
	local := Here + c.CreateCalendar(t, "Local", "")
	if got := c.Listed(t); !got[remote] || !got[local] || len(got) != 2 {
		t.Errorf("listed %v, want the other alborz's Remote and this one's Local", got)
	}
}

// A domain whose DAV server is this alborz is this alborz, not a second server beside it.
func TestADomainServedByThisAlborzListsItsCollectionsOnce(t *testing.T) {
	c := Alborz(t, WithDAVHere(), NoCache()).SignIn(t, User)
	local := Here + c.CreateCalendar(t, "Local", "")
	if got := c.Listed(t); !got[local] || len(got) != 1 {
		t.Errorf("listed %v, want only this alborz's Local", got)
	}
}
