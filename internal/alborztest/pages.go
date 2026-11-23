package alborztest

import (
	"html"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"git.mehdix.org/alborz/internal/locale"
)

// T is what key says in English, the language a test browser reads, filled with args as the page fills it.
// A test reads its wording from the locales, so a reworded string never breaks it while a missing key does.
func T(t *testing.T, key string, args ...any) string {
	t.Helper()
	return TIn(t, "en", key, args...)
}

// TIn is T in lang.
func TIn(t *testing.T, lang, key string, args ...any) string {
	t.Helper()
	if locale.T(lang, key) == key {
		t.Fatalf("the locales have no %s", key)
	}
	return locale.Tf(lang, key, args...)
}

// Any fills a verb of T with what the test cannot know, such as a server's error.
const Any = "\x00"

// Says reports whether page shows T(key, args...), each Any in args matching whatever stands there.
func Says(t *testing.T, page, key string, args ...any) bool {
	t.Helper()
	text := html.UnescapeString(page)
	for part := range strings.SplitSeq(T(t, key, args...), Any) {
		i := strings.Index(text, part)
		if i < 0 {
			return false
		}
		text = text[i+len(part):]
	}
	return true
}

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
	noticeForm   = regexp.MustCompile(`<form method="post" action="([^"]+)" class="notice-action"[^>]*>`)
	noticeLink   = regexp.MustCompile(`<a class="notice-action" href="([^"]+)">`)
	hiddenFields = regexp.MustCompile(`name="([^"]+)" value="([^"]*)"`)
	shownField   = regexp.MustCompile(`<input type="hidden" name="shown" value="(\{[^"]*)"`)
	alert        = regexp.MustCompile(`(?s)<div class="alert"[^>]*>(.*?)</div>`)
)

// Shown is the hidden copy of what a page's form was drawn from, or "" where it carries none; a rail's
// shown collections are a list, not a copy.
func Shown(page string) string {
	m := shownField.FindStringSubmatch(page)
	if m == nil {
		return ""
	}
	return html.UnescapeString(m[1])
}

var (
	optionTag = regexp.MustCompile(`(?s)<option\b([^>]*)>`)
	selected  = regexp.MustCompile(`\bselected\b`)
	valueAttr = regexp.MustCompile(`value="([^"]*)"`)
)

// SelectedValue is the value of the option a page's named select marks selected, or "".
func SelectedValue(page, name string) string {
	block := regexp.MustCompile(`(?s)<select[^>]*name="` + regexp.QuoteMeta(name) + `"[^>]*>(.*?)</select>`).FindStringSubmatch(page)
	if block == nil {
		return ""
	}
	for _, tag := range optionTag.FindAllStringSubmatch(block[1], -1) {
		if !selected.MatchString(tag[1]) {
			continue
		}
		if v := valueAttr.FindStringSubmatch(tag[1]); v != nil {
			return html.UnescapeString(v[1])
		}
	}
	return ""
}

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

// PagerLinks are the pager's links.
func PagerLinks(page string) []string {
	var links []string
	for _, m := range pagerLink.FindAllStringSubmatch(Between(page, `class="actions-pagination"`, "</div>"), -1) {
		links = append(links, html.UnescapeString(m[1]))
	}
	return links
}

// PagerSteps are the page parameters the pager's links name, the first page's being empty.
func PagerSteps(t *testing.T, page string) []string {
	t.Helper()
	var steps []string
	for _, link := range PagerLinks(page) {
		u, err := url.Parse(link)
		if err != nil {
			t.Fatalf("pager link %q: %v", link, err)
		}
		steps = append(steps, u.Query().Get("page"))
	}
	return steps
}

var (
	mainBlock  = regexp.MustCompile(`(?s)<main\b[^>]*>(.*)</main>`)
	outOfBand  = regexp.MustCompile(`<\w+\b[^>]*\bid="([^"]+)"[^>]*\bhx-swap-oob=`)
	nextField  = regexp.MustCompile(`<input type="hidden" (?:id="[^"]*" )?name="next" value="([^"]*)"`)
	linkTarget = regexp.MustCompile(`<a\b[^>]*\bhref="([^"]+)"`)
)

// Swapped is the page a swap into main leaves of before: main is the swap's, what the swap carries out of
// band takes the place of the element of its id, and the rest is as it was (ADR 13).
func Swapped(before, swap string) string {
	for _, m := range outOfBand.FindAllStringSubmatch(swap, -1) {
		before = withoutElement(before, m[1])
	}
	return mainBlock.ReplaceAllLiteralString(before, "<main>"+swap+"</main>")
}

// withoutElement is page without the element of the id, its end found by counting its kind.
func withoutElement(page, id string) string {
	at := strings.Index(page, ` id="`+id+`"`)
	if at < 0 {
		return page
	}
	start := strings.LastIndexByte(page[:at], '<')
	tag, _, _ := strings.Cut(page[start+1:at], " ")
	if tag == "input" {
		return page[:start] + page[at+strings.IndexByte(page[at:], '>')+1:]
	}
	open, end := 0, start
	for {
		next := strings.IndexByte(page[end:], '<')
		if next < 0 {
			return page
		}
		end += next
		switch rest := page[end:]; {
		case strings.HasPrefix(rest, "</"+tag+">"):
			open--
			end += len("</" + tag + ">")
		case strings.HasPrefix(rest, "<"+tag+" "), strings.HasPrefix(rest, "<"+tag+">"):
			open++
			end++
		default:
			end++
		}
		if open == 0 {
			return page[:start] + page[end:]
		}
	}
}

// Returns are the addresses a page's forms and links name to come back to, their next, each without its fragment.
func Returns(page string) []string {
	var out []string
	add := func(raw string) {
		if u, err := url.Parse(raw); err == nil {
			u.Fragment = ""
			out = append(out, u.String())
		}
	}
	for _, m := range nextField.FindAllStringSubmatch(page, -1) {
		add(html.UnescapeString(m[1]))
	}
	for _, m := range linkTarget.FindAllStringSubmatch(page, -1) {
		if u, err := url.Parse(html.UnescapeString(m[1])); err == nil {
			for _, next := range u.Query()["next"] {
				add(next)
			}
		}
	}
	return out
}
