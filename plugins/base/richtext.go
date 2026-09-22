package alborzbase

import (
	"html"
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"jaytaylor.com/html2text"

	"git.mehdix.org/alborz"
)

// The editor writes simple HTML and nothing more; what it did not
// write is dropped on the way out, so a paste from a web page cannot
// carry a style sheet or a script into the message.
var composedPolicy = func() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements("p", "div", "br", "b", "strong", "i", "em", "u", "s", "ul", "ol", "li", "blockquote", "a")
	p.AllowAttrs("dir").Matching(regexp.MustCompile(`^(ltr|rtl|auto)$`)).OnElements("p", "div", "li", "blockquote", "ul", "ol")
	p.AllowAttrs("href").Matching(regexp.MustCompile(`^(?i)(https?|mailto):`)).OnElements("a")
	p.AllowURLSchemes("http", "https", "mailto")
	// An image names what alborz holds and nothing else (ADR 40); the
	// relative URL is what lets the reference through, and the pattern
	// is what keeps every other one out.
	p.AllowRelativeURLs(true)
	p.AllowElements("img")
	p.AllowAttrs("src").Matching(localImage).OnElements("img")
	p.AllowAttrs("alt").OnElements("img")
	p.RequireNoFollowOnLinks(false)
	return p
}()

var (
	composedBlock = regexp.MustCompile(`(?s)<(p|li)((?: [^>]*)?)>(.*?)</(?:p|li)>`)
	composedList  = regexp.MustCompile(`(?s)<(ul|ol)((?: [^>]*)?)>(.*?)</(?:ul|ol)>`)
	composedTag   = regexp.MustCompile(`<[^>]*>`)
	composedDir   = regexp.MustCompile(` dir="[^"]*"`)
	itemDir       = regexp.MustCompile(`<li[^>]* dir="(ltr|rtl)"`)
	quoteTag      = regexp.MustCompile(`<blockquote([^>]*)>`)
)

// composedHTML is the editor's HTML made fit to send: the policy
// applied, and each paragraph's direction written down. The editor
// marks its blocks dir=auto, which a browser resolves and Outlook does
// not; the paragraph rule is applied here, once, so every client lays
// the message out the same way.
func composedHTML(raw string) string {
	clean := composedPolicy.Sanitize(raw)
	if strings.TrimSpace(composedTag.ReplaceAllString(clean, "")) == "" && !strings.Contains(clean, "<img") {
		return ""
	}
	clean = composedBlock.ReplaceAllStringFunc(clean, func(block string) string {
		m := composedBlock.FindStringSubmatch(block)
		text := html.UnescapeString(composedTag.ReplaceAllString(m[3], ""))
		attrs := composedDir.ReplaceAllString(m[2], "")
		return "<" + m[1] + attrs + ` dir="` + alborz.ParagraphDir(text) + `">` + m[3] + "</" + m[1] + ">"
	})
	// Every quote is marked as one (type=cite, as Apple Mail and
	// Thunderbird write it): Apple Mail draws its quote bars from it, and
	// Gmail and Outlook fold the history behind it. The policy keeps no
	// type, so the mark is added once, here.
	clean = quoteTag.ReplaceAllString(clean, `<blockquote$1 type="cite">`)
	// A list's marker and indent sit on the side its items run to, so
	// the list takes its first item's direction.
	return composedList.ReplaceAllStringFunc(clean, func(list string) string {
		m := composedList.FindStringSubmatch(list)
		first := itemDir.FindStringSubmatch(m[3])
		if first == nil {
			return list
		}
		attrs := composedDir.ReplaceAllString(m[2], "")
		return "<" + m[1] + attrs + ` dir="` + first[1] + `">` + m[3] + "</" + m[1] + ">"
	})
}

var signatureBlock = regexp.MustCompile(`(?s)<p[^>]*>-- <br>.*?</p>\s*$`)

// withSignatureHTML is withSignature for a message written in the
// editor: the signature is the last paragraph, under its "-- " line,
// and choosing another replaces it.
func withSignatureHTML(body, text string) string {
	body = strings.TrimSpace(signatureBlock.ReplaceAllString(body, ""))
	if text == "" {
		return body
	}
	lines := strings.Split(html.EscapeString(text), "\n")
	return body + "\n<p dir=\"" + alborz.ParagraphDir(text) + "\">-- <br>" + strings.Join(lines, "<br>") + "</p>"
}

// composedText is the plain part of a message written in the editor:
// what a client that wants no HTML reads, and what a reply quotes.
func composedText(htmlBody string) string {
	text, err := html2text.FromString(htmlBody, html2text.Options{})
	if err != nil {
		return ""
	}
	// The converter pads every block inside a quote with empty quoted
	// lines; one is enough between paragraphs.
	return emptyQuoted.ReplaceAllString(text, "\n>\n")
}

var emptyQuoted = regexp.MustCompile(`(?:\n> ?){2,}\n`)
