package dav

import (
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim/store"
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

func (p *Provider) invitations(ctx *alborz.Context) ([]Invitation, error) {
	now := time.Now()
	var out []Invitation
	for _, s := range ctx.Logins() {
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
				Path: store.PathOf(account, *c), Write: sh.Write, Expires: sh.Expires,
			})
		}
	}
	return out, nil
}

// RailInvitations are the open invitations the section's rail offers, answered under base; nil for none.
func (p *Provider) RailInvitations(ctx *alborz.Context, base string) (*RailInvitations, error) {
	items, err := p.invitations(ctx)
	if err != nil || len(items) == 0 {
		return nil, err
	}
	return &RailInvitations{Base: base, Items: items}, nil
}

// Authors are who added and last changed an object kept here, omitted where that is the viewer.
type Authors struct {
	Creator, Editor string
}

// Authors of the object at path as viewer sees them; none for an object elsewhere.
func (p *Provider) Authors(objPath, viewer string) Authors {
	ref, name, ok := store.HeldObject(objPath)
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
	ref, _, ok := store.Held(collPath)
	if !ok {
		return false
	}
	c, err := p.Store.Collection(ref)
	return err == nil && c.Public != ""
}

// SharedWith counts the accounts a collection kept here is shared with or offered to, less refusals and ended shares.
func (p *Provider) SharedWith(collPath string) int {
	ref, home, ok := store.Held(collPath)
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
