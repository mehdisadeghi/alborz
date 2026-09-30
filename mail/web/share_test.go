package web_test

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

// What another app shares opens compose: its title the subject, its text and link the body, its file
// attached and sent with the message even without a script.
func TestShareOpensCompose(t *testing.T) {
	smtp := SMTP(t)
	a := Alborz(t, WithSMTP(smtp))
	c := a.SignIn(t, User)
	resp := c.Upload(t, "mail.share", url.Values{"title": {"Trip notes"}, "text": {"See the plan."}, "url": {"https://example.org/plan"}},
		[]File{{Field: "files", Name: "plan.txt", Type: "text/plain", Body: strings.NewReader("Day one: the coast.")}})
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("sharing answered %s", resp.Status)
	}
	page := c.Follow(t, resp.Header.Get("Location"))
	for _, want := range []string{`value="Trip notes"`, "See the plan.\n\nhttps://example.org/plan", "plan.txt"} {
		if !strings.Contains(page, want) {
			t.Fatalf("compose lacks %q", want)
		}
	}
	uuids := Between(page, `name="attachment-uuids" value="`, `"`)
	if uuids == "" {
		t.Fatal("the shared file is not carried by the form")
	}
	sent := c.Upload(t, "mail.compose", url.Values{"to": {"eve@example.org"}, "subject": {"Trip notes"}, "text": {"See the plan."}, "attachment-uuids": {uuids}}, nil)
	if sent.StatusCode != http.StatusFound {
		t.Fatalf("sending answered %s", sent.Status)
	}
	file := "Day one: the coast."
	if msg := smtp.Last(); !strings.Contains(msg, "plan.txt") || !strings.Contains(msg, file) && !strings.Contains(msg, base64.StdEncoding.EncodeToString([]byte(file))) {
		t.Fatalf("the sent message lacks the shared file:\n%s", msg)
	}
}
