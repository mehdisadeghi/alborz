package alborzbase

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/labstack/echo/v4"
)

// handleExportMbox writes the selected messages as one mbox, which is
// the container every mail client reads and the only one that holds
// more than a single message without inventing a layout.
//
// It streams: a folder does not fit in memory, and the reader should
// see the file start rather than a spinner.
func handleExportMbox(ctx *alborz.Context) error {
	mboxName, err := mailboxRef(ctx)
	if err != nil {
		return err
	}
	params, err := ctx.FormParams()
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}
	uids, err := parseUidList(params["uids"])
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}
	if len(uids) == 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "no messages selected")
	}

	// The header is written only once a message is actually in hand.
	// Committing the response first meant that a fetch which failed had
	// the error page rendered into a body already claiming to be an
	// mbox, and the reader was handed an .mbox file full of HTML.
	res := ctx.Response()
	started := false
	for _, uid := range uids {
		// One round trip per message, each with the session's own
		// budget. The whole export inside one call was cut off by the
		// watchdog after ten seconds, with the file left short.
		var raw []byte
		var env *imap.Envelope
		err := ctx.DoIMAP(func(c *imapclient.Client) error {
			var err error
			raw, env, err = fetchRawMessage(c, mboxName, uid)
			return err
		})
		if err != nil {
			if started {
				// Too late to say so in the response: stop, leave
				// the file short, and put the reason in the log
				// rather than in the download.
				ctx.Logger().Printf("export %q uid %v: %v", mboxName, uid, err)
				return nil
			}
			return fmt.Errorf("export %q uid %v: %w", mboxName, uid, err)
		}
		if !started {
			res.Header().Set("Content-Disposition", downloadName(mboxName, "messages", ".mbox"))
			res.Header().Set("Content-Type", "application/mbox")
			res.WriteHeader(http.StatusOK)
			started = true
		}
		if err := writeMbox(res, raw, env); err != nil {
			ctx.Logger().Printf("export %q uid %v: %v", mboxName, uid, err)
			return nil
		}
		res.Flush()
	}
	return nil
}

// mboxSeparator opens each message in an mbox file. The address and the
// date are the envelope sender and delivery time in the original
// format's own shape; nothing reads them, but a file without them is
// not an mbox.
func mboxSeparator(env *imap.Envelope) string {
	from := "alborz"
	if env != nil && len(env.From) > 0 {
		if addr := env.From[0].Addr(); addr != "" {
			from = addr
		}
	}
	when := time.Now()
	if env != nil && !env.Date.IsZero() {
		when = env.Date
	}
	return "From " + from + " " + when.UTC().Format(time.ANSIC) + "\r\n"
}

// writeMbox appends one message in mboxrd form. A line that would be
// read as the next message's separator is quoted with a ">", and one
// already quoted gains another, which is what makes the escaping
// reversible.
func writeMbox(w io.Writer, raw []byte, env *imap.Envelope) error {
	if _, err := io.WriteString(w, mboxSeparator(env)); err != nil {
		return err
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), maxMboxLine)
	for scanner.Scan() {
		line := scanner.Bytes()
		trimmed := bytes.TrimLeft(line, ">")
		if bytes.HasPrefix(trimmed, []byte("From ")) {
			if _, err := w.Write([]byte(">")); err != nil {
				return err
			}
		}
		if _, err := w.Write(line); err != nil {
			return err
		}
		if _, err := io.WriteString(w, "\r\n"); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\r\n")
	return err
}

// downloadName offers a filename a reader will recognise, in both the
// plain and the encoded form RFC 6266 asks for: the plain one is
// stripped to ASCII for clients that read only that.
func downloadName(name, fallback, ext string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r < 0x20, r == '"', r == '\\', r == '/', r == 0x7f:
			return -1
		}
		return r
	}, name)
	cleaned = strings.TrimSpace(cleaned)
	if len([]rune(cleaned)) > maxDownloadName {
		cleaned = string([]rune(cleaned)[:maxDownloadName])
	}
	if cleaned == "" {
		cleaned = fallback
	}
	cleaned += ext
	ascii := strings.Map(func(r rune) rune {
		if r > 0x7f {
			return '_'
		}
		return r
	}, cleaned)
	return mime.FormatMediaType("attachment", map[string]string{"filename": ascii}) +
		"; filename*=UTF-8''" + url.PathEscape(cleaned)
}
