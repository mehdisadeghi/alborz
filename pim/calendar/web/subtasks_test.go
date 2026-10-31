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
