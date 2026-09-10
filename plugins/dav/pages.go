package dav

import (
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"github.com/labstack/echo/v4"
)

// CollectionRenderData renders collection.html, the page a collection
// has of its own: its name, its colour, when it was made, and what it
// holds - which is what a delete has to say out loud before it happens.
// Calendars, task lists and address books share it: the fields are what
// any of them has, so one page answers for all three.
type CollectionRenderData struct {
	alborz.BaseRenderData
	Name      string
	Color     string
	Path      string
	Account   string
	Count     int // -1 when the server would not say
	Base      string
	ListHref  string
	BackLabel string
	Rail      Rail
	Error     string
	// Ext is the file type the collection is exported as and imported
	// from, ".ics" or ".vcf"; OffersRange says the export form takes a
	// date range, which only a calendar has.
	Ext         string
	OffersRange bool
	// Address is the feed a subscribed calendar follows; it marks the
	// page as one with nothing on the server to import into, export
	// from, or delete - only a subscription to end.
	Address string
}

// NewCollectionRenderData renders create-collection.html. Only a
// calendar has a component set to choose, so only it offers Holds.
type NewCollectionRenderData struct {
	alborz.BaseRenderData
	Accounts    []alborz.Account
	Name        string
	Account     string
	Color       string
	Title       string
	ListHref    string
	BackLabel   string
	Rail        Rail
	OffersHolds bool
	Holds       string // "events", "tasks" or "both"
	Next        string // the list it was opened from
	Error       string
}

// ParseObjectPath reads the collection or object path a route names.
func ParseObjectPath(s string) (string, error) {
	p, err := url.PathUnescape(s)
	if err != nil {
		err = fmt.Errorf("failed to parse path: %v", err)
		return "", echo.NewHTTPError(http.StatusBadRequest, err)
	}
	return p, nil
}

// Page is what a collection's own page needs from its kind: the
// collection as the account has it, how many objects it holds, the
// list it belongs to and that list's label, and how to forget the
// cached list once the collection changed. Base is the page's own path
// prefix.
type Page struct {
	Base   string
	Color  Prop
	Ext    string
	Lookup func(ctx *alborz.Context, path string) (info Collection, count func() int, list, label string, err error)
	Forget func(username string)
	// Import writes the objects of an uploaded file into the collection
	// and says how many; Export renders the collection, or the range
	// asked for, as one file. Range is nil where the kind has no dates.
	Import func(ctx *alborz.Context, path string, raw []byte) (int, error)
	Export func(ctx *alborz.Context, path string, from, to time.Time) ([]byte, error)
	// Rail is the section's rail for the list the page returns to.
	Rail func(ctx *alborz.Context, list string) (Rail, error)
}

// Handle is the collection's own page. Renaming and recolouring are a
// PROPPATCH; the resource type and the component set are not offered,
// because they are protected on every server in use and changing them
// would mean copying every object into a new collection - a migration,
// not an edit.
func (pg Page) Handle(p *Provider) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		collPath, err := ParseObjectPath(ctx.Param("path"))
		if err != nil {
			return err
		}
		collPath = CanonicalCollectionPath(collPath)
		info, count, list, label, err := pg.Lookup(ctx, collPath)
		if err != nil {
			return err
		}
		rail, err := pg.Rail(ctx, list)
		if err != nil {
			return err
		}
		data := &CollectionRenderData{
			Rail:           rail,
			BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(info.Name),
			Name:           info.Name,
			Color:          info.Color,
			Path:           info.Path,
			Account:        ctx.Session.Username(),
			Count:          count(),
			Base:           pg.Base,
			ListHref:       list,
			BackLabel:      label,
			Ext:            pg.Ext,
			OffersRange:    pg.Ext == ".ics",
			Address:        info.Address,
		}

		if ctx.Request().Method == http.MethodPost {
			name := strings.TrimSpace(ctx.FormValue("name"))
			data.Name, data.Color = name, ctx.FormValue("color")
			if name == "" {
				data.Error = ctx.T("form.nameneeded")
				return ctx.Render(http.StatusUnprocessableEntity, "collection.html", data)
			}
			base, _ := p.URL(ctx.Session)
			target := base.ResolveReference(&url.URL{Path: collPath}).String()
			if err := Proppatch(ctx.Request().Context(), p.HTTPClient(ctx.Session),
				target, name, ctx.FormValue("color"), pg.Color); err != nil {
				data.Error = err.Error()
				return ctx.Render(http.StatusUnprocessableEntity, "collection.html", data)
			}
			pg.Forget(ctx.Session.Username())
			return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.AccountPath(list)))
		}
		return ctx.Render(http.StatusOK, "collection.html", data)
	}
}

// maxImportSize bounds an uploaded calendar or address book: a few
// thousand entries fit in a few megabytes, and a file past this is
// not one a person exported.
const maxImportSize = 16 << 20

// HandleImport writes an uploaded file into the collection and returns
// to its page with what happened.
func (pg Page) HandleImport(p *Provider) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		collPath, err := ParseObjectPath(ctx.Param("path"))
		if err != nil {
			return err
		}
		collPath = CanonicalCollectionPath(collPath)
		if _, _, _, _, err := pg.Lookup(ctx, collPath); err != nil {
			return err
		}
		file, err := ctx.FormFile("file")
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "no file was sent")
		}
		if file.Size > maxImportSize {
			return echo.NewHTTPError(http.StatusRequestEntityTooLarge, "the file is too large to import")
		}
		if err := pg.importFile(ctx, collPath, file); err != nil {
			return err
		}
		return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.AccountPath(pg.Base+url.PathEscape(collPath))))
	}
}

// importFile reads the upload into the collection on ctx's account and
// leaves the notice saying what happened.
func (pg Page) importFile(ctx *alborz.Context, collPath string, file *multipart.FileHeader) error {
	f, err := file.Open()
	if err != nil {
		return err
	}
	defer f.Close()
	raw, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	return pg.importRaw(ctx, collPath, raw)
}

// fetchAddress reads a calendar from an address once and forgets it: a
// subscription that follows the address is a different thing. webcal:
// is https: by another name.
func fetchAddress(address string) ([]byte, error) {
	u, err := url.Parse(address)
	if err != nil {
		return nil, fmt.Errorf("not an address: %w", err)
	}
	if u.Scheme == "webcal" {
		u.Scheme = "https"
	}
	resp, err := alborz.NewRemoteClient(alborz.RoundTripTimeout).Get(u.String())
	if err != nil {
		return nil, fmt.Errorf("failed to fetch the calendar: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch the calendar: %s", resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxImportSize+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxImportSize {
		return nil, echo.NewHTTPError(http.StatusRequestEntityTooLarge, "the calendar is too large to import")
	}
	return raw, nil
}

func (pg Page) importRaw(ctx *alborz.Context, collPath string, raw []byte) error {
	n, err := pg.Import(ctx, collPath, raw)
	if err != nil {
		return err
	}
	if n == 0 {
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeWarning, Text: ctx.T("import.nothing")})
	} else {
		ctx.PutNotice(ctx.Tf("import."+strings.TrimPrefix(pg.Ext, "."), n))
	}
	pg.Forget(ctx.Session.Username())
	return nil
}

// Held is what a rail item says about the collection it stands for and
// the account holding it; the import page groups the rail's items by
// it, so the picker lists what the rail lists.
type Held interface {
	Held() (Collection, string)
}

// ImportData is the section's import page: a collection to choose and
// a file to bring into it.
type ImportData struct {
	alborz.BaseRenderData
	Rail          Rail
	Section       string
	Title         string
	Key           string
	Ext           string
	Hint          string
	Groups        []Group[Collection]
	Account, Path string
	// Address is the second source a calendar has - a webcal: link the
	// browser handed over, or one typed - and URL what it holds.
	Address bool
	URL     string
	Error   string
}

// HandleImportPage is the section's import page, reached from its
// rail: the file lands in the collection chosen on the page, which may
// be any writable one of any account. section and list name the crumb
// and the rail; title, hint and key are the section's words and its
// picker memory.
func (pg Page) HandleImportPage(p *Provider, list, section, title, hint, key string, address bool) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		rail, err := pg.Rail(ctx, list)
		if err != nil {
			return err
		}
		rail.ImportHref = list + "/import"
		var groups []Group[Collection]
		for _, item := range rail.Items {
			held, ok := item.(Held)
			if !ok {
				continue
			}
			coll, account := held.Held()
			if !coll.Writable || coll.Address != "" {
				continue
			}
			if account == "" {
				account = ctx.Session.Username()
			}
			i := len(groups) - 1
			if i < 0 || groups[i].Account != account {
				groups = append(groups, Group[Collection]{Account: account})
				i++
			}
			groups[i].Collections = append(groups[i].Collections, coll)
		}
		data := &ImportData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T(title)),
			Rail:           rail,
			Section:        ctx.T(section),
			Title:          ctx.T(title),
			Key:            key,
			Ext:            pg.Ext,
			Hint:           ctx.T(hint),
			Groups:         groups,
			Account:        ctx.Session.Username(),
			Address:        address,
			URL:            ctx.QueryParam("url"),
		}
		if ctx.URLAccount() != "" {
			data.Account = ctx.URLAccount()
		}
		if ctx.Request().Method != http.MethodPost {
			return ctx.Render(http.StatusOK, "dav-import.html", data)
		}
		acct, collPath, ok := strings.Cut(ctx.FormValue("collection"), "|")
		session := ctx.SessionFor(acct)
		if !ok || session == nil {
			data.Error = ctx.T("form.destinationneeded")
			return ctx.Render(http.StatusUnprocessableEntity, "dav-import.html", data)
		}
		data.Account, data.Path = acct, collPath
		var raw []byte
		if data.URL = strings.TrimSpace(ctx.FormValue("url")); address && data.URL != "" {
			raw, err = fetchAddress(data.URL)
			if err != nil {
				data.Error = err.Error()
				return ctx.Render(http.StatusUnprocessableEntity, "dav-import.html", data)
			}
		} else {
			file, err := ctx.FormFile("file")
			if err != nil {
				data.Error = ctx.T("form.fileneeded")
				return ctx.Render(http.StatusUnprocessableEntity, "dav-import.html", data)
			}
			if file.Size > maxImportSize {
				return echo.NewHTTPError(http.StatusRequestEntityTooLarge, "the file is too large to import")
			}
			f, err := file.Open()
			if err != nil {
				return err
			}
			defer f.Close()
			if raw, err = io.ReadAll(f); err != nil {
				return err
			}
		}
		// The page's own hooks read the account off the context, so the
		// chosen account is the context's for the write.
		ctx.Session = session
		if err := pg.importRaw(ctx, collPath, raw); err != nil {
			// A file that is not what it claims answers on the form,
			// which is where the reader can pick another.
			data.Error = err.Error()
			return ctx.Render(http.StatusUnprocessableEntity, "dav-import.html", data)
		}
		return ctx.Redirect(http.StatusFound, ctx.AccountPath(list))
	}
}

// HandleExport sends the collection as one file to keep. A date range
// narrows a calendar; an address book has no dates to narrow by.
func (pg Page) HandleExport(p *Provider) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		collPath, err := ParseObjectPath(ctx.Param("path"))
		if err != nil {
			return err
		}
		collPath = CanonicalCollectionPath(collPath)
		info, _, _, _, err := pg.Lookup(ctx, collPath)
		if err != nil {
			return err
		}
		var from, to time.Time
		for _, bound := range []struct {
			name string
			into *time.Time
		}{{"from", &from}, {"to", &to}} {
			if v := ctx.QueryParam(bound.name); v != "" {
				t, err := time.Parse("2006-01-02", v)
				if err != nil {
					return echo.NewHTTPError(http.StatusBadRequest, err)
				}
				*bound.into = t
			}
		}
		if !to.IsZero() {
			// The form names the last day; the range ends after it.
			to = to.AddDate(0, 0, 1)
		}
		body, err := pg.Export(ctx, collPath, from, to)
		if err != nil {
			return err
		}
		name := info.Name
		if name == "" {
			name = "export"
		}
		ctx.Response().Header().Set("Content-Disposition",
			mime.FormatMediaType("attachment", map[string]string{"filename": name + pg.Ext}))
		return ctx.Blob(http.StatusOK, exportTypes[pg.Ext], body)
	}
}

// exportTypes are the media types the two file kinds are sent as.
var exportTypes = map[string]string{
	".ics": "text/calendar; charset=utf-8",
	".vcf": "text/vcard; charset=utf-8",
}

// HandleDelete removes the collection and everything in it. The page
// above says how much that is; this only refuses to do it blind.
func (pg Page) HandleDelete(p *Provider) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		collPath, err := ParseObjectPath(ctx.Param("path"))
		if err != nil {
			return err
		}
		collPath = CanonicalCollectionPath(collPath)
		info, _, list, _, err := pg.Lookup(ctx, collPath)
		if err != nil {
			return err
		}
		base, _ := p.URL(ctx.Session)
		target := base.ResolveReference(&url.URL{Path: collPath}).String()
		if err := DeleteCollection(ctx.Request().Context(), p.HTTPClient(ctx.Session), target); err != nil {
			ctx.Notify(alborz.Notice{Kind: alborz.NoticeFailed,
				Text: fmt.Sprintf(ctx.T("notice.collectiondeletefailed"), info.Name)})
			return ctx.Redirect(http.StatusFound, ctx.AccountPath(pg.Base+url.PathEscape(collPath)))
		}
		pg.Forget(ctx.Session.Username())
		// A 2xx is not proof: some servers accept the DELETE and keep the
		// collection. Listing again shows what the reader will see.
		if _, _, _, _, err := pg.Lookup(ctx, collPath); err == nil {
			ctx.Notify(alborz.Notice{Kind: alborz.NoticeFailed,
				Text: fmt.Sprintf(ctx.T("notice.collectionkept"), info.Name)})
			return ctx.Redirect(http.StatusFound, ctx.AccountPath(pg.Base+url.PathEscape(collPath)))
		}
		ctx.PutNotice(fmt.Sprintf(ctx.T("notice.collectiondeleted"), info.Name))
		return ctx.Redirect(http.StatusFound, ctx.AccountPath(list))
	}
}
