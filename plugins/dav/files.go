package dav

import (
	"context"
	"io"
	"net/http"

	"git.mehdix.org/alborz"
)

// files is what every go-webdav client can do with an object's path,
// through the WebDAV client it embeds; the clients themselves share no
// type to name it by.
type files interface {
	Open(ctx context.Context, name string) (io.ReadCloser, error)
	RemoveAll(ctx context.Context, name string) error
}

// Raw streams the object the route names exactly as it is stored.
// Nothing parses it: a raw view is only useful while it is verbatim.
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
	return ctx.Stream(http.StatusOK, "text/plain; charset=utf-8", body)
}
