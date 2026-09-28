package alborzbase

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"mime"
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
	"github.com/emersion/go-message/charset"
	"github.com/emersion/go-message/textproto"
)

type MailboxInfo struct {
	*imap.ListData

	Active bool
	Total  int
	Unseen int

	// Label is the display name; the wire name stays in URLs and values.
	Label string
}

// CrumbLink is one step of the path a page shows above itself.
type CrumbLink struct {
	Label   string
	URL     string
	Address bool // reads left to right whatever the page does
}

// Steps are named by path segment: a custom folder's Label is its whole wire name.
func mailboxCrumb(mailboxes []MailboxInfo, name, account string) []CrumbLink {
	crumb := []CrumbLink{{Label: account, URL: "/mailbox/INBOX", Address: true}}
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

// RawPath keeps a slash in a folder name from being encoded twice.
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

// Counts ride on LIST-STATUS where offered, so the sidebar needs no STATUS for those.
func startListMailboxes(conn *imapclient.Client) *imapclient.ListCommand {
	var options *imap.ListOptions
	if conn.Caps().Has(imap.CapListStatus) {
		options = &imap.ListOptions{ReturnStatus: listingStatusOptions(conn)}
	}
	return conn.List("", "*", options)
}

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

// RoleFolder names the account's folder for a role, empty when it has none.
func RoleFolder(ctx *alborz.Context, role string) (string, error) {
	var name string
	err := ctx.DoIMAP(func(c *imapclient.Client) error {
		mbox, err := getMailboxByRole(c, role)
		if err != nil || mbox == nil {
			return err
		}
		name = mbox.Name()
		return nil
	})
	return name, err
}

// MailboxNames lists the account's selectable folders.
func MailboxNames(ctx *alborz.Context) ([]string, error) {
	var names []string
	err := ctx.DoIMAP(func(c *imapclient.Client) error {
		mailboxes, err := listMailboxes(c)
		for _, m := range mailboxes {
			if !slices.Contains(m.Attrs, imap.MailboxAttrNoSelect) {
				names = append(names, m.Name())
			}
		}
		return err
	})
	return names, err
}

type MailboxStatus struct {
	*imap.StatusData

	// Display name, like MailboxInfo.Label
	Label string
}

func (mbox *MailboxStatus) Name() string {
	return mbox.Mailbox
}

// IsEmpty reports a folder the server says holds nothing; an absent count is not empty.
func (mbox *MailboxStatus) IsEmpty() bool {
	return mbox.NumMessages != nil && *mbox.NumMessages == 0
}

func (mbox *MailboxStatus) URL() *url.URL {
	return mailboxPageURL(mbox.Name())
}

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

	// ListPost is empty unless the whole message's header was fetched.
	ListPost string
	// ListUnsubscribe is where the page sends the reader to leave the list.
	ListUnsubscribe string
	// OneClick is the RFC 8058 endpoint, posted from the server.
	OneClick   string
	rootHeader textproto.Header
	// References is the thread chain; ENVELOPE does not carry it.
	References string
	// Author wrote a message whose From the list Via replaced; nil otherwise.
	Author *imap.Address
	Via    *imap.Address
	// ListID names the list; it, not List-Post, says a message is list mail.
	ListID string
	// ListHelp, ListSubscribe, ListOwner and ListArchive are the rest of RFC 2369.
	ListHelp      string
	ListSubscribe string
	ListOwner     string
	ListArchive   string
	// Depth is a row's depth in its thread, set only by the threaded view.
	Depth int
	// Alias is the trusted delivery address when it is not the account's.
	Alias string
	// DeliveredTo is every address the delivery path recorded, topmost first.
	DeliveredTo []string
}

// Date is the sender's Date, or INTERNALDATE when that did not parse.
func (msg *IMAPMessage) Date() time.Time {
	if msg.Envelope != nil && !msg.Envelope.Date.IsZero() {
		return msg.Envelope.Date
	}
	return msg.InternalDate
}

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

// A mailto would open whatever mail app the machine has, not this account.
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
	return "/compose?" + alborz.Query(q)
}

// Which header holds the alias depends on the server, so all are collected.
var deliveryHeaders = []string{
	"x-original-to",
	"x-envelope-to",
	"envelope-to",
	"delivered-to",
	"x-delivered-to",
	"x-forwarded-to",
	"x-rcptto",
}

var receivedFor = regexp.MustCompile(`(?i)\bfor\s+<([^>]+)>`)

// cut is how many addresses our own server answers for: all without authserv,
// none when no Received names it.
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
	ended, ours := false, false
	for fields := h.Fields(); fields.Next(); {
		key := strings.ToLower(fields.Key())
		if key == "received" {
			// Our server writes delivery headers around its Received, so the
			// boundary is the first Received that is not ours.
			hop := strings.ToLower(fields.Value())
			if want != "" && !ended {
				if strings.Contains(hop, want) {
					ours = true
				} else {
					cut, ended = len(addrs), true
				}
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
	if want != "" && !ours {
		cut = 0
	}
	return addrs, cut
}

func listAction(value string) string {
	if uri := firstListURI(value, "https", "http"); uri != "" {
		return uri
	}
	return composeFromMailto(firstListURI(value, "mailto"))
}

// Senders put encoded-words even in structured fields like List-Unsubscribe.
var headerWords = &mime.WordDecoder{CharsetReader: charset.Reader}

func decodedField(h textproto.Header, key string) string {
	value := h.Get(key)
	if !strings.Contains(value, "=?") {
		return value
	}
	decoded, err := headerWords.DecodeHeader(value)
	if err != nil {
		return value
	}
	return decoded
}

// A list that rewrites From names it "Author via List".
var viaName = regexp.MustCompile(`^(.*\S)\s+via\s+(\S.*)$`)

// Nothing is inferred from absence: without one of these headers the From stands.
func authorBehindList(h textproto.Header, from *imap.Address, listID string) *imap.Address {
	if listID == "" || from == nil {
		return nil
	}
	parts := viaName.FindStringSubmatch(from.Name)
	if parts == nil {
		return nil
	}
	name := parts[1]
	listed := strings.ToLower(from.Mailbox + "@" + from.Host)
	for _, key := range []string{"X-Original-From", "X-MailFrom", "Reply-To"} {
		value := decodedField(h, key)
		if value == "" {
			continue
		}
		addr, err := mail.ParseAddress(value)
		if err != nil {
			// X-MailFrom may be a bare address.
			if !strings.Contains(value, "@") || strings.ContainsAny(value, " ,<") {
				continue
			}
			addr = &mail.Address{Address: strings.TrimSpace(value)}
		}
		if strings.ToLower(addr.Address) == listed {
			continue
		}
		// A Reply-To is the sender's to write, so it must name the From's person.
		if key == "Reply-To" && !strings.EqualFold(strings.TrimSpace(addr.Name), name) {
			continue
		}
		mailbox, host, ok := strings.Cut(addr.Address, "@")
		if !ok {
			continue
		}
		return &imap.Address{Name: name, Mailbox: mailbox, Host: host}
	}
	return nil
}

// HeaderField is one decoded field of the whole message's header.
func (msg *IMAPMessage) HeaderField(key string) string {
	return decodedField(msg.rootHeader, key)
}

// List-Post: NO means the list refuses posts, which is not an address.
func (msg *IMAPMessage) setListHeaders(h textproto.Header) {
	msg.rootHeader = h
	msg.References = strings.Join(strings.Fields(h.Get("References")), " ")
	msg.DeliveredTo, _ = deliveryAddresses(h, "")
	msg.ListHelp = listAction(decodedField(h, "List-Help"))
	msg.ListSubscribe = listAction(decodedField(h, "List-Subscribe"))
	msg.ListOwner = listAction(decodedField(h, "List-Owner"))
	msg.ListArchive = firstListURI(decodedField(h, "List-Archive"), "https", "http")
	if post := firstListURI(decodedField(h, "List-Post"), "mailto"); post != "" {
		msg.ListPost = strings.TrimPrefix(post, "mailto:")
	}
	msg.ListID = listID(h)
	// Plain http is not offered: the POST is made from here and must not be impersonated.
	msg.ListUnsubscribe = firstListURI(decodedField(h, "List-Unsubscribe"), "https")
	// Without List-Unsubscribe-Post an https URI is a page to visit (RFC 8058).
	if strings.Contains(strings.ToLower(decodedField(h, "List-Unsubscribe-Post")), "one-click") {
		msg.OneClick = msg.ListUnsubscribe
	}
	if msg.ListUnsubscribe == "" {
		msg.ListUnsubscribe = composeFromMailto(firstListURI(decodedField(h, "List-Unsubscribe"), "mailto"))
	}
	if msg.FetchMessageBuffer != nil && msg.Envelope != nil && len(msg.Envelope.From) > 0 {
		from := msg.Envelope.From[0]
		if msg.Author = authorBehindList(h, &from, msg.ListID); msg.Author != nil {
			msg.Via = &imap.Address{Name: viaName.FindStringSubmatch(from.Name)[2],
				Mailbox: from.Mailbox, Host: from.Host}
		}
	}
}

// ListDetails reports whether the message names anything about its list beyond leaving it.
func (msg *IMAPMessage) ListDetails() bool {
	return msg.ListPost != "" || msg.ListHelp != "" || msg.ListSubscribe != "" ||
		msg.ListOwner != "" || msg.ListArchive != ""
}

func (msg *IMAPMessage) URL() *url.URL {
	return messageURL(msg.Mailbox, msg.UID)
}

// Without the narrowing and order, one step sideways lands in the plain folder.
func neighbourURL(ctx *alborz.Context, mboxName string, uid imap.UID) *url.URL {
	next := messageURL(mboxName, uid)
	if next == nil {
		return nil
	}
	kept := url.Values{}
	for _, name := range []string{"query", "view", "sort", "dir", "ipp", "account", "all", "thread"} {
		if value := ctx.QueryParam(name); value != "" {
			kept.Set(name, value)
		}
	}
	next.RawQuery = alborz.Query(kept)
	return next
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
		node.Size = decodedSize(singlePart)
		if inner := singlePart.MessageRFC822; node.Filename == "" && inner != nil && inner.Envelope != nil {
			node.Filename = attachmentName(inner.Envelope.Subject)
		}
	}
	return node
}

// A base64 line is 76 characters and its CRLF, carrying 57 bytes (RFC 2045 6.8).
const (
	base64LineOctets = 78
	base64LineBytes  = 57
)

// The structure counts octets as transferred, not as saved.
func decodedSize(part *imap.BodyStructureSinglePart) uint32 {
	if strings.EqualFold(part.Encoding, "base64") {
		return uint32(uint64(part.Size) * base64LineBytes / base64LineOctets)
	}
	return part.Size
}

func (msg *IMAPMessage) TextPart() *IMAPPartNode {
	if msg.BodyStructure == nil {
		return nil
	}

	files := fileParts(msg.BodyStructure)
	var best *IMAPPartNode
	isTextPlain := false
	msg.BodyStructure.Walk(func(path []int, part imap.BodyStructure) bool {
		singlePart, ok := part.(*imap.BodyStructureSinglePart)
		if !ok {
			return true
		}

		if !strings.EqualFold(singlePart.Type, "text") || files[fmt.Sprint(path)] {
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

// PreferredPart is the part a message opens at.
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

	files := fileParts(msg.BodyStructure)
	var best *IMAPPartNode
	msg.BodyStructure.Walk(func(path []int, part imap.BodyStructure) bool {
		singlePart, ok := part.(*imap.BodyStructureSinglePart)
		if !ok {
			return true
		}

		if !strings.EqualFold(singlePart.Type, "text") || files[fmt.Sprint(path)] {
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

	files := fileParts(msg.BodyStructure)
	var attachments []IMAPPartNode
	msg.BodyStructure.Walk(func(path []int, part imap.BodyStructure) bool {
		singlePart, ok := part.(*imap.BodyStructureSinglePart)
		if !ok || !files[fmt.Sprint(path)] {
			return true
		}

		attachments = append(attachments, *newIMAPPartNode(msg, path, singlePart))
		return true
	})
	return attachments
}

// attached is the one rule for what is a file and what is the message itself.
// parent is the subtype of the multipart the part sits in.
func attached(mediaType, disposition, filename, contentID, parent string) bool {
	switch {
	case strings.EqualFold(parent, "signed") && signatureTypes[strings.ToLower(mediaType)]:
		return false
	case strings.EqualFold(disposition, "attachment"):
		return true
	case strings.EqualFold(parent, "related") && contentID != "":
		return false
	case filename != "":
		return true
	}
	return !strings.HasPrefix(strings.ToLower(mediaType), "text/")
}

// OpenPGP (RFC 3156) and S/MIME (RFC 8551, with the older x- name).
var signatureTypes = map[string]bool{
	"application/pgp-signature":     true,
	"application/pkcs7-signature":   true,
	"application/x-pkcs7-signature": true,
}

func fileParts(bs imap.BodyStructure) map[string]bool {
	parents := map[string]string{}
	files := map[string]bool{}
	bs.Walk(func(path []int, part imap.BodyStructure) bool {
		switch part := part.(type) {
		case *imap.BodyStructureMultiPart:
			parents[fmt.Sprint(path)] = part.Subtype
		case *imap.BodyStructureSinglePart:
			var disposition string
			if disp := part.Disposition(); disp != nil {
				disposition = disp.Value
			}
			files[fmt.Sprint(path)] = attached(part.MediaType(), disposition, part.Filename(),
				part.ID, parents[fmt.Sprint(path[:len(path)-1])])
		}
		return true
	})
	return files
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

// ShownImages are the images beside the part at path that the HTML shows by Content-ID.
func (msg *IMAPMessage) ShownImages(path []int) []IMAPPartNode {
	if msg.BodyStructure == nil || len(path) == 0 {
		return nil
	}
	beside := path[:len(path)-1]
	files := fileParts(msg.BodyStructure)
	var images []IMAPPartNode
	msg.BodyStructure.Walk(func(p []int, part imap.BodyStructure) bool {
		single, ok := part.(*imap.BodyStructureSinglePart)
		if ok && strings.EqualFold(single.Type, "image") && !files[fmt.Sprint(p)] &&
			pathsEqual(p[:len(p)-1], beside) {
			images = append(images, *newIMAPPartNode(msg, p, single))
		}
		return true
	})
	return images
}

// HTMLPieces are the pieces of an HTML split around inline images, nil when whole.
func (msg *IMAPMessage) HTMLPieces(path []int) []IMAPPartNode {
	if msg.BodyStructure == nil || len(path) == 0 {
		return nil
	}
	parent := path[:len(path)-1]
	files := fileParts(msg.BodyStructure)
	var (
		mixed, named bool
		pieces       []IMAPPartNode
	)
	msg.BodyStructure.Walk(func(p []int, part imap.BodyStructure) bool {
		switch part := part.(type) {
		case *imap.BodyStructureMultiPart:
			if pathsEqual(p, parent) {
				mixed = strings.EqualFold(part.Subtype, "mixed")
			}
		case *imap.BodyStructureSinglePart:
			if len(p) != len(path) || !pathsEqual(p[:len(p)-1], parent) {
				return true
			}
			disp := part.Disposition()
			switch {
			case part.MediaType() == "text/html" && !files[fmt.Sprint(p)]:
				named = named || pathsEqual(p, path)
				pieces = append(pieces, *newIMAPPartNode(msg, p, part))
			case strings.EqualFold(part.Type, "image") && (disp == nil || strings.EqualFold(disp.Value, "inline")):
				pieces = append(pieces, *newIMAPPartNode(msg, p, part))
			}
		}
		return true
	})
	if !mixed || !named || len(pieces) < 2 {
		return nil
	}
	return pieces
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

// AttachmentsSize is what the whole set of attachments weighs.
func (msg *IMAPMessage) AttachmentsSize() string {
	var total int64
	for _, part := range msg.Attachments() {
		total += int64(part.Size)
	}
	return formatSize(total)
}

func (node IMAPPartNode) SizeString() string {
	return formatSize(int64(node.Size))
}

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

// URL is where a part is fetched from; without the account the first signed-in one serves it.
func (node IMAPPartNode) URL(raw bool, account string) *url.URL {
	u := node.Message.URL()
	// Both halves or neither: url.URL uses RawPath only while it encodes Path.
	if raw {
		u.Path += "/raw"
		u.RawPath += "/raw"
	}
	q := u.Query()
	q.Set("part", node.PathString())
	if account != "" {
		q.Set("account", account)
	}
	u.RawQuery = alborz.Query(q)
	return u
}

// IsText reports a part the page itself can show, which not every text/* part is.
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
		node.Size = decodedSize(bs)
	}

	return node
}

func (msg *IMAPMessage) PartTree() *IMAPPartNode {
	if msg.BodyStructure == nil {
		return nil
	}

	return imapPartTree(msg, msg.BodyStructure, nil)
}

// Low bit first; the bits mean nothing without \Flagged.
var flagColorBits = [3]imap.Flag{"$MailFlagBit0", "$MailFlagBit1", "$MailFlagBit2"}

// FlagColor is the colour this message is flagged in, empty when unflagged.
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
	if n >= len(alborz.FlagColors) {
		n = 0
	}
	return alborz.FlagColors[n]
}

// FlagColorOption is one colour offered by a picker.
type FlagColorOption struct {
	Name  string
	Label string
}

// FlagColorFlags returns the flags to add and remove for a colour; "" unflags.
func FlagColorFlags(color string) (add, del []imap.Flag) {
	if color == "" {
		return nil, append([]imap.Flag{imap.FlagFlagged}, flagColorBits[:]...)
	}
	n := -1
	for i, c := range alborz.FlagColors {
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

// HasFlag reports whether the message carries a flag, ignoring case.
func (msg *IMAPMessage) HasFlag(flag imap.Flag) bool {
	for _, f := range msg.Flags {
		if strings.EqualFold(string(f), string(flag)) {
			return true
		}
	}
	return false
}

// Warnings live on the message page, so a row fetches no scanner or Received lines.
func listHeaderItem() *imap.FetchItemBodySection {
	return &imap.FetchItemBodySection{
		Peek:         true,
		Specifier:    imap.PartSpecifierHeader,
		HeaderFields: append(slices.Clone(deliveryHeaders), "List-Id"),
	}
}

type rowFields func(header *imap.FetchItemBodySection) *imap.FetchOptions

// One place, so a column added to one list is not missing from another.
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

// A row deeper than the page shown is read only to find the page.
func mergeFetchOptions(*imap.FetchItemBodySection) *imap.FetchOptions {
	return &imap.FetchOptions{
		Envelope:     true,
		Flags:        true,
		UID:          true,
		RFC822Size:   true,
		InternalDate: true,
	}
}

func wholeRows(conn *imapclient.Client, mboxName string, uids []imap.UID) ([]IMAPMessage, error) {
	if err := ensureMailboxSelected(conn, mboxName); err != nil {
		return nil, err
	}
	header := listHeaderItem()
	fetched, err := conn.Fetch(imap.UIDSetNum(uids...), listFetchOptions(header)).Collect()
	if err != nil {
		return nil, fmt.Errorf("failed to fetch message list: %v", err)
	}
	rows := make([]IMAPMessage, len(fetched))
	for i, msg := range fetched {
		rows[i] = IMAPMessage{FetchMessageBuffer: msg, Mailbox: mboxName}
		rows[i].setRowHeaders(msg.FindBodySection(header))
	}
	return rows, nil
}

// A server that returned nothing leaves the row without its marks.
func (msg *IMAPMessage) setRowHeaders(buf []byte) {
	if len(buf) == 0 {
		return
	}
	h, err := textproto.ReadHeader(bufio.NewReader(bytes.NewReader(buf)))
	if err != nil {
		return
	}
	// Kept: deciding which delivery addresses to believe needs where each sat.
	msg.rootHeader = h
	msg.DeliveredTo, _ = deliveryAddresses(h, "")
	msg.ListID = listID(h)
}

// Whether a reply goes to a list is asked of the message, not of a form field.
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

// The phrase before the bracketed identifier is prose, not a name to match on.
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

func listMessages(conn *imapclient.Client, mboxName string, page, messagesPerPage int, fields rowFields) (msgs []IMAPMessage, total int, err error) {
	// Only an already selected mailbox needs a NOOP to notice new mail.
	if mbox := conn.Mailbox(); mbox != nil && mbox.Name == mboxName {
		if err := conn.Noop().Wait(); err != nil {
			return nil, 0, err
		}
	} else if _, err := conn.Select(mboxName, nil).Wait(); err != nil {
		return nil, 0, fmt.Errorf("failed to select mailbox: %v", err)
	}

	// A connection dropped since the SELECT reports no mailbox.
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
	imapMsgs, err := conn.Fetch(seqSet, fields(header)).Collect()
	if err != nil {
		return nil, 0, fmt.Errorf("failed to fetch message list: %v", err)
	}

	for _, msg := range imapMsgs {
		row := IMAPMessage{FetchMessageBuffer: msg, Mailbox: mboxName}
		row.setRowHeaders(msg.FindBodySection(header))
		msgs = append(msgs, row)
	}

	for i := len(msgs)/2 - 1; i >= 0; i-- {
		opp := len(msgs) - 1 - i
		msgs[i], msgs[opp] = msgs[opp], msgs[i]
	}

	return msgs, total, nil
}

// Envelopes only: the listing fetch is many bytes for a caller reading addresses.
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

// ThreadAlgorithm is the algorithm the server offers, REFERENCES first, or "".
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

type threadRow struct {
	uid   imap.UID
	depth int
}

// Depth first is the reading order: a reply sits under what it answers.
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

// A page of threads, not messages; THREAD has no window, so the whole set is asked.
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

	// A thread's age is its newest message, so a long-running one stays on top.
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

// FETCH does not promise the order asked for.
func indexByUID(msgs []*imapclient.FetchMessageBuffer) map[imap.UID]*imapclient.FetchMessageBuffer {
	byUID := make(map[imap.UID]*imapclient.FetchMessageBuffer, len(msgs))
	for _, m := range msgs {
		byUID[m.UID] = m
	}
	return byUID
}

// A message expunged between THREAD and FETCH is simply not shown.
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

const answerLimit = 12

// Only INBOX is searched: a reply filed elsewhere is not found.
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

// THREAD groups by message id, so this does not depend on header search.
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

// UIDs rise with arrival, so the highest is a thread's newest message.
func newestUID(rows []threadRow) imap.UID {
	var newest imap.UID
	for _, r := range rows {
		if r.uid > newest {
			newest = r.uid
		}
	}
	return newest
}

// ThreadNeighbour is another message in the same conversation.
type ThreadNeighbour struct {
	UID     imap.UID
	Subject string
	// From tells apart the messages of a list thread, which share one subject.
	From string
	URL  *url.URL
}

func threadParent(conn *imapclient.Client, mboxName, sent string, msg *IMAPMessage) (*ThreadNeighbour, error) {
	if err := ensureMailboxSelected(conn, mboxName); err != nil {
		return nil, err
	}
	if msg.Envelope == nil {
		return nil, nil
	}

	// Without In-Reply-To, the last References entry is the parent.
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
	// Sent is searched only when this folder does not hold the parent.
	if len(found) == 0 && sent != "" && sent != mboxName {
		// findByHeader searches what is selected; the name only labels results.
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

// Only the list's own name is dropped, never a "via" the sender wrote.
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

func envelopeName(from []imap.Address) string {
	if len(from) == 0 {
		return ""
	}
	if name := strings.TrimSpace(from[0].Name); name != "" {
		return name
	}
	return from[0].Addr()
}

// A full-text index matches addresses inside attached messages; only what the
// envelope contradicts is dropped, over every answer so totals hold.
func carried(c *imapclient.Client, q Query, answers imap.NumSet) (map[uint32]imap.UID, error) {
	wanted := q.Addresses()
	kept := map[uint32]imap.UID{}
	cmd := c.Fetch(answers, &imap.FetchOptions{UID: true, Envelope: true})
	for msg := cmd.Next(); msg != nil; msg = cmd.Next() {
		buf, err := msg.Collect()
		if err != nil {
			cmd.Close()
			return nil, err
		}
		if envelopeCarries(buf.Envelope, wanted) {
			kept[buf.SeqNum] = buf.UID
		}
	}
	return kept, cmd.Close()
}

// The comparison is the one a header search makes: name and address together.
func envelopeCarries(envelope *imap.Envelope, wanted map[string][]string) bool {
	if envelope == nil {
		return true
	}
	fields := map[string][]imap.Address{
		"from": envelope.From, "to": envelope.To, "cc": envelope.Cc, "bcc": envelope.Bcc,
	}
	for key, values := range wanted {
		for _, value := range values {
			found := false
			for _, address := range fields[key] {
				written := address.Name + " <" + address.Addr() + ">"
				if strings.Contains(strings.ToLower(written), strings.ToLower(value)) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}
	return true
}

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

// It shares the sort parameter: a reader picks threads or a column, never both.
const threadSort = "thread"

var sortKeys = map[string]struct {
	key      imapclient.SortKey
	descends bool
}{
	"":        {imapclient.SortKeyDate, true},
	"date":    {imapclient.SortKeyDate, true},
	"starred": {imapclient.SortKeyDate, true},
	"from":    {imapclient.SortKeyFrom, false},
	"to":      {imapclient.SortKeyTo, false},
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

// SORT has no flag key, so the starred order concatenates two queries.
func searchSeqNums(conn *imapclient.Client, criteria *imap.SearchCriteria, sort string, reverse bool) ([]uint32, error) {
	if !conn.Caps().Has(imap.CapSort) {
		data, err := conn.Search(criteria, nil).Wait()
		if err != nil {
			return nil, fmt.Errorf("SEARCH failed: %v", err)
		}
		// Without SORT, sequence order is the only date order available.
		nums := data.AllSeqNums()
		if reverse {
			slices.Reverse(nums)
		}
		return nums, nil
	}

	if sort == "starred" {
		first, second := starredHalves(criteria, reverse)
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

func starredHalves(criteria *imap.SearchCriteria, reverse bool) (first, second *imap.SearchCriteria) {
	flagged := *criteria
	flagged.Flag = append(append([]imap.Flag{}, criteria.Flag...), imap.FlagFlagged)
	unflagged := *criteria
	unflagged.NotFlag = append(append([]imap.Flag{}, criteria.NotFlag...), imap.FlagFlagged)
	if !reverse {
		return &unflagged, &flagged
	}
	return &flagged, &unflagged
}

func sortedUIDs(conn *imapclient.Client, criteria *imap.SearchCriteria, sort string, reverse bool) ([]imap.UID, error) {
	var cmds []*imapclient.SortCommand
	if sort == "starred" {
		first, second := starredHalves(criteria, reverse)
		byDate := []imapclient.SortCriterion{{Key: imapclient.SortKeyDate, Reverse: true}}
		cmds = append(cmds,
			conn.UIDSort(&imapclient.SortOptions{SearchCriteria: first, SortCriteria: byDate}),
			conn.UIDSort(&imapclient.SortOptions{SearchCriteria: second, SortCriteria: byDate}))
	} else {
		cmds = append(cmds, conn.UIDSort(&imapclient.SortOptions{SearchCriteria: criteria,
			SortCriteria: []imapclient.SortCriterion{{Key: sortKeys[sort].key, Reverse: reverse}}}))
	}
	var uids []imap.UID
	for _, cmd := range cmds {
		nums, err := cmd.Wait()
		if err != nil {
			return nil, fmt.Errorf("UID SORT failed: %v", err)
		}
		for _, num := range nums {
			uids = append(uids, imap.UID(num))
		}
	}
	return uids, nil
}

// q is the query the criteria came from, empty for a list no query narrowed.
func searchMessages(conn *imapclient.Client, mboxName string, q Query, searchCriteria *imap.SearchCriteria, page, messagesPerPage int, sort string, reverse bool, fields rowFields) (msgs []IMAPMessage, total int, err error) {
	if err := ensureMailboxSelected(conn, mboxName); err != nil {
		return nil, 0, err
	}

	nums, err := searchSeqNums(conn, searchCriteria, sort, reverse)
	if err != nil {
		return nil, 0, err
	}
	if q.Addressed() && len(nums) > 0 {
		kept, err := carried(conn, q, imap.SeqSetNum(nums...))
		if err != nil {
			return nil, 0, err
		}
		nums = slices.DeleteFunc(nums, func(num uint32) bool { _, ok := kept[num]; return !ok })
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
	results, err := conn.Fetch(seqSet, fields(header)).Collect()
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

// A zero position means the message is not in the view. Asked in UIDs: a
// remembered sequence number may predate an expunge.
func messageNeighbors(conn *imapclient.Client, uid imap.UID, criteria *imap.SearchCriteria, sortKey string, reverse bool) (newer, older imap.UID, pos, total int, err error) {
	plain := criteria == nil && sortKey == ""
	if criteria == nil {
		criteria = &imap.SearchCriteria{}
	}
	var uids []imap.UID
	if plain || !conn.Caps().Has(imap.CapSort) {
		// The folder's own order, and any list without SORT, is UID order.
		descending := plain || reverse
		if conn.Caps().Has(imap.CapESearch) {
			return uidNeighbors(conn, uid, criteria, descending)
		}
		data, err := conn.UIDSearch(criteria, nil).Wait()
		if err != nil {
			return 0, 0, 0, 0, fmt.Errorf("UID SEARCH failed: %v", err)
		}
		uids = data.AllUIDs()
		if descending {
			slices.Reverse(uids)
		}
	} else if uids, err = sortedUIDs(conn, criteria, sortKey, reverse); err != nil {
		return 0, 0, 0, 0, err
	}
	i := slices.Index(uids, uid)
	if i < 0 {
		return 0, 0, 0, 0, nil
	}
	if i > 0 {
		newer = uids[i-1]
	}
	if i+1 < len(uids) {
		older = uids[i+1]
	}
	return newer, older, i + 1, len(uids), nil
}

// Counting either side answers a folder of any size in a few numbers.
func uidNeighbors(conn *imapclient.Client, uid imap.UID, criteria *imap.SearchCriteria, descending bool) (newer, older imap.UID, pos, total int, err error) {
	within := func(r imap.UIDRange) *imap.SearchCriteria {
		c := *criteria
		c.UID = append(slices.Clone(criteria.UID), imap.UIDSet{r})
		return &c
	}
	// A zero Stop is "*", and uid+1:* still holds the highest UID when
	// that is below uid+1 (RFC 9051 6.4.9).
	aboveCmd := conn.UIDSearch(within(imap.UIDRange{Start: uid + 1}), &imap.SearchOptions{ReturnMin: true, ReturnCount: true})
	selfCmd := conn.UIDSearch(within(imap.UIDRange{Start: uid, Stop: uid}), &imap.SearchOptions{ReturnCount: true})
	var belowCmd *imapclient.SearchCommand
	if uid > 1 {
		belowCmd = conn.UIDSearch(within(imap.UIDRange{Start: 1, Stop: uid - 1}), &imap.SearchOptions{ReturnMax: true, ReturnCount: true})
	}
	above, err := aboveCmd.Wait()
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("UID SEARCH failed: %v", err)
	}
	self, err := selfCmd.Wait()
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("UID SEARCH failed: %v", err)
	}
	below := &imap.SearchData{}
	if belowCmd != nil {
		if below, err = belowCmd.Wait(); err != nil {
			return 0, 0, 0, 0, fmt.Errorf("UID SEARCH failed: %v", err)
		}
	}
	if self.Count == 0 {
		return 0, 0, 0, 0, nil
	}
	if imap.UID(above.Min) <= uid {
		above = &imap.SearchData{}
	}
	total = int(above.Count + 1 + below.Count)
	if descending {
		return imap.UID(above.Min), imap.UID(below.Max), int(above.Count) + 1, total, nil
	}
	return imap.UID(below.Max), imap.UID(above.Min), int(below.Count) + 1, total, nil
}

func getMessagePart(conn *imapclient.Client, mboxName string, uid imap.UID, partPath []int) (*IMAPMessage, *message.Entity, error) {
	if err := ensureMailboxSelected(conn, mboxName); err != nil {
		return nil, nil, err
	}
	// TODO: stream attachments
	msgs, err := conn.Fetch(imap.UIDSetNum(uid), partFetchOptions(partPath, false)).Collect()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to fetch message: %v", err)
	} else if len(msgs) == 0 {
		return nil, nil, alborz.NotFound("notfound.message", fmt.Sprint(uid))
	}
	return messagePart(msgs[0], mboxName, uid, partPath)
}

// The message's header comes too, for what a part's does not carry, like List-Post.
func partFetchOptions(partPath []int, peek bool) *imap.FetchOptions {
	headerItem := &imap.FetchItemBodySection{Peek: true, Part: partPath}
	if len(partPath) > 0 {
		headerItem.Specifier = imap.PartSpecifierMIME
	} else {
		headerItem.Specifier = imap.PartSpecifierHeader
	}
	bodyItem := &imap.FetchItemBodySection{Peek: peek, Part: partPath}
	if len(partPath) > 0 {
		bodyItem.Specifier = imap.PartSpecifierNone
	} else {
		bodyItem.Specifier = imap.PartSpecifierText
	}
	sections := []*imap.FetchItemBodySection{headerItem, bodyItem}
	if len(partPath) > 0 {
		sections = append(sections, &imap.FetchItemBodySection{Peek: true, Specifier: imap.PartSpecifierHeader})
	}
	return &imap.FetchOptions{
		Envelope:      true,
		UID:           true,
		RFC822Size:    true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
		Flags:         true,
		BodySection:   sections,
	}
}

func messagePart(msg *imapclient.FetchMessageBuffer, mboxName string, uid imap.UID, partPath []int) (*IMAPMessage, *message.Entity, error) {
	options := partFetchOptions(partPath, true)
	headerItem, bodyItem := options.BodySection[0], options.BodySection[1]
	var rootHeaderItem *imap.FetchItemBodySection
	if len(partPath) > 0 {
		rootHeaderItem = options.BodySection[2]
	}

	headerBuf := msg.FindBodySection(headerItem)
	bodyBuf := msg.FindBodySection(bodyItem)
	if headerBuf == nil || bodyBuf == nil {
		// A part path the message lacks comes back without the sections.
		if len(partPath) > 0 {
			return nil, nil, alborz.NotFound("notfound.part", fmt.Sprint(uid))
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
		// Show a broken header's part raw rather than fail the page.
		h = textproto.Header{}
		h.Set("Content-Type", "text/plain; charset=utf-8")
		bodyBuf = append(headerBuf, bodyBuf...)
	}

	// An unknown charset leaves the part as its bytes, as the download does.
	part, err := message.New(message.Header{Header: h}, bytes.NewReader(bodyBuf))
	if err != nil && !message.IsUnknownCharset(err) {
		return nil, nil, fmt.Errorf("failed to create message reader: %v", err)
	}

	out := &IMAPMessage{FetchMessageBuffer: msg, Mailbox: mboxName}
	if rh, err := textproto.ReadHeader(bufio.NewReader(bytes.NewReader(rootHeaderBuf))); err == nil {
		out.setListHeaders(rh)
	}
	return out, part, nil
}

// Attaching a message needs its exact bytes, which quoting can never reproduce.
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
		return nil, nil, alborz.NotFound("notfound.message", fmt.Sprint(uid))
	}
	buf := msgs[0].FindBodySection(section)
	if buf == nil {
		return nil, nil, fmt.Errorf("server didn't return the message body")
	}
	return buf, msgs[0].Envelope, nil
}

// A whole message carried inside another, arriving intact (RFC 2046 5.2).
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

// A keyword, not a system flag, so a server may refuse to keep it.
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

// The UID is named only with UIDPLUS (RFC 4315); zero says it was not.
func appendMessage(c *imapclient.Client, msg *OutgoingMessage, role string) (*MailboxInfo, imap.UID, error) {
	mbox, err := getMailboxByRole(c, role)
	if err != nil {
		return nil, 0, err
	}
	if mbox == nil {
		return nil, 0, fmt.Errorf("Unable to resolve mailbox")
	}

	// IMAP needs the final size in advance.
	var buf bytes.Buffer
	if err := msg.WriteMessage(&buf); err != nil {
		return nil, 0, err
	}

	flags := []imap.Flag{imap.FlagSeen}
	if role == "drafts" {
		flags = append(flags, imap.FlagDraft)
	}
	options := imap.AppendOptions{Flags: flags}
	appendCmd := c.Append(mbox.Name(), int64(buf.Len()), &options)
	defer appendCmd.Close()
	if _, err := io.Copy(appendCmd, &buf); err != nil {
		return nil, 0, err
	}
	if err := appendCmd.Close(); err != nil {
		return nil, 0, err
	}
	data, err := appendCmd.Wait()
	if err != nil {
		return nil, 0, err
	}
	return mbox, data.UID, nil
}

func deleteMessage(conn *imapclient.Client, mboxName string, uid imap.UID) error {
	return deleteMessages(conn, mboxName, imap.UIDSetNum(uid))
}

func deleteMessages(conn *imapclient.Client, mboxName string, set imap.NumSet) error {
	if err := storeFlags(conn, mboxName, set, imap.StoreFlagsAdd, []imap.Flag{imap.FlagDeleted}); err != nil {
		return fmt.Errorf("failed to add deleted flag: %w", err)
	}
	if err := conn.Expunge().Close(); err != nil {
		return fmt.Errorf("failed to expunge mailbox: %w", err)
	}
	return nil
}

// Folders are renamed rarely.
const unifiedFoldersTTL = 5 * time.Minute

func (p *plugin) resolveRole(conn *imapclient.Client, username, role string) (string, error) {
	folders, err := p.unifiedFolders.Get(username, func() (map[string]string, error) {
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
