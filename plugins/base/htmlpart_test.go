package alborzbase

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset"
)

const mixedBody = "سلام، این یک پیام فارسی است.\n\nAnd this paragraph is English."

func parts(t *testing.T, sendHTML bool) (types []string, bodies map[string]string, raw string) {
	t.Helper()
	msg := &OutgoingMessage{
		From: "a@example.org", To: []string{"b@example.org"},
		Subject: "RTL", MessageID: "<x@example.org>",
		Text: mixedBody, SendHTML: sendHTML,
	}
	var buf bytes.Buffer
	if err := msg.WriteMessage(&buf); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	raw = buf.String()

	bodies = map[string]string{}
	entity, err := message.Read(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("read written message: %v", err)
	}
	var walk func(e *message.Entity)
	walk = func(e *message.Entity) {
		if mr := e.MultipartReader(); mr != nil {
			for {
				p, err := mr.NextPart()
				if err != nil {
					return
				}
				walk(p)
			}
		}
		ct, _, _ := e.Header.ContentType()
		body, _ := io.ReadAll(e.Body)
		types = append(types, ct)
		bodies[ct] = string(body)
	}
	walk(entity)
	return types, bodies, raw
}

// Least-preferred goes first (RFC 2046 5.1.4).
func TestSendHTMLKeepsThePlainPartFirst(t *testing.T) {
	types, bodies, raw := parts(t, true)

	if !strings.Contains(raw, "multipart/alternative") {
		t.Fatalf("no alternative part; types were %v", types)
	}
	plain, html := indexOf(types, "text/plain"), indexOf(types, "text/html")
	if plain < 0 || html < 0 {
		t.Fatalf("expected both parts, got %v", types)
	}
	if plain > html {
		t.Errorf("the HTML part came first: %v", types)
	}
	for _, line := range strings.Split(mixedBody, "\n\n") {
		if !strings.Contains(bodies["text/plain"], line) {
			t.Errorf("the typed text did not survive: %q\ngot: %q", line, bodies["text/plain"])
		}
	}
}

func TestSendHTMLCarriesDirection(t *testing.T) {
	_, bodies, _ := parts(t, true)
	for _, want := range []string{`dir="rtl"`, `dir="ltr"`} {
		if !strings.Contains(bodies["text/html"], want) {
			t.Errorf("the HTML part does not carry %s:\n%s", want, bodies["text/html"])
		}
	}
	if !strings.Contains(bodies["text/html"], "سلام") {
		t.Errorf("the Persian text is missing from the HTML part:\n%s", bodies["text/html"])
	}
}

func TestWithoutSendHTMLNothingChanges(t *testing.T) {
	types, _, raw := parts(t, false)
	if strings.Contains(raw, "multipart/alternative") || indexOf(types, "text/html") >= 0 {
		t.Fatalf("an alternative was sent without being asked for: %v", types)
	}
	if indexOf(types, "text/plain") < 0 {
		t.Errorf("no text part at all: %v", types)
	}
}

func indexOf(all []string, want string) int {
	for i, v := range all {
		if v == want {
			return i
		}
	}
	return -1
}
