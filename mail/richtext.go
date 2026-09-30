package mail

import (
	"html"
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"jaytaylor.com/html2text"

	"git.mehdix.org/alborz/internal/lang"
)

// composedPolicy drops what the editor did not write, so a paste cannot carry styles or scripts; images
// may reference only what alborz holds (ADR 40), which images matches.
func composedPolicy(images *regexp.Regexp) *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements("p", "div", "br", "b", "strong", "i", "em", "u", "s", "ul", "ol", "li", "blockquote", "a")
	p.AllowAttrs("dir").Matching(regexp.MustCompile(`^(ltr|rtl|auto)$`)).OnElements("p", "div", "li", "blockquote", "ul", "ol")
	p.AllowAttrs("href").Matching(regexp.MustCompile(`^(?i)(https?|mailto):`)).OnElements("a")
	p.AllowURLSchemes("http", "https", "mailto")
	p.AllowRelativeURLs(true)
	p.AllowElements("img")
	p.AllowAttrs("src").Matching(images).OnElements("img")
	p.AllowAttrs("alt").OnElements("img")
	p.RequireNoFollowOnLinks(false)
	return p
}

var (
	composedBlock = regexp.MustCompile(`(?s)<(p|li)((?: [^>]*)?)>(.*?)</(?:p|li)>`)
	composedList  = regexp.MustCompile(`(?s)<(ul|ol)((?: [^>]*)?)>(.*?)</(?:ul|ol)>`)
	composedTag   = regexp.MustCompile(`<[^>]*>`)
	composedDir   = regexp.MustCompile(` dir="[^"]*"`)
	itemDir       = regexp.MustCompile(`<li[^>]* dir="(ltr|rtl)"`)
	quoteTag      = regexp.MustCompile(`<blockquote([^>]*)>`)
)

func composedHTML(policy *bluemonday.Policy, raw string) string {
	clean := policy.Sanitize(raw)
	if strings.TrimSpace(composedTag.ReplaceAllString(clean, "")) == "" && !strings.Contains(clean, "<img") {
		return ""
	}
	clean = composedBlock.ReplaceAllStringFunc(clean, func(block string) string {
		m := composedBlock.FindStringSubmatch(block)
		text := html.UnescapeString(composedTag.ReplaceAllString(m[3], ""))
		attrs := composedDir.ReplaceAllString(m[2], "")
		return "<" + m[1] + attrs + ` dir="` + lang.ParagraphDir(text) + `">` + m[3] + "</" + m[1] + ">"
	})
	clean = quoteTag.ReplaceAllString(clean, `<blockquote$1 type="cite">`)
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

func withSignatureHTML(body, text string) string {
	body = strings.TrimSpace(signatureBlock.ReplaceAllString(body, ""))
	if text == "" {
		return body
	}
	lines := strings.Split(html.EscapeString(text), "\n")
	return body + "\n<p dir=\"" + lang.ParagraphDir(text) + "\">-- <br>" + strings.Join(lines, "<br>") + "</p>"
}

func composedText(htmlBody string) string {
	text, err := html2text.FromString(htmlBody, html2text.Options{})
	if err != nil {
		return ""
	}
	return emptyQuoted.ReplaceAllString(text, "\n>\n")
}

var emptyQuoted = regexp.MustCompile(`(?:\n> ?){2,}\n`)
