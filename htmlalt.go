package alborz

import (
	"html"
	"strings"
)

// ParagraphDir is the direction one paragraph runs, "ltr" or "rtl". It
// is SubjectDir's question without the reply prefixes, and it never
// answers "auto": the point of writing the direction down is that the
// recipient's client will not work it out.
func ParagraphDir(text string) string {
	if dir := SubjectDir(text); dir != "auto" {
		return dir
	}
	return "ltr"
}

// HTMLAlternative renders plain text as the least HTML that can carry
// its direction.
//
// A plain-text part says nothing about which way it runs, and Gmail and
// Outlook lay a received text/plain part out left to right whatever it
// says - so a Persian paragraph arrives mangled and the format has no
// way to correct them. HTML has dir, and it is the only thing those
// clients obey.
//
// So this adds direction and nothing else: no styles, no fonts, no
// divs for their own sake. Each paragraph is asked which way it runs by
// the Unicode Bidi Algorithm's own rule - a paragraph runs the way its
// first strong character runs - and a run of quoted lines becomes a
// blockquote whose paragraphs are asked the same question, since a
// quote often runs the other way from the reply around it.
func HTMLAlternative(text string) string {
	var out strings.Builder
	out.WriteString("<html><body>\n")
	for _, block := range blocks(text) {
		if block.quoted {
			out.WriteString("<blockquote>\n")
			for _, p := range paragraphs(block.lines) {
				writeParagraph(&out, p)
			}
			out.WriteString("</blockquote>\n")
			continue
		}
		for _, p := range paragraphs(block.lines) {
			writeParagraph(&out, p)
		}
	}
	out.WriteString("</body></html>\n")
	return out.String()
}

func writeParagraph(out *strings.Builder, lines []string) {
	joined := strings.Join(lines, "\n")
	out.WriteString(`<p dir="`)
	out.WriteString(ParagraphDir(joined))
	out.WriteString(`">`)
	for i, line := range lines {
		if i > 0 {
			// A hard break inside a paragraph is a break the writer
			// typed, and it survives as one.
			out.WriteString("<br>\n")
		}
		out.WriteString(html.EscapeString(line))
	}
	out.WriteString("</p>\n")
}

// block is a run of lines that are all quoted or all not. The quote
// marks are stripped from a quoted run so its own paragraphs can be
// read for direction rather than for the ">" in front of them.
type block struct {
	quoted bool
	lines  []string
}

func blocks(text string) []block {
	var out []block
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		quoted := strings.HasPrefix(line, ">")
		if quoted {
			line = strings.TrimPrefix(strings.TrimPrefix(line, ">"), " ")
		}
		if n := len(out); n > 0 && out[n-1].quoted == quoted {
			out[n-1].lines = append(out[n-1].lines, line)
			continue
		}
		out = append(out, block{quoted: quoted, lines: []string{line}})
	}
	return out
}

// paragraphs splits on blank lines, which is what a blank line means in
// plain text and the unit the bidi algorithm works in.
func paragraphs(lines []string) [][]string {
	var out [][]string
	var cur []string
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			if len(cur) > 0 {
				out = append(out, cur)
				cur = nil
			}
			continue
		}
		cur = append(cur, line)
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}
