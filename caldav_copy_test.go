package alborz_test

import (
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	. "git.mehdix.org/alborz/internal/alborztest"
)

// servedFeed publishes body as a calendar feed.
func servedFeed(t *testing.T, body string) *httptest.Server {
	t.Helper()
	f := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(body))
	}))
	t.Cleanup(f.Close)
	return f
}

// clubNight repeats weekly from today in Berlin, with an exception, an extra date, a moved
// instance and an organizer, as a club's published calendar has them.
func clubNight() string {
	day := func(n int) string { return time.Now().AddDate(0, 0, n).Format("20060102") }
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nMETHOD:PUBLISH\r\nX-WR-CALNAME:Club\r\n" +
		"BEGIN:VTIMEZONE\r\nTZID:Europe/Berlin\r\n" +
		"BEGIN:STANDARD\r\nDTSTART:19701025T030000\r\nRRULE:FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU\r\nTZOFFSETFROM:+0200\r\nTZOFFSETTO:+0100\r\nEND:STANDARD\r\n" +
		"BEGIN:DAYLIGHT\r\nDTSTART:19700329T020000\r\nRRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU\r\nTZOFFSETFROM:+0100\r\nTZOFFSETTO:+0200\r\nEND:DAYLIGHT\r\n" +
		"END:VTIMEZONE\r\n" +
		"BEGIN:VEVENT\r\nUID:club-night\r\nDTSTAMP:20260901T080000Z\r\n" +
		"DTSTART;TZID=Europe/Berlin:" + day(0) + "T190000\r\nDTEND;TZID=Europe/Berlin:" + day(0) + "T210000\r\n" +
		"RRULE:FREQ=WEEKLY;COUNT=10\r\nEXDATE;TZID=Europe/Berlin:" + day(14) + "T190000\r\n" +
		"RDATE;TZID=Europe/Berlin:" + day(3) + "T190000\r\nSUMMARY:Club night\r\n" +
		"ORGANIZER:mailto:host@example.org\r\nATTENDEE:mailto:guest@example.org\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:club-night\r\nDTSTAMP:20260901T080000Z\r\n" +
		"RECURRENCE-ID;TZID=Europe/Berlin:" + day(7) + "T190000\r\n" +
		"DTSTART;TZID=Europe/Berlin:" + day(7) + "T200000\r\nDTEND;TZID=Europe/Berlin:" + day(7) + "T220000\r\n" +
		"SUMMARY:Club night, late\r\nORGANIZER:mailto:host@example.org\r\nEND:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
}

// icsLines are an object's unfolded content lines.
func icsLines(raw string) []string {
	return strings.Split(strings.ReplaceAll(raw, "\r\n ", ""), "\r\n")
}

// withName are the lines of the property called name.
func withName(lines []string, name string) []string {
	var out []string
	for _, l := range lines {
		if strings.HasPrefix(l, name+":") || strings.HasPrefix(l, name+";") {
			out = append(out, l)
		}
	}
	return out
}

// via is the value of the LINK;LINKREL=via of the kind, or "".
func via(lines []string, kind string) string {
	for _, l := range withName(lines, "LINK") {
		params, value, _ := strings.Cut(l, ":")
		if strings.Contains(params, ";LINKREL=via") && strings.Contains(params, ";VALUE="+kind) {
			return value
		}
	}
	return ""
}

// copied posts the copy and returns where it landed.
func copied(t *testing.T, c *Client, to, route string, args ...any) string {
	t.Helper()
	resp := c.Post(t, route, url.Values{"to": {to}}, args...)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("copying answered %s", resp.Status)
	}
	return resp.Header.Get("Location")
}

// raw is an object's source, read through the raw route of the page at path.
func raw(t *testing.T, a *App, c *Client, path string) string {
	t.Helper()
	u, err := url.Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	name, values, ok := a.Server.Match(u)
	if !ok {
		t.Fatalf("%s is no route", path)
	}
	return c.Page(t, name+".raw", values["path"], u.Query())
}

// A feed's event taken into one's own calendar is a new event there, no invitation, and says where it came from.
func TestCopyingAFeedEventMakesAnEventOfOurOwn(t *testing.T) {
	f := servedFeed(t, clubNight())
	a, c := subscriber(t, f)
	subscribe(t, c, f.URL+"/club.ics")
	plans := Here + c.CreateCalendar(t, "Plans", "")

	linked := events(t, a, c)
	if len(linked) == 0 {
		t.Fatal("the agenda links no event of the feed")
	}
	event := linked[0]
	if page := c.Page(t, "calendar.event", event); !strings.Contains(page, a.Path("calendar.event.copy", event)) {
		t.Fatal("the read-only event's page offers no copy")
	}
	chooser := c.Page(t, "calendar.event.copy", event)
	if !strings.Contains(chooser, `value="`+User+`|`+plans+`"`) || !strings.Contains(chooser, "Club night") {
		t.Fatal("the copy page does not offer Plans for Club night")
	}
	if strings.Contains(chooser, "Club</option>") {
		t.Error("the copy page offers the read-only feed")
	}

	landed := copied(t, c, User+"|"+plans, "calendar.event.copy", event)
	if !strings.Contains(landed, url.PathEscape(plans)) {
		t.Fatalf("the copy landed on %s, not an event in Plans", landed)
	}
	if page := c.Follow(t, landed); !strings.Contains(page, `Copied to “<span class="notice-name">Plans</span>”.`) {
		t.Error("the copy's page does not say where it went")
	}

	lines := icsLines(raw(t, a, c, landed))
	uids := withName(lines, "UID")
	if len(uids) != 2 || uids[0] != uids[1] || uids[0] == "UID:club-night" {
		t.Errorf("the copy's UIDs are %q, want one fresh UID on the series and its moved instance", uids)
	}
	for _, gone := range []string{"METHOD", "ORGANIZER", "ATTENDEE"} {
		if got := withName(lines, gone); len(got) > 0 {
			t.Errorf("the copy keeps %q", got)
		}
	}
	for _, kept := range []string{"RRULE", "EXDATE", "RDATE", "RECURRENCE-ID", "TZID"} {
		if len(withName(lines, kept)) == 0 {
			t.Errorf("the copy lost its %s", kept)
		}
	}
	if got := via(lines, "URI"); got != f.URL+"/club.ics" {
		t.Errorf("the copy links to %q, want the feed", got)
	}
	if got := via(lines, "UID"); got != "club-night" {
		t.Errorf("the copy names %q as its original, want club-night", got)
	}
}

// A task copies as an event does, and the notice's undo deletes the copy and nothing else.
func TestCopyingATaskIsUndoneByDeletingTheCopy(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	chores := c.CreateCalendar(t, "Chores", "tasks")
	errands := Here + c.CreateCalendar(t, "Errands", "tasks")
	a.Put(t, chores+"water.ics", "text/calendar", Todo("water", "Water the ferns"))

	landed := copied(t, c, User+"|"+errands, "calendar.task.copy", Here+chores+"water.ics", url.Values{"account": {User}})
	if !strings.Contains(landed, url.PathEscape(errands)) {
		t.Fatalf("the copy landed on %s, not a task in Errands", landed)
	}
	lines := icsLines(raw(t, a, c, landed))
	if uids := withName(lines, "UID"); len(uids) != 1 || uids[0] == "UID:water" || !slices.Equal(withName(lines, "SUMMARY"), []string{"SUMMARY:Water the ferns"}) {
		t.Errorf("the copy is %q, want Water the ferns under a fresh UID", lines)
	}
	if got := via(lines, "URI"); !strings.HasSuffix(got, chores) {
		t.Errorf("the copy links to %q, want the Chores list", got)
	}

	page := html.UnescapeString(c.Follow(t, landed))
	undo := regexp.MustCompile(`<form method="post" action="([^"]+/delete[^"]*)" class="notice-action">\s*((?:<input[^>]+>)+)`).FindStringSubmatch(page)
	if undo == nil {
		t.Fatal("the copy's notice offers no undo")
	}
	fields := url.Values{}
	for _, in := range regexp.MustCompile(`name="([^"]+)" value="([^"]*)"`).FindAllStringSubmatch(undo[2], -1) {
		fields.Add(in[1], in[2])
	}
	if resp := c.PostTo(t, undo[1], fields); resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != fields.Get("next") {
		t.Fatalf("the undo answered %s to %s", resp.Status, resp.Header.Get("Location"))
	}
	u, err := url.Parse(landed)
	if err != nil {
		t.Fatal(err)
	}
	_, values, ok := a.Server.Match(u)
	if !ok {
		t.Fatalf("the copy's page %s is no route", landed)
	}
	if resp := a.DAV(t, http.MethodGet, strings.TrimPrefix(values["path"], Here), nil, ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("the copy is still there after the undo: %s", resp.Status)
	}
	if resp := a.DAV(t, http.MethodGet, chores+"water.ics", nil, ""); resp.StatusCode != http.StatusOK {
		t.Errorf("the undo took the original: %s", resp.Status)
	}
}

// With nothing writable the form is offered all the same, and the server says why it did nothing.
func TestCopyingWithNoWritableCalendarIsRefused(t *testing.T) {
	f := servedFeed(t, clubNight())
	a, c := subscriber(t, f)
	subscribe(t, c, f.URL+"/club.ics")
	linked := events(t, a, c)
	if len(linked) == 0 {
		t.Fatal("the agenda links no event of the feed")
	}
	if chooser := c.Page(t, "calendar.event.copy", linked[0]); !strings.Contains(chooser, `name="to"`) || strings.Contains(chooser, `value="`+User+`|`) {
		t.Fatal("the copy page is not an empty choice")
	}
	resp := c.Post(t, "calendar.event.copy", url.Values{}, linked[0])
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(resp.Text, "Choose where to save it.") {
		t.Fatalf("copying to nowhere answered %s, want the form refused with the reason", resp.Status)
	}
	if got := c.Listed(t); len(got) != 1 {
		t.Errorf("the calendars are %v after a refused copy, want the feed alone", got)
	}
}
