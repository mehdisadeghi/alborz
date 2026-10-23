package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

func TestHiddenCollectionsStayOutOfCreatePickers(t *testing.T) {
	_, c := SignedIn(t, User)
	for _, tc := range []struct{ list, create string }{
		{"calendar.month", "calendar.event.create"},
		{"calendar.tasks", "calendar.task.create"},
		{"calendar.journal", "calendar.entry.create"},
	} {
		t.Run(tc.list, func(t *testing.T) {
			holds := "both"
			if tc.list == "calendar.journal" { holds = "journal" }
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
