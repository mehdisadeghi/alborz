package web_test

import (
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

func TestQuickEventDestinationMatchesTaskScope(t *testing.T) {
	_, c := SignedIn(t, User)
	first := Here + c.CreateCalendar(t, "First", "")
	quick := func(params url.Values) string {
		return html.UnescapeString(Between(c.Page(t, "calendar.month", params), `id="quick-add">`, `</form>`))
	}
	single := func(form string) {
		t.Helper()
		if strings.Contains(form, `<select`) || !strings.Contains(form, `type="hidden" name="calendar" value="`+User+`|`+first+`"`) {
			t.Fatalf("single destination should be fixed without a picker:\n%s", form)
		}
		if !Says(t, form, "common.saveto") || !strings.Contains(form, "First") {
			t.Fatalf("fixed destination must remain visible:\n%s", form)
		}
	}
	single(quick(nil))
	c.CreateCalendar(t, "Second", "")
	if form := quick(nil); !strings.Contains(form, `data-destination-key="quickevent"`) || !strings.Contains(form, "First") || !strings.Contains(form, "Second") {
		t.Fatalf("multiple destinations need a picker:\n%s", form)
	}
	single(quick(url.Values{"cal": {first}}))
}

func TestQuickEventCreatesInChosenCalendarAndKeepsInvalidInput(t *testing.T) {
	a, c := SignedIn(t, User)
	c.CreateCalendar(t, "First", "")
	cal := c.CreateCalendar(t, "Chosen", "")
	c.Post(t, "settings.profile", url.Values{"timezone": {"Europe/Berlin"}})
	for _, route := range []string{"calendar.month", "calendar.day"} {
		page := c.Page(t, route)
		if !strings.Contains(page, `name="quick" value="event"`) || !strings.Contains(page, `data-destination-key="quickevent"`) {
			t.Fatalf("%s has no quick event/calendar picker", route)
		}
		if !strings.Contains(page, `<details class="quick-help">`) || !strings.Contains(page, `hx-get="`+a.Path("calendar.quick.preview")+`"`) || !strings.Contains(page, `hx-boost="true" hx-replace-url="true" id="quick-add"`) {
			t.Fatalf("%s has no collapsed help/server interpretation", route)
		}
	}
	preview := html.UnescapeString(c.Page(t, "calendar.quick.preview", url.Values{"summary": {"Lunch tomorrow at noon"}}))
	if !strings.Contains(preview, "12:00–13:00") || !strings.Contains(preview, "Lunch") || !strings.Contains(preview, "Europe/Berlin") || strings.Index(preview, "12:00") > strings.Index(preview, "Lunch") {
		t.Fatalf("preview must put interpreted clocks before title: %s", preview)
	}
	if preview := c.Page(t, "calendar.quick.preview", url.Values{"summary": {"Lunch tomorrow afternoon"}}); !Says(t, preview, "calendar.quick.invalid") {
		t.Fatalf("unclear timing needs an inline preview error: %s", preview)
	}
	form := url.Values{"quick": {"event"}, "summary": {"Review 2026-11-03 14:00-15:30"}, "calendar": {User + "|" + Here + cal}, "next": {a.Path("calendar.month") + "#quick-add"}}
	if resp := c.Post(t, "calendar.event.create", form); resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != form.Get("next") {
		t.Fatalf("quick create: %s\n%s", resp.Status, resp.Text)
	}
	report := a.DAV(t, "REPORT", cal, map[string]string{"Depth": "1", "Content-Type": "application/xml"}, `<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:prop><C:calendar-data/></D:prop><C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter name="VEVENT"/></C:comp-filter></C:filter></C:calendar-query>`).Text
	if !strings.Contains(report, "SUMMARY:Review") || !strings.Contains(report, "20261103T140000") || !strings.Contains(report, "Europe/Berlin") {
		t.Fatalf("quick event not stored in chosen calendar/timezone:\n%s", report)
	}
	form.Set("summary", "Keep this ambiguous 03/04/2026")
	resp := c.Post(t, "calendar.event.create", form)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(resp.Text, `value="Keep this ambiguous 03/04/2026"`) || !Says(t, resp.Text, "calendar.quick.invalid") {
		t.Fatalf("invalid quick event lost input: %s\n%s", resp.Status, resp.Text)
	}
	if !strings.Contains(resp.Text, `id="quick-add"`) || !strings.Contains(html.UnescapeString(resp.Text), `value="`+form.Get("calendar")+`" selected`) || !Says(t, resp.Text, "calendar.quick.editor") {
		t.Fatalf("invalid event must stay in calendar with destination and editor link: %s", resp.Text)
	}
	form.Set("summary", "Lunch tomorrow afternoon")
	back := a.Path("calendar.day", url.Values{"date": {"2026-11-03"}, "cal": {Here + cal}})
	form.Set("next", back+"#quick-add")
	resp = c.Post(t, "calendar.event.create", form)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(resp.Text, `class="events" data-swipe-calendar`) || !strings.Contains(resp.Text, `value="Lunch tomorrow afternoon"`) || !strings.Contains(html.UnescapeString(resp.Text), `value="`+back+`#quick-add"`) {
		t.Fatalf("invalid event must retain originating day and filters: %s", resp.Text)
	}
	req, err := http.NewRequest(http.MethodPost, a.URL+a.Path("calendar.event.create"), strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Boosted", "true")
	resp = c.Do(t, req)
	if resp.StatusCode != http.StatusUnprocessableEntity || resp.Header.Get("HX-Replace-Url") != back || !strings.Contains(resp.Text, `id="event-quick-error"`) {
		t.Fatalf("boosted invalid submission must replace create URL with calendar: %s %v", resp.Status, resp.Header)
	}
	editor := c.Page(t, "calendar.event.create", url.Values{"summary": {form.Get("summary")}, "calendar": {form.Get("calendar")}})
	if !strings.Contains(editor, `value="Lunch tomorrow afternoon"`) || !strings.Contains(html.UnescapeString(editor), `value="`+form.Get("calendar")+`" selected`) {
		t.Fatalf("full editor must preserve quick draft and destination: %s", editor)
	}
}
