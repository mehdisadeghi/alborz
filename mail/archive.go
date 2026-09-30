package mail

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strings"

	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-mbox"
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
	return streamMbox(ctx, FolderRefs(ctx.Login.Username(), mboxName, uids), name, strip)
}

func streamMbox(ctx *web.Context, refs []Ref, name string, strip bool) error {
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
		for n < len(refs) && refs[n].Account == refs[0].Account {
			n++
		}
		run := refs[:n]
		refs = refs[n:]
		s := ctx.LoginFor(run[0].Account)
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

const maxDownloadName = 80

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
	if err := p.CreateFolder(login, fullName); err != nil {
		return render(http.StatusUnprocessableEntity, fmt.Sprintf(ctx.T("form.foldertaken"), fullName, err))
	}

	count, held, importErr := p.ImportFile(ctx.Request().Context(), login, fullName, file)
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
	count, held, bodyErr, importErr := p.DropFiles(ctx.Request().Context(), login, mboxName, files)
	if bodyErr != nil {
		return importBodyError(bodyErr)
	}
	if importErr != nil {
		ctx.Logger().Printf("drop into %q stopped after %d: %v", mboxName, count, importErr)
		ctx.Notify(web.Notice{Kind: web.NoticeFailed, Text: ctx.Tf("notice.importstopped", count, mboxName)})
		return ctx.NoContent(http.StatusOK)
	}
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
