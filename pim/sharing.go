package pim

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"git.mehdix.org/alborz/internal/dates"
	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/pim/dav"
	"git.mehdix.org/alborz/pim/store"
	"git.mehdix.org/alborz/web"
)

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

func (pg Page) kindKey(c *store.Collection) string {
	switch {
	case pg.Ext == ".vcf":
		return "share.kindaddressbook"
	case len(c.Components) == 1 && c.Components[0] == "VTODO":
		return "share.kindtasks"
	}
	return "share.kindcalendar"
}

// offer is the account's share of the collection at collPath in its own home, nil when none.
func (pg Page) offer(ctx *web.Context, p *dav.Provider, collPath, account string) (*Offer, error) {
	ref, home, here := p.Held(collPath)
	account = strings.ToLower(account)
	if !here || home != account || ref.Owner == account {
		return nil, nil
	}
	sh, err := p.Store.Share(ref, account)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c, err := p.Store.Collection(ref)
	if err != nil {
		return nil, err
	}
	// A calendar's invitation is not an address book's to show.
	if c.Kind != p.Kind.Holds {
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
	q := "?account=" + web.QueryValue(account)
	return &Offer{
		Kind: pg.kindKey(c), Name: c.Name, From: c.Owner, Write: sh.Write,
		Expires: sh.Expires, Offered: sh.Created, Answered: sh.Answered, State: state,
		Accept: ctx.Path(pg.Collection+".accept", collPath) + q, Decline: ctx.Path(pg.Collection+".decline", collPath) + q,
		Answer: ctx.QueryParam("answer"), Next: ctx.Path(pg.List) + q,
	}, nil
}

// ShowOffer puts the invitation a mail announces beside it; the header only points, so a forged one shows nothing.
func (pg Page) ShowOffer(p *dav.Provider) func(*web.Context, web.RenderData) error {
	return func(ctx *web.Context, data web.RenderData) error {
		m := data.(*mail.MessageRenderData)
		path := strings.TrimSpace(m.Message.HeaderField(mail.ShareHeader))
		if path == "" {
			return nil
		}
		offer, err := pg.offer(ctx, p, dav.CanonicalCollectionPath(path), ctx.Login.Username())
		if err != nil || offer == nil {
			return err
		}
		offer.Next = m.GlobalData.URL.RequestURI()
		m.ShareOffer = offer
		return nil
	}
}

// Sharing is what a collection kept here says about who else reaches it.
type Sharing struct {
	// Shares are the owner's to see and end; an invitee sees only that it is someone else's.
	Shares []store.Share
	// PublicURL is the calendar's public address, empty when unpublished; OffersPublic says whether the kind can be.
	PublicURL    string
	OffersPublic bool
	// To and Until keep what a refused share had typed.
	To    string
	Until dates.Field
	Write bool
}

func (pg Page) sharing(ctx *web.Context, p *dav.Provider, info dav.Collection) (*Sharing, error) {
	if !info.Here || info.SharedBy != "" {
		return nil, nil
	}
	ref, _, _ := p.Held(info.Path)
	c, err := p.Store.Collection(ref)
	if err != nil {
		return nil, err
	}
	shares, err := p.Store.Shares(func(sh store.Share) bool { return sh.Ref() == ref })
	if err != nil {
		return nil, err
	}
	sharing := &Sharing{Shares: shares, OffersPublic: pg.Ext == ".ics", Until: ctx.Dates().Input(time.Time{})}
	if c.Public != "" {
		sharing.PublicURL = ctx.Origin() + store.PublicPath(c.Public)
	}
	return sharing, nil
}

func (pg Page) owned(ctx *web.Context, p *dav.Provider) (dav.Collection, store.Ref, error) {
	collPath := ctx.Param("path")
	info, _, _, _, err := pg.Lookup(ctx, dav.CanonicalCollectionPath(collPath))
	if err != nil {
		return info, store.Ref{}, err
	}
	ref, _, here := p.Held(info.Path)
	if !here || info.SharedBy != "" {
		return info, ref, web.NewError(http.StatusForbidden, "not one of this account's collections on alborz")
	}
	return info, ref, nil
}

func (pg Page) pageOf(ctx *web.Context, path string) string {
	return ctx.URL(pg.Collection, path)
}

var (
	errPastDate = errors.New("the last day has passed")
	errNotADate = errors.New("not a date")
)

var shareRefusals = map[error]string{
	ErrNotAnAddress: "form.sharenotaddress",
	ErrNotServed:    "form.sharenotserved",
	ErrOwnAddress:   "form.shareown",
	errPastDate:     "form.sharepast",
	errNotADate:     "form.shareuntil",
}

// HandleShare invites an address to the collection, or changes an existing invitation's terms.
func (pg Page) HandleShare(p *dav.Provider) func(*web.Context) error {
	return func(ctx *web.Context) error {
		info, ref, err := pg.owned(ctx, p)
		if err != nil {
			return err
		}
		share := store.Share{
			Owner: ref.Owner, Collection: ref.ID,
			Write: ctx.FormValue("access") == "write", Created: time.Now().UTC(),
		}
		share.To, err = Invitee(ctx.FormValue("to"), ctx.Server.Domains(), ctx.Login.Username())
		if until := ctx.DateField("until"); err == nil && until.Value != "" {
			var day time.Time
			if day, err = until.Read(web.UserLocation(ctx)); err == nil {
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
			data.Refused(ctx.T(shareRefusals[err]))
			data.Sharing.To, data.Sharing.Until = ctx.FormValue("to"), ctx.DateField("until")
			data.Sharing.Write = share.Write
			return ctx.Render(http.StatusUnprocessableEntity, "pim/collection", data)
		}
		if share, err = Share(p.Store, share); err != nil {
			return err
		}
		// The invitation waits in the invitee's rail regardless of the mail, so a refused mail is only a warning.
		notice := web.Notice{Kind: web.NoticeDone, Text: fmt.Sprintf(ctx.T("notice.shared"), info.Name, share.To)}
		if !share.Accepted {
			c, err := p.Store.Collection(ref)
			if err != nil {
				return err
			}
			subject, text := pg.offerMail(ctx, p, c, share)
			headers := map[string]string{mail.ShareHeader: p.PathOf(share.To, *c)}
			if err := mail.SendText(ctx, []string{share.To}, subject, text, headers); err != nil {
				ctx.Logger().Printf("dav: failed to mail the invitation to %s: %v", share.To, err)
				notice = web.Notice{Kind: web.NoticeWarning, Text: fmt.Sprintf(ctx.T("notice.sharednomail"), share.To)}
			}
		}
		ctx.Notify(notice)
		return ctx.Redirect(http.StatusFound, pg.pageOf(ctx, info.Path))
	}
}

// offerMail is the invitation as mail; its links only open the answer page, since mail scanners open links on arrival.
func (pg Page) offerMail(ctx *web.Context, p *dav.Provider, c *store.Collection, sh store.Share) (subject, text string) {
	kind := pg.kindKey(c)
	subject = fmt.Sprintf(ctx.T(kind+"mail"), sh.Owner, c.Name)
	page := ctx.Origin() + ctx.Path(pg.Collection, p.PathOf(sh.To, *c)) + "?account=" + web.QueryValue(sh.To)
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
func (pg Page) HandleAccess(p *dav.Provider) func(*web.Context) error {
	return func(ctx *web.Context) error {
		info, ref, err := pg.owned(ctx, p)
		if err != nil {
			return err
		}
		sh, err := p.Store.Share(ref, ctx.FormValue("to"))
		if errors.Is(err, store.ErrNotFound) {
			return web.NewError(http.StatusNotFound, "no such share")
		}
		if err != nil {
			return err
		}
		sh.Write = !sh.Write
		if err := p.Store.PutShare(*sh); err != nil {
			return err
		}
		key := "notice.sharecanview"
		if sh.Write {
			key = "notice.sharecanedit"
		}
		ctx.PutNotice(fmt.Sprintf(ctx.T(key), sh.To, info.Name))
		return ctx.Redirect(http.StatusFound, pg.pageOf(ctx, info.Path)+"#sharing")
	}
}

// HandleUnshare ends one address's access.
func (pg Page) HandleUnshare(p *dav.Provider) func(*web.Context) error {
	return func(ctx *web.Context) error {
		info, ref, err := pg.owned(ctx, p)
		if err != nil {
			return err
		}
		to := ctx.FormValue("to")
		if err := p.Store.RemoveShare(ref, to); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		ctx.PutNotice(fmt.Sprintf(ctx.T("notice.unshared"), info.Name, to))
		return ctx.Redirect(http.StatusFound, pg.pageOf(ctx, info.Path))
	}
}

// HandleAnswer is the invited account's yes or no, at the path in its own home.
func (pg Page) HandleAnswer(p *dav.Provider, accept bool) func(*web.Context) error {
	return func(ctx *web.Context) error {
		collPath := ctx.Param("path")
		ref, home, here := p.Held(collPath)
		account := strings.ToLower(ctx.Login.Username())
		if !here || home != account {
			return web.NewError(http.StatusNotFound, "no such invitation")
		}
		if _, err := p.Store.Share(ref, account); err != nil {
			return web.NewError(http.StatusNotFound, "no such invitation")
		}
		c, err := p.Store.Collection(ref)
		if err != nil {
			return err
		}
		if err := p.Store.Answer(ref, account, accept); err != nil {
			return err
		}
		if !accept {
			ctx.PutNotice(fmt.Sprintf(ctx.T("notice.sharedeclined"), c.Name))
			return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.URL(pg.List)))
		}
		if err := pg.Show(ctx.Request().Context(), ctx.Login.Store(), dav.CanonicalCollectionPath(collPath)); err != nil {
			ctx.Notify(Refused(ctx, err))
			return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.URL(pg.List)))
		}
		// The name leads to the collection the account just accepted.
		ctx.Notify(web.Notice{Kind: web.NoticeDone,
			Text:   fmt.Sprintf(ctx.T("notice.shareaccepted"), c.Name),
			Markup: template.HTML(fmt.Sprintf(ctx.T("notice.shareaccepted"), web.NoticeLink(c.Name, pg.pageOf(ctx, dav.CanonicalCollectionPath(collPath)))))})
		return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.URL(pg.List)))
	}
}

// HandlePublish gives the calendar a public address or takes it away; republishing makes a new one.
func (pg Page) HandlePublish(p *dav.Provider, publish bool) func(*web.Context) error {
	return func(ctx *web.Context) error {
		info, ref, err := pg.owned(ctx, p)
		if err != nil {
			return err
		}
		if err := Publish(p.Store, ref, publish); err != nil {
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
