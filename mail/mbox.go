package mail

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"net/mail"
	"net/url"
	"path"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/pgp"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-mbox"
	"github.com/emersion/go-message"
	"github.com/emersion/go-message/textproto"
)

func (p *plugin) handleExportMbox(ctx *web.Context) error {
	mboxName, err := mailboxRef(ctx)
	if err != nil {
		return err
	}
	params, err := ctx.FormParams()
	if err != nil {
		return web.NewError(http.StatusBadRequest, err)
	}
	strip := ctx.FormValue("strip") != ""
	var uids []imap.UID
	if ctx.FormValue("all") != "" {
		query := ctx.QueryParam("query")
		view, err := readView(ctx.QueryParams())
		if err != nil {
			return err
		}
		if uids, err = folderViewUIDs(ctx.Request().Context(), ctx.Login, mboxName, query, view); err != nil {
			return err
		}
	} else if uids, err = selection(ctx, mboxName, params); err != nil {
		return err
	}
	if len(uids) == 0 {
		return web.NewError(http.StatusBadRequest, "no messages selected")
	}
	if ctx.FormValue("ask") != "" {
		return p.exportPage(ctx, uids, params)
	}

	name := mboxName
	if strip {
		name += " " + ctx.T("mailbox.textonly")
	}
	return streamMbox(ctx, folderRefs(ctx.Login.Username(), mboxName, uids), name, strip)
}

func folderRefs(account, mailbox string, uids []imap.UID) []rowRef {
	refs := make([]rowRef, len(uids))
	for i, uid := range uids {
		refs[i] = rowRef{account: account, mailbox: mailbox, uid: uid}
	}
	return refs
}

func streamMbox(ctx *web.Context, refs []rowRef, name string, strip bool) error {
	// The header waits for the first message, so a failed fetch never renders into an .mbox body.
	res := ctx.Response()
	archive := mbox.NewWriter(res)
	archive.Mboxrd = true
	defer archive.Close()
	begin := func() {
		if !res.Committed {
			res.Header().Set("Content-Disposition", downloadName(name, "messages", ".mbox"))
			res.Header().Set("Content-Type", "application/mbox")
			res.WriteHeader(http.StatusOK)
		}
	}
	for len(refs) > 0 {
		// Each account's run of messages shares one connection of its own.
		n := 1
		for n < len(refs) && refs[n].account == refs[0].account {
			n++
		}
		run := refs[:n]
		refs = refs[n:]
		s := ctx.LoginFor(run[0].account)
		if s == nil {
			continue
		}
		err := exportRun(ctx.Request().Context(), ctx.Logger(), s, run, archive, strip, ctx.T("mailbox.attachmentremoved"), begin, res.Flush)
		// Too late for the response: stop, leave the file short, and log why.
		if err != nil {
			return streamed(ctx, err)
		}
	}
	return nil
}

// exportRun streams one account's run of messages into the archive, on a connection of its own.
func exportRun(ctx context.Context, log *log.Logger, s *alborz.Login, run []rowRef, archive *mbox.Writer, strip bool, note string, begin, flush func()) error {
	return s.DoIMAPTransfer(ctx, func(c *imapclient.Client) error {
		for _, r := range run {
			if err := exportMessage(log, c, archive, r, strip, note, begin); err != nil {
				return fmt.Errorf("export %s %q uid %v: %w", r.account, r.mailbox, r.uid, err)
			}
			flush()
		}
		return nil
	})
}

// exportMessage streams one message into the archive; stripped, only its text is held, note standing for what went.
func exportMessage(log *log.Logger, c *imapclient.Client, archive *mbox.Writer, r rowRef, strip bool, note string, begin func()) error {
	if err := ensureMailboxSelected(c, r.mailbox); err != nil {
		return err
	}
	msgs, err := c.Fetch(imap.UIDSetNum(r.uid), &imap.FetchOptions{UID: true, Envelope: true}).Collect()
	if err != nil {
		return err
	}
	if len(msgs) == 0 {
		return alborz.NotFound("notfound.message", fmt.Sprint(r.uid))
	}
	env := msgs[0].Envelope
	whole := &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{{Peek: true}}}
	var stripped []byte
	if strip {
		err := streamSection(c, r.uid, whole, func(lit imap.LiteralReader) error {
			var err error
			stripped, err = withoutAttachments(lit, note)
			return err
		})
		if err != nil && !errors.Is(err, errKeepWhole) {
			// A message that cannot be taken apart goes out whole: larger than asked, not wrong.
			log.Printf("export %q uid %v: kept attachments: %v", r.mailbox, r.uid, err)
		}
	}
	if stripped != nil {
		begin()
		return writeMbox(archive, bytes.NewReader(stripped), env)
	}
	return streamSection(c, r.uid, whole, func(lit imap.LiteralReader) error {
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
			pw.Close()
			continue
		}
		size, _ := io.Copy(io.Discard, part.Body)
		var stub message.Header
		stub.SetContentType("text/plain", map[string]string{"charset": "utf-8"})
		pw, err := w.CreatePart(stub)
		if err != nil {
			return err
		}
		fmt.Fprintf(pw, "[%s: %s, %s, %d B]\r\n", note, name, mediaType, size)
		pw.Close()
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

// ExportRenderData renders the page before a folder or a search's results are exported.
type ExportRenderData struct {
	IMAPBaseRenderData
	Query string
	// ViewName is the rail view being exported; empty for a whole folder or a search.
	ViewName string
	Count    int
	Size     int64 // zero when the server does not say (STATUS=SIZE)
	// UIDs are the messages picked in the list; empty for a whole folder or a search.
	UIDs []imap.UID
	// Everything is the list's address when all of it was picked: a hidden field per message would weigh megabytes.
	Everything string
}

func (p *plugin) exportPage(ctx *web.Context, uids []imap.UID, form url.Values) error {
	ibase, err := p.newIMAPBaseRenderData(ctx, web.NewBaseRenderData(ctx))
	if err != nil {
		return err
	}
	ibase.BaseRenderData.WithTitle(fmt.Sprintf(ctx.T("folder.exporttitle"), ibase.Mailbox.Label))
	data := &ExportRenderData{IMAPBaseRenderData: *ibase, Count: len(uids), UIDs: uids}
	if _, _, whole := wholeView(form); whole {
		data.UIDs, data.Everything = nil, form.Get("next")
	}
	return ctx.Render(http.StatusOK, "mail/export", data)
}

func (p *plugin) handleExportPage(ctx *web.Context) error {
	mboxName, err := mailboxRef(ctx)
	if err != nil {
		return err
	}
	ibase, err := p.newIMAPBaseRenderData(ctx, web.NewBaseRenderData(ctx))
	if err != nil {
		return err
	}
	ibase.BaseRenderData.WithTitle(fmt.Sprintf(ctx.T("folder.exporttitle"), mboxName))
	data := &ExportRenderData{IMAPBaseRenderData: *ibase, Query: ctx.QueryParam("query")}
	if ibase.ListView != "" {
		data.ViewName = viewTitle(ctx, ibase.ListView)
	}
	settings, err := LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return err
	}
	err = ctx.DoIMAPScan(func(c *imapclient.Client) error {
		var err error
		data.Count, data.Size, err = exportSize(c, settings, mboxName, data.Query, ibase.ListView)
		return err
	})
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "mail/export", data)
}

// Nothing is stored: the bound is for the reader's patience and the connection, not disk.
const importMaxSize = 1 << 30

// Always into a new folder, so an import never lands in an existing one.
func (p *plugin) handleImport(ctx *web.Context) error {
	ibase, err := p.newIMAPBaseRenderData(ctx, web.NewBaseRenderData(ctx))
	if err != nil {
		return err
	}
	ibase.BaseRenderData.WithTitle(ctx.T("folder.import"))
	selectedAccount := ctx.Login.Username()
	if ctx.URLAccount() != "" {
		selectedAccount = ctx.URLAccount()
	}
	locationGroups := p.newMailboxLocationGroups(ctx)
	selectedLocation := ""
	for _, group := range locationGroups {
		if group.Account == selectedAccount && len(group.Locations) > 0 {
			selectedLocation = group.Locations[0].Key
			break
		}
	}
	name := ""
	render := func(status int, errText string) error {
		ibase.BaseRenderData.Refused(errText)
		return ctx.Render(status, "mail/import", &NewMailboxRenderData{
			IMAPBaseRenderData: *ibase,
			Name:               name,
			SelectedAccount:    selectedAccount,
			SelectedLocation:   selectedLocation,
			LocationGroups:     locationGroups,
		})
	}
	if ctx.Request().Method != http.MethodPost {
		return render(http.StatusOK, "")
	}

	// Streamed part by part: the file is the last field, so the folder is settled first.
	req := ctx.Request()
	form, err := req.MultipartReader()
	if err != nil {
		return web.NewError(http.StatusBadRequest, err)
	}
	var file *multipart.Part
	for file == nil {
		part, err := form.NextPart()
		if err == io.EOF {
			return render(http.StatusUnprocessableEntity, ctx.T("form.fileneeded"))
		}
		if err != nil {
			return web.NewError(http.StatusBadRequest, err)
		}
		switch part.FormName() {
		case "location":
			selectedLocation = formField(part)
		case "name":
			name = strings.TrimSpace(formField(part))
		case "file":
			file = part
		}
	}
	location, account := locationByKey(locationGroups, selectedLocation)
	if location == nil {
		return render(http.StatusUnprocessableEntity, ctx.T("form.destinationneeded"))
	}
	selectedAccount = account
	if name == "" {
		name = strings.TrimSuffix(path.Base(file.FileName()), path.Ext(file.FileName()))
	}
	if name == "" {
		return render(http.StatusUnprocessableEntity, ctx.T("form.nameneeded"))
	}
	login := ctx.LoginFor(selectedAccount)
	if login == nil {
		return web.NewError(http.StatusBadRequest, "not signed in to that account")
	}
	fullName := location.folder(name)
	if err := createFolder(login, fullName); err != nil {
		return render(http.StatusUnprocessableEntity, fmt.Sprintf(ctx.T("form.foldertaken"), fullName, err))
	}

	count, held, importErr := importFile(ctx.Request().Context(), login, fullName, file)
	p.accountChanged(selectedAccount)
	if importErr != nil {
		ctx.Logger().Printf("import into %q stopped after %d: %v", fullName, count, importErr)
		ctx.PutNotice(ctx.Tf("notice.importstopped", count, fullName))
	} else {
		ctx.PutNotice(ctx.Tf("notice.imported", count, fullName))
	}
	if held > 0 {
		ctx.Notify(web.Notice{Text: ctx.Tf("notice.importheld", held)})
	}
	return ctx.Redirect(http.StatusFound, folderURL(ctx, selectedAccount, fullName))
}

// importFile appends the messages of an mbox or .eml file to folder, taking the connection once.
func importFile(ctx context.Context, login *alborz.Login, folder string, r io.Reader) (added, held int, err error) {
	err = login.DoIMAPWork(ctx, alborz.IMAPScan, importBound, func(c *imapclient.Client) error {
		var err error
		added, held, err = appendMessages(c, folder, r, map[string]bool{})
		return err
	})
	return added, held, err
}

// dropFiles appends the messages of each file next gives, until io.EOF, to folder; the connection is
// taken once and the files read inside, so they are appended as they arrive. A file that fails to
// arrive is fileErr, the sender's; err is the server's.
func dropFiles(ctx context.Context, login *alborz.Login, folder string, next func() (io.Reader, error)) (added, held int, fileErr, err error) {
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
			n, already, err := appendMessages(c, folder, file, seen)
			added += n
			held += already
			if err != nil {
				return err
			}
		}
	})
	return added, held, fileErr, err
}

// A message whose Message-ID the folder already holds is skipped, so a repeated drop adds nothing.
func appendMessages(c *imapclient.Client, folder string, r io.Reader, seen map[string]bool) (added, held int, err error) {
	next := mboxMessages(r)
	for {
		raw, err := next()
		if err == io.EOF {
			return added, held, nil
		}
		if err != nil {
			return added, held, err
		}
		id := messageID(raw)
		if id != "" && seen[id] {
			held++
			continue
		}
		if id != "" {
			seen[id] = true
		}
		options := imap.AppendOptions{Flags: []imap.Flag{imap.FlagSeen}, Time: messageDate(raw)}
		cmd := c.Append(folder, int64(len(raw)), &options)
		if _, err := cmd.Write(raw); err != nil {
			return added, held, err
		}
		if err := cmd.Close(); err != nil {
			return added, held, err
		}
		if _, err := cmd.Wait(); err != nil {
			return added, held, err
		}
		added++
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
		if id := strings.Trim(buf.Envelope.MessageID, "<>"); id != "" {
			held[id] = true
		}
	}
	return held, cmd.Close()
}

func messageID(raw []byte) string {
	header, err := textproto.ReadHeader(bufio.NewReader(bytes.NewReader(raw)))
	if err != nil {
		return ""
	}
	return strings.Trim(strings.TrimSpace(header.Get("Message-Id")), "<>")
}

// A body past the bound is 413, as a proxy in front would answer, so clients split alike.
func importBodyError(err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return web.NewError(http.StatusRequestEntityTooLarge, err)
	}
	return web.NewError(http.StatusBadRequest, err)
}

func (p *plugin) handleDropImport(ctx *web.Context) error {
	mboxName, err := mailboxRef(ctx)
	if err != nil {
		return err
	}
	req := ctx.Request()
	form, err := req.MultipartReader()
	if err != nil {
		return importBodyError(err)
	}
	account := ctx.Login.Username()
	if ctx.URLAccount() != "" {
		account = ctx.URLAccount()
	}
	login := ctx.LoginFor(account)
	if login == nil {
		return web.NewError(http.StatusBadRequest, "not signed in to that account")
	}
	files := func() (io.Reader, error) {
		for {
			part, err := form.NextPart()
			if err != nil || part.FormName() == "file" {
				return part, err
			}
		}
	}
	count, held, bodyErr, importErr := dropFiles(ctx.Request().Context(), login, mboxName, files)
	if bodyErr != nil {
		p.accountChanged(account)
		return importBodyError(bodyErr)
	}
	if importErr != nil {
		p.accountChanged(account)
		ctx.Logger().Printf("drop into %q stopped after %d: %v", mboxName, count, importErr)
		ctx.Notify(web.Notice{Kind: web.NoticeFailed, Text: ctx.Tf("notice.importstopped", count, mboxName)})
		return ctx.NoContent(http.StatusOK)
	}
	p.accountChanged(account)
	ctx.Notify(web.Notice{Text: ctx.Tf("notice.imported", count, mboxName)})
	if held > 0 {
		ctx.Notify(web.Notice{Text: ctx.Tf("notice.importheld", held)})
	}
	return ctx.NoContent(http.StatusOK)
}

func formField(part *multipart.Part) string {
	b, _ := io.ReadAll(io.LimitReader(part, maxDownloadName*4))
	return string(b)
}

// messageDate is kept as the delivery time, so the folder sorts as the archive did.
func messageDate(raw []byte) time.Time {
	header, err := textproto.ReadHeader(bufio.NewReader(bytes.NewReader(raw)))
	if err == nil {
		if t, err := mail.ParseDate(header.Get("Date")); err == nil {
			return t
		}
	}
	return time.Now()
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

// exportSize is how many messages a folder, or its narrowing, holds, and their size where the server says (STATUS=SIZE).
func exportSize(c *imapclient.Client, settings *Settings, mboxName, query, view string) (count int, size int64, err error) {
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
