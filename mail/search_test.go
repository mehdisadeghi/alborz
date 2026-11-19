package mail

import (
	"testing"

	"git.mehdix.org/alborz/mail/imapsearch"
	"git.mehdix.org/alborz/search"
	"github.com/emersion/go-imap/v2"
)

// The rows alborz holds and the answers it drops by their envelope are matched here, not by the server.
func TestHeldRowsAndEnvelopesMatchInAnyCase(t *testing.T) {
	env := &imap.Envelope{
		Subject: "Grüße aus der Hauptstraße",
		From:    []imap.Address{{Name: "Jürgen Weiß", Mailbox: "Juergen.Weiss", Host: "Example.org"}},
		To:      []imap.Address{{Name: "İpek Işık", Mailbox: "ipek", Host: "example.org"}},
		Cc:      []imap.Address{{Name: "مریم گزارش", Mailbox: "maryam", Host: "example.org"}},
	}
	for _, tc := range []struct {
		query string
		want  bool
	}{
		{"hauptstraße", true},
		{"HAUPTSTRASSE", true},
		{"subject:GRÜSSE", true},
		{"-subject:grüsse", false},
		{"from:JUERGEN.WEISS@EXAMPLE.ORG", true},
		{"from:weiss", true},
		{"from:WEIẞ", true},
		{"to:ipek", true},
		{"to:IŞIK", true},
		{"to:isik", false},
		{"cc:گزارش", true},
		{"cc:مريم", true},
		{"to:jürgen", false},
	} {
		q := search.Parse(tc.query)
		if got := heldMatches(q, env); got != tc.want {
			t.Errorf("a held row for %q: got %v, want %v", tc.query, got, tc.want)
		}
		if !imapsearch.Addressed(q) {
			continue
		}
		if got := envelopeCarries(env, imapsearch.Addresses(q)); got != tc.want {
			t.Errorf("an envelope for %q: got %v, want %v", tc.query, got, tc.want)
		}
	}
}

func TestSearchNamesAFolderInAnyCase(t *testing.T) {
	folders := []MailboxInfo{
		{ListData: &imap.ListData{Mailbox: "INBOX", Delim: '/'}},
		{ListData: &imap.ListData{Mailbox: "Ablage/Straße", Delim: '/'}},
		{ListData: &imap.ListData{Mailbox: "Entwürfe", Delim: '/', Attrs: []imap.MailboxAttr{imap.MailboxAttrDrafts}}},
	}
	for query, want := range map[string]string{
		"in:inbox":         "INBOX",
		"in:ENTWÜRFE":      "Entwürfe",
		"in:Drafts":        "Entwürfe",
		"in:STRASSE":       "Ablage/Straße",
		"in:ablage/straße": "Ablage/Straße",
	} {
		if got := searchFolders(folders, search.Parse(query)); len(got) != 1 || got[0] != want {
			t.Errorf("%s searches %q, want %q alone", query, got, want)
		}
	}
}
