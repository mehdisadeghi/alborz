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
	"github.com/emersion/go-message"
	"github.com/emersion/go-message/textproto"
	"github.com/labstack/echo/v4"
)

// handleExportMbox writes messages as one mbox, which is the container
// every mail client reads and the only one that holds more than a
// single message without inventing a layout. The selection is the
// usual ask; all=1 takes everything the view holds, a folder or a
// search's results, and strip=1 leaves the attachments out.
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
	// Leaving the attachments out is a choice, and a choice gets the
	// page rather than a menu row that decides for the reader.
	if ctx.FormValue("ask") != "" {
		return exportPage(ctx, uids, params)
	}

	name := mboxName
	if strip {
		name += " " + ctx.T("mailbox.textonly")
	}
	return streamMbox(ctx, folderRefs(ctx.Session.Username(), mboxName, uids), name, strip)
}

// folderRefs names a folder's selection as a merged list names its
// rows, for what acts on either.
func folderRefs(account, mailbox string, uids []imap.UID) []rowRef {
	refs := make([]rowRef, len(uids))
	for i, uid := range uids {
		refs[i] = rowRef{account: account, mailbox: mailbox, uid: uid}
	}
	return refs
}

// streamMbox writes a selection, of one folder or across accounts, as
// one mbox named name.
func streamMbox(ctx *alborz.Context, refs []rowRef, name string, strip bool) error {
	// The header is written only once a message is actually in hand.
	// Committing the response first meant that a fetch which failed had
	// the error page rendered into a body already claiming to be an
	// mbox, and the reader was handed an .mbox file full of HTML.
	res := ctx.Response()
	started := false
	for _, r := range refs {
		s := ctx.SessionFor(r.account)
		if s == nil {
			continue
		}
		// One round trip per message, each with the session's own
		// budget. The whole export inside one call was cut off by the
		// watchdog after ten seconds, with the file left short.
		var raw []byte
		var env *imap.Envelope
		err := s.DoIMAP(func(c *imapclient.Client) error {
			var err error
			raw, env, err = fetchRawMessage(c, r.mailbox, r.uid)
			return err
		})
		if err != nil {
			if started {
				// Too late to say so in the response: stop, leave
				// the file short, and put the reason in the log
				// rather than in the download.
				ctx.Logger().Printf("export %s %q uid %v: %v", r.account, r.mailbox, r.uid, err)
				return nil
			}
			return fmt.Errorf("export %q uid %v: %w", r.mailbox, r.uid, err)
		}
		if strip {
			if stripped, err := withoutAttachments(raw, ctx.T("mailbox.attachmentremoved")); err != nil {
				// A message that cannot be taken apart goes out whole:
				// the archive is then larger than asked, not wrong.
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
		if err := writeMbox(res, raw, env); err != nil {
			ctx.Logger().Printf("export %s %q uid %v: %v", r.account, r.mailbox, r.uid, err)
			return nil
		}
		res.Flush()
	}
	return nil
}

// withoutAttachments is the message with every part that is not text
// replaced by a line saying what was there, the way Apple Mail's
// Remove Attachments leaves a note. A signed or encrypted message is
// left whole, since taking it apart would break what it proves. The
// text parts are re-encoded in UTF-8, which is what they were decoded
// into on the way through.
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
		keep := strings.HasPrefix(mediaType, "multipart/") ||
			(strings.HasPrefix(mediaType, "text/") && disposition != "attachment")
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
		name := dispParams["filename"]
		if name == "" {
			_, params, _ := header.ContentType()
			name = params["name"]
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

// ExportRenderData is the page before a whole folder, or a search's
// results, is written out: what it holds, and the one choice.
type ExportRenderData struct {
	IMAPBaseRenderData
	Query string
	// ViewName is the rail view being exported, named as the rail names
	// it; empty for a whole folder or a search.
	ViewName string
	Count    int
	Size     int64 // zero when the server does not say (STATUS=SIZE)
	// UIDs are the messages picked in the list, empty when the page is
	// about the whole folder or a search's results.
	UIDs []imap.UID
	// Everything is the address of the list when the pick was all of
	// it. The form names the list again, as the list's own form did: a
	// hidden field for each of a hundred thousand messages is a page of
	// megabytes, posted back.
	Everything string
}

// exportPage asks about a selection the reader has already made: the
// same page, counting what was picked rather than what the folder
// holds.
func exportPage(ctx *alborz.Context, uids []imap.UID, form url.Values) error {
	ibase, err := newIMAPBaseRenderData(ctx, alborz.NewBaseRenderData(ctx))
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

// handleExportPage asks before a folder leaves: a whole folder is not
// a casual click, and this is where the archive's size and the choice
// to leave attachments out belong, rather than in a menu.
func handleExportPage(ctx *alborz.Context) error {
	mboxName, err := mailboxRef(ctx)
	if err != nil {
		return err
	}
	ibase, err := newIMAPBaseRenderData(ctx, alborz.NewBaseRenderData(ctx))
	if err != nil {
		return err
	}
	ibase.BaseRenderData.WithTitle(fmt.Sprintf(ctx.T("folder.exporttitle"), mboxName))
	data := &ExportRenderData{IMAPBaseRenderData: *ibase, Query: ctx.QueryParam("query")}
	if ibase.ListView != "" {
		data.ViewName = viewTitle(ctx, ibase.ListView)
	}
	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return err
	}
	err = ctx.DoIMAPScan(func(c *imapclient.Client) error {
		// A search or a view is counted the way it is listed; the whole
		// folder is a STATUS away.
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

// importMaxSize bounds an upload. Nothing of it is stored: the file is
// read as it arrives and each message handed to the server, so the
// bound is for the reader's patience and the connection, not for disk.
// A larger archive is split by whoever made it.
const importMaxSize = 1 << 30

// handleImport reads an mbox or a single message into a folder made
// for it. Never into one that exists: the form names the folder, the
// file's own name as the default, and the server refuses a name taken,
// which is what keeps an import from landing in somebody's inbox.
func handleImport(ctx *alborz.Context) error {
	ibase, err := newIMAPBaseRenderData(ctx, alborz.NewBaseRenderData(ctx))
	if err != nil {
		return err
	}
	ibase.BaseRenderData.WithTitle(ctx.T("folder.import"))
	selectedAccount := ctx.Session.Username()
	if ctx.URLAccount() != "" {
		selectedAccount = ctx.URLAccount()
	}
	locationGroups := newMailboxLocationGroups(ctx)
	selectedLocation := ""
	for _, group := range locationGroups {
		if group.Account == selectedAccount && len(group.Locations) > 0 {
			selectedLocation = group.Locations[0].Key
			break
		}
	}
	name := ""
	render := func(status int, errText string) error {
		return ctx.Render(status, "import.html", &NewMailboxRenderData{
			IMAPBaseRenderData: *ibase,
			Error:              errText,
			Name:               name,
			SelectedAccount:    selectedAccount,
			SelectedLocation:   selectedLocation,
			LocationGroups:     locationGroups,
		})
	}
	if ctx.Request().Method != http.MethodPost {
		return render(http.StatusOK, "")
	}

	// The body is read as a stream, part by part, and never parsed
	// whole: the file is the last field of the form, so the folder is
	// settled before the first message arrives.
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
	session := ctx.SessionFor(selectedAccount)
	if session == nil {
		return echo.NewHTTPError(http.StatusBadRequest, "not signed in to that account")
	}
	fullName := location.folder(name)
	if err := session.DoIMAP(func(c *imapclient.Client) error {
		return c.Create(fullName, nil).Wait()
	}); err != nil {
		return render(http.StatusUnprocessableEntity, fmt.Sprintf(ctx.T("form.foldertaken"), fullName, err))
	}

	var count, held int
	importErr := session.DoIMAPWork(ctx.Request().Context(), alborz.IMAPScan, importBound, func(c *imapclient.Client) error {
		var err error
		count, held, err = appendMessages(c, fullName, file, map[string]bool{})
		return err
	})
	accountChanged(selectedAccount)
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

// appendMessages puts every message an mbox or a single file holds into
// the folder, one APPEND each within the session's own bound. It
// answers how many landed and how many were there already: an import
// that stops leaves the first n where they are, which a notice can then
// say rather than guess.
//
// A message the folder already holds is left alone, so the same file
// dropped twice - or a drop repeated after a failure - adds nothing the
// second time. Identity is the Message-ID (RFC 5322 3.6.4); a message
// without one is taken at its word and appended.
func appendMessages(c *imapclient.Client, folder string, r io.Reader, seen map[string]bool) (added, held int, err error) {
	messages := newMboxReader(r)
	for {
		raw, err := messages.next()
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

// importBound is how long a whole import may hold its connection. One
// turn per message put every message behind the page's own requests,
// and an import of eighty stopped halfway when one of those waits ran
// out; the import takes the connection once and keeps it.
const importBound = 10 * time.Minute

// folderMessageIDs reads what the selected folder already holds, by
// identity. A header search would be the obvious way to ask, and it is
// the wrong one: it matches a substring of the field (RFC 9051 6.4.4),
// a server that cannot search headers may answer with everything, and
// one that indexes in its own time does not find what was appended a
// moment ago - which is how a retried batch became a second copy.
// Reading the envelopes answers from the folder itself, one at a time:
// a large inbox's envelopes all at once are its ids a hundred times
// over, held for a drop of one file.
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

// messageID is what the message calls itself, empty when it says
// nothing.
func messageID(raw []byte) string {
	header, err := textproto.ReadHeader(bufio.NewReader(bytes.NewReader(raw)))
	if err != nil {
		return ""
	}
	return strings.Trim(strings.TrimSpace(header.Get("Message-Id")), "<>")
}

// importBodyError names what went wrong with the upload itself. A body
// past the bound is 413, the answer a proxy in front of alborz gives
// for the same reason, so a client that splits its batches on 413 does
// the same here.
func importBodyError(err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return echo.NewHTTPError(http.StatusRequestEntityTooLarge, err)
	}
	return echo.NewHTTPError(http.StatusBadRequest, err)
}

// handleDropImport takes files dropped on a folder and puts their
// messages in that folder. The drop names the destination, so there is
// nothing left to ask; the answer is the notice the page will show.
func handleDropImport(ctx *alborz.Context) error {
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
	account := ctx.Session.Username()
	if ctx.URLAccount() != "" {
		account = ctx.URLAccount()
	}
	session := ctx.SessionFor(account)
	if session == nil {
		return echo.NewHTTPError(http.StatusBadRequest, "not signed in to that account")
	}
	// The whole drop takes the connection once: every message behind
	// the page's own requests is what stopped an import of eighty
	// halfway. The body is read inside, so the files arrive as they are
	// appended rather than being held in memory first.
	count, held := 0, 0
	var bodyErr error
	importErr := session.DoIMAPWork(ctx.Request().Context(), alborz.IMAPScan, importBound, func(c *imapclient.Client) error {
		if err := ensureMailboxSelected(c, mboxName); err != nil {
			return err
		}
		// What the folder holds already, so a file dropped twice - or a
		// batch the proxy refused and the browser sent again in halves -
		// adds nothing the second time.
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
		accountChanged(account)
		return importBodyError(bodyErr)
	}
	if importErr != nil {
		accountChanged(account)
		ctx.Logger().Printf("drop into %q stopped after %d: %v", mboxName, count, importErr)
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeFailed, Text: ctx.Tf("notice.importstopped", count, mboxName)})
		return ctx.NoContent(http.StatusOK)
	}
	accountChanged(account)
	ctx.Notify(alborz.Notice{Text: ctx.Tf("notice.imported", count, mboxName)})
	if held > 0 {
		ctx.Notify(alborz.Notice{Text: ctx.Tf("notice.importheld", held)})
	}
	return ctx.NoContent(http.StatusOK)
}

// formField reads one small text field of a streamed form.
func formField(part *multipart.Part) string {
	b, _ := io.ReadAll(io.LimitReader(part, maxDownloadName*4))
	return string(b)
}

// messageDate is the Date header of a message, for the server to keep
// as the delivery time so the folder sorts as the archive did; now
// when there is none it can read.
func messageDate(raw []byte) time.Time {
	header, err := textproto.ReadHeader(bufio.NewReader(bytes.NewReader(raw)))
	if err == nil {
		if t, err := mail.ParseDate(header.Get("Date")); err == nil {
			return t
		}
	}
	return time.Now()
}

// mboxReader hands out the messages of an mbox one at a time, in the
// CRLF form the server wants, with mboxrd's quoting undone. A file that
// does not open with a separator is one message.
type mboxReader struct {
	r     *bufio.Reader
	first bool
	done  bool
}

func newMboxReader(r io.Reader) *mboxReader {
	return &mboxReader{r: bufio.NewReaderSize(r, 64*1024), first: true}
}

func (m *mboxReader) next() ([]byte, error) {
	if m.done {
		return nil, io.EOF
	}
	var msg bytes.Buffer
	for {
		line, err := m.readLine()
		if err == io.EOF {
			m.done = true
			if msg.Len() == 0 {
				return nil, io.EOF
			}
			return msg.Bytes(), nil
		}
		if err != nil {
			return nil, err
		}
		if bytes.HasPrefix(line, []byte("From ")) {
			if m.first {
				m.first = false
				continue
			}
			if msg.Len() > 0 {
				return msg.Bytes(), nil
			}
			continue
		}
		if m.first {
			// No separator at the top: the whole file is one message,
			// and the line is its first.
			m.first = false
			m.done = true
			msg.Write(line)
			msg.WriteString("\r\n")
			rest, err := io.ReadAll(m.r)
			if err != nil {
				return nil, err
			}
			msg.Write(crlf(rest))
			return msg.Bytes(), nil
		}
		if trimmed := bytes.TrimLeft(line, ">"); bytes.HasPrefix(trimmed, []byte("From ")) {
			line = line[1:]
		}
		msg.Write(line)
		msg.WriteString("\r\n")
	}
}

// readLine is one line without its ending, of any length.
func (m *mboxReader) readLine() ([]byte, error) {
	var line []byte
	for {
		chunk, isPrefix, err := m.r.ReadLine()
		if err != nil {
			if err == io.EOF && len(line) > 0 {
				return line, nil
			}
			return nil, err
		}
		line = append(line, chunk...)
		if !isPrefix {
			return line, nil
		}
	}
}
