package alborzbase

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message"
	"github.com/emersion/go-message/textproto"
)

type MailboxInfo struct {
	*imap.ListData

	Active bool
	Total  int
	Unseen int

	// Display name: the special-use folders translate, custom folders
	// keep their real names; the wire name stays in URLs and values.
	Label string
}

// CrumbLink is one step of the path a page shows above itself: what it
// is called and where it goes. A step with no URL is named but not
// linked, which is what a folder that cannot be selected deserves.
type CrumbLink struct {
	Label string
	URL   string
}

// mailboxCrumb names the path to a folder, each step leading to it. The
// account is the first step and leads to its inbox, because that is
// where "back" means for mail.
//
// A step is named by its own path segment, not by the folder's Label:
// a custom folder's label is its whole wire name, which would repeat
// the path at every step. A special-use folder is the exception worth
// making, so the first step reads Inbox rather than INBOX. A segment
// the folder list does not hold, or holds as a parent that cannot be
// selected, is named but not linked.
func mailboxCrumb(mailboxes []MailboxInfo, name, account string) []CrumbLink {
	crumb := []CrumbLink{{Label: account, URL: "/mailbox/INBOX"}}
	if name == "" {
		return crumb
	}

	byName := make(map[string]*MailboxInfo, len(mailboxes))
	delim := rune(0)
	for i := range mailboxes {
		byName[mailboxes[i].Name()] = &mailboxes[i]
		if delim == 0 {
			delim = mailboxes[i].Delim
		}
	}

	parts := []string{name}
	if delim != 0 {
		parts = strings.Split(name, string(delim))
	}
	for i := range parts {
		step := CrumbLink{Label: parts[i]}
		path := strings.Join(parts[:i+1], string(delim))
		if mbox, ok := byName[path]; ok {
			if mbox.role() != "" {
				step.Label = mbox.Label
			}
			if !slices.Contains(mbox.Attrs, imap.MailboxAttrNoSelect) {
				step.URL = mbox.URL().String()
			}
		}
		crumb = append(crumb, step)
	}
	return crumb
}

// role names the special-use category, empty for custom folders. It
// checks the RFC 6154 attributes first, then the conventional names.
func (mbox *MailboxInfo) role() string {
	name := mbox.Mailbox
	switch {
	case name == "INBOX":
		return "inbox"
	case mbox.HasAttr(string(imap.MailboxAttrDrafts)) || name == "Drafts" || name == "Draft":
		return "drafts"
	case mbox.HasAttr(string(imap.MailboxAttrSent)) || name == "Sent":
		return "sent"
	case mbox.HasAttr(string(imap.MailboxAttrJunk)) || name == "Junk" || name == "Spam":
		return "junk"
	case mbox.HasAttr(string(imap.MailboxAttrTrash)) || name == "Trash":
		return "trash"
	case mbox.HasAttr(string(imap.MailboxAttrArchive)) || name == "Archive":
		return "archive"
	}
	return ""
}

func (mbox *MailboxInfo) Name() string {
	return mbox.Mailbox
}

func (mbox *MailboxInfo) URL() *url.URL {
	return mailboxPageURL(mbox.Name())
}

// mailboxPageURL holds the decoded name in Path and the escaping in
// RawPath, so a folder with a slash in its name is not encoded twice
// by String.
func mailboxPageURL(name string) *url.URL {
	return &url.URL{
		Path:    "/mailbox/" + name,
		RawPath: "/mailbox/" + url.PathEscape(name),
	}
}

func (mbox *MailboxInfo) HasAttr(flag string) bool {
	for _, attr := range mbox.Attrs {
		if string(attr) == flag {
			return true
		}
	}
	return false
}

func (mbox *MailboxInfo) IsInternal() bool {
	if strings.HasPrefix(mbox.Mailbox, ".") {
		return true
	}
	return slices.Contains(mbox.Attrs, imap.MailboxAttrNoSelect)
}

// startListMailboxes issues the mailbox LIST without draining it, so the
// caller can pipeline further commands behind it on the same connection and
// pay one round trip for all of them. Counts come from a scoped STATUS pass
// instead of LIST-STATUS, so the server is not asked to count every folder.
func startListMailboxes(conn *imapclient.Client) *imapclient.ListCommand {
	return conn.List("", "*", nil)
}

// finishListMailboxes drains a command from startListMailboxes into the
// sorted mailbox list.
func finishListMailboxes(list *imapclient.ListCommand) ([]MailboxInfo, error) {
	var mailboxes []MailboxInfo
	for {
		data := list.Next()
		if data == nil {
			break
		}
		mbox := MailboxInfo{ListData: data, Total: -1, Unseen: -1}
		if mbox.Status != nil {
			mbox.Unseen = int(*mbox.Status.NumUnseen)
			mbox.Total = int(*mbox.Status.NumMessages)
		}
		mailboxes = append(mailboxes, mbox)
	}
	if err := list.Close(); err != nil {
		return nil, fmt.Errorf("failed to list mailboxes: %v", err)
	}

	sort.Slice(mailboxes, func(i, j int) bool {
		if mailboxes[i].Mailbox == "INBOX" {
			return true
		}
		if mailboxes[j].Mailbox == "INBOX" {
			return false
		}
		return mailboxes[i].Mailbox < mailboxes[j].Mailbox
	})
	return mailboxes, nil
}

func listMailboxes(conn *imapclient.Client) ([]MailboxInfo, error) {
	return finishListMailboxes(startListMailboxes(conn))
}

type MailboxStatus struct {
	*imap.StatusData

	// Display name, like MailboxInfo.Label
	Label string
}

func (mbox *MailboxStatus) Name() string {
	return mbox.Mailbox
}

// IsEmpty reports a folder the server says holds nothing, so the
// controls that would clear it can say so before they are pressed. An
// absent count means the server did not answer it; the folder is then
// treated as holding something rather than blocking the action.
func (mbox *MailboxStatus) IsEmpty() bool {
	return mbox.NumMessages != nil && *mbox.NumMessages == 0
}

func (mbox *MailboxStatus) URL() *url.URL {
	return mailboxPageURL(mbox.Name())
}

// getMailboxByRole finds the account's folder for a special-use role
// through the same classifier the sidebar labels use.
func getMailboxByRole(conn *imapclient.Client, role string) (*MailboxInfo, error) {
	mailboxes, err := listMailboxes(conn)
	if err != nil {
		return nil, err
	}
	for i := range mailboxes {
		if mailboxes[i].role() == role {
			return &mailboxes[i], nil
		}
	}
	return nil, nil
}

func ensureMailboxSelected(conn *imapclient.Client, mboxName string) error {
	if mbox := conn.Mailbox(); mbox == nil || mbox.Name != mboxName {
		if _, err := conn.Select(mboxName, nil).Wait(); err != nil {
			return fmt.Errorf("failed to select mailbox: %v", err)
		}
	}
	return nil
}

type IMAPMessage struct {
	*imapclient.FetchMessageBuffer

	Mailbox string

	// Account owning the message, set only in the unified view
	Account string

	// Mailing-list addresses from the whole message's header, empty
	// unless it was fetched: a part's own header does not carry them.
	ListPost string
	// ListUnsubscribe is where the page should send the reader: our own
	// compose form when the list offers only a mailto, the list's own
	// page otherwise.
	ListUnsubscribe string
	// OneClick is the https endpoint a list promises to honour on a bare
	// POST (RFC 8058). It is posted by us, from the server: the browser
	// cannot, and a GET is what link scanners fire by accident, which is
	// the reason the POST exists.
	OneClick string
	// rootHeader is the whole message's header, kept because a part's
	// own header does not carry Autocrypt any more than it carries
	// List-Post. Empty unless the message was fetched with it.
	rootHeader textproto.Header
	// References is the thread chain the message carries. ENVELOPE does
	// not include it, so it comes from the header like the list ones.
	References string
	// ListID names the list, without the angle brackets and without the
	// description some senders put before them. It is what says a
	// message is list mail at all: List-Post can be absent from a list
	// that refuses posts, and present on nothing else.
	ListID string
	// ListHelp, ListSubscribe, ListOwner and ListArchive are the rest of
	// RFC 2369. A list that offers them is saying where to ask, how to
	// join, who runs it and where the past is kept.
	ListHelp      string
	ListSubscribe string
	ListOwner     string
	ListArchive   string
	// Depth is how deep in its thread a row sits, 0 for the message that
	// started one. Only the threaded view sets it.
	Depth int
	// Alias is the address this message was delivered to when that is
	// not the account it landed in and the delivery path is worth
	// believing. Set by the route, which knows the served domains and
	// what the reader trusts; empty means show nothing.
	Alias string
	// DeliveredTo is every address the delivery path recorded, topmost
	// header first. Mail to an alias often names the alias nowhere
	// else: the envelope carried it, To and Cc hold the list or the
	// original recipient, and only the delivering server writes it down.
	DeliveredTo []string
}

// Date is when the message is shown to have arrived. A Date header is
// written by the sender and is not always a date at all - SourceHut
// sends a bare "2026-08-27", which parses to the zero time and once
// rendered as "2025 years ago". The server's own INTERNALDATE stands in
// then: every server keeps one, and no sender can break it.
func (msg *IMAPMessage) Date() time.Time {
	if msg.Envelope != nil && !msg.Envelope.Date.IsZero() {
		return msg.Envelope.Date
	}
	return msg.InternalDate
}

// firstListURI returns the first bracketed URI of an RFC 2369 header
// whose scheme is wanted, or the first of any scheme when wanted is
// empty. Such a header is a comma-separated list of <URI> entries.
func firstListURI(value string, wanted ...string) string {
	for _, field := range strings.Split(value, ",") {
		field = strings.TrimSpace(field)
		if !strings.HasPrefix(field, "<") || !strings.HasSuffix(field, ">") {
			continue
		}
		uri := field[1 : len(field)-1]
		if len(wanted) == 0 {
			return uri
		}
		for _, scheme := range wanted {
			if strings.HasPrefix(strings.ToLower(uri), scheme+":") {
				return uri
			}
		}
	}
	return ""
}

// composeFromMailto turns a mailto: URI into a link to our own compose
// form. A webmail that hands the browser a mailto opens whatever mail
// app the machine has - which is not the account the message arrived
// in, and on a phone is not alborz at all. RFC 6068 puts the address
// before the query and the rest in it; only the fields compose already
// speaks are carried, and the person still reads the message before
// sending it.
func composeFromMailto(uri string) string {
	if !strings.HasPrefix(strings.ToLower(uri), "mailto:") {
		return uri
	}
	u, err := url.Parse(uri)
	if err != nil {
		return ""
	}
	q := url.Values{}
	if to := u.Opaque; to != "" {
		q.Set("to", to)
	}
	for _, field := range []string{"subject", "body"} {
		if v := u.Query().Get(field); v != "" {
			q.Set(field, v)
		}
	}
	if q.Get("to") == "" {
		return ""
	}
	return "/compose?" + alborz.AddressQuery(q)
}

// deliveryHeaders are the ways an MTA writes down which address a
// message was delivered to. There is no standard one: Postfix writes
// X-Original-To for the address that was aliased and Delivered-To for
// the mailbox it landed in, Exim writes Envelope-To, and others write
// X-Envelope-To. All of them are collected rather than ranked, because
// which one holds the alias depends on the server rather than on the
// mail, and the caller is matching against a known set of identities.
var deliveryHeaders = []string{
	"x-original-to",
	"x-envelope-to",
	"envelope-to",
	"delivered-to",
	"x-delivered-to",
	"x-forwarded-to",
	"x-rcptto",
}

// receivedFor matches the "for <addr>" clause of a Received header,
// which is the envelope recipient written by the receiving server. It
// is the one place the address appears when no MTA on the path added a
// header of its own.
var receivedFor = regexp.MustCompile(`(?i)\bfor\s+<([^>]+)>`)

// deliveryAddresses gathers the addresses the delivery path named, in
// the order the header carries them: a hop prepends what it writes, so
// the topmost entries are the most recent and the bottom ones were in
// the message before anyone but the sender had touched it.
//
// cut is how many of them our own server is answerable for - those
// above the Received naming authserv. It is len(addrs) when authserv is
// empty, because without a name there is nothing to measure against,
// and 0 when a name is given and no Received carries it: a message that
// cannot be judged is not believed.
func deliveryAddresses(h textproto.Header, authserv string) (addrs []string, cut int) {
	seen := map[string]bool{}
	add := func(v string) {
		v = strings.Trim(strings.TrimSpace(v), "<>")
		if v == "" || !strings.Contains(v, "@") {
			return
		}
		if parsed, err := mail.ParseAddress(v); err == nil {
			v = parsed.Address
		}
		key := strings.ToLower(v)
		if seen[key] {
			return
		}
		seen[key] = true
		addrs = append(addrs, v)
	}

	want := strings.ToLower(authserv)
	ended := false
	for fields := h.Fields(); fields.Next(); {
		key := strings.ToLower(fields.Key())
		if key == "received" {
			// Our own server writes its delivery headers around its
			// Received, not only above it, so the boundary is the first
			// Received belonging to somebody else: from there down the
			// message is as an earlier hop handed it over.
			if want != "" && !ended && !strings.Contains(strings.ToLower(fields.Value()), want) {
				cut, ended = len(addrs), true
			}
			if m := receivedFor.FindStringSubmatch(fields.Value()); m != nil {
				add(m[1])
			}
			continue
		}
		if slices.Contains(deliveryHeaders, key) {
			add(fields.Value())
		}
	}
	if want == "" || !ended {
		cut = len(addrs)
	}
	return addrs, cut
}

// listAction turns one of RFC 2369's action headers into somewhere the
// page can send the reader: the list's own page when it offers one, and
// our compose form when it offers only a mailto, for the same reason
// unsubscribe does.
func listAction(value string) string {
	if uri := firstListURI(value, "https", "http"); uri != "" {
		return uri
	}
	return composeFromMailto(firstListURI(value, "mailto"))
}

// setListHeaders records the mailing-list addresses a message carries.
// List-Post: NO means the list refuses posts, which is not an address.
func (msg *IMAPMessage) setListHeaders(h textproto.Header) {
	msg.rootHeader = h
	msg.References = strings.Join(strings.Fields(h.Get("References")), " ")
	msg.DeliveredTo, _ = deliveryAddresses(h, "")
	msg.ListHelp = listAction(h.Get("List-Help"))
	msg.ListSubscribe = listAction(h.Get("List-Subscribe"))
	msg.ListOwner = listAction(h.Get("List-Owner"))
	msg.ListArchive = firstListURI(h.Get("List-Archive"), "https", "http")
	if post := firstListURI(h.Get("List-Post"), "mailto"); post != "" {
		msg.ListPost = strings.TrimPrefix(post, "mailto:")
	}
	msg.ListID = listID(h)
	// A one-click https endpoint is preferred; a mailto asks the user to
	// send a message, which they can at least read before sending. Plain
	// http is not offered: the POST is made from here, and only over a
	// connection the list cannot be impersonated on.
	msg.ListUnsubscribe = firstListURI(h.Get("List-Unsubscribe"), "https")
	// RFC 8058: the promise is in a header of its own, and without it an
	// https URI is a page to visit rather than an endpoint to post to.
	if strings.Contains(strings.ToLower(h.Get("List-Unsubscribe-Post")), "one-click") {
		msg.OneClick = msg.ListUnsubscribe
	}
	if msg.ListUnsubscribe == "" {
		msg.ListUnsubscribe = composeFromMailto(firstListURI(h.Get("List-Unsubscribe"), "mailto"))
	}
}

func (msg *IMAPMessage) URL() *url.URL {
	return messageURL(msg.Mailbox, msg.UID)
}

// messageURL returns nil for the zero UID so templates can elide the link.
func messageURL(mboxName string, uid imap.UID) *url.URL {
	if uid == 0 {
		return nil
	}
	return &url.URL{
		Path:    fmt.Sprintf("/message/%v/%v", mboxName, uid),
		RawPath: fmt.Sprintf("/message/%v/%v", url.PathEscape(mboxName), uid),
	}
}

func newIMAPPartNode(msg *IMAPMessage, path []int, part imap.BodyStructure) *IMAPPartNode {
	node := &IMAPPartNode{
		Path:     path,
		MIMEType: part.MediaType(),
		Message:  msg,
	}
	if singlePart, ok := part.(*imap.BodyStructureSinglePart); ok {
		node.Filename = singlePart.Filename()
		node.Size = singlePart.Size
	}
	return node
}

func (msg *IMAPMessage) TextPart() *IMAPPartNode {
	if msg.BodyStructure == nil {
		return nil
	}

	var best *IMAPPartNode
	isTextPlain := false
	msg.BodyStructure.Walk(func(path []int, part imap.BodyStructure) bool {
		singlePart, ok := part.(*imap.BodyStructureSinglePart)
		if !ok {
			return true
		}

		if !strings.EqualFold(singlePart.Type, "text") {
			return true
		}
		if disp := singlePart.Disposition(); disp != nil && !strings.EqualFold(disp.Value, "inline") {
			return true
		}

		switch strings.ToLower(singlePart.Subtype) {
		case "plain":
			isTextPlain = true
			best = newIMAPPartNode(msg, path, singlePart)
		case "html":
			if !isTextPlain {
				best = newIMAPPartNode(msg, path, singlePart)
			}
		}
		return true
	})

	return best
}

// PreferredPart is the part a message opens at. Plain text unless the
// account asked for the other order, and either way whatever the message
// actually has.
func (msg *IMAPMessage) PreferredPart(preferHTML bool) *IMAPPartNode {
	if preferHTML {
		if html := msg.HTMLPart(); html != nil {
			return html
		}
	}
	if text := msg.TextPart(); text != nil {
		return text
	}
	return msg.HTMLPart()
}

func (msg *IMAPMessage) HTMLPart() *IMAPPartNode {
	if msg.BodyStructure == nil {
		return nil
	}

	var best *IMAPPartNode
	msg.BodyStructure.Walk(func(path []int, part imap.BodyStructure) bool {
		singlePart, ok := part.(*imap.BodyStructureSinglePart)
		if !ok {
			return true
		}

		if !strings.EqualFold(singlePart.Type, "text") {
			return true
		}
		if disp := singlePart.Disposition(); disp != nil && !strings.EqualFold(disp.Value, "inline") {
			return true
		}

		if singlePart.Subtype == "html" {
			best = newIMAPPartNode(msg, path, singlePart)
		}
		return true
	})

	return best
}

func (msg *IMAPMessage) Attachments() []IMAPPartNode {
	if msg.BodyStructure == nil {
		return nil
	}

	var attachments []IMAPPartNode
	msg.BodyStructure.Walk(func(path []int, part imap.BodyStructure) bool {
		singlePart, ok := part.(*imap.BodyStructureSinglePart)
		if !ok {
			return true
		}

		if disp := singlePart.Disposition(); disp == nil || !strings.EqualFold(disp.Value, "attachment") {
			return true
		}

		attachments = append(attachments, *newIMAPPartNode(msg, path, singlePart))
		return true
	})
	return attachments
}

func pathsEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (msg *IMAPMessage) PartByPath(path []int) *IMAPPartNode {
	if msg.BodyStructure == nil {
		return nil
	}
	if len(path) == 0 {
		return newIMAPPartNode(msg, nil, msg.BodyStructure)
	}

	var result *IMAPPartNode
	msg.BodyStructure.Walk(func(p []int, part imap.BodyStructure) bool {
		if result == nil && pathsEqual(path, p) {
			result = newIMAPPartNode(msg, p, part)
		}
		return result == nil
	})
	return result
}

func (msg *IMAPMessage) PartByID(id string) *IMAPPartNode {
	if msg.BodyStructure == nil || id == "" {
		return nil
	}

	var result *IMAPPartNode
	msg.BodyStructure.Walk(func(path []int, part imap.BodyStructure) bool {
		singlePart, ok := part.(*imap.BodyStructureSinglePart)
		if !ok {
			return result == nil
		}
		if result == nil && singlePart.ID == "<"+id+">" {
			result = newIMAPPartNode(msg, path, singlePart)
		}
		return result == nil
	})
	return result
}

type IMAPPartNode struct {
	Path     []int
	MIMEType string
	Filename string
	Children []IMAPPartNode
	Message  *IMAPMessage
	Size     uint32
}

func (node IMAPPartNode) PathString() string {
	l := make([]string, len(node.Path))
	for i, partNum := range node.Path {
		l[i] = strconv.Itoa(partNum)
	}
	return strings.Join(l, ".")
}

func (node IMAPPartNode) SizeString() string {
	return formatSize(int64(node.Size))
}

// formatSize writes a byte count the way every size on a page reads:
// the list column and the attachment rows the same.
func formatSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func (node IMAPPartNode) URL(raw bool) *url.URL {
	u := node.Message.URL()
	// Both halves or neither: url.URL uses RawPath only while it is a
	// valid encoding of Path, so appending to one alone silently drops
	// the escaping and a mailbox named INBOX/Lists became two path
	// segments. Every download of a part in a nested folder 404d.
	if raw {
		u.Path += "/raw"
		u.RawPath += "/raw"
	}
	q := u.Query()
	q.Set("part", node.PathString())
	u.RawQuery = q.Encode()
	return u
}

// IsText reports a part the page itself can show. Not every text/* part
// is one: a calendar, a card and a spreadsheet are text by MIME type and
// data by intent, and sending them to the viewer means an attachment
// that has no way to be saved - which is what an attachment is for.
//
// Only what the view plugins render belongs here.
func (node IMAPPartNode) IsText() bool {
	switch strings.ToLower(node.MIMEType) {
	case "text/plain", "text/html":
		return true
	}
	return false
}

func (node IMAPPartNode) String() string {
	if node.Filename != "" {
		return fmt.Sprintf("%s (%s)", node.Filename, node.MIMEType)
	} else {
		return node.MIMEType
	}
}

func imapPartTree(msg *IMAPMessage, bs imap.BodyStructure, path []int) *IMAPPartNode {
	node := &IMAPPartNode{
		Path:     path,
		MIMEType: bs.MediaType(),
		Message:  msg,
	}

	switch bs := bs.(type) {
	case *imap.BodyStructureMultiPart:
		for i, part := range bs.Children {
			num := i + 1

			partPath := append([]int(nil), path...)
			partPath = append(partPath, num)

			node.Children = append(node.Children, *imapPartTree(msg, part, partPath))
		}
	case *imap.BodyStructureSinglePart:
		if len(path) == 0 {
			node.Path = []int{1}
		}
		node.Filename = bs.Filename()
		node.Size = bs.Size
	}

	return node
}

func (msg *IMAPMessage) PartTree() *IMAPPartNode {
	if msg.BodyStructure == nil {
		return nil
	}

	return imapPartTree(msg, msg.BodyStructure, nil)
}

// flagColorBits are the three keywords Apple Mail and Outlook agree to
// read as a bit field beside \Flagged, low bit first. A flag is only
// coloured when \Flagged is set too; the bits alone mean nothing.
var flagColorBits = [3]imap.Flag{"$MailFlagBit0", "$MailFlagBit1", "$MailFlagBit2"}

// FlagColors names the seven colours those three bits address, in the
// order the two clients number them. The index is the value of the bit
// field, so FlagColors[0] is what a plain \Flagged shows as.
// The bit field's zero is "flagged, no colour bits", which is the state
// a plain click leaves and the state every other client sets. It wears
// the star's own gold here; Apple draws that same state red.
var FlagColors = [7]string{"gold", "orange", "yellow", "green", "blue", "purple", "grey"}

// FlagColor is the colour this message is flagged in, empty when it
// carries no flag at all. A bit field naming no colour we know (the one
// unused value) reads as the plain flag rather than as nothing.
func (msg *IMAPMessage) FlagColor() string {
	if !msg.HasFlag(imap.FlagFlagged) {
		return ""
	}
	n := 0
	for i, bit := range flagColorBits {
		if msg.HasFlag(bit) {
			n |= 1 << i
		}
	}
	if n >= len(FlagColors) {
		n = 0
	}
	return FlagColors[n]
}

// FlagColorOption is one colour offered by a picker: what to post, and
// what to call it in the page's language.
type FlagColorOption struct {
	Name  string
	Label string
}

// FlagColorFlags turns a colour name into the flags a message must
// carry to wear it, and the ones it must not. An empty name unflags.
func FlagColorFlags(color string) (add, del []imap.Flag) {
	if color == "" {
		return nil, append([]imap.Flag{imap.FlagFlagged}, flagColorBits[:]...)
	}
	n := -1
	for i, c := range FlagColors {
		if c == color {
			n = i
		}
	}
	if n < 0 {
		return nil, nil
	}
	add = []imap.Flag{imap.FlagFlagged}
	for i, bit := range flagColorBits {
		if n&(1<<i) != 0 {
			add = append(add, bit)
		} else {
			del = append(del, bit)
		}
	}
	return add, del
}

// HasFlag reports whether the message carries a flag. Flags are
// case-insensitive (RFC 9051 2.3.2) and servers differ on what they
// give back: Dovecot keeps the case a keyword was stored in, others
// fold it, so an exact comparison finds $MailFlagBit0 on one server and
// misses it on the next.
func (msg *IMAPMessage) HasFlag(flag imap.Flag) bool {
	for _, f := range msg.Flags {
		if strings.EqualFold(string(f), string(flag)) {
			return true
		}
	}
	return false
}

// listHeaderItem is the small header fetch a row needs on top of its
// envelope: which address the message was delivered to, and whether it
// came from a list. HEADER.FIELDS is a handful of short lines rather
// than the whole header, and it rides the round trip the row already
// costs.
//
// Received is asked for as well, and it is the expensive part - a
// message carries several and they are long. It is here because a
// delivery header is only worth believing when our own server wrote it,
// and the Received lines are what says where in the message's life a
// header appeared. A row that cannot judge that would have to show an
// address the sender could have written.
func listHeaderItem() *imap.FetchItemBodySection {
	return &imap.FetchItemBodySection{
		Peek:         true,
		Specifier:    imap.PartSpecifierHeader,
		HeaderFields: append(slices.Clone(deliveryHeaders), "List-Id", "Received"),
	}
}

// listFetchOptions is what every row in every list needs, in one place
// so a column added to one list is not missing from the other.
func listFetchOptions(header *imap.FetchItemBodySection) *imap.FetchOptions {
	return &imap.FetchOptions{
		Envelope:      true,
		Flags:         true,
		UID:           true,
		RFC822Size:    true,
		InternalDate:  true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
		BodySection:   []*imap.FetchItemBodySection{header},
	}
}

// setRowHeaders fills in what the small header fetch answered. A server
// that returned nothing leaves the row without its marks rather than
// failing the listing.
func (msg *IMAPMessage) setRowHeaders(buf []byte) {
	if len(buf) == 0 {
		return
	}
	h, err := textproto.ReadHeader(bufio.NewReader(bytes.NewReader(buf)))
	if err != nil {
		return
	}
	// The header is kept, not just read: deciding which delivery
	// addresses to believe needs to know where each one sat.
	msg.rootHeader = h
	msg.DeliveredTo, _ = deliveryAddresses(h, "")
	msg.ListID = listID(h)
}

// messageListID reads one message's List-Id and nothing else. It exists
// so a decision that belongs to the server - whether a reply is going to
// a list - is answered by asking the message, rather than by trusting a
// field the page put in the form when it was rendered.
func messageListID(conn *imapclient.Client, mboxName string, uid imap.UID) (string, error) {
	if err := ensureMailboxSelected(conn, mboxName); err != nil {
		return "", err
	}
	section := &imap.FetchItemBodySection{
		Peek:         true,
		Specifier:    imap.PartSpecifierHeader,
		HeaderFields: []string{"List-Id"},
	}
	msgs, err := conn.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{
		BodySection: []*imap.FetchItemBodySection{section},
	}).Collect()
	if err != nil {
		return "", err
	}
	if len(msgs) == 0 {
		return "", nil
	}
	h, err := textproto.ReadHeader(bufio.NewReader(bytes.NewReader(msgs[0].FindBodySection(section))))
	if err != nil {
		return "", err
	}
	return listID(h), nil
}

// listID is the identifier out of RFC 2919's List-Id: an optional
// phrase, then the identifier in angle brackets. The phrase is the
// sender's prose and not a name we can match on, so only the identifier
// is kept.
func listID(h textproto.Header) string {
	id := h.Get("List-Id")
	if id == "" {
		return ""
	}
	if i := strings.LastIndex(id, "<"); i >= 0 {
		id = strings.TrimSuffix(id[i+1:], ">")
	}
	return strings.TrimSpace(id)
}

func listMessages(conn *imapclient.Client, mboxName string, page, messagesPerPage int) (msgs []IMAPMessage, total int, err error) {
	// A fresh SELECT already reports the message count; only an already
	// selected mailbox needs a NOOP to notice new mail.
	if mbox := conn.Mailbox(); mbox != nil && mbox.Name == mboxName {
		if err := conn.Noop().Wait(); err != nil {
			return nil, 0, err
		}
	} else if _, err := conn.Select(mboxName, nil).Wait(); err != nil {
		return nil, 0, fmt.Errorf("failed to select mailbox: %v", err)
	}

	// A selected mailbox can still be gone by the time it is read: a
	// connection that dropped between the SELECT and here reports none,
	// and a listing goroutine panicking takes the whole server down.
	mbox := conn.Mailbox()
	if mbox == nil {
		return nil, 0, fmt.Errorf("mailbox %q is no longer selected", mboxName)
	}
	total = int(mbox.NumMessages)

	to := total - page*messagesPerPage
	from := to - messagesPerPage + 1
	if from <= 0 {
		from = 1
	}
	if to <= 0 {
		return nil, total, nil
	}

	var seqSet imap.SeqSet
	seqSet.AddRange(uint32(from), uint32(to))
	header := listHeaderItem()
	imapMsgs, err := conn.Fetch(seqSet, listFetchOptions(header)).Collect()
	if err != nil {
		return nil, 0, fmt.Errorf("failed to fetch message list: %v", err)
	}

	for _, msg := range imapMsgs {
		row := IMAPMessage{FetchMessageBuffer: msg, Mailbox: mboxName}
		row.setRowHeaders(msg.FindBodySection(header))
		msgs = append(msgs, row)
	}

	// Reverse list of messages
	for i := len(msgs)/2 - 1; i >= 0; i-- {
		opp := len(msgs) - 1 - i
		msgs[i], msgs[opp] = msgs[opp], msgs[i]
	}

	return msgs, total, nil
}

// recentEnvelopes fetches the envelopes of the last n messages in a
// mailbox and nothing besides. The listing fetch carries flags, a body
// structure and a header section as well, which is a great many bytes
// over a couple of hundred messages for a caller that only reads
// addresses off the envelope.
func recentEnvelopes(conn *imapclient.Client, mboxName string, n int) ([]*imapclient.FetchMessageBuffer, error) {
	data, err := conn.Select(mboxName, nil).Wait()
	if err != nil {
		return nil, err
	}
	total := int(data.NumMessages)
	if total == 0 {
		return nil, nil
	}
	from := total - n + 1
	if from < 1 {
		from = 1
	}
	var seqSet imap.SeqSet
	seqSet.AddRange(uint32(from), uint32(total))
	return conn.Fetch(seqSet, &imap.FetchOptions{Envelope: true}).Collect()
}

// ThreadAlgorithm is the algorithm the server offers, or "" when it
// offers none. REFERENCES follows the reply chain and is what a mailing
// list needs; ORDEREDSUBJECT only groups by subject and is taken when
// it is all there is.
func ThreadAlgorithm(conn *imapclient.Client) imap.ThreadAlgorithm {
	have := conn.Caps().ThreadAlgorithms()
	if slices.Contains(have, imap.ThreadReferences) {
		return imap.ThreadReferences
	}
	if slices.Contains(have, imap.ThreadOrderedSubject) {
		return imap.ThreadOrderedSubject
	}
	return ""
}

// threadRow is one message's place in a thread: which message, and how
// far in from the one that started it.
type threadRow struct {
	uid   imap.UID
	depth int
}

// flattenThread walks one thread depth first, which is the order it
// reads in: a reply sits under what it answers.
func flattenThread(data imapclient.ThreadData, depth int, out []threadRow) []threadRow {
	for _, num := range data.Chain {
		out = append(out, threadRow{uid: imap.UID(num), depth: depth})
		depth++
	}
	for _, sub := range data.SubThreads {
		out = flattenThread(sub, depth, out)
	}
	return out
}

// threadMessages returns one page of threads rather than one page of
// messages: a conversation split across a page boundary is worse than a
// page of uneven length, and the thread is the thing being read.
//
// The server is asked for the whole set because THREAD has no window of
// its own; only the page's own messages are then fetched.
func threadMessages(conn *imapclient.Client, mboxName string, algorithm imap.ThreadAlgorithm, criteria *imap.SearchCriteria, page, threadsPerPage int) (msgs []IMAPMessage, totalThreads int, err error) {
	if err := ensureMailboxSelected(conn, mboxName); err != nil {
		return nil, 0, err
	}
	if criteria == nil {
		criteria = &imap.SearchCriteria{}
	}
	threads, err := conn.UIDThread(&imapclient.ThreadOptions{
		Algorithm:      algorithm,
		SearchCriteria: criteria,
	}).Wait()
	if err != nil {
		return nil, 0, fmt.Errorf("THREAD failed: %v", err)
	}

	// Newest conversation first, to match every other view. A thread's
	// age is its newest message, so a long-running one stays at the top
	// rather than sinking to where it started.
	rows := make([][]threadRow, 0, len(threads))
	for _, t := range threads {
		if flat := flattenThread(t, 0, nil); len(flat) > 0 {
			rows = append(rows, flat)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return newestUID(rows[i]) > newestUID(rows[j])
	})

	totalThreads = len(rows)
	from := page * threadsPerPage
	if from >= len(rows) {
		return nil, totalThreads, nil
	}
	to := min(from+threadsPerPage, len(rows))

	var want []threadRow
	for _, thread := range rows[from:to] {
		want = append(want, thread...)
	}
	if len(want) == 0 {
		return nil, totalThreads, nil
	}

	uids := make([]imap.UID, len(want))
	for i, r := range want {
		uids[i] = r.uid
	}
	header := listHeaderItem()
	fetched, err := conn.Fetch(imap.UIDSetNum(uids...), listFetchOptions(header)).Collect()
	if err != nil {
		return nil, 0, fmt.Errorf("failed to fetch thread page: %v", err)
	}
	return threadRows(fetched, want, mboxName, header), totalThreads, nil
}

// indexByUID lets fetched messages be put back in the order they were
// asked for, which a FETCH does not promise.
func indexByUID(msgs []*imapclient.FetchMessageBuffer) map[imap.UID]*imapclient.FetchMessageBuffer {
	byUID := make(map[imap.UID]*imapclient.FetchMessageBuffer, len(msgs))
	for _, m := range msgs {
		byUID[m.UID] = m
	}
	return byUID
}

// threadRows turns the fetched messages into rows in the order want
// lists them. A message expunged between the THREAD and the FETCH is
// simply not shown; the rest of the page still is.
func threadRows(fetched []*imapclient.FetchMessageBuffer, want []threadRow, mboxName string, header *imap.FetchItemBodySection) []IMAPMessage {
	byUID := indexByUID(fetched)
	var msgs []IMAPMessage
	for _, r := range want {
		buf, ok := byUID[r.uid]
		if !ok {
			continue
		}
		row := IMAPMessage{FetchMessageBuffer: buf, Mailbox: mboxName, Depth: r.depth}
		row.setRowHeaders(buf.FindBodySection(header))
		msgs = append(msgs, row)
	}
	return msgs
}

// answerLimit caps what one message's row will count. A message with
// forty answers is a thread view's problem.
const answerLimit = 12

// threadAnswers finds what answered this message, for a message being
// read in Sent. In-Reply-To names exactly one id, so this matches a
// whole header value rather than searching inside a list, which is the
// one direction of threading that does not depend on how well a server
// searches headers. Only direct answers: a reply to a reply names its
// own parent, not this message.
//
// INBOX is where ordinary correspondence lands, and it is the folder
// RFC 3501 guarantees exists. A reply that was filed elsewhere - a
// mailing list folder - is not found.
func threadAnswers(conn *imapclient.Client, mboxName string, msg *IMAPMessage) ([]ThreadNeighbour, error) {
	if msg.Envelope == nil || msg.Envelope.MessageID == "" {
		return nil, nil
	}
	if err := ensureMailboxSelected(conn, "INBOX"); err != nil {
		return nil, err
	}
	found, err := findByHeader(conn, "INBOX", "In-Reply-To", msg.Envelope.MessageID, answerLimit)
	if err != nil {
		return nil, err
	}
	if err := ensureMailboxSelected(conn, mboxName); err != nil {
		return nil, err
	}
	return found, nil
}

// oneThread returns just the conversation holding uid, in reading
// order. THREAD groups by message id rather than by matching header
// text, so this is the one way of naming a conversation that does not
// depend on how well a server searches headers.
func oneThread(conn *imapclient.Client, mboxName string, algorithm imap.ThreadAlgorithm, uid imap.UID) (msgs []IMAPMessage, err error) {
	if err := ensureMailboxSelected(conn, mboxName); err != nil {
		return nil, err
	}
	threads, err := conn.UIDThread(&imapclient.ThreadOptions{
		Algorithm:      algorithm,
		SearchCriteria: &imap.SearchCriteria{},
	}).Wait()
	if err != nil {
		return nil, fmt.Errorf("THREAD failed: %v", err)
	}

	var want []threadRow
	for _, t := range threads {
		flat := flattenThread(t, 0, nil)
		if slices.ContainsFunc(flat, func(r threadRow) bool { return r.uid == uid }) {
			want = flat
			break
		}
	}
	if len(want) == 0 {
		return nil, nil
	}

	uids := make([]imap.UID, len(want))
	for i, r := range want {
		uids[i] = r.uid
	}
	header := listHeaderItem()
	fetched, err := conn.Fetch(imap.UIDSetNum(uids...), listFetchOptions(header)).Collect()
	if err != nil {
		return nil, fmt.Errorf("failed to fetch thread: %v", err)
	}
	return threadRows(fetched, want, mboxName, header), nil
}

// newestUID stands in for a thread's age: UIDs rise with arrival, so the
// highest one in a thread is its most recent message.
func newestUID(rows []threadRow) imap.UID {
	var newest imap.UID
	for _, r := range rows {
		if r.uid > newest {
			newest = r.uid
		}
	}
	return newest
}

// ThreadNeighbour is another message in the same conversation, named
// well enough to link to.
type ThreadNeighbour struct {
	UID     imap.UID
	Subject string
	// From is who wrote it. On a list every message in a thread carries
	// the same subject, so the name is what tells them apart.
	From string
	URL  *url.URL
}

// threadParent finds what a message answers. It is one exact search:
// In-Reply-To names a single Message-Id. Nothing indexes the other
// direction, so counting the answers would mean scanning References
// across the whole folder, which is a search the row links to rather
// than one the page waits for.
//
// A failure here is not a failure of the page: a server that will not
// search headers shows the message without its parent.
func threadParent(conn *imapclient.Client, mboxName, sent string, msg *IMAPMessage) (*ThreadNeighbour, error) {
	if err := ensureMailboxSelected(conn, mboxName); err != nil {
		return nil, err
	}
	if msg.Envelope == nil {
		return nil, nil
	}

	// In-Reply-To names the parent when the sender wrote one. Some do
	// not, and then the last entry of References is the same answer:
	// that chain ends at what this message answers.
	parentID := ""
	if len(msg.Envelope.InReplyTo) > 0 {
		parentID = msg.Envelope.InReplyTo[0]
	} else if refs := strings.Fields(msg.References); len(refs) > 0 {
		parentID = strings.Trim(refs[len(refs)-1], "<>")
	}
	if parentID == "" {
		return nil, nil
	}
	found, err := findByHeader(conn, mboxName, "Message-Id", parentID, 1)
	if err != nil {
		return nil, err
	}
	// A message answering one of yours has its parent in Sent, which is
	// the case worth resolving most: it is your own half of the
	// conversation. Only looked for when the folder being read does not
	// hold it, so the ordinary reply still costs one search.
	if len(found) == 0 && sent != "" && sent != mboxName {
		// findByHeader searches whatever is selected; the name it takes
		// only labels the results.
		if err := ensureMailboxSelected(conn, sent); err != nil {
			return nil, err
		}
		found, err = findByHeader(conn, sent, "Message-Id", parentID, 1)
		if err != nil {
			return nil, err
		}
		if err := ensureMailboxSelected(conn, mboxName); err != nil {
			return nil, err
		}
	}
	if len(found) == 0 {
		return nil, nil
	}
	parent := found[0]
	parent.From = trimListVia(parent.From, msg.ListID)
	return &parent, nil
}

// trimListVia drops the "via List" a list appends to the sender's
// display name to survive DMARC. The row already names the list beside
// the name, so the suffix would say it twice - but only that list's own
// name is dropped, never a "via" the sender wrote.
func trimListVia(name, listID string) string {
	label, _, _ := strings.Cut(listID, ".")
	cut := strings.LastIndex(name, " via ")
	if label == "" || cut < 0 {
		return name
	}
	if !strings.EqualFold(name[cut+len(" via "):], label) {
		return name
	}
	return name[:cut]
}

// envelopeName is the display name of the first sender, falling back to
// the address when a sender gave none.
func envelopeName(from []imap.Address) string {
	if len(from) == 0 {
		return ""
	}
	if name := strings.TrimSpace(from[0].Name); name != "" {
		return name
	}
	return from[0].Addr()
}

// findByHeader returns up to limit messages whose header field contains
// value, newest first.
func findByHeader(conn *imapclient.Client, mboxName, key, value string, limit int) ([]ThreadNeighbour, error) {
	criteria := imap.SearchCriteria{
		Header: []imap.SearchCriteriaHeaderField{{Key: key, Value: value}},
	}
	data, err := conn.UIDSearch(&criteria, nil).Wait()
	if err != nil {
		return nil, err
	}
	uids := data.AllUIDs()
	if len(uids) == 0 {
		return nil, nil
	}
	slices.Reverse(uids)
	if len(uids) > limit {
		uids = uids[:limit]
	}
	msgs, err := conn.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{
		Envelope: true, UID: true,
	}).Collect()
	if err != nil {
		return nil, err
	}
	byUID := indexByUID(msgs)
	out := make([]ThreadNeighbour, 0, len(uids))
	for _, uid := range uids {
		m, ok := byUID[uid]
		if !ok || m.Envelope == nil {
			continue
		}
		out = append(out, ThreadNeighbour{
			UID:     uid,
			Subject: m.Envelope.Subject,
			From:    envelopeName(m.Envelope.From),
			URL:     messageURL(mboxName, uid),
		})
	}
	return out, nil
}

// threadSort is the sort key that means "group this into conversations"
// rather than "order it by a column". It travels in the same parameter
// because it answers the same question - what order are these in - and
// a reader picks one or the other, never both.
const threadSort = "thread"

// sortKeys maps a sort key from the query string to the SORT key sent
// to the server, along with the direction each defaults to; picking the
// active key again reverses it. "" is the default newest-first view.
var sortKeys = map[string]struct {
	key      imapclient.SortKey
	descends bool
}{
	"":        {imapclient.SortKeyDate, true},
	"date":    {imapclient.SortKeyDate, true},
	"starred": {imapclient.SortKeyDate, true},
	"from":    {imapclient.SortKeyFrom, false},
	"subject": {imapclient.SortKeySubject, false},
	"size":    {imapclient.SortKeySize, true},
}

func sortSeqNums(conn *imapclient.Client, criteria *imap.SearchCriteria, crit imapclient.SortCriterion) ([]uint32, error) {
	sortOptions := &imapclient.SortOptions{
		SearchCriteria: criteria,
		SortCriteria:   []imapclient.SortCriterion{crit},
	}
	nums, err := conn.Sort(sortOptions).Wait()
	if err != nil {
		return nil, fmt.Errorf("SORT failed: %v", err)
	}
	return nums, nil
}

// searchSeqNums returns the sequence numbers matching criteria in display
// order, using SORT when the server supports it. The SORT vocabulary has
// no flag key, so the starred order concatenates two queries, each newest
// first within its group.
func searchSeqNums(conn *imapclient.Client, criteria *imap.SearchCriteria, sort string, reverse bool) ([]uint32, error) {
	if !conn.Caps().Has(imap.CapSort) {
		data, err := conn.Search(criteria, nil).Wait()
		if err != nil {
			return nil, fmt.Errorf("SEARCH failed: %v", err)
		}
		// SEARCH answers oldest first, and sequence order is the one
		// date order available without SORT; reverse still means
		// newest first, as it does with it.
		nums := data.AllSeqNums()
		if reverse {
			slices.Reverse(nums)
		}
		return nums, nil
	}

	if sort == "starred" {
		flagged := *criteria
		flagged.Flag = append(append([]imap.Flag{}, criteria.Flag...), imap.FlagFlagged)
		unflagged := *criteria
		unflagged.NotFlag = append(append([]imap.Flag{}, criteria.NotFlag...), imap.FlagFlagged)

		first, second := &flagged, &unflagged
		if !reverse {
			first, second = second, first
		}
		byDate := imapclient.SortCriterion{Key: imapclient.SortKeyDate, Reverse: true}
		nums, err := sortSeqNums(conn, first, byDate)
		if err != nil {
			return nil, err
		}
		rest, err := sortSeqNums(conn, second, byDate)
		if err != nil {
			return nil, err
		}
		return append(nums, rest...), nil
	}

	return sortSeqNums(conn, criteria, imapclient.SortCriterion{Key: sortKeys[sort].key, Reverse: reverse})
}

func searchMessages(conn *imapclient.Client, mboxName string, searchCriteria *imap.SearchCriteria, page, messagesPerPage int, sort string, reverse bool) (msgs []IMAPMessage, total int, err error) {
	if err := ensureMailboxSelected(conn, mboxName); err != nil {
		return nil, 0, err
	}

	nums, err := searchSeqNums(conn, searchCriteria, sort, reverse)
	if err != nil {
		return nil, 0, err
	}

	total = len(nums)

	from := page * messagesPerPage
	to := from + messagesPerPage
	if from >= len(nums) {
		return nil, total, nil
	}
	if to > len(nums) {
		to = len(nums)
	}
	nums = nums[from:to]

	indexes := make(map[uint32]int)
	for i, num := range nums {
		indexes[num] = i
	}

	seqSet := imap.SeqSetNum(nums...)
	header := listHeaderItem()
	results, err := conn.Fetch(seqSet, listFetchOptions(header)).Collect()
	if err != nil {
		return nil, 0, fmt.Errorf("failed to fetch message list: %v", err)
	}

	msgs = make([]IMAPMessage, len(nums))
	for _, msg := range results {
		i, ok := indexes[msg.SeqNum]
		if !ok {
			continue
		}
		msgs[i] = IMAPMessage{FetchMessageBuffer: msg, Mailbox: mboxName}
		msgs[i].setRowHeaders(msg.FindBodySection(header))
	}

	return msgs, total, nil
}

// messageNeighbors locates the message in its view's display order and
// returns the UIDs shown before (newer) and after (older) it, along with its
// 1-based position and the view's total. criteria narrows the walk to the
// filtered view the message was opened from; nil walks the whole mailbox.
// A zero position means the message is not part of the filtered view.
func messageNeighbors(conn *imapclient.Client, seqNum uint32, criteria *imap.SearchCriteria) (newer, older imap.UID, pos, total int, err error) {
	var newerSeq, olderSeq uint32
	if criteria == nil {
		total = int(conn.Mailbox().NumMessages)
		pos = total - int(seqNum) + 1
		if int(seqNum) < total {
			newerSeq = seqNum + 1
		}
		if seqNum > 1 {
			olderSeq = seqNum - 1
		}
	} else {
		nums, err := searchSeqNums(conn, criteria, "", true)
		if err != nil {
			return 0, 0, 0, 0, err
		}
		total = len(nums)
		i := -1
		for j, num := range nums {
			if num == seqNum {
				i = j
				break
			}
		}
		if i < 0 {
			return 0, 0, 0, 0, nil
		}
		pos = i + 1
		if i > 0 {
			newerSeq = nums[i-1]
		}
		if i < len(nums)-1 {
			olderSeq = nums[i+1]
		}
	}

	var seqs []uint32
	for _, s := range []uint32{newerSeq, olderSeq} {
		if s != 0 {
			seqs = append(seqs, s)
		}
	}
	if len(seqs) == 0 {
		return 0, 0, pos, total, nil
	}
	msgs, err := conn.Fetch(imap.SeqSetNum(seqs...), &imap.FetchOptions{UID: true}).Collect()
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("failed to fetch neighbor UIDs: %v", err)
	}
	for _, m := range msgs {
		if m.SeqNum == newerSeq {
			newer = m.UID
		}
		if m.SeqNum == olderSeq {
			older = m.UID
		}
	}
	return newer, older, pos, total, nil
}

func getMessagePart(conn *imapclient.Client, mboxName string, uid imap.UID, partPath []int) (*IMAPMessage, *message.Entity, error) {
	if err := ensureMailboxSelected(conn, mboxName); err != nil {
		return nil, nil, err
	}

	headerItem := &imap.FetchItemBodySection{
		Peek: true,
		Part: partPath,
	}
	if len(partPath) > 0 {
		headerItem.Specifier = imap.PartSpecifierMIME
	} else {
		headerItem.Specifier = imap.PartSpecifierHeader
	}

	bodyItem := &imap.FetchItemBodySection{
		Part: partPath,
	}
	if len(partPath) > 0 {
		bodyItem.Specifier = imap.PartSpecifierNone
	} else {
		bodyItem.Specifier = imap.PartSpecifierText
	}

	sections := []*imap.FetchItemBodySection{headerItem, bodyItem}
	// A part's own header carries no List-Post; ask for the message's in
	// the same round trip.
	var rootHeaderItem *imap.FetchItemBodySection
	if len(partPath) > 0 {
		rootHeaderItem = &imap.FetchItemBodySection{
			Peek:      true,
			Specifier: imap.PartSpecifierHeader,
		}
		sections = append(sections, rootHeaderItem)
	}

	options := imap.FetchOptions{
		Envelope:      true,
		UID:           true,
		RFC822Size:    true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
		Flags:         true,
		BodySection:   sections,
	}

	// TODO: stream attachments
	msgs, err := conn.Fetch(imap.UIDSetNum(uid), &options).Collect()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to fetch message: %v", err)
	} else if len(msgs) == 0 {
		return nil, nil, alborz.NotFoundf("message %v does not exist in this folder", uid)
	}
	msg := msgs[0]

	headerBuf := msg.FindBodySection(headerItem)
	bodyBuf := msg.FindBodySection(bodyItem)
	if headerBuf == nil || bodyBuf == nil {
		// The server answers a part path the message doesn't have
		// with a fetch result missing the asked-for sections.
		if len(partPath) > 0 {
			return nil, nil, alborz.NotFoundf("message %v has no part %v", uid, partPath)
		}
		return nil, nil, fmt.Errorf("server didn't return header and body")
	}

	rootHeaderBuf := headerBuf
	if rootHeaderItem != nil {
		if buf := msg.FindBodySection(rootHeaderItem); buf != nil {
			rootHeaderBuf = buf
		}
	}

	h, err := textproto.ReadHeader(bufio.NewReader(bytes.NewReader(headerBuf)))
	if err != nil {
		// Broken messages exist in the wild; show the part raw instead
		// of failing the whole page.
		h = textproto.Header{}
		h.Set("Content-Type", "text/plain; charset=utf-8")
		bodyBuf = append(headerBuf, bodyBuf...)
	}

	part, err := message.New(message.Header{Header: h}, bytes.NewReader(bodyBuf))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create message reader: %v", err)
	}

	out := &IMAPMessage{FetchMessageBuffer: msg, Mailbox: mboxName}
	if rh, err := textproto.ReadHeader(bufio.NewReader(bytes.NewReader(rootHeaderBuf))); err == nil {
		out.setListHeaders(rh)
	}
	return out, part, nil
}

// fetchRawMessage returns a message exactly as the server holds it,
// headers and all, beside the envelope that names it. BODY.PEEK[] with
// no part is the whole thing, which is what attaching a message needs
// and what quoting one can never reproduce.
func fetchRawMessage(conn *imapclient.Client, mboxName string, uid imap.UID) ([]byte, *imap.Envelope, error) {
	if err := ensureMailboxSelected(conn, mboxName); err != nil {
		return nil, nil, err
	}
	section := &imap.FetchItemBodySection{Peek: true}
	msgs, err := conn.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{
		Envelope:    true,
		UID:         true,
		BodySection: []*imap.FetchItemBodySection{section},
	}).Collect()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to fetch message: %v", err)
	}
	if len(msgs) == 0 {
		return nil, nil, alborz.NotFoundf("message %v does not exist in this folder", uid)
	}
	buf := msgs[0].FindBodySection(section)
	if buf == nil {
		return nil, nil, fmt.Errorf("server didn't return the message body")
	}
	return buf, msgs[0].Envelope, nil
}

// messageAttachment is a whole message carried inside another one, the
// shape RFC 2046 5.2 gives a forward that must arrive intact: headers,
// MIME structure, signatures and its own attachments, none of them
// re-rendered by us.
type messageAttachment struct {
	Path messagePath
	Name string
	Body []byte
}

func (a *messageAttachment) Open() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(a.Body)), nil
}

func (a *messageAttachment) MIMEType() string { return "message/rfc822" }

func (a *messageAttachment) Filename() string { return a.Name }

// attachmentName is a subject made into a file name: a forwarded
// message arrives as a file and wants to be recognisable in a list of
// them. Anything a file system or a header would argue about goes.
func attachmentName(subject string) string {
	name := strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '"', '<', '>', '|', '?', '*', '\n', '\r', '\t':
			return '_'
		}
		return r
	}, strings.TrimSpace(subject))
	if len([]rune(name)) > 60 {
		name = string([]rune(name)[:60])
	}
	if name == "" {
		name = "message"
	}
	return name + ".eml"
}

func markMessageAnswered(conn *imapclient.Client, mboxName string, uid imap.UID) error {
	return addFlag(conn, mboxName, uid, imap.FlagAnswered)
}

// forwardedFlag is the keyword mail clients agree to mean "this one was
// forwarded". It is a convention rather than a system flag, so a server
// may refuse to keep it; the mail has still gone out either way.
const forwardedFlag imap.Flag = "$Forwarded"

func markMessageForwarded(conn *imapclient.Client, mboxName string, uid imap.UID) error {
	return addFlag(conn, mboxName, uid, forwardedFlag)
}

func addFlag(conn *imapclient.Client, mboxName string, uid imap.UID, flag imap.Flag) error {
	return storeFlags(conn, mboxName, imap.UIDSetNum(uid), imap.StoreFlagsAdd, []imap.Flag{flag})
}

// storeFlags is the one STORE every flag change goes through.
func storeFlags(conn *imapclient.Client, mboxName string, set imap.NumSet, op imap.StoreFlagsOp, flags []imap.Flag) error {
	if err := ensureMailboxSelected(conn, mboxName); err != nil {
		return err
	}
	return conn.Store(set, &imap.StoreFlags{Op: op, Silent: true, Flags: flags}, nil).Close()
}

func appendMessage(c *imapclient.Client, msg *OutgoingMessage, role string) (*MailboxInfo, error) {
	mbox, err := getMailboxByRole(c, role)
	if err != nil {
		return nil, err
	}
	if mbox == nil {
		return nil, fmt.Errorf("Unable to resolve mailbox")
	}

	// IMAP needs to know in advance the final size of the message, so
	// there's no way around storing it in a buffer here.
	var buf bytes.Buffer
	if err := msg.WriteMessage(&buf); err != nil {
		return nil, err
	}

	flags := []imap.Flag{imap.FlagSeen}
	if role == "drafts" {
		flags = append(flags, imap.FlagDraft)
	}
	options := imap.AppendOptions{Flags: flags}
	appendCmd := c.Append(mbox.Name(), int64(buf.Len()), &options)
	defer appendCmd.Close()
	if _, err := io.Copy(appendCmd, &buf); err != nil {
		return nil, err
	}
	if err := appendCmd.Close(); err != nil {
		return nil, err
	}
	return mbox, nil
}

func deleteMessage(conn *imapclient.Client, mboxName string, uid imap.UID) error {
	return deleteMessages(conn, mboxName, imap.UIDSetNum(uid))
}

// deleteMessages flags the set deleted and expunges the folder.
func deleteMessages(conn *imapclient.Client, mboxName string, set imap.NumSet) error {
	if err := storeFlags(conn, mboxName, set, imap.StoreFlagsAdd, []imap.Flag{imap.FlagDeleted}); err != nil {
		return fmt.Errorf("failed to add deleted flag: %w", err)
	}
	if err := conn.Expunge().Close(); err != nil {
		return fmt.Errorf("failed to expunge mailbox: %w", err)
	}
	return nil
}

// unifiedFolders memoizes each account's role-to-folder resolution for the
// unified view: folder names change rarely, so one LIST per account per
// interval replaces one per click.
var unifiedFolders = alborz.NewMemo[map[string]string](unifiedFoldersTTL)

// unifiedFoldersTTL is how long a role-to-folder answer stands; folders
// are renamed rarely and the next merged page after that is soon enough.
const unifiedFoldersTTL = 5 * time.Minute

// resolveRole maps a unified role name to the account's folder carrying
// that special use, or "" when the account has none.
func resolveRole(conn *imapclient.Client, username, role string) (string, error) {
	folders, err := unifiedFolders.Get(username, func() (map[string]string, error) {
		mailboxes, err := listMailboxes(conn)
		if err != nil {
			return nil, err
		}
		var categorized CategorizedMailboxes
		for i := range mailboxes {
			categorized.Append(mailboxes[i], nil)
		}
		folders := map[string]string{"INBOX": "INBOX"}
		for role, details := range map[string]*MailboxDetails{
			"Drafts":  categorized.Common.Drafts,
			"Sent":    categorized.Common.Sent,
			"Junk":    categorized.Common.Junk,
			"Trash":   categorized.Common.Trash,
			"Archive": categorized.Common.Archive,
		} {
			if details != nil {
				folders[role] = details.Info.Name()
			}
		}
		return folders, nil
	})
	if err != nil {
		return "", err
	}
	return folders[role], nil
}
