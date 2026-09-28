package dav

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
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
	// Path is where the account will reach the collection once it accepts.
	Path    string
	Write   bool
	Expires time.Time
}

// RailInvitations are a section's open invitations and the prefix their answers post under.
type RailInvitations struct {
	Base  string
	Items []Invitation
}

const invitationsKey = "Invitations"

func (p *Provider) invitations(ctx *alborz.Context) ([]Invitation, error) {
	now := time.Now()
	var out []Invitation
	for _, s := range ctx.Logins() {
		account := strings.ToLower(s.Username())
		shares, err := p.store.Shares(func(sh collections.Share) bool {
			return sh.To == account && !sh.Accepted && !sh.Declined && !sh.Left && (sh.Expires.IsZero() || now.Before(sh.Expires))
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

// InjectInvitations hands the section's pages their open invitations; sections spare mail pages a share read.
func (p *Provider) InjectInvitations(field, base string, sections ...string) alborz.InjectFunc {
	return func(ctx *alborz.Context, data alborz.RenderData) error {
		if ctx.Login == nil {
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

// Authors are who added and last changed an object kept here, omitted where that is the viewer.
type Authors struct {
	Creator, Editor string
}

// Authors of the object at path as viewer sees them; none for an object elsewhere.
func (p *Provider) Authors(objPath, viewer string) Authors {
	ref, name, ok := collections.HeldObject(objPath)
	if !ok {
		return Authors{}
	}
	o, err := p.store.Object(ref, name)
	if err != nil {
		return Authors{}
	}
	a := Authors{Creator: o.Creator, Editor: o.Editor}
	if strings.EqualFold(a.Creator, viewer) {
		a.Creator = ""
	}
	if strings.EqualFold(a.Editor, viewer) || !o.Modified.After(o.Created) {
		a.Editor = ""
	}
	return a
}

// AddedBy is, for a list row, the account that added the object when it is not the row's own.
func (p *Provider) AddedBy(viewer string) func(account, path string) string {
	return func(account, objPath string) string {
		if account == "" {
			account = viewer
		}
		return p.Authors(objPath, account).Creator
	}
}

// Published says whether a collection kept here answers at a public address.
func (p *Provider) Published(collPath string) bool {
	ref, _, ok := collections.Held(collPath)
	if !ok {
		return false
	}
	c, err := p.store.Collection(ref)
	return err == nil && c.Public != ""
}

// Offer is one share as its invitee sees it, on the collection's page and beside the announcing mail.
type Offer struct {
	// Kind is the locale key naming what the collection is.
	Kind       string
	Name, From string
	Write      bool
	Expires    time.Time
	Offered    time.Time
	Answered   time.Time
	// State is pending, accepted, declined or left.
	State string
	// Accept and Decline are where answers post; Answer is the one a mail's link asked for.
	Accept, Decline string
	Answer          string
	Next            string
}

func (pg Page) kindKey(c *collections.Collection) string {
	switch {
	case pg.Ext == ".vcf":
		return "share.kindaddressbook"
	case len(c.Components) == 1 && c.Components[0] == "VTODO":
		return "share.kindtasks"
	}
	return "share.kindcalendar"
}

// offer is the account's share of the collection at collPath in its own home, nil when none.
func (pg Page) offer(ctx *alborz.Context, p *Provider, collPath, account string) (*Offer, error) {
	ref, home, here := collections.Held(collPath)
	account = strings.ToLower(account)
	if !here || home != account || ref.Owner == account {
		return nil, nil
	}
	sh, err := p.store.Share(ref, account)
	if errors.Is(err, collections.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c, err := p.store.Collection(ref)
	if err != nil {
		return nil, err
	}
	// A calendar's invitation is not an address book's to show.
	if c.Kind != p.kind.Holds {
		return nil, nil
	}
	state := "pending"
	switch {
	case sh.Accepted:
		state = "accepted"
	case sh.Declined:
		state = "declined"
	case sh.Left:
		state = "left"
	}
	base := pg.Base + url.PathEscape(collPath)
	q := "?account=" + alborz.QueryValue(account)
	return &Offer{
		Kind: pg.kindKey(c), Name: c.Name, From: c.Owner, Write: sh.Write,
		Expires: sh.Expires, Offered: sh.Created, Answered: sh.Answered, State: state,
		Accept: base + "/accept" + q, Decline: base + "/decline" + q,
		Answer: ctx.QueryParam("answer"), Next: pg.List + q,
	}, nil
}

// InjectOffer puts the invitation a mail announces beside it; the header only points, so a forged one shows nothing.
func (pg Page) InjectOffer(p *Provider) alborz.InjectFunc {
	return func(ctx *alborz.Context, data alborz.RenderData) error {
		m, ok := data.(*alborzbase.MessageRenderData)
		if !ok || m.Message == nil || ctx.Login == nil {
			return nil
		}
		path := strings.TrimSpace(m.Message.HeaderField(alborzbase.ShareHeader))
		if path == "" {
			return nil
		}
		offer, err := pg.offer(ctx, p, CanonicalCollectionPath(path), ctx.Login.Username())
		if err != nil || offer == nil {
			return err
		}
		offer.Next = m.GlobalData.URL.RequestURI()
		if m.Extra == nil {
			m.Extra = make(map[string]interface{})
		}
		m.Extra["ShareOffer"] = offer
		return nil
	}
}

// SharedWith counts the accounts a collection kept here is shared with or offered to, less refusals and ended shares.
func (p *Provider) SharedWith(collPath string) int {
	ref, home, ok := collections.Held(collPath)
	if !ok || ref.Owner != home {
		return 0
	}
	shares, err := p.store.Shares(func(sh collections.Share) bool {
		return sh.Ref() == ref && !sh.Declined && !sh.Left && !sh.Ended()
	})
	if err != nil {
		return 0
	}
	return len(shares)
}

// Sharing is what a collection kept here says about who else reaches it.
type Sharing struct {
	// Shares are the owner's to see and end; an invitee sees only that it is someone else's.
	Shares []collections.Share
	// PublicURL is the calendar's public address, empty when unpublished; OffersPublic says whether the kind can be.
	PublicURL    string
	OffersPublic bool
	// To and Until keep what a refused share had typed.
	To, Until string
	Write     bool
}

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
		sharing.PublicURL = ctx.Origin() + collections.PublicPath(c.Public)
	}
	return sharing, nil
}

// forgetInvitees drops invitees' cached lists before the collection goes, or they would go on naming it.
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

var (
	errNotAnAddress = errors.New("form.sharenotaddress")
	errNotServed    = errors.New("form.sharenotserved")
	errOwnAddress   = errors.New("form.shareown")
	errPastDate     = errors.New("form.sharepast")
	errNotADate     = errors.New("form.shareuntil")
)

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
	if to == strings.ToLower(ctx.Login.Username()) {
		return "", errOwnAddress
	}
	return to, nil
}

// HandleShare invites an address to the collection, or changes an existing invitation's terms.
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
			data.Refused(ctx.T(err.Error()))
			data.Sharing.To, data.Sharing.Until = ctx.FormValue("to"), ctx.FormValue("until")
			data.Sharing.Write = share.Write
			return ctx.Render(http.StatusUnprocessableEntity, "collection.html", data)
		}
		// Changing a share keeps a yes already given; sharing again after a no is a new invitation.
		if was, err := p.store.Share(ref, share.To); err == nil && was.Accepted {
			share.Accepted, share.Created = true, was.Created
		}
		if err := p.store.PutShare(share); err != nil {
			return err
		}
		// The invitee's access and the owner's count changed; both lists are asked again.
		pg.Forget(share.To)
		pg.Forget(ctx.Login.Username())
		// The invitation waits in the invitee's rail regardless of the mail, so a refused mail is only a warning.
		notice := alborz.Notice{Kind: alborz.NoticeDone, Text: fmt.Sprintf(ctx.T("notice.shared"), info.Name, share.To)}
		if !share.Accepted {
			c, err := p.store.Collection(ref)
			if err != nil {
				return err
			}
			subject, text := pg.offerMail(ctx, c, share)
			headers := map[string]string{alborzbase.ShareHeader: collections.PathOf(share.To, *c)}
			if err := alborzbase.SendText(ctx, []string{share.To}, subject, text, headers); err != nil {
				ctx.Logger().Printf("dav: failed to mail the invitation to %s: %v", share.To, err)
				notice = alborz.Notice{Kind: alborz.NoticeWarning, Text: fmt.Sprintf(ctx.T("notice.sharednomail"), share.To)}
			}
		}
		ctx.Notify(notice)
		return ctx.Redirect(http.StatusFound, pg.pageOf(ctx, info.Path))
	}
}

// offerMail is the invitation as mail; its links only open the answer page, since mail scanners open links on arrival.
func (pg Page) offerMail(ctx *alborz.Context, c *collections.Collection, sh collections.Share) (subject, text string) {
	kind := pg.kindKey(c)
	subject = fmt.Sprintf(ctx.T(kind+"mail"), sh.Owner, c.Name)
	page := ctx.Origin() + pg.Base + url.PathEscape(collections.PathOf(sh.To, *c)) + "?account=" + alborz.QueryValue(sh.To)
	access := ctx.T("share.canview")
	if sh.Write {
		access = ctx.T("share.canedit")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n%s: %s\n", subject, ctx.T("share.access"), access)
	if !sh.Expires.IsZero() {
		fmt.Fprintf(&b, "%s: %s\n", ctx.T("share.lastday"), sh.Expires.AddDate(0, 0, -1).Format(time.DateOnly))
	}
	fmt.Fprintf(&b, "\n%s: %s&answer=accept\n%s: %s&answer=decline\n",
		ctx.T("share.accept"), page, ctx.T("share.decline"), page)
	return subject, b.String()
}

// HandleAccess turns one address's access between viewing and editing.
func (pg Page) HandleAccess(p *Provider) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		info, ref, err := pg.owned(ctx)
		if err != nil {
			return err
		}
		sh, err := p.store.Share(ref, ctx.FormValue("to"))
		if errors.Is(err, collections.ErrNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "no such share")
		}
		if err != nil {
			return err
		}
		sh.Write = !sh.Write
		if err := p.store.PutShare(*sh); err != nil {
			return err
		}
		pg.Forget(sh.To)
		pg.Forget(ctx.Login.Username())
		key := "notice.sharecanview"
		if sh.Write {
			key = "notice.sharecanedit"
		}
		ctx.PutNotice(fmt.Sprintf(ctx.T(key), sh.To, info.Name))
		return ctx.Redirect(http.StatusFound, pg.pageOf(ctx, info.Path)+"#sharing")
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
		pg.Forget(ctx.Login.Username())
		ctx.PutNotice(fmt.Sprintf(ctx.T("notice.unshared"), info.Name, to))
		return ctx.Redirect(http.StatusFound, pg.pageOf(ctx, info.Path))
	}
}

// HandleAnswer is the invited account's yes or no, at the path in its own home.
func (pg Page) HandleAnswer(p *Provider, accept bool) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		collPath, err := ParseObjectPath(ctx.Param("path"))
		if err != nil {
			return err
		}
		ref, home, here := collections.Held(collPath)
		account := strings.ToLower(ctx.Login.Username())
		if !here || home != account {
			return echo.NewHTTPError(http.StatusNotFound, "no such invitation")
		}
		if _, err := p.store.Share(ref, account); err != nil {
			return echo.NewHTTPError(http.StatusNotFound, "no such invitation")
		}
		c, err := p.store.Collection(ref)
		if err != nil {
			return err
		}
		if err := p.store.Answer(ref, account, accept); err != nil {
			return err
		}
		// The owner's list counts who the collection is shared with.
		pg.Forget(c.Owner)
		if !accept {
			ctx.PutNotice(fmt.Sprintf(ctx.T("notice.sharedeclined"), c.Name))
			return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.AccountPath(pg.List)))
		}
		if err := pg.Show(ctx.Request().Context(), ctx.Login.Store(), CanonicalCollectionPath(collPath)); err != nil {
			ctx.Notify(Refused(ctx, err))
			return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.AccountPath(pg.List)))
		}
		pg.Forget(ctx.Login.Username())
		// The name leads to the collection the account just accepted.
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeDone,
			Text:   fmt.Sprintf(ctx.T("notice.shareaccepted"), c.Name),
			Markup: template.HTML(fmt.Sprintf(ctx.T("notice.shareaccepted"), alborz.NoticeLink(c.Name, pg.pageOf(ctx, CanonicalCollectionPath(collPath)))))})
		return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.AccountPath(pg.List)))
	}
}

// 192 bits: past guessing, short enough to paste.
const publicSecretBytes = 24

// HandlePublish gives the calendar a public address or takes it away; republishing makes a new one.
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
		pg.Forget(ctx.Login.Username())
		notice := "notice.unpublished"
		if publish {
			notice = "notice.published"
		}
		ctx.PutNotice(fmt.Sprintf(ctx.T(notice), info.Name))
		return ctx.Redirect(http.StatusFound, pg.pageOf(ctx, info.Path))
	}
}
