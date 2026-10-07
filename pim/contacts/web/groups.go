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
	pimweb "git.mehdix.org/alborz/pim/web"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav/carddav"
)

// FilterRow is one narrowing the rail offers.
type FilterRow = pimweb.FilterRow

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
		err := contacts.ChangeCard(ctx.Request().Context(), pim.Ref[*carddav.Client]{Client: c, Path: ao.Path}, func(card vcard.Card) {
			if uid = cardOf(card).UID(); uid == "" {
				uid = uuid.New().String()
				card.SetValue(vcard.FieldUID, contacts.UIDPrefix+uid)
			}
		})
		if err != nil {
			ctx.Notify(pimweb.Refused(ctx, err))
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
			changeMembers(ctx, c, g.Path, func(members []string) []string {
				return slices.DeleteFunc(members, func(m string) bool { return m == uid })
			})
		}
		return ctx.Redirect(http.StatusFound, back)
	}

	if name := strings.TrimSpace(ctx.FormValue("newgroup")); name != "" {
		card := vcard.Card{}
		card.SetValue(vcard.FieldVersion, "4.0")
		card.SetValue(vcard.FieldFormattedName, name)
		card.SetValue(vcard.FieldUID, contacts.UIDPrefix+uuid.New().String())
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
		changeMembers(ctx, c, g.Path, func(members []string) []string {
			if slices.Contains(members, uid) {
				return members
			}
			return append(members, uid)
		})
	}
	return ctx.Redirect(http.StatusFound, back)
}

// changeMembers changes the group as the server holds it now, not as the list read it, so a write
// landing meanwhile is kept; a refusal shows on the page.
func changeMembers(ctx *web.Context, c *carddav.Client, group string, change func([]string) []string) {
	err := contacts.ChangeCard(ctx.Request().Context(), pim.Ref[*carddav.Client]{Client: c, Path: group}, func(card vcard.Card) {
		contacts.SetMembers(card, change(cardOf(card).Members()))
	})
	if err != nil {
		ctx.Notify(pimweb.Refused(ctx, err))
	}
}

func cardOf(card vcard.Card) contacts.AddressObject {
	return contacts.AddressObject{AddressObject: &carddav.AddressObject{Card: card}}
}

// A server's refusal shows on the page, never as an error page.
func (p *plugin) putCard(ctx *web.Context, c *carddav.Client, at string, card vcard.Card) error {
	if err := contacts.WriteCard(ctx.Request().Context(), c, at, card); err != nil {
		ctx.Notify(pimweb.Refused(ctx, err))
	}
	return nil
}
