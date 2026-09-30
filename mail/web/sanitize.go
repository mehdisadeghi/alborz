package web

import (
	"bytes"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"git.mehdix.org/alborz/mail"
	"github.com/aymerick/douceur/css"
	cssparser "github.com/aymerick/douceur/parser"
	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
)

// TODO: the url( and expression( patterns ignore CSS quoting.
var (
	cssURLRegexp  = regexp.MustCompile(`url\([^)]*\)`)
	cssExprRegexp = regexp.MustCompile(`expression\([^)]*\)`)
)

var allowedStyles = map[string]bool{
	"direction":       true,
	"font":            true,
	"font-family":     true,
	"font-style":      true,
	"font-variant":    true,
	"font-size":       true,
	"font-weight":     true,
	"letter-spacing":  true,
	"line-height":     true,
	"text-align":      true,
	"text-decoration": true,
	"text-indent":     true,
	"text-overflow":   true,
	"text-shadow":     true,
	"text-transform":  true,
	"white-space":     true,
	"word-spacing":    true,
	"word-wrap":       true,
	"vertical-align":  true,

	"color":             true,
	"background":        true,
	"background-color":  true,
	"background-image":  true,
	"background-repeat": true,

	"border":         true,
	"border-color":   true,
	"border-radius":  true,
	"border-style":   true,
	"border-width":   true,
	"border-top":     true,
	"border-right":   true,
	"border-bottom":  true,
	"border-left":    true,
	"height":         true,
	"max-height":     true,
	"min-height":     true,
	"margin":         true,
	"margin-top":     true,
	"margin-right":   true,
	"margin-bottom":  true,
	"margin-left":    true,
	"padding":        true,
	"padding-top":    true,
	"padding-right":  true,
	"padding-bottom": true,
	"padding-left":   true,
	"width":          true,
	"max-width":      true,
	"min-width":      true,

	"background-position": true,
	"background-size":     true,

	"box-shadow": true,
	"box-sizing": true,
	"display":    true,
	"opacity":    true,

	"clear": true,
	"float": true,

	"border-collapse": true,
	"border-spacing":  true,
	"caption-side":    true,
	"empty-cells":     true,
	"table-layout":    true,

	"list-style-type":     true,
	"list-style-position": true,
}

type sanitizer struct {
	msg     *mail.IMAPMessage
	account string
	// raw is where the message's parts are fetched, proxy where a remote image is.
	raw, proxy           string
	allowRemoteResources bool
	hasRemoteResources   bool
	// text is sampled during the walk so the language needs no second parse.
	text strings.Builder
}

// A few hundred runes settle the language; a newsletter is not worth copying whole.
const textCap = 2000

func hiddenText(parent *html.Node) bool {
	if parent == nil || parent.Type != html.ElementNode {
		return false
	}
	switch strings.ToLower(parent.Data) {
	case "style", "script", "title", "head":
		return true
	}
	return false
}

func (san *sanitizer) sanitizeImageURL(src string) string {
	u, err := url.Parse(src)
	if err != nil {
		return "about:blank"
	}

	switch strings.ToLower(u.Scheme) {
	// Like a Content-ID, a part path reaches only this message's images.
	case "part":
		if san.msg == nil {
			return "about:blank"
		}
		var path []int
		for _, s := range strings.Split(u.Opaque, ".") {
			n, err := strconv.Atoi(s)
			if err != nil {
				return "about:blank"
			}
			path = append(path, n)
		}
		part := san.msg.PartByPath(path)
		if part == nil || !strings.HasPrefix(part.MIMEType, "image/") {
			return "about:blank"
		}
		return san.raw + partQuery(*part, san.account)
	// TODO: mid support?
	case "cid":
		if san.msg == nil {
			return "about:blank"
		}

		part := san.msg.PartByID(u.Opaque)
		if part == nil || !strings.HasPrefix(part.MIMEType, "image/") {
			return "about:blank"
		}

		return san.raw + partQuery(*part, san.account)
	case "https":
		san.hasRemoteResources = true

		if !san.allowRemoteResources {
			return "about:blank"
		}

		proxyURL := url.URL{Path: san.proxy}
		proxyQuery := make(url.Values)
		proxyQuery.Set("src", u.String())
		proxyURL.RawQuery = proxyQuery.Encode()
		return proxyURL.String()
	default:
		return "about:blank"
	}
}

func (san *sanitizer) sanitizeCSSDecls(decls []*css.Declaration) []*css.Declaration {
	sanitized := make([]*css.Declaration, 0, len(decls))
	for _, decl := range decls {
		if !allowedStyles[decl.Property] {
			continue
		}
		if cssExprRegexp.FindStringIndex(decl.Value) != nil {
			continue
		}

		// TODO: more robust CSS declaration parsing
		decl.Value = cssURLRegexp.ReplaceAllString(decl.Value, "url(about:blank)")

		sanitized = append(sanitized, decl)
	}
	return sanitized
}

func (san *sanitizer) sanitizeCSSRule(rule *css.Rule) {
	if rule.Kind == css.AtRule && strings.EqualFold(rule.Name, "@import") {
		rule.Prelude = "url(about:blank)"
	}

	rule.Declarations = san.sanitizeCSSDecls(rule.Declarations)

	for _, child := range rule.Rules {
		san.sanitizeCSSRule(child)
	}
}

func (san *sanitizer) sanitizeNode(n *html.Node) {
	if n.Type == html.TextNode && san.text.Len() < textCap && !hiddenText(n.Parent) {
		san.text.WriteString(n.Data)
		san.text.WriteByte(' ')
	}
	if n.Type == html.ElementNode {
		if strings.EqualFold(n.Data, "img") {
			for i := range n.Attr {
				attr := &n.Attr[i]
				if strings.EqualFold(attr.Key, "src") {
					attr.Val = san.sanitizeImageURL(attr.Val)
				}
			}
		} else if strings.EqualFold(n.Data, "style") {
			var s string
			c := n.FirstChild
			for c != nil {
				if c.Type == html.TextNode {
					s += c.Data
				}

				next := c.NextSibling
				n.RemoveChild(c)
				c = next
			}

			stylesheet, err := cssparser.Parse(s)
			if err != nil {
				s = ""
			} else {
				for _, rule := range stylesheet.Rules {
					san.sanitizeCSSRule(rule)
				}

				s = stylesheet.String()
			}

			n.AppendChild(&html.Node{
				Type: html.TextNode,
				Data: s,
			})
		}

		for i := range n.Attr {
			// Don't use `i, attr := range n.Attr` since `attr` would be a copy
			attr := &n.Attr[i]

			// A background is an image, and resolves as one.
			if strings.EqualFold(attr.Key, "background") {
				attr.Val = san.sanitizeImageURL(attr.Val)
			}
			if strings.EqualFold(attr.Key, "style") {
				// douceur reads an unterminated last value as empty and rejects ";;".
				value := strings.TrimSpace(attr.Val)
				if value != "" && !strings.HasSuffix(value, ";") {
					value += ";"
				}
				decls, err := cssparser.ParseDeclarations(value)
				if err != nil {
					attr.Val = ""
					continue
				}

				decls = san.sanitizeCSSDecls(decls)

				attr.Val = ""
				for _, d := range decls {
					attr.Val += d.String()
				}
			}
		}
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		san.sanitizeNode(c)
	}
}

func (san *sanitizer) sanitizeHTML(b []byte) ([]byte, error) {
	doc, err := html.Parse(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML: %v", err)
	}

	san.sanitizeNode(doc)

	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil {
		return nil, fmt.Errorf("failed to render HTML: %v", err)
	}
	b = buf.Bytes()

	// bluemonday must always be run last
	return incomingPolicy().SanitizeBytes(b), nil
}

// incomingPolicy names all an incoming message keeps: the markup mail is laid out with, and nothing
// that can run, fetch or address what lies outside the message (ADR 44).
func incomingPolicy() *bluemonday.Policy {
	var (
		colour = regexp.MustCompile(`^(?:#?[0-9a-fA-F]{3,8}|[a-zA-Z]+)$`)
		length = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?(?:%|px)?$`)
		flag   = func(name string) *regexp.Regexp { return regexp.MustCompile(`(?i)^(?:|` + name + `)$`) }
		// A relative URL resolves against alborz, not the message, so links and citations are absolute.
		link = regexp.MustCompile(`(?i)^\s*(?:https?|mailto):`)
		// Images are written by sanitizeImageURL as paths on this server: a part of the message or the proxy.
		image = regexp.MustCompile(`^/[^/\\]`)
	)
	p := bluemonday.NewPolicy()

	p.AllowElements(
		"article", "aside", "details", "summary", "figure", "figcaption", "section", "hgroup",
		"h1", "h2", "h3", "h4", "h5", "h6", "p", "div", "span", "br", "wbr", "hr", "blockquote", "pre", "center",
		"a", "map", "area", "img",
		"abbr", "acronym", "cite", "code", "dfn", "em", "mark", "q", "s", "samp", "strong", "sub", "sup", "var", "time",
		"b", "i", "u", "strike", "tt", "big", "small", "font",
		"bdi", "bdo", "ruby", "rp", "rt", "del", "ins",
		"ul", "ol", "li", "dl", "dt", "dd",
		"table", "caption", "colgroup", "col", "thead", "tbody", "tfoot", "tr", "td", "th",
		"meter", "progress",
		"style",
	)
	// bluemonday drops an element left without attributes unless it is on its own list, which lacks big.
	p.AllowNoAttrs().OnElements("big")

	// A style sheet runs no script and allowedStyles has already emptied its url()s; bluemonday
	// lets the element through only with AllowUnsafe.
	p.AllowUnsafe(true)
	p.AllowElementsContent("style")
	// id and class reach only the message's own style sheet.
	p.AllowAttrs("style", "class", "id").Globally()
	p.AllowAttrs("dir").Matching(bluemonday.Direction).Globally()
	p.AllowAttrs("lang").Matching(regexp.MustCompile(`^[a-zA-Z]{1,8}(?:[-_][a-zA-Z0-9]{1,8})*$`)).Globally()
	p.AllowAttrs("title").Matching(bluemonday.Paragraph).Globally()

	p.RequireParseableURLs(true)
	p.AllowURLSchemes("http", "https", "mailto")
	p.AllowRelativeURLs(true)
	p.RequireNoFollowOnLinks(true)
	p.RequireNoReferrerOnLinks(true)
	p.AddTargetBlankToFullyQualifiedLinks(true)
	p.AllowAttrs("href").Matching(link).OnElements("a", "area")
	p.AllowAttrs("cite").Matching(link).OnElements("blockquote", "q", "del", "ins")

	p.AllowAttrs("src").Matching(image).OnElements("img")
	p.AllowAttrs("background").Matching(image).OnElements("table", "tr", "td", "th")
	p.AllowAttrs("alt").Matching(bluemonday.Paragraph).OnElements("img", "area")
	p.AllowAttrs("align").Matching(bluemonday.ImageAlign).OnElements("img")
	p.AllowAttrs("width", "height").Matching(length).OnElements("img")
	p.AllowAttrs("border").Matching(bluemonday.Integer).OnElements("img", "table")
	p.AllowAttrs("usemap").Matching(regexp.MustCompile(`^#[\p{L}\p{N}_-]+$`)).OnElements("img")
	p.AllowAttrs("name").Matching(regexp.MustCompile(`^[\p{L}\p{N}_-]+$`)).OnElements("map")
	p.AllowAttrs("coords").Matching(regexp.MustCompile(`^(?:[0-9]+,)+[0-9]+$`)).OnElements("area")
	p.AllowAttrs("shape").Matching(regexp.MustCompile(`(?i)^(?:default|circle|rect|poly)$`)).OnElements("area")
	p.AllowAttrs("rel").Matching(bluemonday.SpaceSeparatedTokens).OnElements("area")

	p.AllowAttrs("color").Matching(colour).OnElements("font", "hr")
	p.AllowAttrs("size").Matching(regexp.MustCompile(`^[+-]?[1-7]$`)).OnElements("font")
	p.AllowAttrs("face").Matching(regexp.MustCompile(`^[\p{L}\p{N}\s,'"_-]+$`)).OnElements("font")
	p.AllowAttrs("align").Matching(bluemonday.CellAlign).OnElements("div", "p", "h1", "h2", "h3", "h4", "h5", "h6", "caption", "hr")
	p.AllowAttrs("size").Matching(bluemonday.Integer).OnElements("hr")
	p.AllowAttrs("width").Matching(length).OnElements("hr")
	p.AllowAttrs("noshade").Matching(flag("noshade")).OnElements("hr")

	p.AllowAttrs("align").Matching(bluemonday.CellAlign).OnElements("table", "colgroup", "col", "thead", "tbody", "tfoot", "tr", "td", "th")
	p.AllowAttrs("valign").Matching(bluemonday.CellVerticalAlign).OnElements("colgroup", "col", "thead", "tbody", "tfoot", "tr", "td", "th")
	p.AllowAttrs("bgcolor").Matching(colour).OnElements("table", "tr", "td", "th")
	p.AllowAttrs("width", "height").Matching(length).OnElements("table", "colgroup", "col", "tr", "td", "th")
	p.AllowAttrs("cellpadding", "cellspacing").Matching(bluemonday.Integer).OnElements("table")
	p.AllowAttrs("summary").Matching(bluemonday.Paragraph).OnElements("table")
	p.AllowAttrs("span").Matching(bluemonday.Integer).OnElements("colgroup", "col")
	p.AllowAttrs("colspan", "rowspan").Matching(bluemonday.Integer).OnElements("td", "th")
	p.AllowAttrs("nowrap").Matching(flag("nowrap")).OnElements("td", "th")
	p.AllowAttrs("abbr").Matching(bluemonday.Paragraph).OnElements("td", "th")
	p.AllowAttrs("headers").Matching(bluemonday.SpaceSeparatedTokens).OnElements("td", "th")
	p.AllowAttrs("scope").Matching(regexp.MustCompile(`(?i)^(?:row|col)(?:group)?$`)).OnElements("td", "th")

	p.AllowAttrs("open").Matching(flag("open")).OnElements("details")
	p.AllowAttrs("datetime").Matching(bluemonday.ISO8601).OnElements("time", "del", "ins")
	p.AllowAttrs("type").Matching(bluemonday.ListType).OnElements("ol", "ul", "li")
	p.AllowAttrs("value").Matching(bluemonday.Integer).OnElements("li")
	p.AllowAttrs("value", "min", "max", "low", "high", "optimum").Matching(bluemonday.Number).OnElements("meter")
	p.AllowAttrs("value", "max").Matching(bluemonday.Number).OnElements("progress")
	return p
}
