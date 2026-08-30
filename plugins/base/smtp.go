package alborzbase

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"mime"
	"mime/multipart"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-message/mail"
	"github.com/emersion/go-smtp"
)

// wrapColumn is where a line is folded. RFC 5322 2.1.1 caps a line at
// 998 octets and asks for 78; 72 leaves room for the chevrons a reply
// will add without pushing the result past that.
const wrapColumn = 72

// wrapText folds long lines so the message obeys 2.1.1 - a Persian
// paragraph typed into a textarea is one line however long it runs - and
// marks the folds as format=flowed (RFC 3676) so a client that
// understands it can lay the paragraph out for its own window. A line
// that is quoted, indented or already short is left exactly as it is:
// preformatted text is the one thing rewrapping destroys.
func wrapText(text string) string {
	var out strings.Builder
	for _, line := range strings.Split(text, "\n") {
		if len(line) <= wrapColumn || quoteDepth(line) > 0 ||
			strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			out.WriteString(line)
			out.WriteString("\n")
			continue
		}
		words := strings.Split(line, " ")
		cur := ""
		for _, w := range words {
			switch {
			case cur == "":
				cur = w
			case len(cur)+1+len(w) <= wrapColumn:
				cur += " " + w
			default:
				// A flowed line ends in a space: that is what says the
				// next line continues it, and what lets the reader's
				// client fold it differently.
				out.WriteString(cur)
				out.WriteString(" \n")
				cur = w
			}
		}
		out.WriteString(cur)
		out.WriteString("\n")
	}
	return strings.TrimSuffix(out.String(), "\n")
}

// quoteMaxDepth is how deep a quoted quote is carried. A reply to a
// reply to a reply says nothing the two above it did not, and a thread
// that keeps all of them grows a wall of chevrons nobody reads.
const quoteMaxDepth = 2

// quote marks up the message being answered, trimmed the way a reply is
// expected to be: the sender's signature goes, since it is not part of
// what they said; quotes deeper than a couple of levels go, since they
// are already in the thread; and the blank lines a trim leaves behind
// collapse, so the quote reads as text rather than as a gap.
func quote(r io.Reader) (string, error) {
	scanner := bufio.NewScanner(r)
	var lines []string
	for scanner.Scan() {
		line := scanner.Text()
		// RFC 3676 4.3: a line of exactly "-- " starts the signature,
		// and everything after it belongs to the sender, not the point.
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
		builder.WriteString("> ")
		builder.WriteString(line)
		builder.WriteString("\n")
	}
	builder.WriteString("\n")
	return builder.String(), nil
}

// quoteDepth counts the chevrons a line already carries.
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
	Open() (io.ReadCloser, error)
}

type formAttachment struct {
	*multipart.FileHeader
}

func (att *formAttachment) Open() (io.ReadCloser, error) {
	return att.FileHeader.Open()
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
	Mailbox string
	Uid     imap.UID
	Node    *IMAPPartNode

	Body []byte
}

func (att *imapAttachment) Open() (io.ReadCloser, error) {
	if att.Body == nil {
		return nil, fmt.Errorf("IMAP attachment has not been pre-fetched")
	}
	return ioutil.NopCloser(bytes.NewReader(att.Body)), nil
}

func (att *imapAttachment) MIMEType() string {
	return att.Node.MIMEType
}

func (att *imapAttachment) Filename() string {
	return att.Node.Filename
}

type OutgoingMessage struct {
	From      string
	To        []string
	Cc        []string
	Bcc       []string
	Subject   string
	MessageID string
	// Mailer names the software that wrote the message, for the
	// conventional User-Agent header; empty leaves the header out.
	Mailer    string
	InReplyTo string
	// References is the thread this message belongs to, oldest first
	// (RFC 5322 3.6.4). Gmail and Thunderbird thread on it before
	// In-Reply-To, which is why a long thread scattered without it.
	References string
	// ReplyTo is where answers should go when that is not the From
	// (RFC 5322 3.6.2): a personal address writing under a shared one,
	// or a send from an identity whose mailbox is elsewhere.
	ReplyTo []string
	// Sender is the mailbox transmitting for a different author
	// (RFC 5322 3.6.2). Sending under one's own identity is not that
	// case and sets nothing here; only a true on-behalf-of send would.
	Sender string
	// Language is what the body is written in, for Content-Language.
	Language string
	Text     string
	// QuoteBelow says the quoted message sits under the space the reply
	// is written in, so the compose form can put the cursor there.
	QuoteBelow  bool
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

	f, err := att.Open()
	if err != nil {
		return fmt.Errorf("failed to open attachment: %v", err)
	}
	defer f.Close()

	if _, err := io.Copy(aw, f); err != nil {
		return fmt.Errorf("failed to write attachment: %v", err)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("failed to close attachment: %v", err)
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

func (msg *OutgoingMessage) WriteTo(w io.Writer) error {
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
	// RFC 3282: the language the body is written in, which a mixed-script
	// mailbox has reason to state and nobody can infer.
	if msg.Language != "" {
		h.Set("Content-Language", msg.Language)
	}

	if msg.Mailer != "" {
		h.Set("User-Agent", msg.Mailer)
	}

	h.Set("Message-Id", msg.MessageID)
	if msg.MessageID == "" {
		panic(fmt.Errorf("Attempting to send message without message ID"))
	}

	mw, err := mail.CreateWriter(w, h)
	if err != nil {
		return fmt.Errorf("failed to create mail writer: %v", err)
	}

	var th mail.InlineHeader
	// format=flowed says the soft line breaks above may be undone, so a
	// paragraph lays itself out in the reader's window instead of at our
	// column (RFC 3676). A client that does not know it sees text
	// wrapped at 72, which is what 5322 asks for anyway.
	th.Set("Content-Type", "text/plain; charset=utf-8; format=flowed")

	tw, err := mw.CreateSingleInline(th)
	if err != nil {
		return fmt.Errorf("failed to create text part: %v", err)
	}
	defer tw.Close()

	if _, err := io.WriteString(tw, wrapText(msg.Text)); err != nil {
		return fmt.Errorf("failed to write text part: %v", err)
	}

	if err := tw.Close(); err != nil {
		return fmt.Errorf("failed to close text part: %v", err)
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

func sendMessage(c *smtp.Client, msg *OutgoingMessage) error {
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

	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("DATA failed: %v", err)
	}
	defer w.Close()

	if err := msg.WriteTo(w); err != nil {
		return fmt.Errorf("failed to write outgoing message: %v", err)
	}

	if err := w.Close(); err != nil {
		return fmt.Errorf("failed to close SMTP data writer: %v", err)
	}

	return nil
}
