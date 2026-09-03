package dav

import (
	"context"
	"io"
	"mime"
	"net/http"
	"path"

	"git.mehdix.org/alborz"
)

// files is what every go-webdav client can do with an object's path,
// through the WebDAV client it embeds; the clients themselves share no
// type to name it by.
type files interface {
	Open(ctx context.Context, name string) (io.ReadCloser, error)
	RemoveAll(ctx context.Context, name string) error
}

// exportTypes are the media types the files handed over are sent as.
var exportTypes = map[string]string{
	".ics": "text/calendar; charset=utf-8",
	".vcf": "text/vcard; charset=utf-8",
}

func attach(ctx *alborz.Context, name string) {
	ctx.Response().Header().Set("Content-Disposition",
		mime.FormatMediaType("attachment", map[string]string{"filename": name}))
}

// Download answers with a file to keep, of the kind its name ends in.
func Download(ctx *alborz.Context, name string, body []byte) error {
	attach(ctx, name)
	return ctx.Blob(http.StatusOK, exportTypes[path.Ext(name)], body)
}

// ServeRaw streams an object exactly as it is stored. Nothing parses
// it: a raw view is only useful while it is verbatim. Saved, it is the
// file other clients open; read, it is text.
func ServeRaw(ctx *alborz.Context, name string, body io.Reader) error {
	if ctx.QueryParam("save") == "1" {
		attach(ctx, name)
		return ctx.Stream(http.StatusOK, exportTypes[path.Ext(name)], body)
	}
	return ctx.Stream(http.StatusOK, "text/plain; charset=utf-8", body)
}

// Raw is ServeRaw for the object the route names.
func Raw[C files](ctx *alborz.Context, client func(*alborz.Session) (C, error)) error {
	refs, err := Selection(ctx, client)
	if err != nil {
		return err
	}
	body, err := refs[0].Client.Open(ctx.Request().Context(), refs[0].Path)
	if err != nil {
		return err
	}
	defer body.Close()
	return ServeRaw(ctx, path.Base(refs[0].Path), body)
}

// HandleExport hands the selection back as one file, each object as its
// server stores it and join making the file of them: the rows name
// their own account, so the selection can span accounts. An empty
// selection lands on list.
func HandleExport[C files](client func(*alborz.Session) (C, error), list string, name func(*alborz.Context) string, join func([][]byte) ([]byte, error)) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		refs, err := Selection(ctx, client)
		if err != nil {
			return err
		}
		if len(refs) == 0 {
			return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.AccountPath(list)))
		}
		var objects [][]byte
		for _, r := range Each(ctx.Request().Context(), refs, func(ctx context.Context, ref Ref[C]) ([]byte, error) {
			body, err := ref.Client.Open(ctx, ref.Path)
			if err != nil {
				return nil, err
			}
			defer body.Close()
			return io.ReadAll(body)
		}) {
			if r.Err != nil {
				return r.Err
			}
			objects = append(objects, r.Value)
		}
		file, err := join(objects)
		if err != nil {
			return err
		}
		return Download(ctx, name(ctx), file)
	}
}
