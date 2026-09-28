package alborz

import (
	"html"
	"strings"
)

// ParagraphDir returns "ltr" or "rtl" for one paragraph, never "auto".
func ParagraphDir(text string) string {
	if dir := SubjectDir(text); dir != "auto" {
		return dir
	}
	return "ltr"
}

// HTMLAlternative renders plain text as the least HTML that carries each paragraph's direction.
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

// HasRTL reports whether any paragraph of text, quoted or not, runs right to left.
func HasRTL(text string) bool {
	for _, block := range blocks(text) {
		for _, p := range paragraphs(block.lines) {
			if ParagraphDir(strings.Join(p, "\n")) == "rtl" {
				return true
			}
		}
	}
	return false
}

func writeParagraph(out *strings.Builder, lines []string) {
	joined := strings.Join(lines, "\n")
	out.WriteString(`<p dir="`)
	out.WriteString(ParagraphDir(joined))
	out.WriteString(`">`)
	for i, line := range lines {
		if i > 0 {
			out.WriteString("<br>\n")
		}
		out.WriteString(html.EscapeString(line))
	}
	out.WriteString("</p>\n")
}

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
