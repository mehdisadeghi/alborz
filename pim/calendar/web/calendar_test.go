package web_test

import (
	"fmt"
	"html"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

// A date is read in the calendar its field names: typed Solar Hijri beyond the reckoning is refused, a stored date beyond it round-trips in Gregorian.
func TestInputPastTheSolarHijriReckoning(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	coll := c.CreateCalendar(t, "Plans", "")
	a.Put(t, coll+"far.ics", "text/calendar", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\n"+
		"BEGIN:VEVENT\r\nUID:far\r\nDTSTAMP:20260901T080000Z\r\nDTSTART:40000101T100000Z\r\nDTEND:40000101T110000Z\r\n"+
		"SUMMARY:Far\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	far := Here + coll + "far.ics"
	saved := func() string { return a.DAV(t, http.MethodGet, coll+"far.ics", nil, "").Text }

	c.Post(t, "settings.profile", url.Values{"calendar": {"shcal"}, "secondary": {""}})
	for _, month := range []string{"3178-01", "-62-01"} {
		if resp := c.Get(t, "calendar.month", url.Values{"month": {month}}); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("month %s answered %s, want it refused", month, resp.Status)
		}
	}
	typed := url.Values{"summary": {"Typed"}, "calendar": {User + "|" + Here + coll},
		"start": {"3178/01/01 10:00"}, "end": {"3178/01/01 11:00"}, "start-calendar": {"shcal"}, "end-calendar": {"shcal"}}
	if resp := c.Post(t, "calendar.event.create", typed); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("a typed year past the reckoning answered %s, want the form refused", resp.Status)
	}

	page := c.Page(t, "calendar.event.update", far)
	shown := url.Values{"summary": {"Far again"}}
	for _, name := range []string{"start", "end", "start-calendar", "end-calendar"} {
		field := regexp.MustCompile(`name="` + name + `"[^>]*value="([^"]*)"`).FindStringSubmatch(page)
		if field == nil {
			t.Fatalf("the edit form has no %s", name)
		}
		shown.Set(name, field[1])
	}
	if shown.Get("start") != "4000-01-01T10:00" || shown.Get("start-calendar") != "gregorian" {
		t.Fatalf("the edit form holds the stored start as %v, want it in Gregorian", shown)
	}
	if resp := c.Post(t, "calendar.event.update", shown, far); resp.StatusCode != http.StatusFound {
		t.Fatalf("saving the form as shown answered %s", resp.Status)
	}
	if body := saved(); !strings.Contains(body, "DTSTART:40000101T100000Z") || !strings.Contains(body, "Far again") {
		t.Errorf("the saved event moved: %s", body)
	}

	early := url.Values{"summary": {"Early"}, "allday": {"on"},
		"start_date": {"0100-01-01"}, "end_date": {"0100-01-01"}, "start_date-calendar": {"shcal"}, "end_date-calendar": {"shcal"}}
	if resp := c.Post(t, "calendar.event.update", early, far); resp.StatusCode != http.StatusFound {
		t.Fatalf("saving Solar Hijri 0100-01-01 answered %s", resp.Status)
	}
	// 1 Farvardin 100 falls in March 721.
	if body := saved(); !strings.Contains(body, "DTSTART;VALUE=DATE:0721032") {
		t.Errorf("Solar Hijri 0100-01-01 was not saved as a day of Nowruz 721: %s", body)
	}

	c.Post(t, "settings.profile", url.Values{"calendar": {"gregorian"}, "secondary": {"shcal"}})
	c.Page(t, "calendar.month", url.Values{"month": {"4000-01"}})
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
