package mail

import (
	"context"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strings"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// The images a message written here shows in its HTML (ADR 40).

var imageSrc = regexp.MustCompile(`(?i)(<img\b[^>]*?\bsrc=")([^"]*)(")`)

type InlineImage struct {
	Src       string
	ContentID string
	Attachment
}

// InlineImages are the images an HTML body shows, each once, as parts to send with it; open reads what a src names.
func InlineImages(body, messageID string, open func(src string) (Attachment, error)) ([]InlineImage, error) {
	var images []InlineImage
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
		images = append(images, InlineImage{
			Src:        src,
			ContentID:  fmt.Sprintf("%d.%s", len(images)+1, strings.Trim(messageID, "<>")),
			Attachment: att,
		})
	}
	return images, nil
}

// PartImage is an image part of a message, to be sent again as it is.
func PartImage(ctx context.Context, login *alborz.Login, mboxName string, uid imap.UID, path []int) (*IMAPAttachment, error) {
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
		return nil, ErrNotImage
	}
	return &IMAPAttachment{
		Mailbox:  mboxName,
		UID:      uid,
		Node:     node,
		Encoding: part.Encoding,
		transfer: transferOf(ctx, login),
	}, nil
}

// ErrNotImage is a part a composed message shows that is no image.
var ErrNotImage = errors.New("not an image")

func cidImages(body string, images []InlineImage) string {
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

// localImages points the images a body names by cid: or part: at what href makes of the part.
func localImages(body string, msg *IMAPMessage, href func(*IMAPPartNode) string) string {
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
		return m[1] + html.EscapeString(href(part)) + m[3]
	})
}
