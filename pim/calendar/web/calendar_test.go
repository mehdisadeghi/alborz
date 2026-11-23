package web_test

import (
	"fmt"
	"html"
	"io"
	"maps"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	. "git.mehdix.org/alborz/internal/alborztest"
	calweb "git.mehdix.org/alborz/pim/calendar/web"
	"git.mehdix.org/alborz/web"
)

// A date is read in the calendar its field names: typed Solar Hijri beyond the reckoning is refused, a stored date beyond it round-trips in Gregorian.
func TestInputPastTheSolarHijriReckoning(t *testing.T) {
	a, c := SignedIn(t, User)
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
	a, c := SignedIn(t, User)
	chores := c.CreateCalendar(t, "Chores", "tasks")
	a.Put(t, chores+"water.ics", "text/calendar", Todo("water", "Water the ferns"))
	if page := c.Page(t, "calendar.tasks"); !strings.Contains(page, "Water the ferns") {
		t.Errorf("the open task without a STATUS is not on the list")
	}
}

// Completing a task revises its DTSTAMP (RFC 5545 3.8.7.2); the added date is its CREATED and stays.
func TestCompletingATaskKeepsItsAddedDate(t *testing.T) {
	a, c := SignedIn(t, User)
	chores := c.CreateCalendar(t, "Chores", "tasks")
	a.Put(t, chores+"water.ics", "text/calendar", strings.Replace(Todo("water", "Water the ferns"),
		"DTSTAMP:", "CREATED:20250301T080000Z\r\nDTSTAMP:", 1))
	if resp := c.Post(t, "calendar.task.complete", url.Values{}, Here+chores+"water.ics"); resp.StatusCode != http.StatusFound {
		t.Fatalf("completing the task answered %s", resp.Status)
	}
	stored := a.DAV(t, http.MethodGet, chores+"water.ics", nil, "").Text
	if !strings.Contains(stored, "STATUS:COMPLETED") {
		t.Fatal("the task was not completed")
	}
	if !strings.Contains(stored, "CREATED:20250301T080000Z") {
		t.Errorf("completing the task moved its CREATED:\n%s", stored)
	}
}

// A list holds whole pages as mail does; a page past the end is the last.
func TestATaskListPages(t *testing.T) {
	a, c := SignedIn(t, User)
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
	if got := shown(c.Page(t, "calendar.tasks", list, url.Values{"page": {"9"}})); !slices.Equal(got, []string{">Chore 5</a>"}) {
		t.Errorf("a page past the end shows %v, want the last page", got)
	}
	if resp := c.Get(t, "calendar.tasks", list, url.Values{"page": {"x"}}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("an unreadable page answered %v, want 400", resp.Status)
	}
}

// The pager swaps the next page into main, so that answer is main alone, never a second page with its nav.
func TestAPagedTaskListSwapsOnlyItsMain(t *testing.T) {
	a, c := SignedIn(t, User)
	chores := c.CreateCalendar(t, "Chores", "tasks")
	for i := 1; i <= 3; i++ {
		a.Put(t, fmt.Sprintf("%st%d.ics", chores, i), "text/calendar", Todo(fmt.Sprintf("t%d", i), fmt.Sprintf("Chore %d", i)))
	}
	second := a.Path("calendar.tasks", url.Values{"sort": {"summary"}, "ipp": {"2"}, "page": {"1"}})
	resp := swapIntoMain(t, a, c, second)
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Text, ">Chore 3</a>") {
		t.Fatalf("the swapped page answered %s without chore 3", resp.Status)
	}
	if strings.Contains(resp.Text, "<nav") || strings.Contains(resp.Text, "<html") {
		t.Error("the swap into main carries a whole page, nav and all")
	}

	// htmx swaps no error, so the list stays and the failed notice says so.
	if resp := swapIntoMain(t, a, c, a.Path("calendar.tasks", url.Values{"page": {"x"}})); resp.StatusCode != http.StatusBadRequest || strings.Contains(resp.Text, "<nav") {
		t.Errorf("an unreadable page swapped into main answered %s, want a bare 400", resp.Status)
	}

	// Signing in takes the window, not the list's place in it.
	resp = swapIntoMain(t, a, a.Client(), second)
	if to := resp.Header.Get("HX-Redirect"); resp.StatusCode != http.StatusOK || !strings.HasPrefix(to, a.Path("auth.login")+"?") {
		t.Errorf("a swap after the session ended answered %s sending the window to %q, want the sign-in", resp.Status, to)
	}
}

// swapIntoMain asks for path as the pager does.
func swapIntoMain(t *testing.T, a *App, c *Client, path string) *Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, a.URL+path, nil)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", "main")
	return c.Do(t, req)
}

// A VCALENDAR holds one component at least (RFC 5545 3.6), so no file is written.
func TestAMonthWithNoEventsExportsNothing(t *testing.T) {
	a, c := SignedIn(t, User)
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
				if resp.StatusCode != http.StatusUnprocessableEntity {
					t.Fatalf("saving answered %s, want the form refused", resp.Status)
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
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("the import answered %s, want the form refused", resp.Status)
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
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("the copy answered %s, want the form refused", resp.Status)
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
	a, c := SignedIn(t, User, WithDAV(d))
	chores := c.CreateCalendar(t, "Chores", "tasks")
	move := url.Values{"paths": {User + "|" + d.At() + personal + "water.ics"}, "to": {User + "|" + Here + chores}}
	if resp := c.Post(t, "calendar.tasks.move", move); resp.StatusCode != http.StatusFound {
		t.Fatalf("the move answered %s", resp.Status)
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
	a, c := SignedIn(t, User)
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
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("the upload past the limit answered %s, want it refused", resp.Status)
	}
	if n := taken.Load(); n > size/4 {
		t.Errorf("the server took %d MiB of %d before answering", n>>20, size>>20)
	}
}

// An EXDATE takes one year out of a Solar Hijri rule, in the store's query and on the page.
func TestAnExceptionHidesOneSolarHijriYear(t *testing.T) {
	a, c := SignedIn(t, User)
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

// Objects alborz cannot read, whatever holds them: a zone no database names, a start that is
// no date, and, where a server sends it, a file cut off inside its component. Each costs the
// page only itself and a notice naming the calendar, never the page.
func TestACalendarWithAnUnreadableObjectStillShowsTheRest(t *testing.T) {
	object := func(comp, uid, summary, start string) string {
		return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\nBEGIN:" + comp + "\r\nUID:" + uid +
			"\r\nDTSTAMP:20260901T080000Z\r\n" + start + "\r\nSUMMARY:" + summary + "\r\nEND:" + comp + "\r\nEND:VCALENDAR\r\n"
	}
	readable := map[string]string{
		"good.ics":  Event("good", "Standup"),
		"water.ics": Todo("water", "Water the ferns"),
	}
	unreadable := map[string]string{
		"start.ics": object("VEVENT", "start", "Nowhen", "DTSTART:2026091XT100000Z"),
		"due.ics":   object("VTODO", "due", "Someday", "DUE:someday"),
	}
	cut := map[string]string{
		"cut.ics":     "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\nBEGIN:VEVENT\r\nUID:cut\r\nSUMMARY:Cut\r\n",
		"cuttodo.ics": "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\nBEGIN:VTODO\r\nUID:cuttodo\r\nSUMMARY:Cut\r\n",
	}
	// served is the rig with the objects on a DAV server, and the cut event's path as pages hold it.
	served := func(opts ...Option) (*Client, string, string) {
		personal := DAVHome + "personal/"
		davOpts := []DAVOption{Calendar(personal, "Personal")}
		for _, objects := range []map[string]string{readable, unreadable, cut} {
			for name, body := range objects {
				davOpts = append(davOpts, Object(personal+name, body))
			}
		}
		d := DAV(t, davOpts...)
		a := Alborz(t, append(opts, WithDAV(d))...)
		return a.SignIn(t, User), "Personal", d.At() + personal + "cut.ics"
	}
	for _, tc := range []struct {
		name  string
		setup func() (*Client, string, string)
	}{
		{"on alborz", func() (*Client, string, string) {
			a, c := SignedIn(t, User)
			coll := c.CreateCalendar(t, "Plans", "events tasks")
			for _, objects := range []map[string]string{readable, unreadable} {
				for name, body := range objects {
					a.Put(t, coll+name, "text/calendar", body)
				}
			}
			return c, "Plans", ""
		}},
		{"on a server, through the mirror", func() (*Client, string, string) { return served() }},
		{"on a server, asked directly", func() (*Client, string, string) { return served(NoCache()) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, calendar, cutEvent := tc.setup()
			for _, page := range []struct {
				route string
				query url.Values
				shows string
			}{
				{"calendar.month", url.Values{"month": {"2026-09"}}, "Standup"},
				{"calendar.month", url.Values{"month": {"2026-09"}, "view": {"list"}}, "Standup"},
				{"calendar.day", url.Values{"date": {"2026-09-15"}}, "Standup"},
				{"calendar.tasks", url.Values{"view": {"all"}}, "Water the ferns"},
			} {
				resp := c.Get(t, page.route, page.query)
				text := html.UnescapeString(resp.Text)
				if resp.StatusCode != http.StatusOK || !strings.Contains(text, page.shows) {
					t.Errorf("%s %v answered %s, want 200 with %q:\n%s", page.route, page.query, resp.Status, page.shows, text)
				}
				if strings.Contains(text, "Nowhen") {
					t.Errorf("%s %v shows the event whose start is no date", page.route, page.query)
				}
			}
			if resp := c.Get(t, "calendar.day", url.Values{"date": {"2026-09-15"}}); !Says(t, resp.Text, "notice.unread", 1, web.NoticeName(calendar)) {
				t.Errorf("the day page does not say %s was read in part", calendar)
			}
			if resp := c.Get(t, "core.status", "calendar"); resp.StatusCode != http.StatusOK {
				t.Errorf("the calendar segment answered %s", resp.Status)
			}
			if cutEvent == "" {
				return
			}
			for _, route := range []string{"calendar.event", "calendar.event.update"} {
				resp := c.Get(t, route, cutEvent)
				if resp.StatusCode != http.StatusBadGateway || !Says(t, resp.Text, "unreadable") {
					t.Errorf("%s of the cut event answered %s, want 502 saying it cannot be read", route, resp.Status)
				}
			}
			if resp := c.Post(t, "calendar.event.delete", nil, cutEvent); resp.StatusCode != http.StatusFound {
				t.Errorf("deleting the cut event answered %s", resp.Status)
			}
		})
	}
}

// A TZID names the VTIMEZONE of its own object (RFC 5545 3.2.19), as Outlook's zones must be read; one
// defined nowhere is floating, the wall clock it states read in the reader's zone.
func TestAnEventIsReadInTheZoneItsObjectDefines(t *testing.T) {
	objects := map[string]string{
		"outlook.ics": "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\n" +
			"BEGIN:VTIMEZONE\r\nTZID:Central Standard Time\r\n" +
			"BEGIN:STANDARD\r\nDTSTART:16010101T020000\r\nTZOFFSETFROM:-0500\r\nTZOFFSETTO:-0600\r\n" +
			"RRULE:FREQ=YEARLY;INTERVAL=1;BYDAY=1SU;BYMONTH=11\r\nEND:STANDARD\r\n" +
			"BEGIN:DAYLIGHT\r\nDTSTART:16010101T020000\r\nTZOFFSETFROM:-0600\r\nTZOFFSETTO:-0500\r\n" +
			"RRULE:FREQ=YEARLY;INTERVAL=1;BYDAY=2SU;BYMONTH=3\r\nEND:DAYLIGHT\r\nEND:VTIMEZONE\r\n" +
			"BEGIN:VEVENT\r\nUID:outlook\r\nDTSTAMP:20260901T080000Z\r\n" +
			"DTSTART;TZID=Central Standard Time:20260915T100000\r\nDTEND;TZID=Central Standard Time:20260915T103000\r\n" +
			"SUMMARY:Planning\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
		"olympus.ics": "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\nBEGIN:VEVENT\r\nUID:olympus\r\n" +
			"DTSTAMP:20260901T080000Z\r\nDTSTART;TZID=Mars/Olympus:20260915T120000\r\nSUMMARY:Olympus\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
	}
	for _, tc := range []struct {
		name  string
		setup func() *Client
	}{
		{"on alborz", func() *Client {
			a, c := SignedIn(t, User)
			coll := c.CreateCalendar(t, "Plans", "")
			for name, body := range objects {
				a.Put(t, coll+name, "text/calendar", body)
			}
			return c
		}},
		{"on a server, through the mirror", func() *Client {
			opts := []DAVOption{Calendar(DAVHome+"personal/", "Personal")}
			for name, body := range objects {
				opts = append(opts, Object(DAVHome+"personal/"+name, body))
			}
			return Alborz(t, WithDAV(DAV(t, opts...))).SignIn(t, User)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.setup()
			c.Post(t, "settings.profile", url.Values{"timezone": {"Asia/Tehran"}})
			// Each row shows its own time just after its summary, so the first clock read after
			// a summary is the time that event is shown at.
			clock := regexp.MustCompile(`\d{2}:\d{2}`)
			page := c.Page(t, "calendar.day", url.Values{"date": {"2026-09-15"}})
			at := map[string]string{}
			for _, name := range []string{"Planning", "Olympus"} {
				i := strings.Index(page, name)
				if i < 0 {
					t.Errorf("the day does not show %s:\n%s", name, page)
					continue
				}
				times := clock.FindAllString(page[i:], -1)
				if len(times) == 0 {
					t.Errorf("the day shows no time after %s:\n%s", name, page)
					continue
				}
				at[name] = times[0]
			}
			// 10:00 CDT is 15:00 UTC, 18:30 in Tehran.
			if want := map[string]string{"Planning": "18:30", "Olympus": "12:00"}; !maps.Equal(at, want) {
				t.Errorf("the day shows %v, want %v", at, want)
			}
		})
	}
}

// An object of occurrences alone, as an invitation to one of a series brings, edits the one its page shows.
func TestAnEventOfOccurrencesAloneIsEdited(t *testing.T) {
	a, c := SignedIn(t, User)
	coll := c.CreateCalendar(t, "Plans", "")
	occurrence := func(day, summary string) string {
		return "BEGIN:VEVENT\r\nUID:weekly\r\nRECURRENCE-ID:202609" + day + "T100000Z\r\nDTSTAMP:20260901T080000Z\r\n" +
			"DTSTART:202609" + day + "T100000Z\r\nDTEND:202609" + day + "T110000Z\r\nSUMMARY:" + summary + "\r\nEND:VEVENT\r\n"
	}
	a.Put(t, coll+"weekly.ics", "text/calendar", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\n"+
		occurrence("15", "First")+occurrence("22", "Second")+"END:VCALENDAR\r\n")
	weekly := Here + coll + "weekly.ics"

	resp := c.Get(t, "calendar.event.update", weekly)
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Text, `value="First"`) {
		t.Fatalf("the edit form answered %s without the occurrence the page shows", resp.Status)
	}
	form := url.Values{"summary": {"First moved"}, "start": {"2026-09-15T12:00"}, "end": {"2026-09-15T13:00"},
		"start-calendar": {"gregorian"}, "end-calendar": {"gregorian"}}
	if resp := c.Post(t, "calendar.event.update", form, weekly); resp.StatusCode != http.StatusFound {
		t.Fatalf("saving the occurrence answered %s", resp.Status)
	}
	saved := a.DAV(t, http.MethodGet, coll+"weekly.ics", nil, "").Text
	for _, want := range []string{"SUMMARY:First moved", "RECURRENCE-ID:20260915T100000Z", "SUMMARY:Second", "RECURRENCE-ID:20260922T100000Z"} {
		if !strings.Contains(saved, want) {
			t.Errorf("the saved object lacks %s:\n%s", want, saved)
		}
	}
}

// An event go-ical will not write is left out of an export with its reason; the rest downloads.
func TestAnExportLeavesOutWhatCannotBeWritten(t *testing.T) {
	a, c := SignedIn(t, User)
	coll := c.CreateCalendar(t, "Plans", "")
	a.Put(t, coll+"plain.ics", "text/calendar", Event("plain", "Plain"))
	a.Put(t, coll+"twice.ics", "text/calendar", strings.Replace(Event("twice", "One"), "SUMMARY:One", "SUMMARY:One\r\nSUMMARY:Two", 1))

	resp := c.Get(t, "calendar.collection.export", Here+coll)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the export answered %s", resp.Status)
	}
	if !strings.Contains(resp.Text, "UID:plain") || strings.Contains(resp.Text, "UID:twice") {
		t.Errorf("the export does not hold the plain event alone:\n%s", resp.Text)
	}
	if bar := NoticeBar(c.Page(t, "settings.profile")); !Says(t, bar, "export.leftout", 1, "twice:") {
		t.Errorf("the page after the export does not say what was left out, and why:\n%s", bar)
	}
}

// An event alborz's DAV server took as sent, though go-ical would not write it, is served as stored: the
// month shows it beside the rest.
func TestAStoredEventGoIcalWouldNotWriteIsServed(t *testing.T) {
	a, c := SignedIn(t, User)
	coll := c.CreateCalendar(t, "Plans", "")
	a.Put(t, coll+"plain.ics", "text/calendar", Event("plain", "Plain"))
	a.Put(t, coll+"twice.ics", "text/calendar", strings.Replace(Event("twice", "One"), "SUMMARY:One", "SUMMARY:One\r\nSUMMARY:Two", 1))
	resp := c.Get(t, "calendar.month", url.Values{"month": {"2026-09"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the month answered %s", resp.Status)
	}
	if !strings.Contains(resp.Text, "Plain") || !strings.Contains(resp.Text, "One") {
		t.Errorf("the month does not show both events")
	}
}

// An object that does not parse, and a calendar the server cannot serve, are left out of an export with
// their reasons; the rest downloads.
func TestAnExportLeavesOutWhatCannotBeRead(t *testing.T) {
	personal, work := DAVHome+"personal/", DAVHome+"work/"
	cut := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\nBEGIN:VEVENT\r\nUID:cut\r\nSUMMARY:Cut\r\n"
	d := DAV(t, Calendar(personal, "Personal"), Calendar(work, "Work"), Failing(work),
		Object(personal+"plain.ics", Event("plain", "Plain")), Object(personal+"cut.ics", cut))
	c := Alborz(t, NoCache(), WithDAV(d)).SignIn(t, User)

	for _, tc := range []struct {
		route string
		args  []any
		left  int
	}{
		{"calendar.collection.export", []any{d.At() + personal}, 1},
		{"calendar.month.export", []any{url.Values{"month": {"2026-09"}}}, 2},
	} {
		resp := c.Get(t, tc.route, tc.args...)
		if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Text, "UID:plain") || strings.Contains(resp.Text, "UID:cut") {
			t.Fatalf("%s answered %s without the readable event alone:\n%s", tc.route, resp.Status, resp.Text)
		}
		if bar := NoticeBar(c.Page(t, "settings.profile")); !Says(t, bar, "export.leftout", tc.left, Any) || !strings.Contains(bar, "cut.ics") {
			t.Errorf("%s: the page after does not say what was left out:\n%s", tc.route, bar)
		}
	}
}

// Two quick ticks on different calendars both stay hidden, whichever is written first, and the rest stay shown.
func TestTwoQuickRailTicksBothHold(t *testing.T) {
	a, c := SignedIn(t, User, WithIMAP(IMAP(t, Metadata())))
	hidden := []string{Here + c.CreateCalendar(t, "Chores", ""), Here + c.CreateCalendar(t, "Trips", "")}
	shown := Here + c.CreateCalendar(t, "Plans", "")
	errs := make(chan error, 2)
	for _, path := range hidden {
		go func() {
			form := url.Values{"shown": {path}, "next": {a.Path("calendar.month")}}
			resp, err := c.HTTP.PostForm(a.URL+a.Path("calendar.month", url.Values{"account": {User}}), form)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode != http.StatusFound {
					err = fmt.Errorf("hiding %s answered %s", path, resp.Status)
				}
			}
			errs <- err
		}()
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	rail := func() map[string]bool {
		ticks := map[string]bool{}
		for _, m := range regexp.MustCompile(`name="cal" value="([^"]+)" (checked)?`).FindAllStringSubmatch(c.Page(t, "calendar.month"), -1) {
			ticks[html.UnescapeString(m[1])] = m[2] != ""
		}
		return ticks
	}
	ticks := rail()
	if len(ticks) < 3 {
		t.Fatalf("the rail shows %d calendar ticks, want three at least", len(ticks))
	}
	for path, on := range ticks {
		if want := !slices.Contains(hidden, path); on != want {
			t.Errorf("%s is ticked %t, want %t", path, on, want)
		}
	}
	if !ticks[shown] {
		t.Errorf("%s is not on the rail ticked", shown)
	}

	// A calendar made again where a hidden one was is new to the reader, so it shows.
	if resp := c.Post(t, "calendar.collection.delete", nil, hidden[0]); resp.StatusCode != http.StatusFound {
		t.Fatalf("deleting %s answered %s", hidden[0], resp.Status)
	}
	if again := Here + c.CreateCalendar(t, "Chores", ""); again != hidden[0] {
		t.Fatalf("Chores was made again at %s, not %s", again, hidden[0])
	}
	if ticks = rail(); len(ticks) == 0 {
		t.Fatal("the rail shows no calendar ticks after Chores was made again")
	}
	if !ticks[hidden[0]] {
		t.Errorf("%s, made again, is not on the rail ticked", hidden[0])
	}
}

func TestCalendarMonthDayAnnotations(t *testing.T) {
	a, c := SignedIn(t, User)
	var dates []time.Time
	calls := 0
	web.Extend(a.Server, func(_ *web.Context, data *calweb.CalendarRenderData) error {
		calls++
		dates = append([]time.Time(nil), data.Dates...)
		data.DayAnnotations = append(data.DayAnnotations, calweb.DayAnnotation{Name: "example", URL: "/calendar/annotations"})
		return nil
	})
	c.Page(t, "calendar.month", url.Values{"month": {"2026-09"}})
	if calls != 1 || len(dates) < 28 {
		t.Fatalf("month extension calls = %d, visible dates = %d", calls, len(dates))
	}
	c.Page(t, "calendar.month", url.Values{"month": {"2026-09"}, "view": {"list"}})
	if calls != 1 {
		t.Fatal("agenda invoked the month extension")
	}
}

func TestMonthEventLinksKeepTheirScope(t *testing.T) {
	a, c := SignedIn(t, User)
	coll := c.CreateCalendar(t, "Trips", "")
	a.Put(t, coll+"trip.ics", "text/calendar", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n"+
		"BEGIN:VEVENT\r\nUID:trip\r\nDTSTAMP:20260901T080000Z\r\nDTSTART;VALUE=DATE:20260904\r\nDTEND;VALUE=DATE:20260909\r\n"+
		"SUMMARY:Factory visit\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	for _, account := range []string{"", User} {
		q := url.Values{"month": {"2026-09"}, "cal": {Here + coll}}
		if account != "" {
			q.Set("account", account)
		}
		page := c.Page(t, "calendar.month", q)
		links := regexp.MustCompile(`<a class="month-event[^"]*" href="([^"]+)"`).FindAllStringSubmatch(page, -1)
		if len(links) == 0 {
			t.Fatalf("account %q: no event links", account)
		}
		for _, link := range links {
			u, err := url.Parse(html.UnescapeString(link[1]))
			if err != nil {
				t.Fatal(err)
			}
			if u.Query().Get("account") != User || u.Query().Get("from") == "" {
				t.Errorf("event link lost its owning account or return path: %s", u)
			}
			back, err := url.Parse(u.Query().Get("from"))
			if err != nil || back.Query().Get("month") != "2026-09" || back.Query().Get("cal") != Here+coll || back.Query().Get("account") != account {
				t.Errorf("event return path lost month scope: %v (%v)", back, err)
			}
		}
	}
}

func TestHiddenCollectionsStayOutOfCreatePickers(t *testing.T) {
	_, c := SignedIn(t, User)
	for _, tc := range []struct{ list, create string }{
		{"calendar.month", "calendar.event.create"},
		{"calendar.tasks", "calendar.task.create"},
		{"calendar.journal", "calendar.entry.create"},
	} {
		t.Run(tc.list, func(t *testing.T) {
			holds := "both"
			if tc.list == "calendar.journal" {
				holds = "journal"
			}
			hidden := Here + c.CreateCalendar(t, "Hidden"+strings.ReplaceAll(tc.list, ".", ""), holds)
			c.CreateCalendar(t, "Visible"+strings.ReplaceAll(tc.list, ".", ""), holds)
			if resp := c.Post(t, tc.list, url.Values{"shown": {hidden}}); resp.StatusCode != http.StatusFound {
				t.Fatalf("hide: %s", resp.Status)
			}
			page := c.Page(t, tc.create)
			picker := Between(page, `<select name="calendar"`, `</select>`)
			if strings.Contains(picker, "Hidden"+strings.ReplaceAll(tc.list, ".", "")) || !strings.Contains(picker, "Visible") {
				t.Fatalf("picker did not respect visibility: %s", picker)
			}
		})
	}
}

func TestEventDateModesWithoutJavaScript(t *testing.T) {
	a, c := SignedIn(t, User)
	coll := c.CreateCalendar(t, "Plans", "")
	path := coll + "modes.ics"
	a.Put(t, path, "text/calendar", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\n"+
		"BEGIN:VEVENT\r\nUID:modes\r\nDTSTAMP:20260901T080000Z\r\nDTSTART:20260928T120000Z\r\nDTEND:20260928T130000Z\r\n"+
		"SUMMARY:Modes\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	for _, route := range []string{"calendar.event.create", "calendar.event.update"} {
		t.Run(route, func(t *testing.T) {
			var args []any
			if route == "calendar.event.update" {
				args = []any{Here + path}
			}
			for _, allDay := range []bool{false, true} {
				form := url.Values{"summary": {"Modes"}, "calendar": {User + "|" + Here + coll},
					"start": {"invalid inactive time"}, "end": {"invalid inactive time"},
					"start_date": {"invalid inactive date"}, "end_date": {"invalid inactive date"},
					"start-calendar": {"gregorian"}, "end-calendar": {"gregorian"},
					"start_date-calendar": {"gregorian"}, "end_date-calendar": {"gregorian"}}
				if allDay {
					form.Set("allday", "1")
					form.Set("start_date", "2026-09-28")
					form.Set("end_date", "2026-09-29")
				} else {
					form.Set("start", "2026-09-28T12:00")
					form.Set("end", "2026-09-28T13:00")
				}
				if resp := c.Post(t, route, form, args...); resp.StatusCode != http.StatusFound {
					t.Fatalf("all-day=%t submission with inactive invalid inputs answered %s", allDay, resp.Status)
				}
			}
		})
	}
}

func TestCreateCalendarWithNoneYet(t *testing.T) {
	d := DAV(t)
	c := Alborz(t, WithDAV(d)).SignIn(t, User)

	resp := c.Post(t, "calendar.event.create", url.Values{"summary": {"Lunch"}, "start": {"2026-09-28T12:00"}, "end": {"2026-09-28T13:00"}, "start-calendar": {"gregorian"}, "end-calendar": {"gregorian"}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("saving an event with nowhere to save answered %s, want the form refused", resp.Status)
	}

	form := url.Values{"name": {"First"}, "color": {"#22aa55"}, "holds": {"both"}, "place": {User + "|domain"}}
	if resp := c.Post(t, "calendar.calendars.create", form); resp.StatusCode != http.StatusFound {
		t.Fatalf("creating the first calendar answered %s, want a redirect", resp.Status)
	}
	if n := len(d.Calendars()); n != 1 {
		t.Fatalf("the server was not asked to make the calendar: %d exist", n)
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if strings.Contains(c.Page(t, "calendar.month"), "First") {
			return
		}
	}
	t.Errorf("the created calendar never appeared in the rail")
}

// The guarded client refuses the loopback stub, which is the refusal under test.
func TestSubscribingToAnUnreadableFeedIsRefused(t *testing.T) {
	d := DAV(t)
	c := Alborz(t, WithDAV(d)).SignIn(t, User)
	if resp := c.Post(t, "calendar.subscribe", url.Values{"address": {d.URL + "/feed.ics"}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("an unreadable feed answered %s, want the form back", resp.Status)
	}
	if n := len(d.Calendars()); n != 0 {
		t.Errorf("an address made a collection on the server")
	}
}

func alarmed(uid, rule, action string, start time.Time) string {
	stamp := func(t time.Time) string { return t.UTC().Format("20060102T150405Z") }
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\nBEGIN:VEVENT\r\nUID:" + uid +
		"\r\nDTSTAMP:20260901T080000Z\r\nDTSTART:" + stamp(start) + "\r\nDTEND:" + stamp(start.Add(time.Hour)) + rule +
		"\r\nSUMMARY:Budget review\r\nBEGIN:VALARM\r\nACTION:" + action + "\r\nDESCRIPTION:Soon\r\nTRIGGER:-PT2S\r\nEND:VALARM" +
		"\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
}

// A display alarm of today's occurrence of a daily series is pushed once, naming its time and summary, though
// two logins of the account find it; an e-mail alarm is not alborz's to fire.
func TestEventReminderPushesItsOccurrence(t *testing.T) {
	push := PushServer(t)
	a, c := SignedIn(t, User, Guarded(push.Client().Transport), ReminderTick(200*time.Millisecond))
	endpoint := push.TurnOn(t, c)
	// A second browser's login of the account finds the same alarm in each round.
	a.SignIn(t, User)
	cal := c.CreateCalendar(t, "Personal", "")

	start := time.Now().Add(4 * time.Second).Truncate(time.Second)
	a.Put(t, cal+"daily.ics", "text/calendar", alarmed("daily", "\r\nRRULE:FREQ=DAILY;COUNT=3", "DISPLAY", start.AddDate(0, 0, -1)))
	a.Put(t, cal+"mailed.ics", "text/calendar", alarmed("mailed", "", "EMAIL", start))

	want := T(t, "push.event", start.UTC().Format("15:04"))
	got := push.Await(t, endpoint, want)
	if got.Title != want || got.Body != User+"\nBudget review" {
		t.Fatalf("the push says %q / %q, want %q / %q", got.Title, got.Body, want, User+"\nBudget review")
	}
	if page := c.Follow(t, got.URL); !strings.Contains(page, "Budget review") {
		t.Fatalf("a click on the push opens %q, which is not the event", got.URL)
	}
	if got.Header.Get("Urgency") != "high" {
		t.Fatalf("urgency %q", got.Header.Get("Urgency"))
	}
	time.Sleep(time.Second)
	if n := len(push.Received()); n != 1 {
		t.Fatalf("%d pushes, want the one reminder: %+v", n, push.Received())
	}
}

func TestSubtasksKeepTheirParentAndCompleteIndependently(t *testing.T) {
	a, c := SignedIn(t, User)
	cal := c.CreateCalendar(t, "Project", "tasks")
	parent, child := cal+"parent.ics", cal+"child.ics"
	a.Put(t, parent, "text/calendar", Todo("parent-uid", "Build prototype"))
	a.Put(t, child, "text/calendar", Todo("child-uid", "Order components"))
	if resp := c.Post(t, "calendar.task.edit", url.Values{"summary": {"Order components"}, "parent": {"parent-uid"}}, Here+child); resp.StatusCode != http.StatusFound {
		t.Fatalf("set parent: %s\n%s", resp.Status, resp.Text)
	}
	// A descendant as parent would close a cycle.
	if resp := c.Post(t, "calendar.task.edit", url.Values{"summary": {"Build prototype"}, "parent": {"child-uid"}}, Here+parent); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("cycle accepted: %s", resp.Status)
	}
	if !strings.Contains(c.Page(t, "calendar.task", Here+parent), "Order components") {
		t.Fatal("parent page does not list its child")
	}
	if resp := c.Post(t, "calendar.task.complete", url.Values{}, Here+parent); resp.StatusCode != http.StatusFound {
		t.Fatalf("complete parent: %s", resp.Status)
	}
	report := a.DAV(t, "REPORT", cal, map[string]string{"Depth": "1", "Content-Type": "application/xml"}, `<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:prop><C:calendar-data/></D:prop><C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter name="VTODO"/></C:comp-filter></C:filter></C:calendar-query>`).Text
	if strings.Count(report, "RELATED-TO;RELTYPE=PARENT:parent-uid") != 1 || strings.Contains(report, "PARENT:child-uid") || strings.Count(report, "STATUS:COMPLETED") != 1 {
		t.Fatalf("relationship/completion not preserved:\n%s", report)
	}
	// Completed parents are absent from the open list; the child remains an open task.
	if !strings.Contains(c.Page(t, "calendar.tasks"), "Order components") {
		t.Fatal("completing the parent hid its open child")
	}
	if !strings.Contains(c.Page(t, "calendar.task", Here+parent), "Order components") {
		t.Fatal("completed parent lost its children")
	}
}

func TestSavingKeepsAMissingParent(t *testing.T) {
	a, c := SignedIn(t, User)
	cal := c.CreateCalendar(t, "Project", "tasks")
	orphan := cal + "orphan.ics"
	a.Put(t, orphan, "text/calendar", strings.Replace(Todo("orphan-uid", "Solder header"), "END:VTODO", "RELATED-TO;RELTYPE=PARENT:gone-uid\r\nEND:VTODO", 1))
	if resp := c.Post(t, "calendar.task.edit", url.Values{"summary": {"Solder header"}, "parent": {"gone-uid"}}, Here+orphan); resp.StatusCode != http.StatusFound {
		t.Fatalf("save: %s\n%s", resp.Status, resp.Text)
	}
	if got := a.DAV(t, "GET", orphan, nil, "").Text; !strings.Contains(got, "RELATED-TO;RELTYPE=PARENT:gone-uid") {
		t.Fatalf("missing parent lost on save:\n%s", got)
	}
}

func TestQuickEventDestinationMatchesTaskScope(t *testing.T) {
	_, c := SignedIn(t, User)
	first := Here + c.CreateCalendar(t, "First", "")
	quick := func(params url.Values) string {
		return html.UnescapeString(Between(c.Page(t, "calendar.day", params), `id="quick-add">`, `</form>`))
	}
	single := func(form string) {
		t.Helper()
		if strings.Contains(form, `<select`) || !strings.Contains(form, `<input type="hidden" name="calendar" value=`) {
			t.Fatalf("single destination should be fixed without a picker:\n%s", form)
		}
		if Says(t, form, "common.saveto") {
			t.Fatalf("fixed destination must stay hidden like the tasks quick add:\n%s", form)
		}
	}
	single(quick(nil))
	c.CreateCalendar(t, "Second", "")
	if form := quick(nil); !strings.Contains(form, `<select`) || !strings.Contains(form, "First") || !strings.Contains(form, "Second") {
		t.Fatalf("multiple destinations need a picker:\n%s", form)
	}
	single(quick(url.Values{"cal": {first}}))
}

// A quick add naming no day lands on the day its page has open: the day page's,
// the agenda's month on its first day, or today in the month that holds it; a
// line it cannot read offers the full editor on that day.
func TestQuickEventLandsOnTheDayOpen(t *testing.T) {
	a, c := SignedIn(t, User)
	cal := c.CreateCalendar(t, "First", "")
	c.Post(t, "settings.profile", url.Values{"timezone": {"UTC"}})
	today := time.Now().UTC()
	quick, field := regexp.MustCompile(`action="([^"]+)" class="quick-add"`), regexp.MustCompile(`name="([^"]+)" value="([^"]*)"`)
	for _, tc := range []struct {
		route  string
		params url.Values
		want   time.Time
	}{
		{"calendar.day", url.Values{"date": {"2026-12-03"}}, time.Date(2026, 12, 3, 0, 0, 0, 0, time.UTC)},
		{"calendar.day", url.Values{"date": {"2026-12-04"}, "account": {User}}, time.Date(2026, 12, 4, 0, 0, 0, 0, time.UTC)},
		{"calendar.month", url.Values{"month": {"2027-03"}, "view": {"list"}}, time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)},
		{"calendar.month", url.Values{"view": {"list"}, "account": {User}}, today},
	} {
		page := c.Page(t, tc.route, tc.params)
		form, action := url.Values{}, ""
		if m := quick.FindStringSubmatch(page); m != nil {
			action = html.UnescapeString(m[1])
		}
		for _, f := range field.FindAllStringSubmatch(Between(page, `id="quick-add">`, `</form>`), -1) {
			form.Set(html.UnescapeString(f[1]), html.UnescapeString(f[2]))
		}
		if action == "" || form.Get("quick") != "event" {
			t.Fatalf("%s %v: no quick add", tc.route, tc.params)
		}
		form.Set("summary", "Lunch with Sara")
		if resp := c.PostTo(t, action, form); resp.StatusCode != http.StatusFound {
			t.Fatalf("%s %v: quick add answered %s", tc.route, tc.params, resp.Status)
		}
		report := a.DAV(t, "REPORT", cal, map[string]string{"Depth": "1", "Content-Type": "application/xml"}, `<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:prop><C:calendar-data/></D:prop><C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter name="VEVENT"/></C:comp-filter></C:filter></C:calendar-query>`).Text
		if !strings.Contains(report, "DTSTART;VALUE=DATE:"+tc.want.Format("20060102")) {
			t.Errorf("%s %v: want an all-day event on %s:\n%s", tc.route, tc.params, tc.want.Format(time.DateOnly), report)
		}
		form.Set("summary", "Keep this ambiguous 03/04/2026")
		resp := c.PostTo(t, action, form)
		if link := html.UnescapeString(Between(resp.Text, `id="event-quick-error"`, `</p>`)); !strings.Contains(link, "date="+tc.want.Format(time.DateOnly)) {
			t.Errorf("%s %v: the editor link must open on %s: %s\n%s", tc.route, tc.params, tc.want.Format(time.DateOnly), resp.Status, link)
		}
	}
}

// Every calendar view's New opens the event form on the day it has open, in its own scope.
func TestNewEventOpensOnTheDayInView(t *testing.T) {
	a, c := SignedIn(t, User)
	c.Post(t, "settings.profile", url.Values{"timezone": {"UTC"}})
	link := regexp.MustCompile(`href="([^"]+)"[^>]*data-key="c"`)
	for _, tc := range []struct {
		route  string
		params url.Values
		day    string
	}{
		{"calendar.month", url.Values{"month": {"2027-03"}}, "2027-03-01"},
		{"calendar.month", url.Values{"account": {User}}, time.Now().UTC().Format(time.DateOnly)},
		{"calendar.month", url.Values{"month": {"2027-03"}, "view": {"list"}, "account": {User}}, "2027-03-01"},
		{"calendar.day", url.Values{"date": {"2026-12-03"}}, "2026-12-03"},
	} {
		m := link.FindStringSubmatch(c.Page(t, tc.route, tc.params))
		if m == nil {
			t.Fatalf("%s %v: no New", tc.route, tc.params)
		}
		href, err := url.Parse(html.UnescapeString(m[1]))
		if err != nil || href.Path != a.Path("calendar.event.create") || href.Query().Get("date") != tc.day || href.Query().Get("account") != tc.params.Get("account") {
			t.Errorf("%s %v: New leads to %s, want the form on %s", tc.route, tc.params, m[1], tc.day)
		}
	}
}

func TestQuickEventCreatesInChosenCalendarAndKeepsInvalidInput(t *testing.T) {
	a, c := SignedIn(t, User)
	c.CreateCalendar(t, "First", "")
	cal := c.CreateCalendar(t, "Chosen", "")
	c.Post(t, "settings.profile", url.Values{"timezone": {"Europe/Berlin"}})
	form := url.Values{"quick": {"event"}, "summary": {"Review 2026-11-03 14:00-15:30"}, "calendar": {User + "|" + Here + cal}, "next": {a.Path("calendar.month", url.Values{"view": {"list"}}) + "#quick-add"}}
	if resp := c.Post(t, "calendar.event.create", form); resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != form.Get("next") {
		t.Fatalf("quick create: %s\n%s", resp.Status, resp.Text)
	}
	report := a.DAV(t, "REPORT", cal, map[string]string{"Depth": "1", "Content-Type": "application/xml"}, `<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:prop><C:calendar-data/></D:prop><C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter name="VEVENT"/></C:comp-filter></C:filter></C:calendar-query>`).Text
	if !strings.Contains(report, "SUMMARY:Review") || !strings.Contains(report, "20261103T140000") || !strings.Contains(report, "Europe/Berlin") {
		t.Fatalf("quick event not stored in chosen calendar/timezone:\n%s", report)
	}
	form.Set("summary", "Keep this ambiguous 03/04/2026")
	if resp := c.Post(t, "calendar.event.create", form); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("an ambiguous quick event answered %s, want it refused", resp.Status)
	}
	form.Set("summary", "Lunch tomorrow afternoon")
	back := a.Path("calendar.day", url.Values{"date": {"2026-11-03"}, "cal": {Here + cal}})
	form.Set("next", back+"#quick-add")
	req, err := http.NewRequest(http.MethodPost, a.URL+a.Path("calendar.event.create"), strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Boosted", "true")
	resp := c.Do(t, req)
	if resp.StatusCode != http.StatusUnprocessableEntity || resp.Header.Get("HX-Replace-Url") != back {
		t.Fatalf("boosted invalid submission must replace create URL with calendar: %s %v", resp.Status, resp.Header)
	}
}
