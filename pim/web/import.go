package web

import (
	"io"
	"net/http"

	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/pim"
	rootweb "git.mehdix.org/alborz/web"
	"github.com/emersion/go-imap/v2"
)

// ReadImportPart reads and bounds an import attachment from a message.
func ReadImportPart(ctx *rootweb.Context, mailbox string, uid imap.UID, part []int) ([]byte, error) {
	var raw []byte
	err := mail.PartAt(ctx.Request().Context(), ctx.Login, mailbox, uid, part, func(r io.Reader) error {
		var err error
		raw, err = pim.ReadImport(r)
		return err
	})
	return raw, err
}

// ImportAttachment reads an import attachment, writes it, and reports the result uniformly.
func ImportAttachment(ctx *rootweb.Context, mailbox string, uid imap.UID, part []int, key, to string, put func([]byte) (int, error)) error {
	raw, err := ReadImportPart(ctx, mailbox, uid, part)
	if err != nil {
		return ImportRedirect(ctx, err, 0, key, to)
	}
	n, err := put(raw)
	return ImportRedirect(ctx, err, n, key, to)
}

// ImportMessagePart parses the common message attachment form and dispatches it to a section importer.
func ImportMessagePart(ctx *rootweb.Context, key, to string, put func([]byte) (int, error)) error {
	mailbox, uid, err := mail.ParseMessageRef(ctx.FormValue("mbox"), ctx.FormValue("uid"))
	if err != nil {
		return err
	}
	part, err := mail.ParsePartPath(ctx.FormValue("part"))
	if err != nil {
		return rootweb.NewError(http.StatusBadRequest, err)
	}
	return ImportAttachment(ctx, mailbox, uid, part, key, to, put)
}
