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

// FilterRow is one narrowing the rail offers: what it is called, where
// it leads, and whether it is the one in force.
type FilterRow = dav.FilterRow

// GroupRows and CategoryRows are the two ways a contact belongs with
// others, as rail rows. The URL keeps the account it is scoped to and
// nothing else: a group and a colour narrow different things and one
// question at a time is enough.
func (d *AddressBookRenderData) GroupRows() []FilterRow {
	return contactGroupRows(d.Groups, d.Group, d.GlobalData.URLAccount)
}

func (d *AddressBookRenderData) CategoryRows() []FilterRow {
	return contactCategoryRows(d.Categories, d.Category, d.GlobalData.URLAccount)
}

// contactGroupRows and contactCategoryRows build the rail's narrowings
// the same way wherever they are drawn. The URL keeps the account it is
// scoped to and nothing else.
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

// groupContact adds a contact to a group or takes it out of one. What
// changes is the group's card - a group names its members (RFC 6350
// 6.6.5), a contact says nothing about its groups - except that a card
// with no UID is given one first, since a member is named by UID.
func (p *plugin) groupContact(ctx *alborz.Context) error {
	objPath, err := dav.ParseObjectPath(ctx.Param("path"))
	if err != nil {
		return err
	}
	c, books, err := p.clientWithAddressBooks(ctx.Request().Context(), ctx.Session)
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
		// The group is made where the contact is, which is the only
		// book the page knows anything about.
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

// putCard writes a card with a fresh revision, and says on the page
// where a server refused it rather than answering with an error page.
func (p *plugin) putCard(ctx *alborz.Context, c *carddav.Client, at string, card vcard.Card) error {
	if err := writeCard(ctx, c, at, card); err != nil {
		ctx.Notify(dav.Refused(ctx, err))
	}
	return nil
}
