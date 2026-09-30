package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

func TestCollectionDeleteReportsItsOutcome(t *testing.T) {
	personal := DAVHome + "personal/"
	d := DAV(t, Calendar(personal, "Personal"), Refusing(personal))
	c := Alborz(t, WithDAV(d)).SignIn(t, User)
	held := d.At() + personal

	if resp := c.Post(t, "calendar.collection.delete", nil, held); resp.StatusCode != http.StatusFound {
		t.Fatalf("a refused delete answered %s, want a redirect not a 500", resp.Status)
	}
	if page := c.Page(t, "calendar.collection", held); !strings.Contains(page, "“Personal” could not be deleted.") {
		t.Errorf("the refused delete left no message on the page")
	}

	d.Apply(Refusing(), Keeping(personal))
	if resp := c.Post(t, "calendar.collection.delete", nil, held); resp.StatusCode != http.StatusFound {
		t.Fatalf("delete: %s", resp.Status)
	}
	if page := c.Page(t, "calendar.collection", held); !strings.Contains(page, "kept “Personal”") {
		t.Errorf("a delete the server accepted but ignored was reported as done")
	}

	d.Apply(Keeping())
	if resp := c.Post(t, "calendar.collection.delete", nil, held); resp.StatusCode != http.StatusFound {
		t.Fatalf("delete: %s", resp.Status)
	}
	if page := c.Page(t, "calendar.month"); !strings.Contains(page, "“Personal” was deleted.") {
		t.Errorf("a completed delete was not confirmed")
	}
}

// A phone deleting a shared calendar is heard by every account that saw it, so no page names it again.
func TestAPhoneDeletingASharedCalendarLeavesNoPageNamingIt(t *testing.T) {
	a := Alborz(t)
	owner := a.SignIn(t, User)
	cal := owner.CreateCalendar(t, "Plans", "")
	ownPath := Here + cal
	if resp := owner.Post(t, "calendar.collection.share", url.Values{"to": {User2}, "access": {"read"}}, ownPath); resp.StatusCode != http.StatusFound {
		t.Fatalf("sharing answered %s", resp.Status)
	}
	guest := a.SignIn(t, User2)
	guestPath := Here + CalendarsOf(User2) + "plans~" + User + "/"
	if resp := guest.Post(t, "calendar.collection.accept", url.Values{}, guestPath); resp.StatusCode != http.StatusFound {
		t.Fatalf("accepting answered %s", resp.Status)
	}
	seen := map[*Client]string{owner: ownPath, guest: guestPath}
	for c, p := range seen {
		if !c.Listed(t)[p] {
			t.Fatalf("%s is not listed before the delete", p)
		}
	}
	if resp := a.DAV(t, http.MethodDelete, cal, nil, ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the phone's DELETE answered %s", resp.Status)
	}
	for c, p := range seen {
		if c.Listed(t)[p] {
			t.Errorf("%s is still listed after a phone deleted it", p)
		}
	}
}

// Another client deleting a calendar on a remote server leaves the page answering from the copy, and asking
// the server again leaves the pages without it.
func TestARemoteCalendarDeletedElsewhereLeavesThePage(t *testing.T) {
	personal := DAVHome + "personal/"
	d := DAV(t, Calendar(personal, "Personal"))
	c := Alborz(t, WithDAV(d)).SignIn(t, User)
	path := d.At() + personal
	if rail := c.Page(t, "calendar.calendars.create"); !strings.Contains(rail, path) {
		t.Fatalf("%s is not listed", path)
	}
	d.Apply(Gone(personal))
	c.Page(t, "calendar.month")
	if resp := c.Post(t, "calendar.refresh", url.Values{}); resp.StatusCode != http.StatusFound {
		t.Fatalf("refreshing answered %s", resp.Status)
	}
	if rail := c.Page(t, "calendar.calendars.create"); strings.Contains(rail, path) {
		t.Errorf("%s is still listed after asking the server again", path)
	}
}
