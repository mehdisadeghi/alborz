package dav

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"slices"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	alborzbase "git.mehdix.org/alborz/plugins/base"
	"git.mehdix.org/alborz/plugins/collections"
	"github.com/labstack/echo/v4"
)

// Invitation is a share offered to a signed-in account and not answered.
type Invitation struct {
	Account string
	Name    string
	From    string
	// Path is where the account will reach the collection once it
	// accepts.
	Path    string
	Write   bool
	Expires time.Time
}

// RailInvitations are a section's open invitations and the page prefix
// their answers are posted under.
type RailInvitations struct {
	Base  string
	Items []Invitation
}

// invitationsKey is where the rail finds them among a page's extras,
// each section's under its rail's field.
const invitationsKey = "Invitations"

// invitations are what the signed-in accounts have been offered.
func (p *Provider) invitations(ctx *alborz.Context) ([]Invitation, error) {
	now := time.Now()
	var out []Invitation
	for _, s := range ctx.Sessions() {
		account := strings.ToLower(s.Username())
		shares, err := p.store.Shares(func(sh collections.Share) bool {
			return sh.To == account && !sh.Accepted && (sh.Expires.IsZero() || now.Before(sh.Expires))
		})
		if err != nil {
			return nil, err
		}
		for _, sh := range shares {
			c, err := p.store.Collection(sh.Ref())
			if err != nil {
				return nil, err
			}
			if c.Kind != p.kind.Holds {
				continue
			}
			out = append(out, Invitation{
				Account: s.Username(), Name: c.Name, From: c.Owner,
				Path: collections.PathOf(account, *c), Write: sh.Write, Expires: sh.Expires,
			})
		}
	}
	return out, nil
}

// InjectInvitations hands the section's pages its open invitations, for
// the rail to list under the collections they would join. sections are
// the paths those pages are under: a hook sees every page rendered, and
// each mail page would otherwise pay a read of the shares per account
// for a rail it does not draw.
func (p *Provider) InjectInvitations(field, base string, sections ...string) alborz.InjectFunc {
	return func(ctx *alborz.Context, data alborz.RenderData) error {
		if p.store == nil || ctx.Session == nil {
			return nil
		}
		at := ctx.Request().URL.Path
		if !slices.ContainsFunc(sections, func(root string) bool { return at == root || strings.HasPrefix(at, root+"/") }) {
			return nil
		}
		items, err := p.invitations(ctx)
		if err != nil || len(items) == 0 {
			return err
		}
		extra := data.Global().Extra
		// Pointers, so a section with none is nothing to a template.
		all, _ := extra[invitationsKey].(map[string]*RailInvitations)
		if all == nil {
			all = make(map[string]*RailInvitations)
			extra[invitationsKey] = all
		}
		all[field] = &RailInvitations{Base: base, Items: items}
		return nil
	}
}

// Sharing is what the page of a collection kept here says about who
// else reaches it.
type Sharing struct {
	// Shares are the owner's to see and end; an invitee sees only that
	// the collection is somebody else's.
	Shares []collections.Share
	// PublicURL is the address the calendar is published at, empty when
	// it is not; OffersPublic is whether the kind can be.
	PublicURL    string
	OffersPublic bool
	// Form is what a refused share had typed, for the page to keep.
	To, Until string
	Write     bool
	Error     string
}

// sharing reads the page's sharing block: nil for a collection on a
// server elsewhere, or one the account was invited to.
func (pg Page) sharing(ctx *alborz.Context, p *Provider, info Collection) (*Sharing, error) {
	if !info.Here || info.SharedBy != "" {
		return nil, nil
	}
	ref, _, _ := collections.Held(info.Path)
	c, err := p.store.Collection(ref)
	if err != nil {
		return nil, err
	}
	shares, err := p.store.Shares(func(sh collections.Share) bool { return sh.Ref() == ref })
	if err != nil {
		return nil, err
	}
	sharing := &Sharing{Shares: shares, OffersPublic: pg.Ext == ".ics"}
	if c.Public != "" {
		sharing.PublicURL = ctx.Scheme() + "://" + ctx.Request().Host + collections.PublicPath(c.Public)
	}
	return sharing, nil
}

// forgetInvitees drops the cached lists of everyone a collection is
// shared with, before it goes: theirs would go on naming it.
func (pg Page) forgetInvitees(p *Provider, path string) error {
	ref, _, here := collections.Held(path)
	if !here {
		return nil
	}
	shares, err := p.store.Shares(func(sh collections.Share) bool { return sh.Ref() == ref })
	for _, sh := range shares {
		pg.Forget(sh.To)
	}
	return err
}

// owned resolves the route's collection for a request only its owner
// may make.
func (pg Page) owned(ctx *alborz.Context) (Collection, collections.Ref, error) {
	collPath, err := ParseObjectPath(ctx.Param("path"))
	if err != nil {
		return Collection{}, collections.Ref{}, err
	}
	info, _, _, _, err := pg.Lookup(ctx, CanonicalCollectionPath(collPath))
	if err != nil {
		return info, collections.Ref{}, err
	}
	ref, _, here := collections.Held(info.Path)
	if !here || info.SharedBy != "" {
		return info, ref, echo.NewHTTPError(http.StatusForbidden, "not a collection of this account's kept here")
	}
	return info, ref, nil
}

func (pg Page) pageOf(ctx *alborz.Context, path string) string {
	return ctx.AccountPath(pg.Base + url.PathEscape(path))
}

// errNotServed and the rest are what a share can be refused for, each a
// sentence of the form's.
var (
	errNotAnAddress = errors.New("form.sharenotaddress")
	errNotServed    = errors.New("form.sharenotserved")
	errOwnAddress   = errors.New("form.shareown")
	errPastDate     = errors.New("form.sharepast")
	errNotADate     = errors.New("form.shareuntil")
)

// invitee reads the address a share names: an account that could sign
// in here, and not the owner's own.
func invitee(ctx *alborz.Context, typed string) (string, error) {
	addr, err := mail.ParseAddress(strings.TrimSpace(typed))
	if err != nil {
		return "", errNotAnAddress
	}
	to := strings.ToLower(addr.Address)
	_, domain, _ := strings.Cut(to, "@")
	served := ctx.Server.Domains()
	// The unnamed domain takes sign-ins from anywhere.
	if !slices.Contains(served, domain) && !slices.Contains(served, "") {
		return "", errNotServed
	}
	if to == strings.ToLower(ctx.Session.Username()) {
		return "", errOwnAddress
	}
	return to, nil
}

// HandleShare invites an address to the collection, or changes what an
// invitation already made allows.
func (pg Page) HandleShare(p *Provider) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		info, ref, err := pg.owned(ctx)
		if err != nil {
			return err
		}
		share := collections.Share{
			Owner: ref.Owner, Collection: ref.ID,
			Write: ctx.FormValue("access") == "write", Created: time.Now().UTC(),
		}
		share.To, err = invitee(ctx, ctx.FormValue("to"))
		if until := strings.TrimSpace(ctx.FormValue("until")); err == nil && until != "" {
			var day time.Time
			if day, err = ctx.ReadDate(until, alborzbase.UserLocation(ctx)); err == nil {
				// The form names the last day; the share ends after it.
				share.Expires = day.AddDate(0, 0, 1)
				if !share.Expires.After(time.Now()) {
					err = errPastDate
				}
			} else {
				err = errNotADate
			}
		}
		if err != nil {
			data, dataErr := pg.data(ctx, p, info.Path)
			if dataErr != nil {
				return dataErr
			}
			data.Sharing.Error = ctx.T(err.Error())
			data.Sharing.To, data.Sharing.Until = ctx.FormValue("to"), ctx.FormValue("until")
			data.Sharing.Write = share.Write
			return ctx.Render(http.StatusUnprocessableEntity, "collection.html", data)
		}
		// Changing what a share allows does not ask again for a yes
		// already given.
		if was, err := p.store.Share(ref, share.To); err == nil {
			share.Accepted, share.Created = was.Accepted, was.Created
		}
		if err := p.store.PutShare(share); err != nil {
			return err
		}
		// What the invitee may do has changed; its list is asked again.
		pg.Forget(share.To)
		// The invitation waits in the invitee's rail whether or not the
		// mail saying so got through, so a refused mail is a warning and
		// the share stands.
		notice := alborz.Notice{Kind: alborz.NoticeDone, Text: fmt.Sprintf(ctx.T("notice.shared"), info.Name, share.To)}
		if !share.Accepted {
			subject := fmt.Sprintf(ctx.T("share.mailsubject"), info.Name)
			text := fmt.Sprintf(ctx.T("share.mailtext"), ctx.Session.Username(), info.Name,
				ctx.Scheme()+"://"+ctx.Request().Host+pg.List)
			if err := alborzbase.SendText(ctx, []string{share.To}, subject, text); err != nil {
				ctx.Logger().Printf("dav: failed to mail the invitation to %s: %v", share.To, err)
				notice = alborz.Notice{Kind: alborz.NoticeWarning, Text: fmt.Sprintf(ctx.T("notice.sharednomail"), share.To)}
			}
		}
		ctx.Notify(notice)
		return ctx.Redirect(http.StatusFound, pg.pageOf(ctx, info.Path))
	}
}

// HandleUnshare ends one address's access.
func (pg Page) HandleUnshare(p *Provider) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		info, ref, err := pg.owned(ctx)
		if err != nil {
			return err
		}
		to := ctx.FormValue("to")
		if err := p.store.RemoveShare(ref, to); err != nil && !errors.Is(err, collections.ErrNotFound) {
			return err
		}
		pg.Forget(to)
		ctx.PutNotice(fmt.Sprintf(ctx.T("notice.unshared"), info.Name, to))
		return ctx.Redirect(http.StatusFound, pg.pageOf(ctx, info.Path))
	}
}

// HandleAnswer is the invited account's yes or no. The path is the one
// the invitation named, in the account's own home.
func (pg Page) HandleAnswer(p *Provider, accept bool) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		collPath, err := ParseObjectPath(ctx.Param("path"))
		if err != nil {
			return err
		}
		ref, home, here := collections.Held(collPath)
		account := strings.ToLower(ctx.Session.Username())
		if !here || home != account {
			return echo.NewHTTPError(http.StatusNotFound, "no such invitation")
		}
		share, err := p.store.Share(ref, account)
		if err != nil {
			return echo.NewHTTPError(http.StatusNotFound, "no such invitation")
		}
		c, err := p.store.Collection(ref)
		if err != nil {
			return err
		}
		if !accept {
			if err := p.store.RemoveShare(ref, account); err != nil {
				return err
			}
			ctx.PutNotice(fmt.Sprintf(ctx.T("notice.sharedeclined"), c.Name))
			return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.AccountPath(pg.List)))
		}
		share.Accepted = true
		if err := p.store.PutShare(*share); err != nil {
			return err
		}
		if err := pg.Show(ctx.Session.Store(), CanonicalCollectionPath(collPath)); err != nil {
			return err
		}
		pg.Forget(ctx.Session.Username())
		ctx.PutNotice(fmt.Sprintf(ctx.T("notice.shareaccepted"), c.Name))
		return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.AccountPath(pg.List)))
	}
}

// publicSecretBytes is the length of the secret a published calendar's
// address carries: 192 bits, past guessing, short enough to paste.
const publicSecretBytes = 24

// HandlePublish gives the calendar an address anyone can read it at, or
// takes it away; the next one published is a new address.
func (pg Page) HandlePublish(p *Provider, publish bool) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		info, ref, err := pg.owned(ctx)
		if err != nil {
			return err
		}
		secret := ""
		if publish {
			raw := make([]byte, publicSecretBytes)
			if _, err := rand.Read(raw); err != nil {
				return err
			}
			secret = base64.RawURLEncoding.EncodeToString(raw)
		}
		if err := p.store.Update(ref, func(c *collections.Collection) { c.Public = secret }); err != nil {
			return err
		}
		notice := "notice.unpublished"
		if publish {
			notice = "notice.published"
		}
		ctx.PutNotice(fmt.Sprintf(ctx.T(notice), info.Name))
		return ctx.Redirect(http.StatusFound, pg.pageOf(ctx, info.Path))
	}
}
