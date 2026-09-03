package dav

import (
	"fmt"
	"io"
	"mime"
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
	Error     string
	// Ext is the file type the collection is exported as and imported
	// from, ".ics" or ".vcf"; OffersRange says the export form takes a
	// date range, which only a calendar has.
	Ext         string
	OffersRange bool
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
		data := &CollectionRenderData{
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
		f, err := file.Open()
		if err != nil {
			return err
		}
		defer f.Close()
		raw, err := io.ReadAll(f)
		if err != nil {
			return err
		}
		n, err := pg.Import(ctx, collPath, raw)
		if err != nil {
			return err
		}
		if n == 0 {
			ctx.Session.Notify(alborz.Notice{Kind: alborz.NoticeWarning, Text: ctx.T("import.nothing")})
		} else {
			ctx.Session.PutNotice(ctx.Tf("import."+strings.TrimPrefix(pg.Ext, "."), n))
		}
		pg.Forget(ctx.Session.Username())
		return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.AccountPath(pg.Base+url.PathEscape(collPath))))
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
		_, _, list, _, err := pg.Lookup(ctx, collPath)
		if err != nil {
			return err
		}
		base, _ := p.URL(ctx.Session)
		target := base.ResolveReference(&url.URL{Path: collPath}).String()
		if err := DeleteCollection(ctx.Request().Context(), p.HTTPClient(ctx.Session), target); err != nil {
			return err
		}
		pg.Forget(ctx.Session.Username())
		return ctx.Redirect(http.StatusFound, ctx.AccountPath(list))
	}
}
