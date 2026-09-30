package mail

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"regexp"

	"git.mehdix.org/alborz/web"
)

// Raster formats alone: an SVG is a document that can script.
var shownTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
}

// Anything but a raster image is offered as a file, so no upload runs as a page here.
func handleGetAttachment(ctx *web.Context) error {
	held := ctx.PeekAttachment(ctx.Param("uuid"))
	if held == nil {
		return web.NewError(http.StatusNotFound)
	}
	f, err := held.File.Open()
	if err != nil {
		return err
	}
	defer f.Close()
	mimeType, _, _ := mime.ParseMediaType(held.File.Header.Get("Content-Type"))
	if !shownTypes[mimeType] {
		mimeType = "application/octet-stream"
		ctx.Response().Header().Set("Content-Disposition",
			mime.FormatMediaType("attachment", map[string]string{"filename": held.File.Filename}))
	}
	return ctx.Stream(http.StatusOK, mimeType, f)
}

// imageSource opens an image the composed HTML shows: an upload the session holds, or a part of a message.
// The session keeps the uploads it names in held until the message has left.
func imageSource(ctx *web.Context, held *[]string) func(src string) (Attachment, error) {
	return func(src string) (Attachment, error) {
		u, err := url.Parse(src)
		if err != nil {
			return nil, err
		}
		route, values, _ := ctx.Server.Match(u)
		switch route {
		case "mail.attachment":
			id := values["uuid"]
			upload := ctx.PeekAttachment(id)
			if upload == nil {
				return nil, fmt.Errorf("the image %s is no longer held", id)
			}
			*held = append(*held, id)
			return &FormAttachment{FileHeader: upload.File, ID: id}, nil
		case "mail.message.raw":
			uid, err := ParseUID(values["uid"])
			if err != nil {
				return nil, err
			}
			path, err := ParsePartPath(u.Query().Get("part"))
			if err != nil {
				return nil, err
			}
			login := ctx.Login
			if account := u.Query().Get("account"); account != "" {
				if login = ctx.LoginFor(account); login == nil {
					return nil, fmt.Errorf("not signed in to %s", account)
				}
			}
			image, err := PartImage(ctx.Request().Context(), login, values["mbox"], uid, path)
			if errors.Is(err, ErrNotImage) {
				return nil, fmt.Errorf("%s is not an image", src)
			}
			if err != nil {
				return nil, fmt.Errorf("failed to fetch image %s: %w", src, err)
			}
			return image, nil
		}
		return nil, fmt.Errorf("not an image reference: %s", src)
	}
}

// localImage matches the images alborz holds (ADR 40): an upload, or a part of a message.
func localImage(s *web.Server) (*regexp.Regexp, error) {
	upload, err := s.PathPattern("mail.attachment", "[0-9a-f-]+")
	if err != nil {
		return nil, err
	}
	part, err := s.PathPattern("mail.message.raw", "[^?]+", "[0-9]+")
	if err != nil {
		return nil, err
	}
	return regexp.Compile("^" + upload + "$|^" + part + `\?(account=[^&]+&)?part=[0-9.]+$`)
}
