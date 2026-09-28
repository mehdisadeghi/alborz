package alborzbase

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/mail"
	"net/url"
	"path"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-mbox"
	"github.com/emersion/go-message"
	"github.com/emersion/go-message/textproto"
	"github.com/labstack/echo/v4"
)

func (p *plugin) handleExportMbox(ctx *alborz.Context) error {
	mboxName, err := mailboxRef(ctx)
	if err != nil {
		return err
	}
	params, err := ctx.FormParams()
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}
	strip := ctx.FormValue("strip") != ""
	var uids []imap.UID
	if ctx.FormValue("all") != "" {
		query := ctx.QueryParam("query")
		view, err := readView(ctx)
		if err != nil {
			return err
		}
		if uids, err = folderViewUIDs(ctx, mboxName, query, view); err != nil {
			return err
		}
	} else if uids, err = selection(ctx, mboxName, params); err != nil {
		return err
	}
	if len(uids) == 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "no messages selected")
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

func streamMbox(ctx *alborz.Context, refs []rowRef, name string, strip bool) error {
	// The header waits for the first message, so a failed fetch never renders into an .mbox body.
	res := ctx.Response()
	archive := mbox.NewWriter(res)
	archive.Mboxrd = true
	defer archive.Close()
	started := false
	for _, r := range refs {
		s := ctx.LoginFor(r.account)
		if s == nil {
			continue
		}
		// One round trip per message, so no single call outlasts the login's budget.
		var raw []byte
		var env *imap.Envelope
		err := s.DoIMAP(func(c *imapclient.Client) error {
			var err error
			raw, env, err = fetchRawMessage(c, r.mailbox, r.uid)
			return err
		})
		if err != nil {
			if started {
				// Too late for the response: stop, leave the file short, and log why.
				ctx.Logger().Printf("export %s %q uid %v: %v", r.account, r.mailbox, r.uid, err)
				return nil
			}
			return fmt.Errorf("export %q uid %v: %w", r.mailbox, r.uid, err)
		}
		if strip {
			if stripped, err := withoutAttachments(raw, ctx.T("mailbox.attachmentremoved")); err != nil {
				// A message that cannot be taken apart goes out whole: larger than asked, not wrong.
				ctx.Logger().Printf("export %q uid %v: kept attachments: %v", r.mailbox, r.uid, err)
			} else {
				raw = stripped
			}
		}
		if !started {
			res.Header().Set("Content-Disposition", downloadName(name, "messages", ".mbox"))
			res.Header().Set("Content-Type", "application/mbox")
			res.WriteHeader(http.StatusOK)
			started = true
		}
		if err := writeMbox(archive, raw, env); err != nil {
			ctx.Logger().Printf("export %s %q uid %v: %v", r.account, r.mailbox, r.uid, err)
			return nil
		}
		res.Flush()
	}
	return nil
}

func withoutAttachments(raw []byte, note string) ([]byte, error) {
	entity, err := message.Read(bytes.NewReader(raw))
	if err != nil && !message.IsUnknownCharset(err) {
		return nil, err
	}
	mediaType, _, _ := entity.Header.ContentType()
	if mediaType == "multipart/signed" || mediaType == "multipart/encrypted" {
		return raw, nil
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
func writeMbox(archive *mbox.Writer, raw []byte, env *imap.Envelope) error {
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
	_, err = w.Write(raw)
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

func (p *plugin) exportPage(ctx *alborz.Context, uids []imap.UID, form url.Values) error {
	ibase, err := p.newIMAPBaseRenderData(ctx, alborz.NewBaseRenderData(ctx))
	if err != nil {
		return err
	}
	ibase.BaseRenderData.WithTitle(fmt.Sprintf(ctx.T("folder.exporttitle"), ibase.Mailbox.Label))
	data := &ExportRenderData{IMAPBaseRenderData: *ibase, Count: len(uids), UIDs: uids}
	if _, _, whole := wholeView(form); whole {
		data.UIDs, data.Everything = nil, form.Get("next")
	}
	return ctx.Render(http.StatusOK, "export.html", data)
}

func (p *plugin) handleExportPage(ctx *alborz.Context) error {
	mboxName, err := mailboxRef(ctx)
	if err != nil {
		return err
	}
	ibase, err := p.newIMAPBaseRenderData(ctx, alborz.NewBaseRenderData(ctx))
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
		if criteria := listCriteria(c, settings, data.Query, ibase.ListView); criteria != nil {
			if err := ensureMailboxSelected(c, mboxName); err != nil {
				return err
			}
			found, err := c.UIDSearch(criteria, nil).Wait()
			if err != nil {
				return err
			}
			data.Count = len(found.AllUIDs())
			return nil
		}
		options := &imap.StatusOptions{NumMessages: true, Size: c.Caps().Has(imap.CapStatusSize)}
		st, err := c.Status(mboxName, options).Wait()
		if err != nil {
			return err
		}
		if st.NumMessages != nil {
			data.Count = int(*st.NumMessages)
		}
		if st.Size != nil {
			data.Size = *st.Size
		}
		return nil
	})
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "export.html", data)
}

// Nothing is stored: the bound is for the reader's patience and the connection, not disk.
const importMaxSize = 1 << 30

// Always into a new folder, so an import never lands in an existing one.
func (p *plugin) handleImport(ctx *alborz.Context) error {
	ibase, err := p.newIMAPBaseRenderData(ctx, alborz.NewBaseRenderData(ctx))
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
		return ctx.Render(status, "import.html", &NewMailboxRenderData{
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
	req.Body = http.MaxBytesReader(ctx.Response(), req.Body, importMaxSize)
	form, err := req.MultipartReader()
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}
	var file *multipart.Part
	for file == nil {
		part, err := form.NextPart()
		if err == io.EOF {
			return render(http.StatusUnprocessableEntity, ctx.T("form.fileneeded"))
		}
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err)
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
		return echo.NewHTTPError(http.StatusBadRequest, "not signed in to that account")
	}
	fullName := location.folder(name)
	if err := login.DoIMAP(func(c *imapclient.Client) error {
		return c.Create(fullName, nil).Wait()
	}); err != nil {
		return render(http.StatusUnprocessableEntity, fmt.Sprintf(ctx.T("form.foldertaken"), fullName, err))
	}

	var count, held int
	importErr := login.DoIMAPWork(ctx.Request().Context(), alborz.IMAPScan, importBound, func(c *imapclient.Client) error {
		var err error
		count, held, err = appendMessages(c, fullName, file, map[string]bool{})
		return err
	})
	p.accountChanged(selectedAccount)
	if importErr != nil {
		ctx.Logger().Printf("import into %q stopped after %d: %v", fullName, count, importErr)
		ctx.PutNotice(ctx.Tf("notice.importstopped", count, fullName))
	} else {
		ctx.PutNotice(ctx.Tf("notice.imported", count, fullName))
	}
	if held > 0 {
		ctx.Notify(alborz.Notice{Text: ctx.Tf("notice.importheld", held)})
	}
	return ctx.Redirect(http.StatusFound, folderURL(ctx, selectedAccount, fullName))
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
		return echo.NewHTTPError(http.StatusRequestEntityTooLarge, err)
	}
	return echo.NewHTTPError(http.StatusBadRequest, err)
}

func (p *plugin) handleDropImport(ctx *alborz.Context) error {
	mboxName, err := mailboxRef(ctx)
	if err != nil {
		return err
	}
	req := ctx.Request()
	req.Body = http.MaxBytesReader(ctx.Response(), req.Body, importMaxSize)
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
		return echo.NewHTTPError(http.StatusBadRequest, "not signed in to that account")
	}
	// The drop takes the connection once and reads the body inside, so files are appended as they arrive.
	count, held := 0, 0
	var bodyErr error
	importErr := login.DoIMAPWork(ctx.Request().Context(), alborz.IMAPScan, importBound, func(c *imapclient.Client) error {
		if err := ensureMailboxSelected(c, mboxName); err != nil {
			return err
		}
		// So a file dropped twice, or a batch resent in halves, adds nothing the second time.
		seen, err := folderMessageIDs(c)
		if err != nil {
			return err
		}
		for {
			part, err := form.NextPart()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				bodyErr = err
				return nil
			}
			if part.FormName() != "file" {
				continue
			}
			n, already, err := appendMessages(c, mboxName, part, seen)
			count += n
			held += already
			if err != nil {
				return err
			}
		}
	})
	if bodyErr != nil {
		p.accountChanged(account)
		return importBodyError(bodyErr)
	}
	if importErr != nil {
		p.accountChanged(account)
		ctx.Logger().Printf("drop into %q stopped after %d: %v", mboxName, count, importErr)
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeFailed, Text: ctx.Tf("notice.importstopped", count, mboxName)})
		return ctx.NoContent(http.StatusOK)
	}
	p.accountChanged(account)
	ctx.Notify(alborz.Notice{Text: ctx.Tf("notice.imported", count, mboxName)})
	if held > 0 {
		ctx.Notify(alborz.Notice{Text: ctx.Tf("notice.importheld", held)})
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
			return crlf(raw), err
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
