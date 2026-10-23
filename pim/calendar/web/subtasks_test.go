package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

func TestSubtasksKeepTheirParentAndCompleteIndependently(t *testing.T) {
	a, c := SignedIn(t, User)
	cal := c.CreateCalendar(t, "Project", "tasks")
	other := c.CreateCalendar(t, "Other", "tasks")
	parent := cal + "parent.ics"
	a.Put(t, parent, "text/calendar", Todo("parent-uid", "Build prototype"))
	page := c.Page(t, "calendar.task", Here+parent)
	if !Says(t, page, "tasks.subtask.add") {
		t.Fatal("parent does not offer subtask creation")
	}
	create := c.Page(t, "calendar.task.create", url.Values{"parent": {Here + parent}})
	if !strings.Contains(create, `name="parent"`) || !strings.Contains(create, "Build prototype") {
		t.Fatal("child form lost its parent")
	}
	form := url.Values{"parent": {Here + parent}, "summary": {"Order components"}, "calendar": {User + "|" + Here + other}}
	if resp := c.Post(t, "calendar.task.create", form); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(resp.Text, `value="Order components"`) {
		t.Fatalf("child in unrelated list did not retain input: %s", resp.Status)
	}
	form.Set("calendar", User+"|"+Here+cal)
	if resp := c.Post(t, "calendar.task.create", form); resp.StatusCode != http.StatusFound {
		t.Fatalf("create child: %s\n%s", resp.Status, resp.Text)
	}
	page = c.Page(t, "calendar.task", Here+parent)
	if !strings.Contains(page, "Order components") {
		t.Fatal("parent page does not list its child")
	}
	if resp := c.Post(t, "calendar.task.complete", url.Values{}, Here+parent); resp.StatusCode != http.StatusFound {
		t.Fatalf("complete parent: %s", resp.Status)
	}
	report := a.DAV(t, "REPORT", cal, map[string]string{"Depth": "1", "Content-Type": "application/xml"}, `<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:prop><C:calendar-data/></D:prop><C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter name="VTODO"/></C:comp-filter></C:filter></C:calendar-query>`).Text
	if !strings.Contains(report, "RELATED-TO;RELTYPE=PARENT:parent-uid") || strings.Count(report, "STATUS:COMPLETED") != 1 || !strings.Contains(report, "STATUS:NEEDS-ACTION") {
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
