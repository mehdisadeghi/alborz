package alborzviewtext

import (
	"bufio"
	"fmt"
	"html/template"
	"io"
	"net/url"
	"strings"

	"git.mehdix.org/alborz"
	alborzbase "git.mehdix.org/alborz/plugins/base"
	"github.com/emersion/go-message"
	"gitlab.com/golang-commonmark/linkify"
)

// TODO: dim quotes and "On xxx, xxx wrote:" lines

const (
	tplStr     = `<pre dir="auto">{{range .Tokens}}{{.}}{{end}}{{range .Images}}<img class="shown-image" src="{{.Src}}" alt="{{.Alt}}">{{end}}</pre>`
	linkTplStr = `<a href="{{.Href}}" target="_blank" rel="nofollow noopener">{{.Text}}</a>`
)

var tpl *template.Template

func init() {
	tpl = template.Must(template.New("view-text.html").Parse(tplStr))
	template.Must(tpl.New("view-text-link.html").Parse(linkTplStr))
}

type textRenderData struct {
	Tokens []interface{}
	Images []imageRenderData
}

type imageRenderData struct {
	Src string
	Alt string
}

type linkRenderData struct {
	Href string
	Text string
}

var allowedSchemes = map[string]bool{
	"http":   true,
	"https":  true,
	"mailto": true,
	"ftp":    true,
	"sftp":   true,
	"ftps":   true,
	"tel":    true,
	"irc":    true,
	"ircs":   true,
}

func executeTemplate(name string, data interface{}) (template.HTML, error) {
	var sb strings.Builder
	err := tpl.ExecuteTemplate(&sb, name, data)
	if err != nil {
		return "", err
	}
	return template.HTML(sb.String()), nil
}

type viewer struct{}

func (viewer) ViewMessagePart(ctx *alborz.Context, msg *alborzbase.IMAPMessage, path []int, part *message.Entity) (interface{}, error) {
	mimeType, params, err := part.Header.ContentType()
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(mimeType, "text/plain") {
		return nil, alborzbase.ErrViewUnsupported
	}

	// Language is decided per paragraph: per line the voice would stutter.
	page := ctx.PageLanguage()
	var tokens, para []interface{}
	var text strings.Builder
	flush := func() {
		if len(para) == 0 {
			return
		}
		if lang := alborz.MessageLang(text.String(), page); lang != "" {
			tokens = append(tokens, template.HTML(`<span lang="`+template.HTMLEscapeString(lang)+`">`))
			tokens = append(tokens, para...)
			tokens = append(tokens, template.HTML(`</span>`))
		} else {
			tokens = append(tokens, para...)
		}
		para = para[:0]
		text.Reset()
	}

	body := io.Reader(part.Body)
	if strings.EqualFold(params["format"], "flowed") {
		raw, err := io.ReadAll(part.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read part body: %v", err)
		}
		body = strings.NewReader(alborzbase.Unflow(string(raw), strings.EqualFold(params["delsp"], "yes")))
	}
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		l := scanner.Text()
		if strings.TrimSpace(l) == "" {
			flush()
			tokens = append(tokens, "\n")
			continue
		}
		text.WriteString(l)
		text.WriteByte(' ')

		i := 0
		for _, link := range linkify.Links(l) {
			href := l[link.Start:link.End]
			if link.Scheme == "" {
				href = "https://" + href
			} else if !strings.HasPrefix(href, link.Scheme) {
				href = link.Scheme + href
			}

			u, err := url.Parse(href)
			if err != nil {
				continue
			}

			if !allowedSchemes[u.Scheme] {
				continue
			}

			if i < link.Start {
				para = append(para, l[i:link.Start])
			}
			tok, err := executeTemplate("view-text-link.html", linkRenderData{
				Href: href,
				Text: l[link.Start:link.End],
			})
			if err != nil {
				return nil, err
			}
			para = append(para, tok)
			i = link.End
		}
		if i < len(l) {
			para = append(para, l[i:])
		}

		para = append(para, "\n")
	}
	flush()
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read part body: %v", err)
	}

	data := textRenderData{Tokens: tokens}
	for _, image := range msg.ShownImages(path) {
		data.Images = append(data.Images, imageRenderData{Src: image.URL(true, ctx.URLAccount()).String(), Alt: image.Filename})
	}
	return executeTemplate("view-text.html", data)
}

func init() {
	alborzbase.RegisterViewer(viewer{})
}
