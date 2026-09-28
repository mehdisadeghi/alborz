package alborzcarddav

import (
	"fmt"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"uuid"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/plugins/dav"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav/carddav"
	"github.com/labstack/echo/v4"
)

// FilterRow is one narrowing the rail offers.
type FilterRow = dav.FilterRow

// GroupRows and CategoryRows are the rail rows for groups and categories.
func (d *AddressBookRenderData) GroupRows() []FilterRow {
	return contactGroupRows(d.Groups, d.Group, d.GlobalData.URLAccount)
}

func (d *AddressBookRenderData) CategoryRows() []FilterRow {
	return contactCategoryRows(d.Categories, d.Category, d.GlobalData.URLAccount)
}

func contactGroupRows(groups []AddressObject, active, account string) []FilterRow {
	rows := make([]FilterRow, 0, len(groups))
	for _, g := range groups {
		uid := g.UID()
		if uid == "" {
			continue
		}
		edit := g.URL()
		if g.Account != "" {
			edit += "?account=" + alborz.QueryValue(g.Account)
		}
		rows = append(rows, FilterRow{
			Label:  g.DisplayName(),
			Href:   filterHref("group", uid, account),
			Active: uid == active,
			Edit:   edit,
		})
	}
	return rows
}

func contactCategoryRows(categories []string, active, account string) []FilterRow {
	rows := make([]FilterRow, 0, len(categories))
	for _, word := range categories {
		rows = append(rows, FilterRow{
			Label:  word,
			Href:   filterHref("category", word, account),
			Active: word == active,
		})
	}
	return rows
}

func filterHref(name, value, account string) string {
	q := url.Values{name: {value}}
	if account != "" {
		q.Set("account", account)
	}
	return "/contacts?" + alborz.Query(q)
}

// A group's card names its members, so that card changes; a card without a UID gets one first.
func (p *plugin) groupContact(ctx *alborz.Context) error {
	objPath, err := dav.ParseObjectPath(ctx.Param("path"))
	if err != nil {
		return err
	}
	c, books, err := p.clientWithAddressBooks(ctx.Request().Context(), ctx.Login)
	if err != nil {
		return err
	}
	ao, err := getAddressObject(ctx, c, objPath)
	if err != nil {
		return fmt.Errorf("failed to read the contact: %v", err)
	}
	contact := AddressObject{AddressObject: ao}
	uid := contact.UID()
	if uid == "" {
		uid = uuid.New().String()
		ao.Card.SetValue(vcard.FieldUID, memberPrefix+uid)
		if err := writeCard(ctx, c, ao.Path, ao.Card); err != nil {
			ctx.Notify(dav.Refused(ctx, err))
			return ctx.Redirect(http.StatusFound, ctx.NextOr(contact.URL()))
		}
	}

	list, err := p.contactList(ctx)
	if err != nil {
		return err
	}
	// The contact comes back naming the groups it is now in.
	ctx.Quiet()
	back := ctx.NextOr(ctx.AccountPath(contact.URL()))
	if leaving := ctx.FormValue("leave"); leaving != "" {
		for _, g := range list.Groups {
			if g.UID() != leaving {
				continue
			}
			kept := slices.DeleteFunc(g.Members(), func(m string) bool { return m == uid })
			SetMembers(g.Card, kept)
			if err := p.putCard(ctx, c, g.Path, g.Card); err != nil {
				return err
			}
		}
		return ctx.Redirect(http.StatusFound, back)
	}

	if name := strings.TrimSpace(ctx.FormValue("newgroup")); name != "" {
		card := vcard.Card{}
		card.SetValue(vcard.FieldVersion, "4.0")
		card.SetValue(vcard.FieldFormattedName, name)
		card.SetValue(vcard.FieldUID, memberPrefix+uuid.New().String())
		card.SetKind(vcard.KindGroup)
		SetMembers(card, []string{uid})
		// The group is made in the contact's book, the only one the page knows.
		book := dav.Holding(books, "", ao.Path)
		if book == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "the contact is in no address book")
		}
		if err := p.putCard(ctx, c, path.Join(book.Path, uuid.New().String()+".vcf"), card); err != nil {
			return err
		}
		return ctx.Redirect(http.StatusFound, back)
	}

	joining := ctx.FormValue("group")
	for _, g := range list.Groups {
		if g.UID() != joining || slices.Contains(g.Members(), uid) {
			continue
		}
		SetMembers(g.Card, append(g.Members(), uid))
		if err := p.putCard(ctx, c, g.Path, g.Card); err != nil {
			return err
		}
	}
	return ctx.Redirect(http.StatusFound, back)
}

// A server's refusal shows on the page, never as an error page.
func (p *plugin) putCard(ctx *alborz.Context, c *carddav.Client, at string, card vcard.Card) error {
	if err := writeCard(ctx, c, at, card); err != nil {
		ctx.Notify(dav.Refused(ctx, err))
	}
	return nil
}
