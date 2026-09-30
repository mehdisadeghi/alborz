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

// Completing a task revises its DTSTAMP (RFC 5545 3.8.7.2); the added date is its CREATED and stays.
func TestCompletingATaskKeepsItsAddedDate(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	chores := c.CreateCalendar(t, "Chores", "tasks")
	a.Put(t, chores+"water.ics", "text/calendar", strings.Replace(Todo("water", "Water the ferns"),
		"DTSTAMP:", "CREATED:20250301T080000Z\r\nDTSTAMP:", 1))
	added := func() string {
		cell := regexp.MustCompile(`class="task-list-added list-cell"[^>]*title="([^"]*)"`).FindStringSubmatch(c.Page(t, "calendar.tasks", url.Values{"view": {"all"}}))
		if cell == nil {
			t.Fatal("the task list has no added cell")
		}
		return cell[1]
	}
	before := added()
	if !strings.Contains(before, "2025") {
		t.Fatalf("the task shows added %q, want its CREATED in 2025", before)
	}
	if resp := c.Post(t, "calendar.task.complete", url.Values{}, Here+chores+"water.ics"); resp.StatusCode != http.StatusFound {
		t.Fatalf("completing the task answered %s", resp.Status)
	}
	if !strings.Contains(a.DAV(t, http.MethodGet, chores+"water.ics", nil, "").Text, "STATUS:COMPLETED") {
		t.Fatal("the task was not completed")
	}
	if after := added(); after != before {
		t.Errorf("completing the task moved its added date from %q to %q", before, after)
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

// The pager swaps the next page into main, so that answer is main alone, never a second page with its nav.
func TestAPagedTaskListSwapsOnlyItsMain(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	chores := c.CreateCalendar(t, "Chores", "tasks")
	for i := 1; i <= 3; i++ {
		a.Put(t, fmt.Sprintf("%st%d.ics", chores, i), "text/calendar", Todo(fmt.Sprintf("t%d", i), fmt.Sprintf("Chore %d", i)))
	}
	first := c.Page(t, "calendar.tasks", url.Values{"sort": {"summary"}, "ipp": {"2"}})
	swap := regexp.MustCompile(`hx-get="([^"]+)" hx-target="#main"`).FindStringSubmatch(Between(first, `class="actions-pagination"`, "</div>"))
	if swap == nil {
		t.Fatal("the first page's pager swaps nothing into main")
	}
	resp := swapIntoMain(t, a, c, html.UnescapeString(swap[1]))
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
	resp = swapIntoMain(t, a, a.Client(), html.UnescapeString(swap[1]))
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
			a := Alborz(t)
			c := a.SignIn(t, User)
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
				{"calendar.month", url.Values{"month": {"2026-09"}, "view": {"list"}, "span": {"month"}}, "Standup"},
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
			said := regexp.MustCompile(`Part of <span class="notice-name">` + calendar + `</span> could not be read\.`)
			if resp := c.Get(t, "calendar.day", url.Values{"date": {"2026-09-15"}}); !said.MatchString(resp.Text) {
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
				if resp.StatusCode != http.StatusBadGateway || !strings.Contains(resp.Text, "cannot be read") {
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
			a := Alborz(t)
			c := a.SignIn(t, User)
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
			at := map[string]string{}
			time := regexp.MustCompile(`data-label="Time">([^<]*)<`)
			summary := regexp.MustCompile(`dir="auto">([^<]*)</a>`)
			for _, row := range strings.Split(c.Page(t, "calendar.day", url.Values{"date": {"2026-09-15"}}), `class="event-row`)[1:] {
				if tm, sm := time.FindStringSubmatch(row), summary.FindStringSubmatch(row); tm != nil && sm != nil {
					at[sm[1]] = tm[1]
				}
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
	a := Alborz(t)
	c := a.SignIn(t, User)
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
	a := Alborz(t)
	c := a.SignIn(t, User)
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
	if bar := NoticeBar(c.Page(t, "settings.profile")); !strings.Contains(bar, "1 left out of the export: twice:") {
		t.Errorf("the page after the export does not say what was left out, and why:\n%s", bar)
	}
}
