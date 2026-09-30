package mail

import (
	"context"
	"errors"
	"fmt"
	"html"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// The images a message written here shows in its HTML (ADR 40).

var imageSrc = regexp.MustCompile(`(?i)(<img\b[^>]*?\bsrc=")([^"]*)(")`)

// Raster formats alone: an SVG is a document that can script.
var shownTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
}

type inlineImage struct {
	Src       string
	ContentID string
	Attachment
}

// raw is where the message's parts are fetched.
func localImages(body, raw string, msg *IMAPMessage, account string) string {
	return imageSrc.ReplaceAllStringFunc(body, func(tag string) string {
		m := imageSrc.FindStringSubmatch(tag)
		src := html.UnescapeString(m[2])
		var part *IMAPPartNode
		switch {
		case strings.HasPrefix(strings.ToLower(src), "cid:"):
			part = msg.PartByID(src[len("cid:"):])
		case strings.HasPrefix(src, "part:"):
			if path, err := ParsePartPath(src[len("part:"):]); err == nil {
				part = msg.PartByPath(path)
			}
		}
		if part == nil || !strings.HasPrefix(part.MIMEType, "image/") {
			return tag
		}
		return m[1] + html.EscapeString(raw+part.Query(account)) + m[3]
	})
}

// inlineImages are the images an HTML body shows, each once, as parts to send with it; open reads what a src names.
func inlineImages(body, messageID string, open func(src string) (Attachment, error)) ([]inlineImage, error) {
	var images []inlineImage
	seen := map[string]bool{}
	for _, m := range imageSrc.FindAllStringSubmatch(body, -1) {
		src := html.UnescapeString(m[2])
		if seen[src] {
			continue
		}
		seen[src] = true
		att, err := open(src)
		if err != nil {
			return nil, err
		}
		images = append(images, inlineImage{
			Src:        src,
			ContentID:  fmt.Sprintf("%d.%s", len(images)+1, strings.Trim(messageID, "<>")),
			Attachment: att,
		})
	}
	return images, nil
}

// partImage is an image part of a message, to be sent again as it is.
func partImage(ctx context.Context, login *alborz.Login, mboxName string, uid imap.UID, path []int) (*imapAttachment, error) {
	var part *imap.BodyStructureSinglePart
	err := login.DoIMAP(func(c *imapclient.Client) error {
		var err error
		part, err = singlePart(c, mboxName, uid, path)
		return err
	})
	if err != nil {
		return nil, err
	}
	node := newIMAPPartNode(nil, path, part)
	if !strings.HasPrefix(node.MIMEType, "image/") {
		return nil, errNotImage
	}
	return &imapAttachment{
		Mailbox:  mboxName,
		Uid:      uid,
		Node:     node,
		Encoding: part.Encoding,
		transfer: transferOf(ctx, login),
	}, nil
}

var errNotImage = errors.New("not an image")

func cidImages(body string, images []inlineImage) string {
	cids := map[string]string{}
	for _, im := range images {
		cids[im.Src] = im.ContentID
	}
	return imageSrc.ReplaceAllStringFunc(body, func(tag string) string {
		m := imageSrc.FindStringSubmatch(tag)
		if cid, ok := cids[html.UnescapeString(m[2])]; ok {
			return m[1] + "cid:" + cid + m[3]
		}
		return tag
	})
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
			return &formAttachment{FileHeader: upload.File, ID: id}, nil
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
			image, err := partImage(ctx.Request().Context(), login, values["mbox"], uid, path)
			if errors.Is(err, errNotImage) {
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
