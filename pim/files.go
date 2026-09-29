package pim

import (
	"context"
	"io"
	"mime"
	"net/http"
	"path"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim/dav"
	"git.mehdix.org/alborz/web"
)

// files is what go-webdav clients share through their embedded WebDAV client; they have no common type.
type files interface {
	Open(ctx context.Context, name string) (io.ReadCloser, error)
	RemoveAll(ctx context.Context, name string) error
}

var exportTypes = map[string]string{
	".ics": "text/calendar; charset=utf-8",
	".vcf": "text/vcard; charset=utf-8",
	".zip": "application/zip",
}

func attach(ctx *web.Context, name string) {
	ctx.Response().Header().Set("Content-Disposition",
		mime.FormatMediaType("attachment", map[string]string{"filename": name}))
}

// Download answers with a file to keep, of the kind its name ends in.
func Download(ctx *web.Context, name string, body []byte) error {
	attach(ctx, name)
	return ctx.Blob(http.StatusOK, exportTypes[path.Ext(name)], body)
}

// ServeRaw streams an object verbatim; a raw view is only useful unparsed.
func ServeRaw(ctx *web.Context, name string, body io.Reader) error {
	if ctx.QueryParam("save") == "1" {
		attach(ctx, name)
		return ctx.Stream(http.StatusOK, exportTypes[path.Ext(name)], body)
	}
	return ctx.Stream(http.StatusOK, "text/plain; charset=utf-8", body)
}

// Raw is ServeRaw for the object the route names.
func Raw[C files](ctx *web.Context, client func(context.Context, *alborz.Login) (C, error)) error {
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

// HandleExport joins the selection into one file; rows name their account, so it may span accounts.
func HandleExport[C files](client func(context.Context, *alborz.Login) (C, error), list string, name func(*web.Context) string, join func([][]byte) ([]byte, error)) func(*web.Context) error {
	return func(ctx *web.Context) error {
		refs, err := Selection(ctx, client)
		if err != nil {
			return err
		}
		if len(refs) == 0 {
			return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.AccountPath(list)))
		}
		var objects [][]byte
		for _, r := range dav.Across(ctx.Request().Context(), refs, func(ref Ref[C]) string { return ref.Account }, func(ctx context.Context, ref Ref[C]) ([]byte, error) {
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
