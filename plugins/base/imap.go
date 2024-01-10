package alpsbase

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	//"time"

	"github.com/dustin/go-humanize"
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
}

func (mbox *MailboxInfo) Name() string {
	return mbox.Mailbox
}

func (mbox *MailboxInfo) URL() *url.URL {
	return &url.URL{
		Path: fmt.Sprintf("/mailbox/%v", url.PathEscape(mbox.Name())),
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

func listMailboxes(conn *imapclient.Client) ([]MailboxInfo, error) {
	var options imap.ListOptions
	if conn.Caps().Has(imap.CapListStatus) {
		options.ReturnStatus = &imap.StatusOptions{
			NumMessages: true,
			UIDValidity: true,
			NumUnseen:   true,
		}
	}

	var mailboxes []MailboxInfo
	list := conn.List("", "*", &options)
	for {
		data := list.Next()
		if data == nil {
			break
		}
		mbox := MailboxInfo{data, false, -1, -1}
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

type MailboxStatus struct {
	*imap.StatusData
}

func (mbox *MailboxStatus) Name() string {
	return mbox.Mailbox
}

func (mbox *MailboxStatus) URL() *url.URL {
	return &url.URL{
		Path: fmt.Sprintf("/mailbox/%v", url.PathEscape(mbox.Name())),
	}
}

func getMailboxStatus(conn *imapclient.Client, name string) (*MailboxStatus, error) {
	status, err := conn.Status(name, &imap.StatusOptions{
		NumMessages: true,
		UIDValidity: true,
		NumUnseen:   true,
	}).Wait()
	if err != nil {
		return nil, fmt.Errorf("failed to get mailbox status: %v", err)
	}
	return &MailboxStatus{status}, nil
}

type mailboxType int

const (
	mailboxSent mailboxType = iota
	mailboxDrafts
)

func getMailboxByType(conn *imapclient.Client, mboxType mailboxType) (*MailboxInfo, error) {
	// TODO: configurable fallback names?
	var attr imap.MailboxAttr
	var fallbackNames []string
	switch mboxType {
	case mailboxSent:
		attr = imap.MailboxAttrSent
		fallbackNames = []string{"Sent"}
	case mailboxDrafts:
		attr = imap.MailboxAttrDrafts
		fallbackNames = []string{"Draft", "Drafts"}
	}

	list := conn.List("", "%", nil)

	var attrMatched bool
	var best *imap.ListData
	for {
		mbox := list.Next()
		if mbox == nil {
			break
		}

		for _, a := range mbox.Attrs {
			if attr == a {
				best = mbox
				attrMatched = true
				break
			}
		}
		if attrMatched {
			break
		}

		for _, fallback := range fallbackNames {
			if strings.EqualFold(fallback, mbox.Mailbox) {
				best = mbox
				break
			}
		}
	}
	if err := list.Close(); err != nil {
		return nil, fmt.Errorf("failed to get mailbox with attribute %q: %v", attr, err)
	}

	if best == nil {
		return nil, nil
	}
	return &MailboxInfo{best, false, -1, -1}, nil
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
}

func (msg *IMAPMessage) URL() *url.URL {
	return &url.URL{
		Path: fmt.Sprintf("/message/%v/%v", url.PathEscape(msg.Mailbox), msg.UID),
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
	return humanize.IBytes(uint64(node.Size))
}

func (node IMAPPartNode) URL(raw bool) *url.URL {
	u := node.Message.URL()
	if raw {
		u.Path += "/raw"
	}
	q := u.Query()
	q.Set("part", node.PathString())
	u.RawQuery = q.Encode()
	return u
}

func (node IMAPPartNode) IsText() bool {
	return strings.HasPrefix(strings.ToLower(node.MIMEType), "text/")
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

func (msg *IMAPMessage) HasFlag(flag imap.Flag) bool {
	for _, f := range msg.Flags {
		if f == flag {
			return true
		}
	}
	return false
}

func listMessages(conn *imapclient.Client, mboxName string, page, messagesPerPage int) (msgs []IMAPMessage, total int, err error) {
	// A NOOP will ensure we notice any new message
	noop := conn.Noop()
	if err := ensureMailboxSelected(conn, mboxName); err != nil {
		return nil, 0, err
	}
	if err := noop.Wait(); err != nil {
		return nil, 0, err
	}

	mbox := conn.Mailbox()
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
	options := imap.FetchOptions{
		Flags:         true,
		Envelope:      true,
		UID:           true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
	}
	imapMsgs, err := conn.Fetch(seqSet, &options).Collect()
	if err != nil {
		return nil, 0, fmt.Errorf("failed to fetch message list: %v", err)
	}

	for _, msg := range imapMsgs {
		msgs = append(msgs, IMAPMessage{msg, mboxName})
	}

	// Reverse list of messages
	for i := len(msgs)/2 - 1; i >= 0; i-- {
		opp := len(msgs) - 1 - i
		msgs[i], msgs[opp] = msgs[opp], msgs[i]
	}

	return msgs, total, nil
}

func searchMessages(conn *imapclient.Client, mboxName, query string, page, messagesPerPage int) (msgs []IMAPMessage, total int, err error) {
	if err := ensureMailboxSelected(conn, mboxName); err != nil {
		return nil, 0, err
	}

	searchCriteria := PrepareSearch(query)

	var nums []uint32
	if !conn.Caps().Has(imap.CapSort) {
		data, err := conn.Search(searchCriteria, nil).Wait()
		if err != nil {
			return nil, 0, fmt.Errorf("SEARCH failed: %v", err)
		}
		nums = data.AllSeqNums()
	} else {
		sortOptions := &imapclient.SortOptions{
			SearchCriteria: searchCriteria,
			SortCriteria: []imapclient.SortCriterion{
				{Key: imapclient.SortKeyDate, Reverse: true},
			},
		}
		nums, err = conn.Sort(sortOptions).Wait()
		if err != nil {
			return nil, 0, fmt.Errorf("SORT failed: %v", err)
		}
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
	options := imap.FetchOptions{
		Envelope:      true,
		Flags:         true,
		UID:           true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
	}
	results, err := conn.Fetch(seqSet, &options).Collect()
	if err != nil {
		return nil, 0, fmt.Errorf("failed to fetch message list: %v", err)
	}

	msgs = make([]IMAPMessage, len(nums))
	for _, msg := range results {
		i, ok := indexes[msg.SeqNum]
		if !ok {
			continue
		}
		msgs[i] = IMAPMessage{msg, mboxName}
	}

	return msgs, total, nil
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

	options := imap.FetchOptions{
		Envelope:      true,
		UID:           true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
		Flags:         true,
		RFC822Size:    true,
		BodySection:   []*imap.FetchItemBodySection{headerItem, bodyItem},
	}

	// TODO: stream attachments
	msgs, err := conn.Fetch(imap.UIDSetNum(uid), &options).Collect()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to fetch message: %v", err)
	} else if len(msgs) == 0 {
		return nil, nil, fmt.Errorf("server didn't return message")
	}
	msg := msgs[0]

	var headerBuf, bodyBuf []byte
	for item, b := range msg.BodySection {
		if item.Specifier == headerItem.Specifier {
			headerBuf = b
		} else if item.Specifier == bodyItem.Specifier {
			bodyBuf = b
		}
	}
	if headerBuf == nil || bodyBuf == nil {
		return nil, nil, fmt.Errorf("server didn't return header and body")
	}

	h, err := textproto.ReadHeader(bufio.NewReader(bytes.NewReader(headerBuf)))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read part header: %v", err)
	}

	part, err := message.New(message.Header{h}, bytes.NewReader(bodyBuf))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create message reader: %v", err)
	}

	return &IMAPMessage{msg, mboxName}, part, nil
}

func markMessageAnswered(conn *imapclient.Client, mboxName string, uid imap.UID) error {
	if err := ensureMailboxSelected(conn, mboxName); err != nil {
		return err
	}

	return conn.Store(imap.UIDSetNum(uid), &imap.StoreFlags{
		Op:     imap.StoreFlagsAdd,
		Silent: true,
		Flags:  []imap.Flag{imap.FlagAnswered},
	}, nil).Close()
}

func appendMessage(c *imapclient.Client, msg *OutgoingMessage, mboxType mailboxType) (*MailboxInfo, error) {
	mbox, err := getMailboxByType(c, mboxType)
	if err != nil {
		return nil, err
	}
	if mbox == nil {
		return nil, fmt.Errorf("Unable to resolve mailbox")
	}

	// IMAP needs to know in advance the final size of the message, so
	// there's no way around storing it in a buffer here.
	var buf bytes.Buffer
	if err := msg.WriteTo(&buf); err != nil {
		return nil, err
	}

	flags := []imap.Flag{imap.FlagSeen}
	if mboxType == mailboxDrafts {
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
	if err := ensureMailboxSelected(conn, mboxName); err != nil {
		return err
	}

	err := conn.Store(imap.UIDSetNum(uid), &imap.StoreFlags{
		Op:     imap.StoreFlagsAdd,
		Silent: true,
		Flags:  []imap.Flag{imap.FlagDeleted},
	}, nil).Close()
	if err != nil {
		return err
	}

	return conn.Expunge().Close()
}
