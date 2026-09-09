package alborzbase

import (
	"bufio"
	"bytes"
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
		settings, err := LoadSettings(ctx.Session.Store())
		if err != nil {
			return err
		}
		err = ctx.DoIMAPWithin(alborz.ScanTimeout, func(c *imapclient.Client) error {
			if err := ensureMailboxSelected(c, mboxName); err != nil {
				return err
			}
			criteria := &imap.SearchCriteria{}
			if query != "" {
				criteria = PrepareSearch(query, SearchesIndex(c, settings))
			} else if view != "" {
				criteria = ViewCriteria(view)
			}
			data, err := c.UIDSearch(criteria, nil).Wait()
			if err != nil {
				return err
			}
			uids = data.AllUIDs()
			return nil
		})
		if err != nil {
			return err
		}
	} else if uids, err = parseUidList(params["uids"]); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}
	if len(uids) == 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "no messages selected")
	}

	// The header is written only once a message is actually in hand.
	// Committing the response first meant that a fetch which failed had
	// the error page rendered into a body already claiming to be an
	// mbox, and the reader was handed an .mbox file full of HTML.
	res := ctx.Response()
	started := false
	for _, uid := range uids {
		// One round trip per message, each with the session's own
		// budget. The whole export inside one call was cut off by the
		// watchdog after ten seconds, with the file left short.
		var raw []byte
		var env *imap.Envelope
		err := ctx.DoIMAP(func(c *imapclient.Client) error {
			var err error
			raw, env, err = fetchRawMessage(c, mboxName, uid)
			return err
		})
		if err != nil {
			if started {
				// Too late to say so in the response: stop, leave
				// the file short, and put the reason in the log
				// rather than in the download.
				ctx.Logger().Printf("export %q uid %v: %v", mboxName, uid, err)
				return nil
			}
			return fmt.Errorf("export %q uid %v: %w", mboxName, uid, err)
		}
		if strip {
			if stripped, err := withoutAttachments(raw, ctx.T("mailbox.attachmentremoved")); err != nil {
				// A message that cannot be taken apart goes out whole:
				// the archive is then larger than asked, not wrong.
				ctx.Logger().Printf("export %q uid %v: kept attachments: %v", mboxName, uid, err)
			} else {
				raw = stripped
			}
		}
		if !started {
			name := mboxName
			if strip {
				name += " " + ctx.T("mailbox.textonly")
			}
			res.Header().Set("Content-Disposition", downloadName(name, "messages", ".mbox"))
			res.Header().Set("Content-Type", "application/mbox")
			res.WriteHeader(http.StatusOK)
			started = true
		}
		if err := writeMbox(res, raw, env); err != nil {
			ctx.Logger().Printf("export %q uid %v: %v", mboxName, uid, err)
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
	err = ctx.DoIMAPWithin(alborz.ScanTimeout, func(c *imapclient.Client) error {
		// A search or a view is counted the way it is listed; the whole
		// folder is a STATUS away.
		criteria := ViewCriteria(ibase.ListView)
		if data.Query != "" {
			criteria = PrepareSearch(data.Query, SearchesIndex(c, settings))
		}
		if criteria != nil {
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

	// One APPEND per message, each within the session's own bound. An
	// import that stops leaves a named folder holding the first n,
	// which the notice says, rather than a guess.
	count := 0
	messages := newMboxReader(file)
	var importErr error
	for {
		raw, err := messages.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			importErr = err
			break
		}
		err = session.DoIMAP(func(c *imapclient.Client) error {
			options := imap.AppendOptions{Flags: []imap.Flag{imap.FlagSeen}, Time: messageDate(raw)}
			cmd := c.Append(fullName, int64(len(raw)), &options)
			if _, err := cmd.Write(raw); err != nil {
				return err
			}
			if err := cmd.Close(); err != nil {
				return err
			}
			_, err := cmd.Wait()
			return err
		})
		if err != nil {
			importErr = err
			break
		}
		count++
	}
	listings.evictAll(selectedAccount)
	if importErr != nil {
		ctx.Logger().Printf("import into %q stopped after %d: %v", fullName, count, importErr)
		session.PutNotice(ctx.Tf("notice.importstopped", count, fullName))
	} else {
		session.PutNotice(ctx.Tf("notice.imported", count, fullName))
	}
	return ctx.Redirect(http.StatusFound, folderURL(ctx, selectedAccount, fullName))
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
