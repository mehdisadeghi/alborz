package web

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/dav"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-webdav"
)

// CollectionRenderData renders collection.html, a calendar's, task list's or address book's own page.
type CollectionRenderData struct {
	web.PageData[Rail]
	Name  string
	Color string
	// Shown is the name and colour the form was drawn from, which a save compares the form against.
	Shown   string
	Path    string
	Account string
	Count   int // -1 when the server would not say
	// Collection is the route of the page, whose actions are its routes beneath.
	Collection string
	ListHref   string
	BackLabel  string
	// Ext is the export and import file type; OffersRange says the export takes a date range.
	Ext         string
	OffersRange bool
	// Feed is the address a subscribed calendar is filled from.
	Feed string
	// SharedBy owns a collection this account was invited to; Sharing is the owner's view, nil elsewhere.
	SharedBy string
	Writable bool
	Sharing  *Sharing
	// Offer is this account's share as invitee: the whole page while unanswered, a line once accepted.
	Offer *Offer
}

// NewCollectionRenderData renders create-collection.html; only a calendar offers Holds.
type NewCollectionRenderData struct {
	web.PageData[Rail]
	Accounts    []web.Account
	Name        string
	Account     string
	Color       string
	Title       string
	ListHref    string
	BackLabel   string
	OffersHolds bool
	Holds       string
	// Places are where the collection can go, and Place the chosen one (ADR 28).
	Places []dav.Group
	Place  string
	// Scope is the account the page was in, empty on a merged page.
	Scope string
	Next  string
}

// DestinationRefusal maps the shared missing-DAV-destination condition to the
// common form refusal while leaving all other errors unchanged.
func DestinationRefusal(ctx *web.Context, err error, refuse func(string) error) (bool, error) {
	if errors.Is(err, pim.ErrNoDestination) {
		return true, refuse(ctx.T("form.destinationneeded"))
	}
	return false, err
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
	// Holds is "events", "tasks", "journal" or "both" as the form opens, empty for an address book.
	Holds       string
	OffersHolds bool
	// Made is the sentence and list for the collection made, by what it holds.
	Made func(holds string) (sentence, list string)
}

var held = map[string][]string{"events": {"VEVENT"}, "tasks": {"VTODO"}, "journal": {"VJOURNAL"}, "both": {"VEVENT", "VTODO"}}

// HandleCreate adds a collection to the chosen account and place, asking for the component set up front.
func (pg Page) HandleCreate(p *dav.Provider, form func(*web.Context) (CreateForm, error)) func(*web.Context) error {
	return func(ctx *web.Context) error {
		f, err := form(ctx)
		if err != nil {
			return err
		}
		data := &NewCollectionRenderData{
			PageData:    *web.NewPageData(ctx, f.Title, f.Rail),
			Accounts:    ctx.Accounts(),
			Account:     ctx.Login.Username(),
			Scope:       ctx.URLAccount(),
			Color:       dav.DefaultColor,
			Title:       f.Title,
			ListHref:    f.List,
			BackLabel:   f.Section,
			OffersHolds: f.OffersHolds,
			Holds:       f.Holds,
			Places:      Places(ctx, p),
			Next:        ctx.FormValue("next"),
		}
		data.Account, data.Place = ReadPlace(ctx, p, data.Account)
		if ctx.Request().Method != http.MethodPost {
			return ctx.Render(http.StatusOK, "pim/collection-create", data)
		}

		data.Name = strings.TrimSpace(ctx.FormValue("name"))
		data.Color = ctx.FormValue("color")
		if data.Asks() && ctx.FormValue("place") == "" {
			return web.RenderRefused(ctx, "pim/collection-create", data, ctx.T("form.destinationneeded"))
		}
		// A form that offers no choice cannot be posted one.
		if h := ctx.FormValue("holds"); f.OffersHolds && held[h] != nil {
			data.Holds = h
		}
		if data.Name == "" {
			return web.RenderRefused(ctx, "pim/collection-create", data, ctx.T("form.nameneeded"))
		}
		if data.Place == dav.SourceHere && !dav.ValidLocalCollectionName(data.Name) {
			return web.RenderRefused(ctx, "pim/collection-create", data, ctx.T("form.localcollectionname"))
		}
		login := ctx.LoginFor(data.Account)
		if login == nil {
			return web.NewError(http.StatusBadRequest, "not signed in to that account")
		}
		path, err := p.Create(ctx.Request().Context(), login, data.Name, data.Color, data.Place, held[data.Holds])
		if err != nil {
			message := err.Error()
			if errors.Is(err, dav.ErrNameTaken) {
				message = fmt.Sprintf(ctx.T("form.nametaken"), data.Name)
			} else if errors.Is(err, dav.ErrPlaceDown) {
				message = ctx.T("form.placedown")
			}
			return web.RenderRefused(ctx, "pim/collection-create", data, message)
		}
		if err := pg.Show(ctx.Request().Context(), login, path); err != nil {
			return ctx.NotifyRedirect(Refused(ctx, err), ctx.URL(pg.List))
		}
		account := ""
		if data.Account != ctx.Login.Username() {
			account = data.Account
		}
		sentence, list := f.Made(data.Holds)
		return Made(ctx, sentence, data.Name, ctx.Path(pg.Collection, path), list, account)
	}
}

// Page is what a collection's own page needs from its kind; Collection is its route.
type Page struct {
	Collection string
	// List is the route of the section's list, where an answered invitation lands.
	List   string
	Ext    string
	Lookup func(ctx *web.Context, path string) (info dav.Collection, count func() int, list, label string, err error)
	Forget func(username string)
	// Show ticks a just-accepted collection among those the account sees.
	Show func(ctx context.Context, l *alborz.Login, path string) error
	// Import writes an uploaded file's objects and counts them; Export renders the collection, or Range, as one file.
	Import func(ctx *web.Context, path string, raw []byte) (int, error)
	// Create makes a collection of the kind for an import that asked for a new one.
	Create func(ctx *web.Context, list, name, place string) (string, error)
	Export func(ctx *web.Context, path string, from, to time.Time) ([]byte, error)
	// Rail is the section's rail for the list the page returns to.
	Rail func(ctx *web.Context, list string) (Rail, error)
}

// LookupCollection opens a kind's collections and returns the named collection with its page label.
// The open and count functions retain protocol-specific clients while this common boundary owns lookup errors.
func LookupCollection[C any](ctx *web.Context, path string, open func(context.Context, *alborz.Login) (C, []dav.Collection, error), count func(context.Context, *alborz.Login, string) func() int, describe func(dav.Collection) (list, label string)) (dav.Collection, func() int, string, string, error) {
	_, collections, err := open(ctx.Request().Context(), ctx.Login)
	if err != nil {
		return dav.Collection{}, nil, "", "", err
	}
	info := dav.At(collections, path)
	if info == nil {
		return dav.Collection{}, nil, "", "", alborz.NotFound("notfound.collection")
	}
	list, label := describe(*info)
	return *info, count(ctx.Request().Context(), ctx.Login, info.Path), list, label, nil
}

// Handle is the collection's own page; renaming and recolouring are a PROPPATCH.
func (pg Page) Handle(p *dav.Provider) func(*web.Context) error {
	return func(ctx *web.Context) error {
		collPath := pg.collectionPath(ctx)
		offer, err := pg.offer(ctx, p, collPath, ctx.Login.Username())
		if err != nil {
			return err
		}
		// An unanswered invitation is not yet a collection: its page is the invitation.
		if offer != nil && offer.State != "accepted" {
			rail, err := pg.Rail(ctx, ctx.Path(pg.List))
			if err != nil {
				return err
			}
			return ctx.Render(http.StatusOK, "pim/collection", &CollectionRenderData{
				PageData: *web.NewPageData(ctx, offer.Name, rail),
				Name:     offer.Name, Collection: pg.Collection, ListHref: ctx.Path(pg.List),
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
				return web.NewError(http.StatusForbidden, "the collection is its owner's to change")
			}
			// What the form changed is what it sends that differs from what it showed; the rest stays as stored.
			drawn := shownCollection{Name: data.Name, Color: data.Color}
			if shown, err := web.DecodeForm(ctx, "shown", &drawn, "the form's shown collection does not decode"); err != nil {
				return err
			} else if shown {
				data.Shown = ctx.FormValue("shown")
			}
			name := strings.TrimSpace(ctx.FormValue("name"))
			data.Name, data.Color = name, ctx.FormValue("color")
			if name == "" {
				return web.RenderRefused(ctx, "pim/collection", data, ctx.T("form.nameneeded"))
			}
			var renamed, recoloured *string
			if name != drawn.Name {
				renamed = &name
			}
			if data.Color != drawn.Color {
				recoloured = &data.Color
			}
			rc := ctx.Request().Context()
			if renamed != nil || recoloured != nil {
				if err := p.Kind.Change(rc, p.HTTPClient(rc, ctx.Login), dav.Base(), collPath, renamed, recoloured); err != nil {
					return web.RenderRefused(ctx, "pim/collection", data, err.Error())
				}
			}
			pg.Forget(ctx.Login.Username())
			return ctx.FoundRedirect(ctx.NextOr(ctx.AccountPath(list)))
		}
		return ctx.Render(http.StatusOK, "pim/collection", data)
	}
}

// shownCollection is what the collection form shows.
type shownCollection struct{ Name, Color string }

func (pg Page) data(ctx *web.Context, p *dav.Provider, collPath string) (*CollectionRenderData, error) {
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
	shown, err := json.Marshal(shownCollection{Name: info.Name, Color: info.Color})
	if err != nil {
		return nil, err
	}
	return &CollectionRenderData{
		PageData:    *web.NewPageData(ctx, info.Name, rail),
		Name:        info.Name,
		Color:       info.Color,
		Shown:       string(shown),
		Path:        info.Path,
		Account:     ctx.Login.Username(),
		Count:       count(),
		Collection:  pg.Collection,
		ListHref:    list,
		BackLabel:   label,
		Ext:         pg.Ext,
		OffersRange: pg.Ext == ".ics",
		Feed:        info.Feed,
		SharedBy:    info.SharedBy,
		Writable:    info.Writable,
		Sharing:     sharing,
	}, nil
}

// ImportBodySize bounds an import's upload: the file and the form around it.
const ImportBodySize = pim.MaxImportSize + web.FormFieldsSize

func importTooLarge(ctx *web.Context) string { return ctx.Tf("import.toolarge", pim.MaxImportSize>>20) }

func (pg Page) collectionPath(ctx *web.Context) string {
	return dav.CanonicalCollectionPath(ctx.Param("path"))
}

// HandleImportTooLarge answers an upload past the limit on the collection it was sent to.
func (pg Page) HandleImportTooLarge(ctx *web.Context) error {
	return ctx.NotifyRedirect(web.Notice{Kind: web.NoticeFailed, Text: importTooLarge(ctx)}, pg.pageOf(ctx, pg.collectionPath(ctx)))
}

// HandleImport writes an uploaded file into the collection.
func (pg Page) HandleImport(ctx *web.Context) error {
	collPath := pg.collectionPath(ctx)
	if _, _, _, _, err := pg.Lookup(ctx, collPath); err != nil {
		return err
	}
	back := ctx.NextOr(pg.pageOf(ctx, collPath))
	file, err := ctx.FormFile("file")
	if err == nil && file.Size() > pim.MaxImportSize {
		return ctx.NotifyRedirect(web.Notice{Kind: web.NoticeFailed, Text: importTooLarge(ctx)}, back)
	}
	if err != nil {
		return web.NewError(http.StatusBadRequest, "no file was sent")
	}
	raw, err := io.ReadAll(file.Open())
	if err != nil {
		return err
	}
	if err := pg.importRaw(ctx, collPath, raw); pim.ImportAnswered(err) {
		return ctx.NotifyRedirect(ImportRefused(ctx, err), back)
	} else if err != nil {
		return err
	}
	return ctx.FoundRedirect(back)
}

// fetchAddress reads a calendar from an address once, unlike a subscription; webcal: is https:.
func fetchAddress(ctx *web.Context, client *http.Client, address string) ([]byte, error) {
	u, err := url.Parse(address)
	if err != nil {
		return nil, errors.New(ctx.Tf("import.notaddress", address))
	}
	if u.Scheme == "webcal" {
		u.Scheme = "https"
	}
	resp, err := client.Get(u.String())
	if err != nil {
		return nil, errors.New(ctx.Tf("import.fetchfailed", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(ctx.Tf("import.fetchfailed", resp.Status))
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, pim.MaxImportSize+1))
	if err != nil {
		return nil, errors.New(ctx.Tf("import.fetchfailed", err))
	}
	if len(raw) > pim.MaxImportSize {
		return nil, errors.New(importTooLarge(ctx))
	}
	return raw, nil
}

// newPrefix marks a new collection per file as destination, in the place that follows.
const newPrefix = "new:"

type importFile struct {
	name string
	raw  []byte
}

const maxBundleFiles = 200

// An upload can inflate a thousandfold, and every file is held in memory until the last is imported.
const maxBundleSize = 4 * pim.MaxImportSize

// importFiles reads a zip as each file of the kind inside it; anything else is itself.
func (pg Page) importFiles(ctx *web.Context, name string, raw []byte) ([]importFile, error) {
	if !bytes.HasPrefix(raw, []byte("PK\x03\x04")) {
		return []importFile{{name, raw}}, nil
	}
	z, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, errors.New(ctx.Tf("import.notzip", err))
	}
	var out []importFile
	size := 0
	for _, f := range z.File {
		if f.FileInfo().IsDir() || f.UncompressedSize64 == 0 || !strings.EqualFold(path.Ext(f.Name), pg.Ext) {
			continue
		}
		if len(out) == maxBundleFiles {
			return nil, errors.New(ctx.Tf("import.zipfiles", maxBundleFiles))
		}
		r, err := f.Open()
		if err != nil {
			return nil, errors.New(ctx.Tf("import.zipentry", f.Name, err))
		}
		body, err := io.ReadAll(io.LimitReader(r, pim.MaxImportSize+1))
		r.Close()
		if err != nil {
			return nil, errors.New(ctx.Tf("import.zipentry", f.Name, err))
		}
		if len(body) > pim.MaxImportSize {
			return nil, errors.New(ctx.Tf("import.ziplarge", f.Name, pim.MaxImportSize>>20))
		}
		if size += len(body); size > maxBundleSize {
			return nil, errors.New(ctx.Tf("import.zipsize", maxBundleSize>>20))
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
func (pg Page) importMany(ctx *web.Context, list string, files []importFile, place string, isNew bool, collPath string) error {
	// Every file is checked before anything is made, so a bad one leaves no empty collection behind.
	begins := map[string]string{".ics": "BEGIN:VCALENDAR", ".vcf": "BEGIN:VCARD"}[pg.Ext]
	for _, f := range files {
		if !strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(string(f.raw), "\ufeff")), begins) {
			return &pim.UnreadableError{Err: fmt.Errorf(ctx.T("import.notkind"), f.name, pg.Ext)}
		}
	}
	objects, collections := 0, 0
	for _, f := range files {
		target := collPath
		if isNew {
			var err error
			if target, err = pg.createFree(ctx, list, collectionName(f), place); err != nil {
				return err
			}
			collections++
		}
		n, err := pg.Import(ctx, target, f.raw)
		if err != nil {
			return &fileError{f.name, err}
		}
		objects += n
	}
	pg.Forget(ctx.Login.Username())
	if !isNew {
		collections = 1
	}
	g := web.GlobalRenderData{Lang: ctx.PageLanguage()}
	ctx.PutNotice(fmt.Sprintf(ctx.T("import.bundle"+strings.TrimPrefix(pg.Ext, ".")), g.Num(collections), g.Num(objects)))
	return nil
}

// createFree makes the collection under name, or the first free "name 2", "name 3".
func (pg Page) createFree(ctx *web.Context, list, name, place string) (string, error) {
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

func (pg Page) importRaw(ctx *web.Context, collPath string, raw []byte) error {
	n, err := pg.Import(ctx, collPath, raw)
	// What was written before a refusal stays written.
	pg.Forget(ctx.Login.Username())
	if err != nil {
		return err
	}
	if n == 0 {
		ctx.Notify(web.Notice{Kind: web.NoticeWarning, Text: ctx.T("import.nothing")})
	} else {
		ctx.PutNotice(ctx.Tf("import."+strings.TrimPrefix(pg.Ext, "."), n))
	}
	return nil
}

// fileError names the file of a bundle an import stopped at.
type fileError struct {
	name string
	err  error
}

func (e *fileError) Error() string { return e.name + ": " + e.err.Error() }
func (e *fileError) Unwrap() error { return e.err }

// LeftOut tells the reader how many objects an export left out, and why; any other error is returned.
func LeftOut(ctx *web.Context, err error) error {
	var left *pim.LeftOutError
	if !errors.As(err, &left) {
		return err
	}
	ctx.Logger().Printf("export: %v", left)
	ctx.Notify(web.Notice{Kind: web.NoticeWarning, Text: ctx.Tf("export.leftout", left.Left, left.Err)})
	return nil
}

// ImportRefused is a failed notice for an import the file or the server stopped.
func ImportRefused(ctx *web.Context, err error) web.Notice {
	return web.Notice{Kind: web.NoticeFailed, Text: ImportRefusal(ctx, err)}
}

// ImportRedirect reports a completed object import and returns to its list.
// It is shared by mail attachment filing so every importer treats parser and
// server refusals, empty imports, and successful counts alike.
func ImportRedirect(ctx *web.Context, err error, count int, key, to string) error {
	if pim.ImportAnswered(err) {
		return ctx.NotifyRedirect(ImportRefused(ctx, err), to)
	}
	if err != nil {
		return err
	}
	if count == 0 {
		ctx.Notify(web.Notice{Kind: web.NoticeWarning, Text: ctx.T("import.nothing")})
	} else {
		ctx.PutNotice(ctx.Tf(key, count))
	}
	return ctx.FoundRedirect(to)
}

// ImportRefusal says why an import ImportAnswered stopped: an unreadable file with the parser's reason,
// a server's refusal as any write's with how much was saved before it; a bundle names the file.
func ImportRefusal(ctx *web.Context, err error) string {
	var file *fileError
	var unreadable *pim.UnreadableError
	var partly *pim.PartlySavedError
	switch {
	case errors.As(err, &file):
		return file.name + ": " + ImportRefusal(ctx, file.err)
	case errors.As(err, &partly) && partly.Saved == 0 && !errors.As(partly.Err, &unreadable):
		return Refusal(ctx, partly.Err)
	case errors.As(err, &partly) && errors.Is(err, dav.ErrScaleUnlisted):
		return ctx.Tf("import.partly", partly.Saved, partly.Total, ctx.T("form.repeatscale"))
	case errors.As(err, &partly):
		return ctx.Tf("import.partly", partly.Saved, partly.Total, partly.Err)
	case errors.As(err, &unreadable):
		return fmt.Sprintf(ctx.T("import.unreadable"), unreadable.Err)
	case errors.Is(err, pim.ErrTooLarge):
		return importTooLarge(ctx)
	case errors.Is(err, dav.ErrPlaceDown):
		return ctx.T("form.placedown")
	}
	return Refusal(ctx, err)
}

// ImportData is the section's import page.
type ImportData struct {
	web.PageData[Rail]
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

// HandleImportPage is the section's import page, reached from the rail of list, a route; any writable collection
// may receive. over answers an upload past the limit on the page.
func (pg Page) HandleImportPage(p *dav.Provider, list, section, title, hint, key string, address bool) (page, over func(*web.Context) error) {
	build := func(ctx *web.Context) (*ImportData, error) {
		rail, err := pg.Rail(ctx, ctx.Path(list))
		if err != nil {
			return nil, err
		}
		var groups []dav.Group
		for _, coll := range rail.Items {
			account := coll.Account
			if !coll.Writable {
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
		for _, place := range Places(ctx, p) {
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
			PageData: *web.NewPageData(ctx, ctx.T(title), rail),
			Section:  ctx.T(section),
			Title:    ctx.T(title),
			Key:      key,
			Ext:      pg.Ext,
			Hint:     ctx.T(hint),
			Groups:   groups,
			Account:  ctx.Login.Username(),
			Address:  address,
			URL:      ctx.QueryParam("url"),
		}
		if ctx.URLAccount() != "" {
			data.Account = ctx.URLAccount()
		}
		return data, nil
	}
	over = func(ctx *web.Context) error {
		data, err := build(ctx)
		if err != nil {
			return err
		}
		data.Refused(importTooLarge(ctx))
		return ctx.Render(http.StatusUnprocessableEntity, "pim/import", data)
	}
	page = func(ctx *web.Context) error {
		data, err := build(ctx)
		if err != nil {
			return err
		}
		if ctx.Request().Method != http.MethodPost {
			return ctx.Render(http.StatusOK, "pim/import", data)
		}
		acct, collPath, ok := strings.Cut(ctx.FormValue("collection"), "|")
		login := ctx.LoginFor(acct)
		if !ok || login == nil {
			data.Refused(ctx.T("form.destinationneeded"))
			return ctx.Render(http.StatusUnprocessableEntity, "pim/import", data)
		}
		data.Account, data.Path = acct, collPath
		var raw []byte
		name := ""
		if data.URL = strings.TrimSpace(ctx.FormValue("url")); address && data.URL != "" {
			raw, err = fetchAddress(ctx, ctx.Server.Outgoing.Remote(alborz.RoundTripTimeout), data.URL)
			if err != nil {
				data.Refused(err.Error())
				return ctx.Render(http.StatusUnprocessableEntity, "pim/import", data)
			}
		} else {
			file, err := ctx.FormFile("file")
			if err != nil {
				data.Refused(ctx.T("form.fileneeded"))
				return ctx.Render(http.StatusUnprocessableEntity, "pim/import", data)
			}
			if file.Size() > pim.MaxImportSize {
				data.Refused(importTooLarge(ctx))
				return ctx.Render(http.StatusUnprocessableEntity, "pim/import", data)
			}
			if raw, err = io.ReadAll(file.Open()); err != nil {
				return err
			}
			name = file.Filename
		}
		// The page's hooks read the account off the context, so the chosen account becomes the context's.
		ctx.Login = login
		files, err := pg.importFiles(ctx, name, raw)
		if err != nil {
			data.Refused(err.Error())
			return ctx.Render(http.StatusUnprocessableEntity, "pim/import", data)
		}
		redirectList := func() error { return ctx.FoundRedirect(ctx.URL(list)) }
		if place, isNew := strings.CutPrefix(collPath, newPrefix); isNew || len(files) > 1 {
			if err := pg.importMany(ctx, ctx.Path(list), files, place, isNew, collPath); pim.ImportAnswered(err) {
				data.Refused(ImportRefusal(ctx, err))
				return ctx.Render(http.StatusUnprocessableEntity, "pim/import", data)
			} else if err != nil {
				return err
			}
			return redirectList()
		}
		if err := pg.importRaw(ctx, collPath, raw); pim.ImportAnswered(err) {
			// A file that is not what it claims answers on the form, where another can be picked.
			data.Refused(ImportRefusal(ctx, err))
			return ctx.Render(http.StatusUnprocessableEntity, "pim/import", data)
		} else if err != nil {
			return err
		}
		return redirectList()
	}
	return page, over
}

// HandleExport sends the collection as one file; a date range narrows a calendar.
func (pg Page) HandleExport(ctx *web.Context) error {
	collPath := pg.collectionPath(ctx)
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
				return web.NewError(http.StatusBadRequest, err)
			}
			*bound.into = t
		}
	}
	if !to.IsZero() {
		// The form names the last day; the range ends after it.
		to = to.AddDate(0, 0, 1)
	}
	body, err := pg.Export(ctx, collPath, from, to)
	if errors.Is(err, pim.ErrNothingToExport) {
		return ctx.NotifyRedirect(web.Notice{Kind: web.NoticeWarning, Text: ctx.T("export.empty")}, pg.pageOf(ctx, collPath))
	}
	if err := LeftOut(ctx, err); err != nil {
		return err
	}
	if len(body) == 0 {
		return ctx.FoundRedirect(pg.pageOf(ctx, collPath))
	}
	name := info.Name
	if name == "" {
		name = "export"
	}
	return Download(ctx, name+pg.Ext, body)
}

// HandleExportAll zips every readable collection of the section whose list is the route list.
func (pg Page) HandleExportAll(list, filename string) func(*web.Context) error {
	return func(ctx *web.Context) error {
		rail, err := pg.Rail(ctx, ctx.Path(list))
		if err != nil {
			return err
		}
		var buf bytes.Buffer
		z := zip.NewWriter(&buf)
		taken := map[string]int{}
		var left pim.LeftOutError
		for _, coll := range rail.Items {
			account := coll.Account
			if account != "" && account != ctx.Login.Username() {
				continue
			}
			body, err := pg.Export(ctx, coll.Path, time.Time{}, time.Time{})
			var out *pim.LeftOutError
			if errors.As(err, &out) {
				left.Left, left.Err, err = left.Left+out.Left, errors.Join(left.Err, fmt.Errorf("%s: %w", coll.Name, out.Err)), nil
			}
			if errors.Is(err, pim.ErrNothingToExport) || (err == nil && len(body) == 0) {
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
		if left.Left > 0 {
			if err := LeftOut(ctx, &left); err != nil {
				return err
			}
		}
		return Download(ctx, filename, buf.Bytes())
	}
}

// HandleDelete removes the collection and everything in it; the page above states how much.
func (pg Page) HandleDelete(p *dav.Provider) func(*web.Context) error {
	return func(ctx *web.Context) error {
		collPath := pg.collectionPath(ctx)
		info, _, list, _, err := pg.Lookup(ctx, collPath)
		if err != nil {
			return err
		}
		rc := ctx.Request().Context()
		c, err := webdav.NewClient(p.HTTPClient(rc, ctx.Login), dav.Base().String())
		if err != nil {
			return err
		}
		back := func() error { return ctx.FoundRedirect(pg.pageOf(ctx, collPath)) }
		if err := c.RemoveAll(rc, collPath); err != nil {
			ctx.Notify(web.Notice{Kind: web.NoticeFailed,
				Text: fmt.Sprintf(ctx.T("notice.collectiondeletefailed"), info.Name)})
			return back()
		}
		pg.Forget(ctx.Login.Username())
		// A 2xx is not taken as proof; listing again shows what the reader will see.
		if _, _, _, _, err := pg.Lookup(ctx, collPath); err == nil {
			ctx.Notify(web.Notice{Kind: web.NoticeFailed,
				Text: fmt.Sprintf(ctx.T("notice.collectionnotdeleted"), info.Name)})
			return back()
		}
		done := "notice.collectiondeleted"
		if info.SharedBy != "" {
			done = "notice.shareleft"
		}
		return ctx.NoticeRedirect(fmt.Sprintf(ctx.T(done), info.Name), ctx.AccountPath(list))
	}
}
