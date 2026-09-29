package mail

import (
	"fmt"
	"html"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// The images a message written here shows in its HTML (ADR 40).

var localImage = regexp.MustCompile(`^/compose/attachment/[0-9a-f-]+$|^/message/[^?]+/[0-9]+/raw\?(account=[^&]+&)?part=[0-9.]+$`)

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
			if path, err := parsePartPath(src[len("part:"):]); err == nil {
				part = msg.PartByPath(path)
			}
		}
		if part == nil || !strings.HasPrefix(part.MIMEType, "image/") {
			return tag
		}
		return m[1] + html.EscapeString(raw+part.Query(account)) + m[3]
	})
}

// The session keeps the named uploads until the message has left.
func inlineImages(ctx *web.Context, body, messageID string) ([]inlineImage, []string, error) {
	var (
		images  []inlineImage
		uploads []string
	)
	seen := map[string]bool{}
	for _, m := range imageSrc.FindAllStringSubmatch(body, -1) {
		src := html.UnescapeString(m[2])
		if seen[src] {
			continue
		}
		seen[src] = true
		var att Attachment
		if id, ok := strings.CutPrefix(src, "/compose/attachment/"); ok {
			held := ctx.PeekAttachment(id)
			if held == nil {
				return nil, nil, fmt.Errorf("the image %s is no longer held", id)
			}
			att = &formAttachment{FileHeader: held.File, ID: id}
			uploads = append(uploads, id)
		} else {
			image, err := fetchPartImage(ctx, src)
			if err != nil {
				return nil, nil, err
			}
			att = image
		}
		images = append(images, inlineImage{
			Src:        src,
			ContentID:  fmt.Sprintf("%d.%s", len(images)+1, strings.Trim(messageID, "<>")),
			Attachment: att,
		})
	}
	return images, uploads, nil
}

func fetchPartImage(ctx *web.Context, src string) (*imapAttachment, error) {
	u, err := url.Parse(src)
	if err != nil {
		return nil, err
	}
	segments := strings.Split(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	if len(segments) != 4 || segments[0] != "message" || segments[3] != "raw" {
		return nil, fmt.Errorf("not an image reference: %s", src)
	}
	mboxName, uid, err := parseMboxAndUid(segments[1], segments[2])
	if err != nil {
		return nil, err
	}
	path, err := parsePartPath(u.Query().Get("part"))
	if err != nil {
		return nil, err
	}
	login := ctx.Login
	if account := u.Query().Get("account"); account != "" {
		if login = ctx.LoginFor(account); login == nil {
			return nil, fmt.Errorf("not signed in to %s", account)
		}
	}
	var part *imap.BodyStructureSinglePart
	err = login.DoIMAP(func(c *imapclient.Client) error {
		var err error
		part, err = singlePart(c, mboxName, uid, path)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("failed to fetch image %s: %w", src, err)
	}
	node := newIMAPPartNode(nil, path, part)
	if !strings.HasPrefix(node.MIMEType, "image/") {
		return nil, fmt.Errorf("%s is not an image", src)
	}
	return &imapAttachment{
		Mailbox:  mboxName,
		Uid:      uid,
		Node:     node,
		Encoding: part.Encoding,
		transfer: transferOf(ctx, login),
	}, nil
}

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
