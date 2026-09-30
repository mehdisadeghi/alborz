package alborztest

import (
	"html"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// Between is what s holds between the first start and the end after it, or "".
func Between(s, start, end string) string {
	_, rest, ok := strings.Cut(s, start)
	if !ok {
		return ""
	}
	in, _, ok := strings.Cut(rest, end)
	if !ok {
		return ""
	}
	return in
}

var (
	noticeBar    = regexp.MustCompile(`(?s)<dialog open class="notice[^"]*"[^>]*>(.*?)</dialog>`)
	noticeDiv    = regexp.MustCompile(`(?s)<div class="notice[^"]*"[^>]*>(.*?)</div>`)
	noticeForm   = regexp.MustCompile(`<form method="post" action="([^"]+)" class="notice-action">`)
	noticeLink   = regexp.MustCompile(`<a class="notice-action" href="([^"]+)">`)
	hiddenFields = regexp.MustCompile(`name="([^"]+)" value="([^"]*)"`)
	alert        = regexp.MustCompile(`(?s)<div class="alert"[^>]*>(.*?)</div>`)
)

// NoticeBar is the page's notice, or "".
func NoticeBar(page string) string { return noticeBar.FindString(page) }

// NoticeAction is where the notice's action leads, with its form's fields; a link has none.
func NoticeAction(page string) (string, url.Values) {
	m := noticeDiv.FindStringSubmatch(page)
	if m == nil {
		return "", nil
	}
	bar := m[1]
	if f := noticeForm.FindStringSubmatch(bar); f != nil {
		fields := url.Values{}
		for _, in := range hiddenFields.FindAllStringSubmatch(bar, -1) {
			fields.Add(html.UnescapeString(in[1]), html.UnescapeString(in[2]))
		}
		return html.UnescapeString(f[1]), fields
	}
	if a := noticeLink.FindStringSubmatch(bar); a != nil {
		return html.UnescapeString(a[1]), nil
	}
	return "", nil
}

// AlertText is the page's alert, or "".
func AlertText(page string) string { return alert.FindString(page) }

// MessageUIDs are the UIDs of a mail list's rows, each once.
func MessageUIDs(page string) []string {
	var out []string
	for _, part := range strings.Split(page, `name="uids" value="`)[1:] {
		if i := strings.IndexByte(part, '"'); i > 0 {
			uid := part[:i]
			if len(out) == 0 || out[len(out)-1] != uid {
				out = append(out, uid)
			}
		}
	}
	return out
}

var pagerLink = regexp.MustCompile(`<a href="([^"]+)" class="icon-button"`)

// PagerSteps are the page parameters the pager's links name, the first page's being empty.
func PagerSteps(t *testing.T, page string) []string {
	t.Helper()
	var steps []string
	for _, m := range pagerLink.FindAllStringSubmatch(Between(page, `class="actions-pagination"`, "</div>"), -1) {
		u, err := url.Parse(html.UnescapeString(m[1]))
		if err != nil {
			t.Fatalf("pager link %q: %v", m[1], err)
		}
		steps = append(steps, u.Query().Get("page"))
	}
	return steps
}
