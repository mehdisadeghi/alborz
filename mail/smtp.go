package mail

import (
	"bufio"
	"fmt"
	"html/template"
	"io"
	"mime"
	"mime/multipart"
	"os"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/lang"
	"github.com/alborzmail/go-itip/imip"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"
	"github.com/emersion/go-smtp"
)

// 72 leaves room for reply chevrons under RFC 5322 2.1.1's 78-character line.
const wrapColumn = 72

// Indented or short lines are left alone: rewrapping destroys preformatted text.
func wrapText(text string) string {
	var out strings.Builder
	// A reader would take a leading space as stuffing and "From " is mangled in transit (RFC 3676 4.4).
	emit := func(line, end string) {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "From ") {
			out.WriteString(" ")
		}
		out.WriteString(line)
		out.WriteString(end)
	}
	for _, line := range strings.Split(text, "\n") {
		line, cr := strings.CutSuffix(line, "\r")
		end := "\n"
		if cr {
			end = "\r\n"
		}
		// A typed trailing space would read as a soft break; quoted lines keep theirs, and so does "-- " (RFC 3676 4.2, 4.3).
		markers := line[:len(line)-len(strings.TrimLeft(line, ">"))]
		if line != "-- " && markers == "" {
			line = strings.TrimRight(line, " ")
		}
		if len(line) <= wrapColumn ||
			strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			emit(line, end)
			continue
		}
		// Every piece of a quoted line carries its markers, so it stays at its depth (RFC 3676 4.5).
		lead, text := "", line
		if markers != "" {
			lead, text = markers+" ", strings.TrimPrefix(line[len(markers):], " ")
		}
		words := strings.Split(text, " ")
		cur := ""
		for _, w := range words {
			switch {
			case cur == "":
				cur = w
			case len(lead)+len(cur)+1+len(w) <= wrapColumn:
				cur += " " + w
			default:
				// A trailing space marks a flowed line (RFC 3676).
				emit(lead+cur, " "+end)
				cur = w
			}
		}
		emit(lead+cur, end)
	}
	return strings.TrimSuffix(out.String(), "\n")
}

// Unflow undoes format=flowed: soft-broken lines of one quote depth join, stuffing goes (RFC 3676 4).
func Unflow(text string, delSp bool) string {
	var out []string
	soft, depth := false, -1
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		content := strings.TrimLeft(line, ">")
		n := len(line) - len(content)
		content = strings.TrimPrefix(content, " ")
		joins := soft && n == depth
		soft, depth = content != "-- " && strings.HasSuffix(content, " "), n
		if soft && delSp {
			content = strings.TrimSuffix(content, " ")
		}
		if joins {
			out[len(out)-1] += content
			continue
		}
		if n > 0 {
			content = strings.Repeat(">", n) + " " + content
		}
		out = append(out, content)
	}
	return strings.Join(out, "\n")
}

// Deeper quotes are already in the thread.
const quoteMaxDepth = 2

func quote(r io.Reader) (string, error) {
	scanner := bufio.NewScanner(r)
	var lines []string
	for scanner.Scan() {
		line := scanner.Text()
		// "-- " starts the signature (RFC 3676 4.3).
		if strings.TrimRight(line, "\r") == "-- " {
			break
		}
		if quoteDepth(line) >= quoteMaxDepth {
			continue
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("quote: failed to read original message: %s", err)
	}

	var builder strings.Builder
	blank := 0
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		// Markers nest without a space; a space after them is stuffing (RFC 3676 4.5).
		if strings.HasPrefix(line, ">") {
			builder.WriteString(">")
		} else {
			builder.WriteString("> ")
		}
		builder.WriteString(line)
		builder.WriteString("\n")
	}
	builder.WriteString("\n")
	return builder.String(), nil
}

func quoteDepth(line string) int {
	n := 0
	for _, r := range line {
		switch r {
		case '>':
			n++
		case ' ', '\t':
		default:
			return n
		}
	}
	return n
}

type Attachment interface {
	MIMEType() string
	Filename() string
	io.WriterTo
}

// ID names an upload the session holds, which a refused send puts back on the form.
type formAttachment struct {
	*multipart.FileHeader
	ID string
}

func (att *formAttachment) SizeString() string {
	return formatSize(att.FileHeader.Size)
}

func (att *formAttachment) WriteTo(w io.Writer) (int64, error) {
	f, err := att.FileHeader.Open()
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return io.Copy(w, f)
}

func (att *formAttachment) MIMEType() string {
	// TODO: retain params, e.g. "charset"?
	t, _, _ := mime.ParseMediaType(att.FileHeader.Header.Get("Content-Type"))
	return t
}

func (att *formAttachment) Filename() string {
	return att.FileHeader.Filename
}

type imapAttachment struct {
	Mailbox  string
	Uid      imap.UID
	Node     *IMAPPartNode
	Encoding string
	transfer transfer
}

func (att *imapAttachment) WriteTo(w io.Writer) (n int64, err error) {
	err = att.transfer(func(c *imapclient.Client) error {
		n, err = copySection(w, c, att.Mailbox, att.Uid, att.Node.Path, att.Encoding)
		return err
	})
	return n, err
}

func (att *imapAttachment) MIMEType() string {
	return att.Node.MIMEType
}

func (att *imapAttachment) Filename() string {
	return att.Node.Filename
}

func newMessageID() string {
	var hdr mail.Header
	hdr.GenerateMessageID()
	id, _ := hdr.MessageID()
	return "<" + id + ">"
}

func mailerName(ctx *alborz.Context) string {
	if v, _ := alborz.Build(); v != "" {
		return alborz.BrandName + "/" + v
	}
	return alborz.BrandName
}

func fromAddress(settings *Settings, address string) string {
	if settings.From == "" {
		return address
	}
	return (&mail.Address{Name: settings.From, Address: address}).String()
}

type OutgoingMessage struct {
	From      string
	To        []string
	Cc        []string
	Bcc       []string
	Subject   string
	MessageID string
	// Mailer is the User-Agent header; empty leaves it out.
	Mailer string
	// Headers are set as they are, for what only a plugin knows to say.
	Headers   map[string]string
	InReplyTo string
	// References is the thread this message belongs to, oldest first.
	References string
	// Calendar makes the mail a scheduling message, Text its plain alternative; nil for ordinary mail.
	Calendar *ical.Calendar
	// ReplyTo is where answers should go when that is not the From.
	ReplyTo []string
	// Sender is the mailbox transmitting for a different author; empty for one's own identity.
	Sender string
	// Language is what the body is written in, for Content-Language.
	Language string
	Text     string
	// QuoteBelow puts the quoted message under the reply, where the cursor goes.
	QuoteBelow bool
	// HTML is the editor's message; QuoteHTML is the answered message's HTML without images or scripts, for the page alone.
	HTML      string
	QuoteHTML template.HTML
	// QuoteRich opens the editor on QuoteHTML.
	QuoteRich bool
	// Inline are the images the HTML shows, resolved for the send.
	Inline []inlineImage
	// SendHTML adds an HTML alternative carrying each paragraph's direction.
	SendHTML    bool
	Attachments []Attachment
}

func (msg *OutgoingMessage) ToString() string {
	return strings.Join(msg.To, ", ")
}

func writeAttachment(mw *mail.Writer, att Attachment) error {
	var h mail.AttachmentHeader
	h.SetContentType(att.MIMEType(), nil)
	h.SetFilename(att.Filename())

	aw, err := mw.CreateAttachment(h)
	if err != nil {
		return fmt.Errorf("failed to create attachment: %v", err)
	}
	defer aw.Close()

	if _, err := att.WriteTo(aw); err != nil {
		return fmt.Errorf("failed to write attachment: %v", err)
	}
	if err := aw.Close(); err != nil {
		return fmt.Errorf("failed to close attachment writer: %v", err)
	}

	return nil
}

func prepareAddressList(addresses []string) ([]*mail.Address, error) {
	l := make([]*mail.Address, len(addresses))
	for i, rcpt := range addresses {
		addr, err := mail.ParseAddress(rcpt)
		if err != nil {
			return nil, err
		}
		l[i] = addr
	}

	return l, nil
}

// WriteMessage writes the message as RFC 5322 bytes; not WriteTo, whose name belongs to io.WriterTo.
func (msg *OutgoingMessage) WriteMessage(w io.Writer) error {
	fromAddr, err := mail.ParseAddress(msg.From)
	if err != nil {
		return err
	}
	from := []*mail.Address{fromAddr}

	to, err := prepareAddressList(msg.To)
	if err != nil {
		return err
	}

	cc, err := prepareAddressList(msg.Cc)
	if err != nil {
		return err
	}

	var h mail.Header
	h.SetDate(time.Now())
	h.SetAddressList("From", from)
	h.SetAddressList("To", to)
	h.SetAddressList("Cc", cc)
	if msg.Subject != "" {
		h.SetText("Subject", msg.Subject)
	}
	if len(msg.ReplyTo) > 0 {
		replyTo, err := prepareAddressList(msg.ReplyTo)
		if err != nil {
			return err
		}
		h.SetAddressList("Reply-To", replyTo)
	}
	if msg.Sender != "" {
		if addr, err := mail.ParseAddress(msg.Sender); err == nil {
			h.SetAddressList("Sender", []*mail.Address{addr})
		}
	}
	if msg.InReplyTo != "" {
		h.Set("In-Reply-To", msg.InReplyTo)
	}
	if msg.References != "" {
		h.Set("References", msg.References)
	}
	if msg.Language != "" {
		h.Set("Content-Language", msg.Language)
	}

	for k, v := range msg.Headers {
		h.Set(k, v)
	}
	if msg.Mailer != "" {
		h.Set("User-Agent", msg.Mailer)
	}

	if msg.MessageID == "" {
		return fmt.Errorf("message has no Message-ID")
	}
	h.Set("Message-Id", msg.MessageID)

	if msg.Calendar != nil {
		return imip.Write(w, h, msg.Calendar, msg.Text)
	}

	mw, err := mail.CreateWriter(w, h)
	if err != nil {
		return fmt.Errorf("failed to create mail writer: %v", err)
	}

	var th mail.InlineHeader
	// format=flowed lets the reader's client undo the soft breaks (RFC 3676).
	th.Set("Content-Type", "text/plain; charset=utf-8; format=flowed")
	body := wrapText(msg.Text)

	switch {
	case msg.HTML != "":
		if err := writeAlternative(mw, th, body, msg.HTML, msg.Inline); err != nil {
			return err
		}
	case msg.SendHTML:
		if err := writeAlternative(mw, th, body, lang.HTMLAlternative(msg.Text), nil); err != nil {
			return err
		}
	default:
		tw, err := mw.CreateSingleInline(th)
		if err != nil {
			return fmt.Errorf("failed to create text part: %v", err)
		}
		if _, err := io.WriteString(tw, body); err != nil {
			tw.Close()
			return fmt.Errorf("failed to write text part: %v", err)
		}
		if err := tw.Close(); err != nil {
			return fmt.Errorf("failed to close text part: %v", err)
		}
	}

	for _, att := range msg.Attachments {
		if err := writeAttachment(mw, att); err != nil {
			return err
		}
	}

	if err := mw.Close(); err != nil {
		return fmt.Errorf("failed to close mail writer: %v", err)
	}

	return nil
}

// spool writes the message once, so what is sent and what is kept are the same
// bytes, and IMAP learns their size before the APPEND. The file is unlinked
// at once and goes with its last descriptor, even if alborz dies (Unix).
func spool(msg *OutgoingMessage) (*os.File, error) {
	f, err := os.CreateTemp("", "")
	if err != nil {
		return nil, err
	}
	if err := os.Remove(f.Name()); err != nil {
		f.Close()
		return nil, err
	}
	if err := msg.WriteMessage(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("failed to write outgoing message: %w", err)
	}
	return f, nil
}

// sendMessage transmits body, a spooled msg, from its start.
func sendMessage(c *smtp.Client, msg *OutgoingMessage, body io.ReadSeeker) error {
	addr, err := mail.ParseAddress(msg.From)
	if err != nil {
		return fmt.Errorf("parsing 'From' address failed: %v", err)
	}

	if err := c.Mail(addr.Address, nil); err != nil {
		return fmt.Errorf("MAIL FROM failed: %v", err)
	}

	for _, to := range append(msg.To, append(msg.Bcc, msg.Cc...)...) {
		addr, err := mail.ParseAddress(to)
		if err != nil {
			return fmt.Errorf("parsing address %q failed: %v", to, err)
		}

		if err := c.Rcpt(addr.Address, nil); err != nil {
			return fmt.Errorf("RCPT TO failed: %v (%s)", err, addr.Address)
		}
	}

	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("DATA failed: %v", err)
	}
	defer w.Close()

	if _, err := io.Copy(w, body); err != nil {
		return fmt.Errorf("failed to write outgoing message: %v", err)
	}

	if err := w.Close(); err != nil {
		return fmt.Errorf("failed to close SMTP data writer: %v", err)
	}

	return nil
}

// The plain part comes first: multipart/alternative orders least preferred first (RFC 2046 5.1.4).
func writeAlternative(mw *mail.Writer, th mail.InlineHeader, body, htmlBody string, images []inlineImage) error {
	iw, err := mw.CreateInline()
	if err != nil {
		return fmt.Errorf("failed to create alternative part: %v", err)
	}
	tw, err := iw.CreatePart(th)
	if err != nil {
		iw.Close()
		return fmt.Errorf("failed to create text part: %v", err)
	}
	if _, err := io.WriteString(tw, body); err != nil {
		tw.Close()
		iw.Close()
		return fmt.Errorf("failed to write text part: %v", err)
	}
	if err := tw.Close(); err != nil {
		iw.Close()
		return fmt.Errorf("failed to close text part: %v", err)
	}

	if len(images) > 0 {
		if err := writeRelated(iw, cidImages(htmlBody, images), images); err != nil {
			iw.Close()
			return err
		}
		return iw.Close()
	}

	var hh mail.InlineHeader
	hh.Set("Content-Type", "text/html; charset=utf-8")
	hw, err := iw.CreatePart(hh)
	if err != nil {
		iw.Close()
		return fmt.Errorf("failed to create html part: %v", err)
	}
	if _, err := io.WriteString(hw, htmlBody); err != nil {
		hw.Close()
		iw.Close()
		return fmt.Errorf("failed to write html part: %v", err)
	}
	if err := hw.Close(); err != nil {
		iw.Close()
		return fmt.Errorf("failed to close html part: %v", err)
	}
	return iw.Close()
}

func writeRelated(iw *mail.InlineWriter, htmlBody string, images []inlineImage) error {
	var rh mail.InlineHeader
	rh.Set("Content-Type", `multipart/related; type="text/html"`)
	// Left unset, the writer would pick base64 for a multipart, which is never encoded.
	rh.Set("Content-Transfer-Encoding", "7bit")
	part, err := iw.CreatePart(rh)
	if err != nil {
		return fmt.Errorf("failed to create related part: %v", err)
	}
	rw := part.(*message.Writer)

	var hh message.Header
	hh.Set("Content-Type", "text/html; charset=utf-8")
	hh.Set("Content-Transfer-Encoding", "quoted-printable")
	hw, err := rw.CreatePart(hh)
	if err != nil {
		rw.Close()
		return fmt.Errorf("failed to create html part: %v", err)
	}
	if _, err := io.WriteString(hw, htmlBody); err != nil {
		hw.Close()
		rw.Close()
		return fmt.Errorf("failed to write html part: %v", err)
	}
	if err := hw.Close(); err != nil {
		rw.Close()
		return fmt.Errorf("failed to close html part: %v", err)
	}

	for _, im := range images {
		var h message.Header
		h.SetContentType(im.MIMEType(), map[string]string{"name": im.Filename()})
		h.SetContentDisposition("inline", map[string]string{"filename": im.Filename()})
		h.Set("Content-Id", "<"+im.ContentID+">")
		h.Set("Content-Transfer-Encoding", "base64")
		w, err := rw.CreatePart(h)
		if err != nil {
			rw.Close()
			return fmt.Errorf("failed to create image part: %v", err)
		}
		if _, err := im.WriteTo(w); err != nil {
			w.Close()
			rw.Close()
			return fmt.Errorf("failed to write image: %v", err)
		}
		if err := w.Close(); err != nil {
			rw.Close()
			return fmt.Errorf("failed to close image part: %v", err)
		}
	}
	return rw.Close()
}
