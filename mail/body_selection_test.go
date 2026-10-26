package mail

import (
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

func TestPrimaryBodyKeepsMultipartBoundaries(t *testing.T) {
	text := func(subtype string) imap.BodyStructure {
		return &imap.BodyStructureSinglePart{Type: "text", Subtype: subtype}
	}
	multi := func(subtype string, children ...imap.BodyStructure) imap.BodyStructure {
		return &imap.BodyStructureMultiPart{Subtype: subtype, Children: children}
	}
	file := &imap.BodyStructureSinglePart{Type: "text", Subtype: "plain", Params: map[string]string{"name": "notes.txt"}}
	for _, tc := range []struct {
		name        string
		body        imap.BodyStructure
		plain, html string
	}{
		{"Mailman footer", multi("mixed", multi("alternative", text("plain"), text("html")), text("plain")), "1.1", "1.2"},
		{"HTML body with plain footer", multi("mixed", text("html"), text("plain")), "1", "1"},
		{"plain body with HTML footer", multi("mixed", text("plain"), text("html")), "1", ""},
		{"last matching alternative", multi("ALTERNATIVE", text("plain"), text("plain"), text("html"), text("html")), "2", "4"},
		{"nested HTML alternative keeps real plain text", multi("alternative", text("plain"), multi("related", text("html"))), "1", "2.1"},
		{"Apple split HTML", multi("alternative", text("plain"), multi("mixed", text("html"), file, text("html"))), "1", "2.1"},
		{"leading file", multi("mixed", file, multi("alternative", text("plain"), text("html"))), "2.1", "2.2"},
		{"forwarded attachment", multi("mixed", text("plain"), &imap.BodyStructureSinglePart{Type: "message", Subtype: "rfc822"}), "1", ""},
		{"single plain", text("plain"), "1", ""},
		{"single HTML", text("html"), "1", "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := &IMAPMessage{FetchMessageBuffer: &imapclient.FetchMessageBuffer{BodyStructure: tc.body}}
			path := func(part *IMAPPartNode) string {
				if part == nil {
					return ""
				}
				return part.PathString()
			}
			if got := path(msg.TextPart()); got != tc.plain {
				t.Errorf("plain body %q, want %q", got, tc.plain)
			}
			if got := path(msg.HTMLPart()); got != tc.html {
				t.Errorf("HTML body %q, want %q", got, tc.html)
			}
			if tc.name == "Apple split HTML" {
				pieces := msg.HTMLPieces(msg.HTMLPart().Path)
				if len(pieces) != 2 || pieces[0].PathString() != "2.1" || pieces[1].PathString() != "2.3" {
					t.Errorf("split HTML pieces: %v", pieces)
				}
			}
		})
	}
}
