package alborzbase

import (
	"bytes"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/labstack/echo/v4"
)

// The images a message written here shows in its HTML (ADR 40).

// localImage is what an image in the editor may name: an upload the
// visit holds, or a part of a message an account holds.
var localImage = regexp.MustCompile(`^/compose/attachment/[0-9a-f-]+$|^/message/[^?]+/[0-9]+/raw\?(account=[^&]+&)?part=[0-9.]+$`)

var imageSrc = regexp.MustCompile(`(?i)(<img\b[^>]*?\bsrc=")([^"]*)(")`)

// shownTypes are the images the editor takes and an upload is shown as:
// raster formats alone, since an SVG is a document that can script.
var shownTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
}

// inlineImage is an image the HTML shows, sent in the multipart/related
// under the Content-ID the HTML then names it by.
type inlineImage struct {
	Src       string
	ContentID string
	Attachment
}

// partImage is an image part of a message, fetched for the send.
type partImage struct {
	mimeType, filename string
	body               []byte
}

func (p *partImage) MIMEType() string { return p.mimeType }
func (p *partImage) Filename() string { return p.filename }
func (p *partImage) Open() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(p.body)), nil
}

// localImages points the images a message's HTML shows at the parts
// that hold them, so the editor can show them and a send can carry
// them: cid: by Content-ID, part: by path (HTMLPieces).
func localImages(body string, msg *IMAPMessage, account string) string {
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
		return m[1] + html.EscapeString(part.URL(true, account).String()) + m[3]
	})
}

// inlineImages resolves every image the HTML shows into what the send
// writes, and names the uploads among them, which the visit keeps until
// the message has left.
func inlineImages(ctx *alborz.Context, body, messageID string) ([]inlineImage, []string, error) {
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

// fetchPartImage reads the image a part reference names, on the
// account the reference names.
func fetchPartImage(ctx *alborz.Context, src string) (*partImage, error) {
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
	session := ctx.Session
	if account := u.Query().Get("account"); account != "" {
		if session = ctx.SessionFor(account); session == nil {
			return nil, fmt.Errorf("not signed in to %s", account)
		}
	}
	var image partImage
	err = session.DoIMAP(func(c *imapclient.Client) error {
		_, part, err := getMessagePart(c, mboxName, uid, path)
		if err != nil {
			return err
		}
		image.mimeType, _, _ = part.Header.ContentType()
		_, dispParams, _ := part.Header.ContentDisposition()
		image.filename = dispParams["filename"]
		image.body, err = io.ReadAll(part.Body)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("failed to fetch image %s: %w", src, err)
	}
	if !strings.HasPrefix(image.mimeType, "image/") {
		return nil, fmt.Errorf("%s is not an image", src)
	}
	return &image, nil
}

// cidImages is the HTML as it is sent: each local reference replaced by
// the Content-ID of its part.
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

// handleGetAttachment shows an upload the visit holds, for the editor
// that placed it. Only a raster image is shown; anything else is
// offered as a file, so no upload runs as a page of this site.
func handleGetAttachment(ctx *alborz.Context) error {
	held := ctx.PeekAttachment(ctx.Param("uuid"))
	if held == nil {
		return echo.NewHTTPError(http.StatusNotFound)
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
