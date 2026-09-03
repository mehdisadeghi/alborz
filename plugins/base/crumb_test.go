package alborzbase

import (
	"testing"

	"github.com/emersion/go-imap/v2"
)

// TestMailboxCrumbNamesEachStep is about the two things that make a
// breadcrumb a path rather than a label. A custom folder's Label is its
// whole wire name, so using it at every step would read
// "INBOX/Lists > INBOX/Lists"; and a parent that exists only to hold
// children cannot be opened, so linking it offers a dead end.
func TestMailboxCrumbNamesEachStep(t *testing.T) {
	boxes := []MailboxInfo{
		{ListData: &imap.ListData{Mailbox: "INBOX", Delim: '/'}, Label: "Inbox"},
		{ListData: &imap.ListData{Mailbox: "INBOX/Lists", Delim: '/'}, Label: "INBOX/Lists"},
		{ListData: &imap.ListData{Mailbox: "INBOX/Lists/Unix", Delim: '/'}, Label: "INBOX/Lists/Unix"},
	}
	boxes[1].Attrs = []imap.MailboxAttr{imap.MailboxAttrNoSelect}

	crumb := mailboxCrumb(boxes, "INBOX/Lists/Unix", "someone@example.org")
	want := []CrumbLink{
		{Label: "someone@example.org", URL: "/mailbox/INBOX"},
		{Label: "Inbox", URL: "/mailbox/INBOX"},
		{Label: "Lists"},
		{Label: "Unix", URL: "/mailbox/INBOX%2FLists%2FUnix"},
	}
	if len(crumb) != len(want) {
		t.Fatalf("got %d steps %v, want %d", len(crumb), crumb, len(want))
	}
	for i := range want {
		if crumb[i] != want[i] {
			t.Errorf("step %d: got %+v, want %+v", i, crumb[i], want[i])
		}
	}
}
