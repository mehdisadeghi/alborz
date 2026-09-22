package dav

import (
	"archive/zip"
	"bytes"
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
	// Ext is the file type the collection is exported as and imported
	// from, ".ics" or ".vcf"; OffersRange says the export form takes a
	// date range, which only a calendar has.
	Ext         string
	OffersRange bool
	// Address is the feed a subscribed calendar follows; it marks the
	// page as one with nothing on the server to import into, export
	// from, or delete - only a subscription to end.
	Address string
	// SharedBy is the owner of a collection this account was invited
	// to: not its to rename, and left rather than deleted. Sharing is
	// the owner's view of the same, nil for a collection that is not
	// kept here.
	SharedBy string
	Writable bool
	Sharing  *Sharing
	// Offer is the share this account holds of the collection, as its
	// invitee: the whole page while it is unanswered, and a line of
	// terms once it is accepted.
	Offer *Offer
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
	// Places are where the collection can go, and Place the one chosen:
	// a source of the account's, or Alborz (ADR 28).
	Places []Group
	Place  string
	// Scope is the account the page was in, empty on a merged page.
	Scope string
	Next  string // the list it was opened from
}

// Asks says the reader has to name the place rather than take one:
// several accounts are signed in and the page they came from names
// none, so any preselection would be alborz choosing an account for
// them - which is how a collection landed in the first one.
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

// CreateForm is what a kind says about its form for a new collection:
// the rail it stands beside, its title, the section and list it goes
// back to, and for a calendar the components it may hold.
type CreateForm struct {
	Rail           Rail
	Title, Section string
	List           string
	// Holds is "events", "tasks" or "both" as the form opens, empty for
	// an address book; OffersHolds lets the reader change it.
	Holds       string
	OffersHolds bool
	// Made is the sentence for the collection made and the list it
	// shows in, by what it came to hold.
	Made func(holds string) (sentence, list string)
}

// held are the components a calendar made to hold those takes.
var held = map[string][]string{"events": {"VEVENT"}, "tasks": {"VTODO"}, "both": {"VEVENT", "VTODO"}}

// HandleCreate is the form that adds a collection to the chosen
// account and place. The component set is the one decision a calendar
// cannot change later on most servers, so it is asked here rather than
// assumed.
func (pg Page) HandleCreate(p *Provider, form func(*alborz.Context) (CreateForm, error)) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		f, err := form(ctx)
		if err != nil {
			return err
		}
		data := &NewCollectionRenderData{
			Rail:           f.Rail,
			BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(f.Title),
			Accounts:       ctx.Accounts(),
			Account:        ctx.Session.Username(),
			Scope:          ctx.URLAccount(),
			Color:          DefaultColor,
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
		session := ctx.SessionFor(data.Account)
		if session == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "not signed in to that account")
		}
		path, err := p.Create(ctx.Request().Context(), session, data.Name, data.Color, data.Place, held[data.Holds])
		if err != nil {
			data.Refused(err.Error())
			if errors.Is(err, ErrNameTaken) {
				data.Refused(fmt.Sprintf(ctx.T("form.nametaken"), data.Name))
			} else if errors.Is(err, ErrPlaceDown) {
				data.Refused(ctx.T("form.placedown"))
			}
			return ctx.Render(http.StatusUnprocessableEntity, "create-collection.html", data)
		}
		if err := pg.Show(session.Store(), path); err != nil {
			return fmt.Errorf("failed to save the %s settings: %w", p.kind.Label, err)
		}
		account := ""
		if data.Account != ctx.Session.Username() {
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

// Page is what a collection's own page needs from its kind: the
// collection as the account has it, how many objects it holds, the
// list it belongs to and that list's label, and how to forget the
// cached list once the collection changed. Base is the page's own path
// prefix.
type Page struct {
	Base string
	// List is the section's list, where an answered invitation lands.
	List   string
	Color  Prop
	Ext    string
	Lookup func(ctx *alborz.Context, path string) (info Collection, count func() int, list, label string, err error)
	Forget func(username string)
	// Show ticks a collection the account just accepted among those it
	// has chosen to see.
	Show func(store alborz.Store, path string) error
	// Import writes the objects of an uploaded file into the collection
	// and says how many; Export renders the collection, or the range
	// asked for, as one file. Range is nil where the kind has no dates.
	Import func(ctx *alborz.Context, path string, raw []byte) (int, error)
	// Create makes a collection of the kind for an import that asked for
	// a new one: on the account's server, or here. list says which
	// section's, a calendar or a task list.
	Create func(ctx *alborz.Context, list, name, place string) (string, error)
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
		offer, err := pg.offer(ctx, p, collPath, ctx.Session.Username())
		if err != nil {
			return err
		}
		// An invitation not taken up is not yet a collection the account
		// has: its page is the invitation.
		if offer != nil && offer.State != "accepted" {
			rail, err := pg.Rail(ctx, pg.List)
			if err != nil {
				return err
			}
			return ctx.Render(http.StatusOK, "collection.html", &CollectionRenderData{
				BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(offer.Name),
				Rail:           rail, Name: offer.Name, Base: pg.Base, ListHref: pg.List,
				Account: ctx.Session.Username(), Offer: offer,
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
			base, _ := p.URL(ctx.Session)
			target := base.ResolveReference(&url.URL{Path: collPath}).String()
			if err := Proppatch(ctx.Request().Context(), p.HTTPClient(ctx.Session),
				target, name, ctx.FormValue("color"), pg.Color); err != nil {
				data.Refused(err.Error())
				return ctx.Render(http.StatusUnprocessableEntity, "collection.html", data)
			}
			pg.Forget(ctx.Session.Username())
			return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.AccountPath(list)))
		}
		return ctx.Render(http.StatusOK, "collection.html", data)
	}
}

// data is the collection's page as it stands.
func (pg Page) data(ctx *alborz.Context, p *Provider, collPath string) (*CollectionRenderData, error) {
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
		Account:        ctx.Session.Username(),
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

// readUpload is the uploaded file whole, which its form has bounded.
func readUpload(file *multipart.FileHeader) ([]byte, error) {
	f, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
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

// ErrNothingToExport is a collection with nothing in it, which no
// calendar file can be written for.
var ErrNothingToExport = errors.New("nothing to export")

// newPrefix marks a destination that is a new collection per file, in
// the place that follows it.
const newPrefix = "new:"

// importFile is one calendar or address book to bring in: a file of
// the kind, or one inside a bundle.
type importFile struct {
	name string
	raw  []byte
}

// maxBundleFiles bounds the files a bundle may hold: an account's worth
// of calendars and books is tens, not thousands.
const maxBundleFiles = 200

// maxBundleSize bounds what a bundle may unpack to, all files together:
// an upload within maxImportSize can inflate a thousandfold, and every
// file of it is held in memory until the last is imported. Four whole
// uploads' worth is more than an account's export comes to.
const maxBundleSize = 4 * maxImportSize

// importFiles reads what was uploaded as the files it holds: a zip, as
// a service's export of every calendar or book comes, is each file of
// the kind inside it; anything else is itself.
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
		// An empty file holds nothing to bring in; an export writes one
		// for an empty address book.
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

// collectionName is what a new collection for a file is called: the
// name the file gives itself (X-WR-CALNAME, as every calendar export
// writes it), else the file's own name.
func collectionName(f importFile) string {
	for _, line := range strings.Split(string(f.raw), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), "X-WR-CALNAME:"); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return strings.TrimSuffix(path.Base(f.name), path.Ext(f.name))
}

// importMany brings in several files, or one into a collection of its
// own: each into a new collection in the chosen place, or all into the
// chosen one. One notice says what came of all of them.
func (pg Page) importMany(ctx *alborz.Context, list string, files []importFile, place string, isNew bool, collPath string) error {
	// Every file is looked at before anything is made, so a bundle with
	// one that is not what it claims leaves no empty collection behind.
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
			if target, err = pg.createFree(ctx, list, collectionName(f), place); errors.Is(err, ErrPlaceDown) {
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
	pg.Forget(ctx.Session.Username())
	if !isNew {
		collections = 1
	}
	g := alborz.GlobalRenderData{Lang: ctx.PageLanguage()}
	ctx.PutNotice(fmt.Sprintf(ctx.T("import.bundle"+strings.TrimPrefix(pg.Ext, ".")), g.Num(collections), g.Num(objects)))
	return nil
}

// createFree makes a collection under the name, or under the first of
// "name 2", "name 3" that is free: a service's export may hold two of
// the same name, and a name already here is not a reason to stop.
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
		if !errors.Is(err, ErrNameTaken) || i == maxCreateAttempts {
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
	pg.Forget(ctx.Session.Username())
	return nil
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
	Groups        []Group
	Account, Path string
	// Address is the second source a calendar has - a webcal: link the
	// browser handed over, or one typed - and URL what it holds.
	Address bool
	URL     string
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
		var groups []Group
		for _, coll := range rail.Items {
			account := coll.Account
			if !coll.Writable || coll.Address != "" {
				continue
			}
			if account == "" {
				account = ctx.Session.Username()
			}
			i := len(groups) - 1
			if i < 0 || groups[i].Account != account {
				groups = append(groups, Group{Account: account})
				i++
			}
			groups[i].Collections = append(groups[i].Collections, coll)
		}
		// A new collection per file is a destination too, first in each
		// account's group: in each place it can keep one.
		for _, place := range p.Places(ctx) {
			var news []Collection
			for _, at := range place.Collections {
				news = append(news, Collection{Path: newPrefix + at.Path, Name: fmt.Sprintf(ctx.T("import.newon"), at.Name)})
			}
			i := slices.IndexFunc(groups, func(g Group) bool { return g.Account == place.Account })
			if i < 0 {
				groups = append(groups, Group{Account: place.Account})
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
			data.Refused(ctx.T("form.destinationneeded"))
			return ctx.Render(http.StatusUnprocessableEntity, "dav-import.html", data)
		}
		data.Account, data.Path = acct, collPath
		var raw []byte
		name := ""
		if data.URL = strings.TrimSpace(ctx.FormValue("url")); address && data.URL != "" {
			raw, err = fetchAddress(data.URL)
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
		// The page's own hooks read the account off the context, so the
		// chosen account is the context's for the write.
		ctx.Session = session
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
			// A file that is not what it claims answers on the form,
			// which is where the reader can pick another.
			data.Refused(err.Error())
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

// HandleExportAll sends every collection of the section the account
// can read, each a file in one zip: what a move to another service, or
// a copy kept aside, takes. A feed followed from elsewhere is not the
// account's to export and stays out.
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
			if coll.Address != "" || (account != "" && account != ctx.Session.Username()) {
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
		if info.SharedBy == "" {
			if err := pg.forgetInvitees(p, collPath); err != nil {
				return err
			}
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
		done := "notice.collectiondeleted"
		if info.SharedBy != "" {
			done = "notice.shareleft"
		}
		ctx.PutNotice(fmt.Sprintf(ctx.T(done), info.Name))
		return ctx.Redirect(http.StatusFound, ctx.AccountPath(list))
	}
}
