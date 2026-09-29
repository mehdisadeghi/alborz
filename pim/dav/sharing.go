package dav

import (
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim/store"
	"git.mehdix.org/alborz/web"
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

// RailInvitations are a section's open invitations and the route of the collection page their answers post under.
type RailInvitations struct {
	Collection string
	Items      []Invitation
}

// Invitations are the logins' open invitations to collections of the kind.
func (p *Provider) Invitations(logins []*alborz.Login) ([]Invitation, error) {
	now := time.Now()
	var out []Invitation
	for _, s := range logins {
		account := strings.ToLower(s.Username())
		shares, err := p.Store.Shares(func(sh store.Share) bool {
			return sh.To == account && !sh.Accepted && !sh.Declined && !sh.Left && (sh.Expires.IsZero() || now.Before(sh.Expires))
		})
		if err != nil {
			return nil, err
		}
		for _, sh := range shares {
			c, err := p.Store.Collection(sh.Ref())
			if err != nil {
				return nil, err
			}
			if c.Kind != p.Kind.Holds {
				continue
			}
			out = append(out, Invitation{
				Account: s.Username(), Name: c.Name, From: c.Owner,
				Path: p.PathOf(account, *c), Write: sh.Write, Expires: sh.Expires,
			})
		}
	}
	return out, nil
}

// RailInvitations are the open invitations the section's rail offers, answered under the collection route; nil for none.
func (p *Provider) RailInvitations(ctx *web.Context, collection string) (*RailInvitations, error) {
	items, err := p.Invitations(ctx.Logins())
	if err != nil || len(items) == 0 {
		return nil, err
	}
	return &RailInvitations{Collection: collection, Items: items}, nil
}

// Held reads a path on this alborz as the store holds it: the collection, and the home it is reached through.
func (p *Provider) Held(path string) (store.Ref, string, bool) { return p.here.held(path) }

// HeldObject reads an object's path on this alborz as its collection and name.
func (p *Provider) HeldObject(path string) (store.Ref, string, bool) {
	rest, ok := strings.CutPrefix(path, marker(p.here.URL.Host))
	if !ok {
		return store.Ref{}, "", false
	}
	return store.HeldObject(rest)
}

// PathOf is a collection kept here as viewer reaches it.
func (p *Provider) PathOf(viewer string, c store.Collection) string {
	return p.here.mark(store.PathOf(viewer, c))
}

// Authors are who added and last changed an object kept here, omitted where that is the viewer.
type Authors struct {
	Creator, Editor string
}

// Authors of the object at path as viewer sees them; none for an object elsewhere.
func (p *Provider) Authors(objPath, viewer string) Authors {
	ref, name, ok := p.HeldObject(objPath)
	if !ok {
		return Authors{}
	}
	o, err := p.Store.Object(ref, name)
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
	ref, _, ok := p.Held(collPath)
	if !ok {
		return false
	}
	c, err := p.Store.Collection(ref)
	return err == nil && c.Public != ""
}

// Feed is the address a calendar kept here is filled from, empty for one that is written to.
func (p *Provider) Feed(collPath string) string {
	ref, _, ok := p.Held(collPath)
	if !ok {
		return ""
	}
	c, err := p.Store.Collection(ref)
	if err != nil || c.Feed == nil {
		return ""
	}
	return c.Feed.URL
}

// SharedWith counts the accounts a collection kept here is shared with or offered to, less refusals and ended shares.
func (p *Provider) SharedWith(collPath string) int {
	ref, home, ok := p.Held(collPath)
	if !ok || ref.Owner != home {
		return 0
	}
	shares, err := p.Store.Shares(func(sh store.Share) bool {
		return sh.Ref() == ref && !sh.Declined && !sh.Left && !sh.Ended()
	})
	if err != nil {
		return 0
	}
	return len(shares)
}
