package web

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
	"uuid"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/contacts"
	"git.mehdix.org/alborz/pim/dav"
	pimweb "git.mehdix.org/alborz/pim/web"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/carddav"
)

// copyContact keeps the complete card, giving its copy a fresh identity and revision.
func copyContact(rc context.Context, to pim.Ref[*carddav.Client], card vcard.Card) (*carddav.AddressObject, error) {
	id := uuid.New().String()
	card.SetValue(vcard.FieldUID, contacts.UIDPrefix+id)
	card.SetRevision(time.Now().UTC())
	return to.Client.PutAddressObject(rc, path.Join(to.Path, id+".vcf"), card, &carddav.PutAddressObjectOptions{IfNoneMatch: dav.IfNew})
}

func (p *plugin) copyContacts(ctx *web.Context) error {
	refs, err := pimweb.Selection(ctx, p.Client)
	if err != nil {
		return err
	}
	next := ctx.NextOr(ctx.URL("contacts.list"))
	if len(refs) == 0 {
		return ctx.FoundRedirect(next)
	}
	// A checked row represents one copy even if submitted more than once.
	paths := make([]string, 0, len(refs))
	unique := refs[:0]
	seen := map[string]bool{}
	for _, ref := range refs {
		key := ref.Account + "|" + ref.Path
		if !seen[key] {
			seen[key] = true
			paths = append(paths, key)
			unique = append(unique, ref)
		}
	}
	refs = unique
	groups, err := p.writableBookGroups(ctx)
	if err != nil {
		return err
	}
	completed := 0
	page := func(status int, refusal string) error {
		rail, err := p.bookRail(ctx)
		if err != nil {
			return err
		}
		data := &pimweb.DestinationRenderData{PageData: *web.NewPageData(ctx, ctx.T("common.copyask"), rail),
			Section: ctx.T("nav.contacts"), Home: ctx.Path("contacts.list"), Title: ctx.T("common.copyask"),
			Lead: ctx.Tf("common.copycount", len(refs)), Label: ctx.T("common.saveto"), Save: ctx.T("common.copydo"),
			Action: ctx.URL("contacts.copy"), Key: "contact", Groups: groups, Next: next, Paths: paths}
		if completed > 0 && refusal != "" {
			refusal = ctx.Tf("notice.itemscopied", completed) + " " + refusal
		}
		data.Refused(refusal)
		return ctx.Render(status, "calendar/destination", data)
	}
	if ctx.Request().Method != http.MethodPost {
		return page(http.StatusOK, "")
	}
	to, err := pimweb.Destination(ctx, ctx.FormValue("to"), p.Open, func(book dav.Collection) bool { return book.Writable })
	if handled, refusal := pimweb.DestinationRefusal(ctx, err, func(message string) error { return page(http.StatusUnprocessableEntity, message) }); handled {
		return refusal
	}
	if err != nil {
		return err
	}
	for done, ref := range refs {
		ao, err := contacts.GetObject(ctx.Request().Context(), ref.Client, ref.Path)
		if err == nil {
			_, err = copyContact(ctx.Request().Context(), to, ao.Card)
		}
		if err != nil {
			if done > 0 {
				completed = done
				ctx.Notify(web.Notice{Kind: web.NoticeDone, Text: ctx.Tf("notice.itemscopied", done)})
			}
			refs, paths = refs[done:], paths[done:]
			return page(http.StatusUnprocessableEntity, pimweb.Refusal(ctx, err))
		}
	}
	ctx.Notify(web.Notice{Kind: web.NoticeDone, Text: ctx.Tf("notice.itemscopied", len(refs))})
	return ctx.FoundRedirect(next)
}

func (p *plugin) transferContact(move bool) func(*web.Context) error {
	return func(ctx *web.Context) error {
		rc, src := ctx.Request().Context(), ctx.Param("path")
		c, books, err := p.Open(rc, ctx.Login)
		if err != nil {
			return err
		}
		held := dav.Holding(books, "", src)
		if held == nil {
			return alborz.NotFound("notfound.contact")
		}
		if move && !held.Writable {
			return web.NewError(http.StatusForbidden, ctx.T("form.readonlysource"))
		}
		ao, err := contacts.GetObject(rc, c, src)
		if code, _ := webdav.HTTPErrorCode(err); code == http.StatusNotFound {
			return alborz.NotFound("notfound.contact")
		}
		if err != nil {
			return err
		}
		groups, err := p.writableBookGroups(ctx)
		if err != nil {
			return err
		}
		kept := url.Values{}
		if from := ctx.From(); from != "" {
			kept.Set("from", from)
		}
		back := pimweb.ObjectURL(ctx, "contacts.contact", src, ctx.Login.Username(), kept)
		ask, save, notice := "common.copyask", "common.copydo", "notice.copiedto"
		if move {
			ask, save, notice = "common.moveask", "common.movedo", "notice.objectmovedto"
		}
		page := func(status int, refusal string) error {
			rail, err := p.bookRail(ctx)
			if err != nil {
				return err
			}
			data := &pimweb.DestinationRenderData{PageData: *web.NewPageData(ctx, ctx.T(ask), rail),
				Section: ctx.T("nav.contacts"), Home: ctx.Path("contacts.list"), Title: ctx.T(ask),
				Lead: contacts.AddressObject{AddressObject: ao}.DisplayName(), Label: ctx.T("common.saveto"),
				Save: ctx.T(save), Key: "contact", Groups: groups, Next: back}
			data.Refused(refusal)
			return ctx.Render(status, "calendar/destination", data)
		}
		if ctx.Request().Method != http.MethodPost {
			return page(http.StatusOK, "")
		}
		to, err := pimweb.Destination(ctx, ctx.FormValue("to"), p.Open, func(book dav.Collection) bool { return book.Writable })
		if handled, refusal := pimweb.DestinationRefusal(ctx, err, func(message string) error { return page(http.StatusUnprocessableEntity, message) }); handled {
			return refusal
		}
		if err != nil {
			return err
		}
		if move && to.Account == ctx.Login.Username() && to.Path == held.Path {
			return ctx.FoundRedirect(back)
		}
		if move && (ao.ETag == "" || strings.HasPrefix(ao.ETag, "W/")) {
			return page(http.StatusUnprocessableEntity, ctx.T("form.movewithoutversion"))
		}
		filename := path.Base(src)
		at := path.Join(to.Path, filename)
		native := false
		if move && to.Account == ctx.Login.Username() {
			for _, group := range groups {
				if group.Account == to.Account {
					if book := dav.At(group.Collections, to.Path); book != nil && book.Source == held.Source {
						native, err = dav.MoveObject(rc, p.DAV.HTTPClient(rc, ctx.Login), src, at, ao.ETag)
					}
				}
			}
			if err != nil {
				return page(http.StatusUnprocessableEntity, pimweb.Refusal(ctx, err))
			}
		}
		made := &carddav.AddressObject{Path: at}
		if !move {
			made, err = copyContact(rc, to, ao.Card)
		} else if !native {
			made, err = to.Client.PutAddressObject(rc, at, ao.Card, &carddav.PutAddressObjectOptions{IfNoneMatch: dav.IfNew})
		}
		if err != nil {
			return page(http.StatusUnprocessableEntity, pimweb.Refusal(ctx, err))
		}
		if move && !native {
			if made.ETag == "" || strings.HasPrefix(made.ETag, "W/") {
				return page(http.StatusUnprocessableEntity, ctx.T("form.moveincomplete"))
			}
			if err := dav.RemoveObject(rc, p.DAV.HTTPClient(rc, ctx.Login), src, ao.ETag); err != nil {
				if !dav.DeleteRefused(err) {
					return page(http.StatusUnprocessableEntity, ctx.T("form.moveuncertain"))
				}
				exists, checkErr := dav.SourceExists(rc, p.DAV.HTTPClient(rc, ctx.Login), src)
				if checkErr != nil || !exists {
					return page(http.StatusUnprocessableEntity, ctx.T("form.moveuncertain"))
				}
				cleanup := dav.RemoveObject(rc, p.DAV.HTTPClient(rc, ctx.LoginFor(to.Account)), made.Path, made.ETag)
				return page(http.StatusUnprocessableEntity, pimweb.Refusal(ctx, errors.Join(err, cleanup)))
			}
		}
		name := ""
		for _, group := range groups {
			if group.Account == to.Account {
				if book := dav.At(group.Collections, to.Path); book != nil {
					name = book.Name
				}
			}
		}
		sentence := ctx.T(notice)
		ctx.Notify(web.Notice{Kind: web.NoticeDone, Text: fmt.Sprintf(sentence, name), Markup: template.HTML(fmt.Sprintf(sentence, web.NoticeName(name)))})
		return ctx.FoundRedirect(pimweb.ObjectURL(ctx, "contacts.contact", made.Path, to.Account, kept))
	}
}
