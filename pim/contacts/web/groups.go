package web

import (
	"fmt"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"uuid"

	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/contacts"
	"git.mehdix.org/alborz/pim/dav"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav/carddav"
)

// FilterRow is one narrowing the rail offers.
type FilterRow = pim.FilterRow

func contactGroupRows(ctx *web.Context, groups []contacts.AddressObject, active, account string) []FilterRow {
	rows := make([]FilterRow, 0, len(groups))
	for _, g := range groups {
		uid := g.UID()
		if uid == "" {
			continue
		}
		edit := ctx.Path("contacts.contact", g.Path)
		if g.Account != "" {
			edit += "?account=" + web.QueryValue(g.Account)
		}
		rows = append(rows, FilterRow{
			Label:  g.DisplayName(),
			Href:   filterHref(ctx, "group", uid, account),
			Active: uid == active,
			Edit:   edit,
		})
	}
	return rows
}

func contactCategoryRows(ctx *web.Context, categories []string, active, account string) []FilterRow {
	rows := make([]FilterRow, 0, len(categories))
	for _, word := range categories {
		rows = append(rows, FilterRow{
			Label:  word,
			Href:   filterHref(ctx, "category", word, account),
			Active: word == active,
		})
	}
	return rows
}

func filterHref(ctx *web.Context, name, value, account string) string {
	q := url.Values{name: {value}}
	if account != "" {
		q.Set("account", account)
	}
	return ctx.Path("contacts.list") + "?" + web.Query(q)
}

// A group's card names its members, so that card changes; a card without a UID gets one first.
func (p *plugin) groupContact(ctx *web.Context) error {
	objPath := ctx.Param("path")
	c, books, err := p.Open(ctx.Request().Context(), ctx.Login)
	if err != nil {
		return err
	}
	ao, err := c.GetAddressObject(ctx.Request().Context(), objPath)
	if err != nil {
		return fmt.Errorf("failed to read the contact: %v", err)
	}
	contact := contacts.AddressObject{AddressObject: ao}
	uid := contact.UID()
	if uid == "" {
		uid = uuid.New().String()
		ao.Card.SetValue(vcard.FieldUID, contacts.MemberPrefix+uid)
		if err := contacts.WriteCard(ctx.Request().Context(), c, ao.Path, ao.Card); err != nil {
			ctx.Notify(pim.Refused(ctx, err))
			return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.Path("contacts.contact", contact.Path)))
		}
	}

	list, err := p.contactList(ctx)
	if err != nil {
		return err
	}
	// The contact comes back naming the groups it is now in.
	ctx.Quiet()
	back := ctx.NextOr(ctx.URL("contacts.contact", contact.Path))
	if leaving := ctx.FormValue("leave"); leaving != "" {
		for _, g := range list.Groups {
			if g.UID() != leaving {
				continue
			}
			kept := slices.DeleteFunc(g.Members(), func(m string) bool { return m == uid })
			contacts.SetMembers(g.Card, kept)
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
		card.SetValue(vcard.FieldUID, contacts.MemberPrefix+uuid.New().String())
		card.SetKind(vcard.KindGroup)
		contacts.SetMembers(card, []string{uid})
		// The group is made in the contact's book, the only one the page knows.
		book := dav.Holding(books, "", ao.Path)
		if book == nil {
			return web.NewError(http.StatusBadRequest, "the contact is in no address book")
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
		contacts.SetMembers(g.Card, append(g.Members(), uid))
		if err := p.putCard(ctx, c, g.Path, g.Card); err != nil {
			return err
		}
	}
	return ctx.Redirect(http.StatusFound, back)
}

// A server's refusal shows on the page, never as an error page.
func (p *plugin) putCard(ctx *web.Context, c *carddav.Client, at string, card vcard.Card) error {
	if err := contacts.WriteCard(ctx.Request().Context(), c, at, card); err != nil {
		ctx.Notify(pim.Refused(ctx, err))
	}
	return nil
}
