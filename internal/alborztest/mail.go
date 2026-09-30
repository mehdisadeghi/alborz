package alborztest

import (
	"html"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// Compose sends the compose form page shows to the route, with fields changed.
func (c *Client) Compose(t *testing.T, page string, fields url.Values, route string, args ...any) *Response {
	t.Helper()
	mid := strings.NewReplacer("&lt;", "<", "&gt;", ">").Replace(Between(page, `name="message_id" value="`, `"`))
	if mid == "" {
		t.Fatalf("the form for %s carries no message id", route)
	}
	form := url.Values{
		"message_id":  {mid},
		"in_reply_to": {Between(page, `name="in_reply_to" value="`, `"`)},
		"from":        {User}, "to": {"friend@example.org"},
		"subject": {"direction"}, "text": {"سلام\n\nEnglish."},
	}
	for k, v := range fields {
		form[k] = v
	}
	return c.Upload(t, route, form, nil, args...)
}

// Send is Compose, which must be taken.
func (c *Client) Send(t *testing.T, page string, fields url.Values, route string, args ...any) {
	t.Helper()
	if resp := c.Compose(t, page, fields, route, args...); resp.StatusCode >= 400 {
		t.Fatalf("composing at %s: %s", route, resp.Status)
	}
}

// RawParts are the paths a message page links and shows its parts by, as the page escaped them.
func (a *App) RawParts(page string) []string {
	raw := a.Pattern("mail.message.raw", `[^"/]*`, `[^"/]*`) + `\?[^"]*`
	var out []string
	for _, m := range regexp.MustCompile(`href="(`+raw+`)"`).FindAllStringSubmatch(page, -1) {
		out = append(out, strings.ReplaceAll(m[1], "&amp;", "&"))
	}
	// The srcdoc arrives attribute-escaped.
	for _, m := range regexp.MustCompile(`src="(`+raw+`)"`).FindAllStringSubmatch(html.UnescapeString(page), -1) {
		out = append(out, m[1])
	}
	return out
}
