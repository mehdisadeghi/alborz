package pim

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim/dav"
	"github.com/labstack/echo/v4"
)

// CollectionRenderData renders collection.html, a calendar's, task list's or address book's own page.
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
	// Ext is the export and import file type; OffersRange says the export takes a date range.
	Ext         string
	OffersRange bool
	// Address is a subscribed calendar's feed: nothing on the server to import, export or delete.
	Address string
	// SharedBy owns a collection this account was invited to; Sharing is the owner's view, nil elsewhere.
	SharedBy string
	Writable bool
	Sharing  *Sharing
	// Offer is this account's share as invitee: the whole page while unanswered, a line once accepted.
	Offer *Offer
}

// NewCollectionRenderData renders create-collection.html; only a calendar offers Holds.
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
	Holds       string
	// Places are where the collection can go, and Place the chosen one (ADR 28).
	Places []dav.Group
	Place  string
	// Scope is the account the page was in, empty on a merged page.
	Scope string
	Next  string
}

// Asks says the reader must name the place: with several accounts and none named, a preselection would choose for them.
func (d *NewCollectionRenderData) Asks() bool {
	return d.Chooses() && d.Scope == ""
}

// Chooses says whether there is more than one place to pick from.
func (d *NewCollectionRenderData) Chooses() bool {
	n := 0
	for _, g := range d.Places {
		n += len(g.Collections)
	}
	return n > 1
}

// CreateForm is what a kind says about its form for a new collection.
type CreateForm struct {
	Rail           Rail
	Title, Section string
	List           string
	// Holds is "events", "tasks" or "both" as the form opens, empty for an address book.
	Holds       string
	OffersHolds bool
	// Made is the sentence and list for the collection made, by what it holds.
	Made func(holds string) (sentence, list string)
}

var held = map[string][]string{"events": {"VEVENT"}, "tasks": {"VTODO"}, "both": {"VEVENT", "VTODO"}}

// HandleCreate adds a collection to the chosen account and place, asking for the component set up front.
func (pg Page) HandleCreate(p *dav.Provider, form func(*alborz.Context) (CreateForm, error)) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		f, err := form(ctx)
		if err != nil {
			return err
		}
		data := &NewCollectionRenderData{
			Rail:           f.Rail,
			BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(f.Title),
			Accounts:       ctx.Accounts(),
			Account:        ctx.Login.Username(),
			Scope:          ctx.URLAccount(),
			Color:          dav.DefaultColor,
			Title:          f.Title,
			ListHref:       f.List,
			BackLabel:      f.Section,
			OffersHolds:    f.OffersHolds,
			Holds:          f.Holds,
			Places:         p.Places(ctx),
			Next:           ctx.FormValue("next"),
		}
		data.Account, data.Place = p.ReadPlace(ctx, data.Account)
		if ctx.Request().Method != http.MethodPost {
			return ctx.Render(http.StatusOK, "create-collection.html", data)
		}

		data.Name = strings.TrimSpace(ctx.FormValue("name"))
		data.Color = ctx.FormValue("color")
		if data.Asks() && ctx.FormValue("place") == "" {
			data.Refused(ctx.T("form.destinationneeded"))
			return ctx.Render(http.StatusUnprocessableEntity, "create-collection.html", data)
		}
		// A form that offers no choice cannot be posted one.
		if h := ctx.FormValue("holds"); f.OffersHolds && held[h] != nil {
			data.Holds = h
		}
		if data.Name == "" {
			data.Refused(ctx.T("form.nameneeded"))
			return ctx.Render(http.StatusUnprocessableEntity, "create-collection.html", data)
		}
		login := ctx.LoginFor(data.Account)
		if login == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "not signed in to that account")
		}
		path, err := p.Create(ctx.Request().Context(), login, data.Name, data.Color, data.Place, held[data.Holds])
		if err != nil {
			data.Refused(err.Error())
			if errors.Is(err, dav.ErrNameTaken) {
				data.Refused(fmt.Sprintf(ctx.T("form.nametaken"), data.Name))
			} else if errors.Is(err, dav.ErrPlaceDown) {
				data.Refused(ctx.T("form.placedown"))
			}
			return ctx.Render(http.StatusUnprocessableEntity, "create-collection.html", data)
		}
		if err := pg.Show(ctx.Request().Context(), login.Store(), path); err != nil {
			ctx.Notify(Refused(ctx, err))
			return ctx.Redirect(http.StatusFound, ctx.AccountPath(pg.List))
		}
		account := ""
		if data.Account != ctx.Login.Username() {
			account = data.Account
		}
		sentence, list := f.Made(data.Holds)
		return Made(ctx, sentence, data.Name, pg.Base+url.PathEscape(path), list, account)
	}
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

// Page is what a collection's own page needs from its kind; Base is its path prefix.
type Page struct {
	Base string
	// List is the section's list, where an answered invitation lands.
	List   string
	Color  dav.Prop
	Ext    string
	Lookup func(ctx *alborz.Context, path string) (info dav.Collection, count func() int, list, label string, err error)
	Forget func(username string)
	// Show ticks a just-accepted collection among those the account sees.
	Show func(ctx context.Context, store alborz.Store, path string) error
	// Import writes an uploaded file's objects and counts them; Export renders the collection, or Range, as one file.
	Import func(ctx *alborz.Context, path string, raw []byte) (int, error)
	// Create makes a collection of the kind for an import that asked for a new one.
	Create func(ctx *alborz.Context, list, name, place string) (string, error)
	Export func(ctx *alborz.Context, path string, from, to time.Time) ([]byte, error)
	// Rail is the section's rail for the list the page returns to.
	Rail func(ctx *alborz.Context, list string) (Rail, error)
}

// Handle is the collection's own page; renaming and recolouring are a PROPPATCH.
func (pg Page) Handle(p *dav.Provider) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		collPath, err := ParseObjectPath(ctx.Param("path"))
		if err != nil {
			return err
		}
		collPath = dav.CanonicalCollectionPath(collPath)
		offer, err := pg.offer(ctx, p, collPath, ctx.Login.Username())
		if err != nil {
			return err
		}
		// An unanswered invitation is not yet a collection: its page is the invitation.
		if offer != nil && offer.State != "accepted" {
			rail, err := pg.Rail(ctx, pg.List)
			if err != nil {
				return err
			}
			return ctx.Render(http.StatusOK, "collection.html", &CollectionRenderData{
				BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(offer.Name),
				Rail:           rail, Name: offer.Name, Base: pg.Base, ListHref: pg.List,
				Account: ctx.Login.Username(), Offer: offer,
			})
		}
		data, err := pg.data(ctx, p, collPath)
		if err != nil {
			return err
		}
		data.Offer = offer
		list := data.ListHref

		if ctx.Request().Method == http.MethodPost {
			if data.SharedBy != "" {
				return echo.NewHTTPError(http.StatusForbidden, "the collection is its owner's to change")
			}
			name := strings.TrimSpace(ctx.FormValue("name"))
			data.Name, data.Color = name, ctx.FormValue("color")
			if name == "" {
				data.Refused(ctx.T("form.nameneeded"))
				return ctx.Render(http.StatusUnprocessableEntity, "collection.html", data)
			}
			base := p.URL(ctx.Request().Context(), ctx.Login)
			target := base.ResolveReference(&url.URL{Path: collPath}).String()
			if err := dav.Proppatch(ctx.Request().Context(), p.HTTPClient(ctx.Request().Context(), ctx.Login),
				target, name, ctx.FormValue("color"), pg.Color); err != nil {
				data.Refused(err.Error())
				return ctx.Render(http.StatusUnprocessableEntity, "collection.html", data)
			}
			pg.Forget(ctx.Login.Username())
			return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.AccountPath(list)))
		}
		return ctx.Render(http.StatusOK, "collection.html", data)
	}
}

func (pg Page) data(ctx *alborz.Context, p *dav.Provider, collPath string) (*CollectionRenderData, error) {
	info, count, list, label, err := pg.Lookup(ctx, collPath)
	if err != nil {
		return nil, err
	}
	rail, err := pg.Rail(ctx, list)
	if err != nil {
		return nil, err
	}
	sharing, err := pg.sharing(ctx, p, info)
	if err != nil {
		return nil, err
	}
	return &CollectionRenderData{
		Rail:           rail,
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(info.Name),
		Name:           info.Name,
		Color:          info.Color,
		Path:           info.Path,
		Account:        ctx.Login.Username(),
		Count:          count(),
		Base:           pg.Base,
		ListHref:       list,
		BackLabel:      label,
		Ext:            pg.Ext,
		OffersRange:    pg.Ext == ".ics",
		Address:        info.Address,
		SharedBy:       info.SharedBy,
		Writable:       info.Writable,
		Sharing:        sharing,
	}, nil
}

const maxImportSize = 16 << 20

// HandleImport writes an uploaded file into the collection.
func (pg Page) HandleImport(p *dav.Provider) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		collPath, err := ParseObjectPath(ctx.Param("path"))
		if err != nil {
			return err
		}
		collPath = dav.CanonicalCollectionPath(collPath)
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
		raw, err := readUpload(file)
		if err != nil {
			return err
		}
		if err := pg.importRaw(ctx, collPath, raw); err != nil {
			return err
		}
		return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.AccountPath(pg.Base+url.PathEscape(collPath))))
	}
}

func readUpload(file *multipart.FileHeader) ([]byte, error) {
	f, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// fetchAddress reads a calendar from an address once, unlike a subscription; webcal: is https:.
func fetchAddress(client *http.Client, address string) ([]byte, error) {
	u, err := url.Parse(address)
	if err != nil {
		return nil, fmt.Errorf("not an address: %w", err)
	}
	if u.Scheme == "webcal" {
		u.Scheme = "https"
	}
	resp, err := client.Get(u.String())
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

// ErrNothingToExport is an empty collection, for which no file can be written.
var ErrNothingToExport = errors.New("nothing to export")

// newPrefix marks a new collection per file as destination, in the place that follows.
const newPrefix = "new:"

type importFile struct {
	name string
	raw  []byte
}

const maxBundleFiles = 200

// An upload can inflate a thousandfold, and every file is held in memory until the last is imported.
const maxBundleSize = 4 * maxImportSize

// importFiles reads a zip as each file of the kind inside it; anything else is itself.
func (pg Page) importFiles(name string, raw []byte) ([]importFile, error) {
	if !bytes.HasPrefix(raw, []byte("PK\x03\x04")) {
		return []importFile{{name, raw}}, nil
	}
	z, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, fmt.Errorf("not a zip file: %v", err)
	}
	var out []importFile
	size := 0
	for _, f := range z.File {
		if f.FileInfo().IsDir() || f.UncompressedSize64 == 0 || !strings.EqualFold(path.Ext(f.Name), pg.Ext) {
			continue
		}
		if len(out) == maxBundleFiles {
			return nil, fmt.Errorf("more than %d files in the bundle", maxBundleFiles)
		}
		r, err := f.Open()
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(io.LimitReader(r, maxImportSize+1))
		r.Close()
		if err != nil {
			return nil, err
		}
		if len(body) > maxImportSize {
			return nil, fmt.Errorf("%s is too large to import", f.Name)
		}
		if size += len(body); size > maxBundleSize {
			return nil, fmt.Errorf("the bundle unpacks to more than %d MB", maxBundleSize>>20)
		}
		out = append(out, importFile{f.Name, body})
	}
	return out, nil
}

// collectionName is the file's X-WR-CALNAME, else its file name.
func collectionName(f importFile) string {
	for _, line := range strings.Split(string(f.raw), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), "X-WR-CALNAME:"); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return strings.TrimSuffix(path.Base(f.name), path.Ext(f.name))
}

// importMany brings each file into a new collection in place, or all into the chosen one, with one notice.
func (pg Page) importMany(ctx *alborz.Context, list string, files []importFile, place string, isNew bool, collPath string) error {
	// Every file is checked before anything is made, so a bad one leaves no empty collection behind.
	begins := map[string]string{".ics": "BEGIN:VCALENDAR", ".vcf": "BEGIN:VCARD"}[pg.Ext]
	for _, f := range files {
		if !strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(string(f.raw), "\ufeff")), begins) {
			return fmt.Errorf(ctx.T("import.notkind"), f.name, pg.Ext)
		}
	}
	objects, collections := 0, 0
	for _, f := range files {
		target := collPath
		if isNew {
			var err error
			if target, err = pg.createFree(ctx, list, collectionName(f), place); errors.Is(err, dav.ErrPlaceDown) {
				return errors.New(ctx.T("form.placedown"))
			} else if err != nil {
				return err
			}
			collections++
		}
		n, err := pg.Import(ctx, target, f.raw)
		if err != nil {
			return fmt.Errorf("%s: %v", f.name, err)
		}
		objects += n
	}
	pg.Forget(ctx.Login.Username())
	if !isNew {
		collections = 1
	}
	g := alborz.GlobalRenderData{Lang: ctx.PageLanguage()}
	ctx.PutNotice(fmt.Sprintf(ctx.T("import.bundle"+strings.TrimPrefix(pg.Ext, ".")), g.Num(collections), g.Num(objects)))
	return nil
}

// createFree makes the collection under name, or the first free "name 2", "name 3".
func (pg Page) createFree(ctx *alborz.Context, list, name, place string) (string, error) {
	if name == "" {
		name = ctx.T("import.untitled")
	}
	for i := 1; ; i++ {
		try := name
		if i > 1 {
			try = fmt.Sprintf("%s %d", name, i)
		}
		p, err := pg.Create(ctx, list, try, place)
		if !errors.Is(err, dav.ErrNameTaken) || i == dav.MaxCreateAttempts {
			return p, err
		}
	}
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
	pg.Forget(ctx.Login.Username())
	return nil
}

// ImportData is the section's import page.
type ImportData struct {
	alborz.BaseRenderData
	Rail          Rail
	Section       string
	Title         string
	Key           string
	Ext           string
	Hint          string
	Groups        []dav.Group
	Account, Path string
	// Address says a calendar can also import from a webcal: link; URL is what it holds.
	Address bool
	URL     string
}

// HandleImportPage is the section's import page, reached from its rail; any writable collection may receive.
func (pg Page) HandleImportPage(p *dav.Provider, list, section, title, hint, key string, address bool) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		rail, err := pg.Rail(ctx, list)
		if err != nil {
			return err
		}
		rail.ImportHref = list + "/import"
		var groups []dav.Group
		for _, coll := range rail.Items {
			account := coll.Account
			if !coll.Writable || coll.Address != "" {
				continue
			}
			if account == "" {
				account = ctx.Login.Username()
			}
			i := len(groups) - 1
			if i < 0 || groups[i].Account != account {
				groups = append(groups, dav.Group{Account: account})
				i++
			}
			groups[i].Collections = append(groups[i].Collections, coll)
		}
		// A new collection per file is a destination too, first in each account's group.
		for _, place := range p.Places(ctx) {
			var news []dav.Collection
			for _, at := range place.Collections {
				news = append(news, dav.Collection{Path: newPrefix + at.Path, Name: fmt.Sprintf(ctx.T("import.newon"), at.Name)})
			}
			i := slices.IndexFunc(groups, func(g dav.Group) bool { return g.Account == place.Account })
			if i < 0 {
				groups = append(groups, dav.Group{Account: place.Account})
				i = len(groups) - 1
			}
			groups[i].Collections = append(news, groups[i].Collections...)
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
			Account:        ctx.Login.Username(),
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
		login := ctx.LoginFor(acct)
		if !ok || login == nil {
			data.Refused(ctx.T("form.destinationneeded"))
			return ctx.Render(http.StatusUnprocessableEntity, "dav-import.html", data)
		}
		data.Account, data.Path = acct, collPath
		var raw []byte
		name := ""
		if data.URL = strings.TrimSpace(ctx.FormValue("url")); address && data.URL != "" {
			raw, err = fetchAddress(ctx.Server.Outgoing.Remote(alborz.RoundTripTimeout), data.URL)
			if err != nil {
				data.Refused(err.Error())
				return ctx.Render(http.StatusUnprocessableEntity, "dav-import.html", data)
			}
		} else {
			file, err := ctx.FormFile("file")
			if err != nil {
				data.Refused(ctx.T("form.fileneeded"))
				return ctx.Render(http.StatusUnprocessableEntity, "dav-import.html", data)
			}
			if file.Size > maxImportSize {
				return echo.NewHTTPError(http.StatusRequestEntityTooLarge, "the file is too large to import")
			}
			if raw, err = readUpload(file); err != nil {
				return err
			}
			name = file.Filename
		}
		// The page's hooks read the account off the context, so the chosen account becomes the context's.
		ctx.Login = login
		files, err := pg.importFiles(name, raw)
		if err != nil {
			data.Refused(err.Error())
			return ctx.Render(http.StatusUnprocessableEntity, "dav-import.html", data)
		}
		if place, isNew := strings.CutPrefix(collPath, newPrefix); isNew || len(files) > 1 {
			if err := pg.importMany(ctx, list, files, place, isNew, collPath); err != nil {
				data.Refused(err.Error())
				return ctx.Render(http.StatusUnprocessableEntity, "dav-import.html", data)
			}
			return ctx.Redirect(http.StatusFound, ctx.AccountPath(list))
		}
		if err := pg.importRaw(ctx, collPath, raw); err != nil {
			// A file that is not what it claims answers on the form, where another can be picked.
			data.Refused(err.Error())
			return ctx.Render(http.StatusUnprocessableEntity, "dav-import.html", data)
		}
		return ctx.Redirect(http.StatusFound, ctx.AccountPath(list))
	}
}

// HandleExport sends the collection as one file; a date range narrows a calendar.
func (pg Page) HandleExport(p *dav.Provider) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		collPath, err := ParseObjectPath(ctx.Param("path"))
		if err != nil {
			return err
		}
		collPath = dav.CanonicalCollectionPath(collPath)
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
		if errors.Is(err, ErrNothingToExport) {
			ctx.Notify(alborz.Notice{Kind: alborz.NoticeWarning, Text: ctx.T("export.empty")})
			return ctx.Redirect(http.StatusFound, pg.pageOf(ctx, collPath))
		}
		if err != nil {
			return err
		}
		name := info.Name
		if name == "" {
			name = "export"
		}
		return Download(ctx, name+pg.Ext, body)
	}
}

// HandleExportAll zips every readable collection of the section; followed feeds stay out.
func (pg Page) HandleExportAll(list, filename string) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		rail, err := pg.Rail(ctx, list)
		if err != nil {
			return err
		}
		var buf bytes.Buffer
		z := zip.NewWriter(&buf)
		taken := map[string]int{}
		for _, coll := range rail.Items {
			account := coll.Account
			if coll.Address != "" || (account != "" && account != ctx.Login.Username()) {
				continue
			}
			body, err := pg.Export(ctx, coll.Path, time.Time{}, time.Time{})
			if errors.Is(err, ErrNothingToExport) || (err == nil && len(body) == 0) {
				continue
			}
			if err != nil {
				return err
			}
			// Two collections of one name are two files.
			name := coll.Name
			if taken[name]++; taken[name] > 1 {
				name = fmt.Sprintf("%s %d", name, taken[name])
			}
			w, err := z.CreateHeader(&zip.FileHeader{Name: name + pg.Ext, Method: zip.Deflate, Modified: time.Now()})
			if err != nil {
				return err
			}
			if _, err := w.Write(body); err != nil {
				return err
			}
		}
		if err := z.Close(); err != nil {
			return err
		}
		return Download(ctx, filename, buf.Bytes())
	}
}

// HandleDelete removes the collection and everything in it; the page above states how much.
func (pg Page) HandleDelete(p *dav.Provider) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		collPath, err := ParseObjectPath(ctx.Param("path"))
		if err != nil {
			return err
		}
		collPath = dav.CanonicalCollectionPath(collPath)
		info, _, list, _, err := pg.Lookup(ctx, collPath)
		if err != nil {
			return err
		}
		if info.SharedBy == "" {
			if err := pg.forgetInvitees(p, collPath); err != nil {
				return err
			}
		}
		base := p.URL(ctx.Request().Context(), ctx.Login)
		target := base.ResolveReference(&url.URL{Path: collPath}).String()
		if err := dav.DeleteCollection(ctx.Request().Context(), p.HTTPClient(ctx.Request().Context(), ctx.Login), target); err != nil {
			ctx.Notify(alborz.Notice{Kind: alborz.NoticeFailed,
				Text: fmt.Sprintf(ctx.T("notice.collectiondeletefailed"), info.Name)})
			return ctx.Redirect(http.StatusFound, ctx.AccountPath(pg.Base+url.PathEscape(collPath)))
		}
		pg.Forget(ctx.Login.Username())
		// A 2xx is not taken as proof; listing again shows what the reader will see.
		if _, _, _, _, err := pg.Lookup(ctx, collPath); err == nil {
			ctx.Notify(alborz.Notice{Kind: alborz.NoticeFailed,
				Text: fmt.Sprintf(ctx.T("notice.collectionkept"), info.Name)})
			return ctx.Redirect(http.StatusFound, ctx.AccountPath(pg.Base+url.PathEscape(collPath)))
		}
		done := "notice.collectiondeleted"
		if info.SharedBy != "" {
			done = "notice.shareleft"
		}
		ctx.PutNotice(fmt.Sprintf(ctx.T(done), info.Name))
		return ctx.Redirect(http.StatusFound, ctx.AccountPath(list))
	}
}
