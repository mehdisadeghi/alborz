package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/web"
)

// handleShare opens compose with what another app shared (Web Share Target): the title as the subject,
// the text and link as the body, the files attached. The files are read into sealed spools (ADR 65).
func handleShare(ctx *web.Context) error {
	reader, err := ctx.Request().MultipartReader()
	if err != nil {
		return web.NewError(http.StatusBadRequest, ctx.T("attach.invalid"))
	}
	form, err := web.ReadForm(reader)
	var over *http.MaxBytesError
	if errors.As(err, &over) {
		return web.NewError(http.StatusRequestEntityTooLarge, ctx.T("attach.toolarge"))
	} else if err != nil {
		return web.NewError(http.StatusBadRequest, ctx.T("attach.invalid"))
	}
	files := form.File["files"]
	delete(form.File, "files")
	defer form.Close()

	q := url.Values{"subject": {form.Value.Get("title")}}
	var body []string
	for _, part := range []string{form.Value.Get("text"), form.Value.Get("url")} {
		if part = strings.TrimSpace(part); part != "" {
			body = append(body, part)
		}
	}
	if len(body) > 0 {
		q.Set("body", strings.Join(body, "\n\n"))
	}
	err = putAttachments(ctx, files, func(id string) { q.Add("upload", id) })
	if err == web.ErrAttachmentCacheSize {
		return web.NewError(http.StatusRequestEntityTooLarge, ctx.T("attach.toolarge"))
	} else if err != nil {
		return err
	}
	return ctx.QuietRedirect(web.QueryPath(ctx.Path("mail.compose"), q))
}

func putAttachments(ctx *web.Context, files []*web.File, add func(string)) error {
	for i, f := range files {
		id, err := ctx.PutAttachment(f)
		if err != nil {
			for _, f := range files[i:] {
				f.Close()
			}
			return err
		}
		add(id)
	}
	return nil
}

// uploaded are the session's uploads the query names, attached as compose shows its uploads.
func uploaded(ctx *web.Context) []mail.Attachment {
	var out []mail.Attachment
	for _, id := range ctx.QueryParams()["upload"] {
		if f := ctx.PeekAttachment(id); f != nil {
			out = append(out, formAttachment(f, id))
		}
	}
	return out
}
