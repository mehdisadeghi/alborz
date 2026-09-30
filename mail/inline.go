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

type inlineImage struct {
	Src       string
	ContentID string
	Attachment
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
