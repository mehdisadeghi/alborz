package mail

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"github.com/alborzmail/go-itip/imip"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message"
	"github.com/emersion/go-message/charset"
	"github.com/emersion/go-message/mail"
	"github.com/emersion/go-message/textproto"
	"jaytaylor.com/html2text"
)

// Shown is a message on its page as the other features see it: they read Message and fill what they
// offer beside it (web.Extend).
type Shown struct {
	Message *IMAPMessage
	// Filled by calendars and contacts, nil where they have nothing: the scheduling request this
	// message carries, the share it announces, where an invitation or an attached file can be filed,
	// and the stored copy a withdrawal can drop.
	Invitation      any
	ShareOffer      any
	InviteCalendars any
	ImportCalendars any
	ImportBooks     any
	InviteFiled     string
	// Contact is what "Add to contacts" offers; nil where no address book takes a card.
	Contact *ContactOffer
}

// ContactOffer is the message's addresses and the address books one can be added to.
type ContactOffer struct {
	Addresses []*mail.Address
	Books     any
}

// StreamMessage hands write the whole message as the server sends it, on a connection of its own.
func StreamMessage(ctx context.Context, login *alborz.Login, mboxName string, uid imap.UID, write func(*imap.Envelope, imap.LiteralReader) error) error {
	return login.DoIMAPTransfer(ctx, func(c *imapclient.Client) error {
		env, err := fetchEnvelope(c, mboxName, uid)
		if err != nil {
			return err
		}
		whole := &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{{Peek: true}}}
		return streamSection(c, uid, whole, func(lit imap.LiteralReader) error {
			return write(env, lit)
		})
	})
}

func fetchEnvelope(c *imapclient.Client, mboxName string, uid imap.UID) (*imap.Envelope, error) {
	if err := ensureMailboxSelected(c, mboxName); err != nil {
		return nil, err
	}
	msgs, err := c.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{UID: true, Envelope: true}).Collect()
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, alborz.NotFound("notfound.message", fmt.Sprint(uid))
	}
	return msgs[0].Envelope, nil
}

// ZipAttachments writes a message's attachments as a zip archive into what open gives once the message reads.
func ZipAttachments(ctx context.Context, login *alborz.Login, mboxName string, uid imap.UID, open func(*imap.Envelope) io.Writer) error {
	return StreamMessage(ctx, login, mboxName, uid, func(env *imap.Envelope, lit imap.LiteralReader) error {
		entity, err := message.Read(lit)
		if err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err) {
			return err
		}
		archive := zip.NewWriter(open(env))
		if err := writeAttachments(archive, entity, map[string]int{}, ""); err != nil {
			return err
		}
		return archive.Close()
	})
}

func singlePart(c *imapclient.Client, mboxName string, uid imap.UID, partPath []int) (*imap.BodyStructureSinglePart, error) {
	if err := ensureMailboxSelected(c, mboxName); err != nil {
		return nil, err
	}
	msgs, err := c.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{
		UID: true, BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
	}).Collect()
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, alborz.NotFound("notfound.message", fmt.Sprint(uid))
	}
	var part *imap.BodyStructureSinglePart
	msgs[0].BodyStructure.Walk(func(p []int, bs imap.BodyStructure) bool {
		if single, ok := bs.(*imap.BodyStructureSinglePart); ok && slices.Equal(p, partPath) {
			part = single
		}
		return part == nil
	})
	if part == nil {
		return nil, alborz.NotFound("notfound.part", fmt.Sprint(uid))
	}
	return part, nil
}

// StreamPart hands write one part, decoded, the name to save it as, and its length where the server
// knows it, else -1.
func StreamPart(ctx context.Context, login *alborz.Login, mboxName string, uid imap.UID, partPath []int, write func(part *imap.BodyStructureSinglePart, name string, size int64, body io.Reader) error) error {
	return login.DoIMAPTransfer(ctx, func(c *imapclient.Client) error {
		part, err := singlePart(c, mboxName, uid, partPath)
		if err != nil {
			return err
		}
		// BINARY (RFC 3516) has the server decode the part, and so know its length.
		options := &imap.FetchOptions{UID: true}
		binary := c.Caps().Has(imap.CapBinary)
		if binary {
			options.BinarySection = []*imap.FetchItemBinarySection{{Part: partPath}}
		} else {
			options.BodySection = []*imap.FetchItemBodySection{{Part: partPath}}
		}
		name := zipEntryName(newIMAPPartNode(nil, partPath, part).Filename, strings.ToLower(part.MediaType()), map[string]int{})
		return streamSection(c, uid, options, func(lit imap.LiteralReader) error {
			if binary {
				return write(part, name, lit.Size(), lit)
			}
			return write(part, name, -1, transferDecoded(part.Encoding, lit))
		})
	})
}

func htmlAsText(part *message.Entity) (*message.Entity, error) {
	text, err := html2text.FromReader(part.Body, html2text.Options{})
	if err != nil {
		return nil, fmt.Errorf("failed to read the HTML as text: %v", err)
	}
	var header textproto.Header
	header.Set("Content-Type", "text/plain; charset=utf-8")
	return message.New(message.Header{Header: header}, strings.NewReader(text))
}

func joinedHTML(c *imapclient.Client, mboxName string, uid imap.UID, pieces []IMAPPartNode) (*message.Entity, error) {
	if err := ensureMailboxSelected(c, mboxName); err != nil {
		return nil, err
	}
	options := &imap.FetchOptions{}
	for _, p := range pieces {
		if p.MIMEType == "text/html" {
			options.BodySection = append(options.BodySection,
				&imap.FetchItemBodySection{Peek: true, Part: p.Path, Specifier: imap.PartSpecifierMIME},
				&imap.FetchItemBodySection{Peek: true, Part: p.Path, Partial: &imap.SectionPartial{Size: partReadMax}})
		}
	}
	msgs, err := c.Fetch(imap.UIDSetNum(uid), options).Collect()
	if err != nil {
		return nil, fmt.Errorf("failed to fetch the HTML pieces: %v", err)
	} else if len(msgs) == 0 {
		return nil, alborz.NotFound("notfound.message", fmt.Sprint(uid))
	}
	var joined bytes.Buffer
	sections := options.BodySection
	for _, p := range pieces {
		if p.MIMEType != "text/html" {
			fmt.Fprintf(&joined, `<p><img src="part:%s" alt="%s"></p>`, p.PathString(), html.EscapeString(p.Filename))
			continue
		}
		headerBuf, bodyBuf := msgs[0].FindBodySection(sections[0]), msgs[0].FindBodySection(sections[1])
		sections = sections[2:]
		if headerBuf == nil || bodyBuf == nil {
			return nil, fmt.Errorf("server didn't return HTML piece %v", p.PathString())
		}
		h, err := textproto.ReadHeader(bufio.NewReader(bytes.NewReader(headerBuf)))
		if err != nil {
			return nil, err
		}
		piece, err := readPart(h, bodyBuf, &p, p.Cut())
		if err != nil {
			return nil, err
		}
		if _, err := io.Copy(&joined, piece.Body); err != nil {
			return nil, err
		}
	}
	var h message.Header
	h.SetContentType("text/html", map[string]string{"charset": "utf-8"})
	return message.New(h, &joined)
}

func writeAttachments(archive *zip.Writer, e *message.Entity, taken map[string]int, parent string) error {
	mediaType, typeParams, _ := e.Header.ContentType()
	if mr := e.MultipartReader(); mr != nil {
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				return nil
			}
			// A part in an encoding go-message lacks is stored as its bytes.
			if err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err) {
				return err
			}
			if err := writeAttachments(archive, part, taken, strings.TrimPrefix(mediaType, "multipart/")); err != nil {
				return err
			}
		}
	}
	disposition, params, _ := e.Header.ContentDisposition()
	filename := params["filename"]
	if filename == "" {
		filename = typeParams["name"]
	}
	if !attached(mediaType, disposition, filename, e.Header.Get("Content-Id"), parent) {
		return nil
	}
	if filename == "" && strings.EqualFold(mediaType, "message/rfc822") {
		// Only the header is read for the name; what it took is put back in front.
		var head bytes.Buffer
		inner, err := textproto.ReadHeader(bufio.NewReader(io.TeeReader(e.Body, &head)))
		if err == nil {
			h := mail.Header{Header: message.Header{Header: inner}}
			subject, _ := h.Subject()
			filename = attachmentName(subject)
		}
		e.Body = io.MultiReader(&head, e.Body)
	}
	w, err := archive.Create(zipEntryName(filename, mediaType, taken))
	if err != nil {
		return err
	}
	_, err = io.Copy(w, e.Body)
	return err
}

func zipEntryName(filename, mediaType string, taken map[string]int) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r < 0x20, r == '\\', r == '/', r == 0x7f:
			return -1
		}
		return r
	}, filename)
	if name == "" {
		name = "part"
		if exts, err := mime.ExtensionsByType(mediaType); err == nil && len(exts) > 0 {
			name += exts[0]
		}
	}
	taken[name]++
	if n := taken[name]; n > 1 {
		ext := path.Ext(name)
		name = fmt.Sprintf("%s %d%s", strings.TrimSuffix(name, ext), n, ext)
	}
	return name
}

// unsubscribeMail is what compose takes to leave a list by mail; nil where the list takes none.
func unsubscribeMail(settings *Settings, trust deliveryTrust, msg *IMAPMessage) url.Values {
	q := Mailto(msg.ListUnsubscribe)
	if q == nil {
		return nil
	}
	// The request leaves from the account the list wrote to, not the browser's active one.
	q.Set("account", trust.owner(msg))
	if from := writeAs(settings, trust, msg, msg.Envelope.To, msg.Envelope.Cc); from != "" {
		q.Set("from", from)
	}
	return q
}

// PartAt hands read one part of a message as it arrives, in UTF-8 where its charset is known.
func PartAt(ctx context.Context, login *alborz.Login, mboxName string, uid imap.UID, partPath []int, read func(io.Reader) error) error {
	return StreamPart(ctx, login, mboxName, uid, partPath, func(part *imap.BodyStructureSinglePart, _ string, _ int64, body io.Reader) error {
		// An unknown charset leaves the part as its bytes, as the download does.
		if r, err := charset.Reader(part.Params["charset"], body); err == nil {
			body = r
		}
		return read(body)
	})
}

// CalendarPartAt reads the scheduling message of one mail, and the part it is.
func CalendarPartAt(ctx context.Context, login *alborz.Login, mboxName string, uid imap.UID) (*ical.Calendar, string, error) {
	var msg *IMAPMessage
	if err := login.DoIMAPContext(ctx, alborz.RoundTripTimeout, func(c *imapclient.Client) error {
		if err := ensureMailboxSelected(c, mboxName); err != nil {
			return err
		}
		msgs, err := c.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{
			UID: true, BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
		}).Collect()
		if err != nil {
			return err
		}
		if len(msgs) == 0 {
			return alborz.NotFound("notfound.message", fmt.Sprint(uid))
		}
		msg = &IMAPMessage{FetchMessageBuffer: msgs[0], Mailbox: mboxName}
		return nil
	}); err != nil {
		return nil, "", err
	}
	return CalendarPart(ctx, login, msg, mboxName, uid)
}

// CalendarPart reads the scheduling message of a fetched mail; nil for none.
func CalendarPart(ctx context.Context, login *alborz.Login, msg *IMAPMessage, mboxName string, uid imap.UID) (*ical.Calendar, string, error) {
	part := invitationPart(msg)
	if part == nil || part.Cut() {
		return nil, "", nil
	}
	var cal *ical.Calendar
	err := login.DoIMAPContext(ctx, alborz.RoundTripTimeout, func(c *imapclient.Client) error {
		if err := ensureMailboxSelected(c, mboxName); err != nil {
			return err
		}
		msgs, err := c.Fetch(imap.UIDSetNum(uid), partFetchOptions(part.Path, true, true)).Collect()
		if err != nil {
			return err
		}
		if len(msgs) == 0 {
			return alborz.NotFound("notfound.message", fmt.Sprint(uid))
		}
		_, entity, err := messagePart(msgs[0], mboxName, uid, part.Path, true)
		if err != nil {
			return err
		}
		cals, err := imip.Read(entity)
		if len(cals) > 0 {
			cal = cals[0]
		}
		return err
	})
	if err != nil {
		return nil, "", err
	}
	return cal, part.PathString(), nil
}

// OneClick is where a message's list takes a one-click unsubscribe (RFC 8058); empty where it offers none.
func OneClick(c *imapclient.Client, mboxName string, uid imap.UID) (string, error) {
	if err := ensureMailboxSelected(c, mboxName); err != nil {
		return "", err
	}
	header := &imap.FetchItemBodySection{Peek: true, Specifier: imap.PartSpecifierHeader,
		HeaderFields: []string{"List-Unsubscribe", "List-Unsubscribe-Post"}}
	msgs, err := c.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{
		BodySection: []*imap.FetchItemBodySection{header},
	}).Collect()
	if err != nil {
		return "", err
	}
	if len(msgs) == 0 {
		return "", alborz.NotFound("notfound.message", fmt.Sprint(uid))
	}
	h, err := textproto.ReadHeader(bufio.NewReader(bytes.NewReader(msgs[0].FindBodySection(header))))
	if err != nil {
		return "", err
	}
	msg := &IMAPMessage{Mailbox: mboxName}
	msg.setListHeaders(h)
	return msg.OneClick, nil
}

// Unsubscribe posts the one-click Unsubscribe to the list's address.
func (p *Mailboxes) Unsubscribe(ctx context.Context, uri string) error {
	body := strings.NewReader("List-Unsubscribe=One-Click")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uri, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.server.Outgoing.Remote(unsubscribeTimeout).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		p.server.Logger().Printf("unsubscribe %s answered %s", uri, resp.Status)
		return fmt.Errorf("unsubscribe %s answered %s", uri, resp.Status)
	}
	return nil
}

const unsubscribeTimeout = 15 * time.Second

func sentFolder(mailboxes []MailboxInfo) string {
	for i := range mailboxes {
		if mailboxes[i].Role() == "sent" {
			return mailboxes[i].Name()
		}
	}
	return ""
}

// ReadAsk asks for one message as its page shows it.
type ReadAsk struct {
	Login *alborz.Login
	// Logins are the accounts signed in: each one's senders tell an imitation.
	Logins   []*alborz.Login
	Settings *Settings
	Mailbox  string
	UID      imap.UID
	// Part is the Part asked for, nil for the one the rows open; named says the address named one.
	Part       []int
	Named      bool
	List       ListAsk
	PreferHTML bool
	// Partial says the page is swapped in place, so its context can follow.
	Partial bool
	// Merged says the list the message was opened from spans accounts or folders, and is read back elsewhere.
	Merged     bool
	MoneyWords []string
}

// Opened is a message read for its page, from what is held where it can be.
type Opened struct {
	Message         *IMAPMessage
	Part            *message.Entity
	PartPath        []int
	Rail            Rail
	PermanentFlags  []imap.Flag
	AuthResults     *AuthResults
	InReplyTo       *ThreadNeighbour
	Answers         []ThreadNeighbour
	ThreadAlgorithm imap.ThreadAlgorithm
	Place           Place
	// DeferContext says the place and the thread are left for the page to ask for.
	DeferContext bool
	Warnings     []Indicator
	Mark         Grade
	// DeliveredTo is the alias the message reached where To and Cc do not name it; ForwardedBy the
	// mailbox that passed it on, proved by our server's SPF check.
	DeliveredTo, ForwardedBy string
	// Unsubscribe is the mail that leaves the list, where it takes one.
	Unsubscribe url.Values
}

// Read reads a message for its page, from what is held where it can, and marks it read.
func (p *Mailboxes) Read(ctx context.Context, r ReadAsk) (*Opened, error) {
	// Read before the message's IMAP work: it may sample the inbox, and the login lock is not reentrant.
	trusted := p.TrustedAuthServ(r.Login, r.Settings)
	partPath := r.Part
	cached, place, placed := p.cachedPlace(r.Login.Username(), r.Mailbox, r.UID, r.List)
	// A list across accounts or folders is read back through its own pages, not this folder's order.
	if r.Merged {
		place, placed = Place{}, true
	}
	// A link naming no part opens the part the rows would link to; the cached row knows it without a fetch.
	if !r.Named && cached != nil {
		if row := cached.row(r.UID); row != nil {
			if preferred := row.PreferredPart(r.PreferHTML); preferred != nil && len(preferred.Path) > 0 {
				partPath = preferred.Path
			}
		}
	}
	held := p.bodies.current(r.Login.Username(), r.Mailbox, r.UID, partPath)
	if held != nil && len(partPath) == 0 {
		partPath = held.part
	}
	alborz.CacheTiming(ctx, "body", held != nil)
	var (
		sb              Rail
		msg             *IMAPMessage
		part            *message.Entity
		permanentFlags  []imap.Flag
		authResults     *AuthResults
		inReplyTo       *ThreadNeighbour
		answers         []ThreadNeighbour
		threadAlgorithm imap.ThreadAlgorithm
		err             error
	)
	// A flag change evicts the listing but not the held body, so the sidebar may load below.
	if cached != nil {
		sb = cached.sb
	}
	if held != nil && cached == nil {
		sb, err = p.Rail(r.Login)
		if err != nil {
			return nil, err
		}
		sb = sb.Clone()
		sb.Active = sb.Statuses[r.Mailbox]
	}
	if held != nil {
		if msg, part, err = messagePart(held.buf, r.Mailbox, r.UID, partPath, true); err != nil {
			return nil, err
		}
		permanentFlags = held.permanent
		authResults = readAuthResults(messageRootHeader(msg), trusted)
		// The peek left it unread on the server; the page is what reads it.
		if !msg.HasFlag(imap.FlagSeen) {
			p.markHeldSeen(r.Login, r.Mailbox, r.UID, held.validity)
		}
	}
	sent := sentFolder(sb.Mailboxes)
	needsThread := msg != nil && (len(msg.Envelope.InReplyTo) > 0 || msg.References != "" || (sent != "" && r.Mailbox == sent))
	deferContext := held != nil && sb.Active != nil && r.Partial && (!placed || needsThread || r.Merged)
	if cached != nil {
		threadAlgorithm = cached.threadAlgorithm
	}
	if held == nil || sb.Active == nil || (!deferContext && (!placed || needsThread)) {
		err = r.Login.DoIMAPContext(ctx, alborz.RoundTripTimeout, func(c *imapclient.Client) error {
			var load *sidebarLoad
			var err error
			if sb.Active == nil {
				if load, err = startSidebar(c, r.Mailbox, r.Mailbox, r.Settings.Subscriptions); err != nil {
					return err
				}
			}
			if held == nil {
				if msg, part, err = getShownPart(c, r.Mailbox, r.UID, partPath); err != nil {
					return err
				}
				// The cached listings are marked read rather than fetched again for one flag.
				p.messageRead(r.Login.Username(), r.Mailbox, r.UID)
				permanentFlags = c.Mailbox().PermanentFlags
			}
			if !placed {
				if place, err = p.placeInList(c, r.Login.Username(), r.Settings, r.Mailbox, r.UID, r.List); err != nil {
					return err
				}
			}
			threadAlgorithm = ThreadAlgorithm(c)
			if load != nil {
				sent = sentFolder(load.sb.Mailboxes)
			}
			inReplyTo, answers = readThread(p.server.Logger(), c, r.Mailbox, sent, msg)
			if held == nil {
				authResults = readAuthResults(messageRootHeader(msg), trusted)
			}
			if load != nil {
				sb, err = load.finish()
			}
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	// Not in Junk: a message filed as junk needs no telling.
	var indicators []Indicator
	if folderRole(sb.Mailboxes, r.Mailbox) != "junk" {
		evidence := &Evidence{
			Header:     messageRootHeader(msg),
			Trusted:    trusted,
			Auth:       authResults,
			Subject:    msg.Envelope.Subject,
			From:       envelopeSender(msg.Envelope),
			MoneyWords: r.MoneyWords,
		}
		if evidence.From != "" {
			// Every signed-in account's inbox is asked: an imitation may reach a different address.
			for _, s := range r.Logins {
				if book := p.senderBookFor(s); book != nil && evidence.Named == nil {
					evidence.Named = book.named(msg.Envelope.From[0].Name, evidence.From)
				}
			}
		}
		indicators = Indicators(evidence)
	}
	var warnings []Indicator
	mark := Mark(indicators, msg.NotJunk())
	if mark != Fact {
		warnings = Warnings(indicators)
	}
	trust := p.newDeliveryTrust(r.Login, r.Settings)
	// The alias shows only where To and Cc do not already name it.
	deliveredTo := trust.alias(msg)
	if addressed(deliveredTo, msg.Envelope.To, msg.Envelope.Cc) {
		deliveredTo = ""
	}
	return &Opened{Message: msg, Part: part, PartPath: partPath, Rail: sb, PermanentFlags: permanentFlags,
		AuthResults: authResults, InReplyTo: inReplyTo, Answers: answers,
		ThreadAlgorithm: threadAlgorithm, Place: place, DeferContext: deferContext, Warnings: warnings, Mark: mark,
		DeliveredTo: deliveredTo, ForwardedBy: ForwardedBy(msg.rootHeader, trusted, msg.ListID),
		Unsubscribe: unsubscribeMail(r.Settings, trust, msg)}, nil
}

// Body is the part a message's page shows: the HTML its pieces make whole, and read as words where
// asText asks it of an HTML part; asText says whether it was.
func Body(ctx context.Context, login *alborz.Login, msg *IMAPMessage, part *message.Entity, path []int, asText bool) (*message.Entity, bool, error) {
	mimeType, _, err := part.Header.ContentType()
	if err != nil {
		return nil, false, fmt.Errorf("failed to parse part Content-Type: %v", err)
	}
	if pieces := msg.HTMLPieces(path); pieces != nil {
		err := login.DoIMAPContext(ctx, alborz.RoundTripTimeout, func(c *imapclient.Client) error {
			var err error
			part, err = joinedHTML(c, msg.Mailbox, msg.UID, pieces)
			return err
		})
		if err != nil {
			return nil, false, err
		}
	}
	asText = asText && strings.EqualFold(mimeType, "text/html")
	if asText {
		if part, err = htmlAsText(part); err != nil {
			return nil, false, err
		}
	}
	return part, asText, nil
}
