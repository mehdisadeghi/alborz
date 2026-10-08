package mail

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/mail"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/pgp"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-mbox"
	"github.com/emersion/go-message"
)

// ExportRun streams one account's run of messages into the archive, on a connection of its own.
func ExportRun(ctx context.Context, log *log.Logger, s *alborz.Login, run []Ref, archive *mbox.Writer, strip bool, note string, begin, flush func()) error {
	return s.DoIMAPTransfer(ctx, func(c *imapclient.Client) error {
		for _, r := range run {
			if err := exportMessage(log, c, archive, r, strip, note, begin); err != nil {
				return fmt.Errorf("export %s %q uid %v: %w", r.Account, r.Mailbox, r.UID, err)
			}
			flush()
		}
		return nil
	})
}

// exportMessage streams one message into the archive; stripped, only its text is held, note standing for what went.
func exportMessage(log *log.Logger, c *imapclient.Client, archive *mbox.Writer, r Ref, strip bool, note string, begin func()) error {
	if err := ensureMailboxSelected(c, r.Mailbox); err != nil {
		return err
	}
	msg, err := fetchOne(c, r.UID, &imap.FetchOptions{UID: true, Envelope: true})
	if err != nil {
		return err
	}
	env := msg.Envelope
	whole := &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{{Peek: true}}}
	var stripped []byte
	if strip {
		err := streamSection(c, r.UID, whole, func(lit imap.LiteralReader) error {
			var err error
			stripped, err = withoutAttachments(lit, note)
			return err
		})
		if err != nil && !errors.Is(err, errKeepWhole) {
			// A message that cannot be taken apart goes out whole: larger than asked, not wrong.
			log.Printf("export %q uid %v: kept attachments: %v", r.Mailbox, r.UID, err)
		}
	}
	if stripped != nil {
		begin()
		return writeMbox(archive, bytes.NewReader(stripped), env)
	}
	return streamSection(c, r.UID, whole, func(lit imap.LiteralReader) error {
		begin()
		return writeMbox(archive, lit, env)
	})
}

// errKeepWhole is a message whose parts cannot change without breaking it.
var errKeepWhole = errors.New("signed or encrypted")

func withoutAttachments(r io.Reader, note string) ([]byte, error) {
	entity, err := message.Read(r)
	if err != nil && !message.IsUnknownCharset(err) {
		return nil, err
	}
	mediaType, _, _ := entity.Header.ContentType()
	if mediaType == "multipart/signed" || mediaType == "multipart/encrypted" {
		return nil, errKeepWhole
	}
	var out bytes.Buffer
	w, err := message.CreateWriter(&out, entity.Header)
	if err != nil {
		return nil, err
	}
	if err := copyWithoutAttachments(w, entity, note); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func copyWithoutAttachments(w *message.Writer, e *message.Entity, note string) error {
	mr := e.MultipartReader()
	if mr == nil {
		_, err := io.Copy(w, e.Body)
		return err
	}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return nil
		}
		if err != nil && !message.IsUnknownCharset(err) {
			return err
		}
		header := part.Header
		mediaType, _, _ := header.ContentType()
		disposition, dispParams, _ := header.ContentDisposition()
		name := dispParams["filename"]
		if name == "" {
			_, params, _ := header.ContentType()
			name = params["name"]
		}
		// Only text stays: an image the HTML shows is weight all the same.
		keep := strings.HasPrefix(mediaType, "multipart/") ||
			(strings.HasPrefix(mediaType, "text/") && !attached(mediaType, disposition, name, "", ""))
		if keep {
			if strings.HasPrefix(mediaType, "text/") {
				_, params, _ := header.ContentType()
				params["charset"] = "utf-8"
				header.SetContentType(mediaType, params)
			}
			pw, err := w.CreatePart(header)
			if err != nil {
				return err
			}
			if err := copyWithoutAttachments(pw, part, note); err != nil {
				return err
			}
			if err := pw.Close(); err != nil {
				return err
			}
			continue
		}
		size, _ := io.Copy(io.Discard, part.Body)
		var stub message.Header
		stub.SetContentType("text/plain", map[string]string{"charset": "utf-8"})
		pw, err := w.CreatePart(stub)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(pw, "[%s: %s, %s, %d B]\r\n", note, name, mediaType, size); err != nil {
			return err
		}
		if err := pw.Close(); err != nil {
			return err
		}
	}
}

// Nothing reads the separator's sender and date, but a file without them is not an mbox.
func writeMbox(archive *mbox.Writer, raw io.Reader, env *imap.Envelope) error {
	var from string
	when := time.Now()
	if env != nil {
		if len(env.From) > 0 {
			from = env.From[0].Addr()
		}
		if !env.Date.IsZero() {
			when = env.Date
		}
	}
	w, err := archive.CreateMessage(from, when)
	if err != nil {
		return err
	}
	held := &heldBreak{w: w}
	if _, err := io.Copy(held, raw); err != nil {
		return err
	}
	return held.end()
}

// heldBreak keeps back a message's final line break: the mbox writer ends
// every message with its own, and a second would join the message.
type heldBreak struct {
	w    io.Writer
	held []byte
}

func (h *heldBreak) Write(b []byte) (int, error) {
	n := len(b)
	b = append(h.held, b...)
	cut := len(b)
	for cut > 0 && len(b)-cut < len("\r\n") && (b[cut-1] == '\n' || b[cut-1] == '\r') {
		cut--
	}
	if _, err := h.w.Write(b[:cut]); err != nil {
		return 0, err
	}
	h.held = bytes.Clone(b[cut:])
	return n, nil
}

func (h *heldBreak) end() error {
	if s := string(h.held); s == "\r\n" || s == "\n" {
		return nil
	}
	_, err := h.w.Write(h.held)
	return err
}

// Imported counts an import's messages: those added, those the folder held already, and those the
// server refused.
type Imported struct{ Added, Held, Refused int }

// ImportFile appends the messages of an mbox or .eml file to folder, taking the connection once.
func (p *Mailboxes) ImportFile(ctx context.Context, login *alborz.Login, folder string, r io.Reader) (n Imported, err error) {
	defer p.accountChanged(login.Username())
	err = login.DoIMAPWork(ctx, alborz.IMAPScan, importBound, func(c *imapclient.Client) error {
		return appendMessages(c, folder, r, map[string]bool{}, &n)
	})
	return n, err
}

// DropFiles appends the messages of each file next gives, until io.EOF, to folder; the connection is
// taken once and the files read inside, so they are appended as they arrive. A file that fails to
// arrive is fileErr, the sender's; err is the server's.
func (p *Mailboxes) DropFiles(ctx context.Context, login *alborz.Login, folder string, next func() (io.Reader, error)) (n Imported, fileErr, err error) {
	defer p.accountChanged(login.Username())
	err = login.DoIMAPWork(ctx, alborz.IMAPScan, importBound, func(c *imapclient.Client) error {
		if err := ensureMailboxSelected(c, folder); err != nil {
			return err
		}
		// So a file dropped twice, or a batch resent in halves, adds nothing the second time.
		seen, err := folderMessageIDs(c)
		if err != nil {
			return err
		}
		for {
			file, err := next()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				fileErr = err
				return nil
			}
			if err := appendMessages(c, folder, file, seen, &n); err != nil {
				return err
			}
		}
	})
	return n, fileErr, err
}

// A message whose Message-ID the folder already holds is skipped, so a repeated drop adds nothing; one the
// server refuses is counted and the rest go on.
func appendMessages(c *imapclient.Client, folder string, r io.Reader, seen map[string]bool, n *Imported) error {
	next := mboxMessages(r)
	for {
		raw, err := next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		id, date := messageHeaders(raw)
		if id != "" && seen[id] {
			n.Held++
			continue
		}
		if id != "" {
			seen[id] = true
		}
		options := imap.AppendOptions{Flags: []imap.Flag{imap.FlagSeen}, Time: date}
		cmd := c.Append(folder, int64(len(raw)), &options)
		if _, err := cmd.Write(raw); err != nil {
			return err
		}
		if err := cmd.Close(); err != nil {
			return err
		}
		var refused *imap.Error
		if _, err := cmd.Wait(); errors.As(err, &refused) {
			log.Printf("import into %q: the server refused message %q: %v", folder, id, err)
			n.Refused++
			continue
		} else if err != nil {
			return err
		}
		n.Added++
	}
}

// The import takes its connection once, not a turn per message behind the page's requests.
const importBound = 10 * time.Minute

// Envelopes, not a HEADER search: that matches substrings.
func folderMessageIDs(c *imapclient.Client) (map[string]bool, error) {
	held := map[string]bool{}
	mbox := c.Mailbox()
	if mbox == nil || mbox.NumMessages == 0 {
		return held, nil
	}
	var all imap.SeqSet
	all.AddRange(1, mbox.NumMessages)
	cmd := c.Fetch(all, &imap.FetchOptions{Envelope: true})
	for msg := cmd.Next(); msg != nil; msg = cmd.Next() {
		buf, err := msg.Collect()
		if err != nil {
			cmd.Close()
			return nil, err
		}
		if buf.Envelope == nil {
			continue
		}
		if id := normalizeMessageID(buf.Envelope.MessageID); id != "" {
			held[id] = true
		}
	}
	return held, cmd.Close()
}

func messageHeaders(raw []byte) (string, time.Time) {
	date := time.Now()
	header, err := readHeaderBytes(raw)
	if err != nil {
		return "", date
	}
	id := normalizeMessageID(header.Get("Message-Id"))
	if t, err := mail.ParseDate(header.Get("Date")); err == nil {
		date = t
	}
	return id, date
}

const mboxSeparator = "From "

// A file that does not open with a separator is one message, as a dropped .eml is.
func mboxMessages(r io.Reader) func() ([]byte, error) {
	buffered := bufio.NewReader(r)
	if head, _ := buffered.Peek(len(mboxSeparator)); string(head) != mboxSeparator {
		done := false
		return func() ([]byte, error) {
			if done {
				return nil, io.EOF
			}
			done = true
			raw, err := io.ReadAll(buffered)
			if err == nil && len(raw) == 0 {
				return nil, io.EOF
			}
			return pgp.CRLF(raw), err
		}
	}
	archive := mbox.NewReader(buffered)
	archive.Mboxrd = true
	return func() ([]byte, error) {
		message, err := archive.NextMessage()
		if err != nil {
			return nil, err
		}
		return io.ReadAll(message)
	}
}

// ExportSize is how many messages a folder, or its narrowing, holds, and their size where the server says (STATUS=SIZE).
func ExportSize(c *imapclient.Client, settings *Settings, mboxName, query, view string) (count int, size int64, err error) {
	if criteria := listCriteria(c, settings, query, view); criteria != nil {
		if err := ensureMailboxSelected(c, mboxName); err != nil {
			return 0, 0, err
		}
		found, err := c.UIDSearch(criteria, nil).Wait()
		if err != nil {
			return 0, 0, err
		}
		return len(found.AllUIDs()), 0, nil
	}
	options := &imap.StatusOptions{NumMessages: true, Size: c.Caps().Has(imap.CapStatusSize)}
	st, err := c.Status(mboxName, options).Wait()
	if err != nil {
		return 0, 0, err
	}
	if st.NumMessages != nil {
		count = int(*st.NumMessages)
	}
	if st.Size != nil {
		size = *st.Size
	}
	return count, size, nil
}
