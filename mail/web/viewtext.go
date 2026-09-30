package web

import (
	"bufio"
	"fmt"
	"html/template"
	"io"
	"net/url"
	"strings"

	"git.mehdix.org/alborz/internal/lang"
	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-message"
	"gitlab.com/golang-commonmark/linkify"
)

// TODO: dim quotes and "On xxx, xxx wrote:" lines

const (
	textTplStr = `<pre dir="auto">{{range .Tokens}}{{.}}{{end}}{{range .Images}}<img class="shown-image" src="{{.Src}}" alt="{{.Alt}}">{{end}}</pre>`
	linkTplStr = `<a href="{{.Href}}" target="_blank" rel="nofollow noopener">{{.Text}}</a>`
)

var textTpl *template.Template

func init() {
	textTpl = template.Must(template.New("view-text.html").Parse(textTplStr))
	template.Must(textTpl.New("view-text-link.html").Parse(linkTplStr))
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
	err := textTpl.ExecuteTemplate(&sb, name, data)
	if err != nil {
		return "", err
	}
	return template.HTML(sb.String()), nil
}

func viewText(ctx *web.Context, msg *mail.IMAPMessage, path []int, part *message.Entity) (View, error) {
	mimeType, params, err := part.Header.ContentType()
	if err != nil {
		return View{}, err
	}
	if !strings.EqualFold(mimeType, "text/plain") {
		return View{}, ErrViewUnsupported
	}

	// Language is decided per paragraph: per line the voice would stutter.
	page := ctx.PageLanguage()
	var tokens, para []interface{}
	var text strings.Builder
	flush := func() {
		if len(para) == 0 {
			return
		}
		if lang := lang.OfMessage(text.String(), page); lang != "" {
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
			return View{}, fmt.Errorf("failed to read part body: %v", err)
		}
		body = strings.NewReader(mail.Unflow(string(raw), strings.EqualFold(params["delsp"], "yes")))
	}
	// No line limit: some mail never breaks its lines.
	lines := bufio.NewReader(body)
	for {
		l, err := lines.ReadString('\n')
		if err != nil && err != io.EOF {
			return View{}, fmt.Errorf("failed to read part body: %v", err)
		}
		if l == "" && err == io.EOF {
			break
		}
		l = strings.TrimSuffix(strings.TrimSuffix(l, "\n"), "\r")
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
				return View{}, err
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

	data := textRenderData{Tokens: tokens}
	for _, image := range msg.ShownImages(path) {
		data.Images = append(data.Images, imageRenderData{Src: ctx.Path("mail.message.raw", msg.Mailbox, msg.UID) + partQuery(image, ctx.URLAccount()), Alt: image.Filename})
	}
	html, err := executeTemplate("view-text.html", data)
	return View{Body: html}, err
}
