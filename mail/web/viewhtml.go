package web

import (
	"bytes"
	_ "embed"
	"fmt"
	"html/template"
	"io"
	"strings"

	"git.mehdix.org/alborz/internal/lang"
	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-message"
)

//go:embed viewhtml.html
var htmlTplSrc string
var htmlTpl = template.Must(template.New("view-html.html").Parse(htmlTplSrc))

// An untitled iframe is announced only as "frame".
type frameData struct {
	Title  string
	Body   string
	Script string
	Style  string
}

func viewHTML(ctx *web.Context, msg *mail.IMAPMessage, part *message.Entity) (View, error) {
	allowRemoteResources := ctx.QueryParam("allow-remote-resources") == "1"

	mimeType, _, err := part.Header.ContentType()
	if err != nil {
		return View{}, err
	}
	if !strings.EqualFold(mimeType, "text/html") {
		return View{}, ErrViewUnsupported
	}

	body, err := io.ReadAll(part.Body)
	if err != nil {
		return View{}, fmt.Errorf("failed to read part body: %v", err)
	}

	san := sanitizer{
		msg:                  msg,
		account:              ctx.URLAccount(),
		raw:                  ctx.Path("mail.message.raw", msg.Mailbox, msg.UID),
		proxy:                ctx.Path("mail.proxy"),
		allowRemoteResources: allowRemoteResources,
	}
	body, err = san.sanitizeHTML(body)
	if err != nil {
		return View{}, fmt.Errorf("failed to sanitize HTML part: %v", err)
	}

	// Mail sizes images for its own layout; long pre lines wrap instead of widening the frame.
	body = append([]byte(`<style>img { max-width: 100%; height: auto; } pre { white-space: pre-wrap; overflow-wrap: anywhere; }</style>`), body...)

	// srcdoc is its own document, so lang on the iframe does not reach its text.
	if lang := lang.OfMessage(san.text.String(), ctx.PageLanguage()); lang != "" {
		body = append([]byte(`<div lang="`+template.HTMLEscapeString(lang)+`">`), body...)
		body = append(body, []byte(`</div>`)...)
	}

	var buf bytes.Buffer
	err = htmlTpl.Execute(&buf, frameData{
		Title:  ctx.T("a11y.body"),
		Body:   string(body),
		Script: ctx.Server.AssetURL("viewhtml.js"),
		Style:  ctx.Server.AssetURL("viewhtml.css"),
	})
	if err != nil {
		return View{}, err
	}

	return View{Body: template.HTML(buf.String()), Remote: san.hasRemoteResources}, nil
}
