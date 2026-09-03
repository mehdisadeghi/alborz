package dav

import (
	"context"
	"slices"

	"git.mehdix.org/alborz"
)

// Account is one signed-in account's client and collections, for the
// pages that pool every account's.
type Account[C any] struct {
	Name        string
	Session     *alborz.Session
	Client      C
	Collections []Collection
}

// Group is one account's collections, for a form asking which one to
// write into.
type Group struct {
	Account     string
	Collections []Collection
}

// Pooled resolves every signed-in account that has the service. An
// account that fails is logged and skipped, so one flaky server does
// not take the page down; the error surfaces only when no account
// answered. Each collection is marked with the account it belongs to,
// which a pooled page has to say; none is the answer when no account
// has the service at all.
func Pooled[C any](ctx *alborz.Context, p *Provider, load func(context.Context, *alborz.Session) (C, []Collection, error), none error) ([]Account[C], error) {
	var accounts []Account[C]
	var lastErr error
	for _, s := range ctx.Sessions() {
		if _, ok := p.URL(s); !ok {
			continue
		}
		c, infos, err := load(ctx.Request().Context(), s)
		if err != nil {
			lastErr = err
			ctx.Logger().Printf("%s: skipping %q in the pooled view: %v", p.kind.Name, s.Username(), err)
			continue
		}
		owned := slices.Clone(infos)
		for i := range owned {
			owned[i].Account = s.Username()
		}
		accounts = append(accounts, Account[C]{Name: s.Username(), Session: s, Client: c, Collections: owned})
	}
	if len(accounts) == 0 {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, none
	}
	return accounts, nil
}

// WritableGroups keeps, per account, the collections keep accepts.
func WritableGroups[C any](accounts []Account[C], keep func(Collection) bool) []Group {
	var groups []Group
	for _, acc := range accounts {
		var kept []Collection
		for _, c := range acc.Collections {
			if keep(c) {
				kept = append(kept, c)
			}
		}
		if len(kept) > 0 {
			groups = append(groups, Group{Account: acc.Name, Collections: kept})
		}
	}
	return groups
}

// Site is one collection to query, with its account's client.
type Site[C any] struct {
	Client     C
	Collection Collection
}

// Only is the set a URL asks to see through field, which stands in for
// the stored visibility for that request alone. The URL carries the
// question and stored state carries the preference, so looking at one
// collection is a link rather than a setting somebody has to put back.
func Only(ctx *alborz.Context, field string) map[string]bool {
	values := ctx.QueryParams()[field]
	if len(values) == 0 {
		return nil
	}
	only := make(map[string]bool, len(values))
	for _, v := range values {
		only[CanonicalCollectionPath(v)] = true
	}
	return only
}

// Visible marks each account's collections with the account's own
// visibility setting, or with the URL's narrowing when it names one.
// Every collection comes back for the rail with its checkbox state;
// only the visible ones come back as sites to query.
// chosen is the kind's answer for one account: the collections the list
// is about, whether the account chose among them, and which.
func Visible[C any](accounts []Account[C], only map[string]bool, chosen func(Account[C]) (colls []Collection, filter bool, paths []string, err error)) ([]Collection, []Site[C], error) {
	var infos []Collection
	var sites []Site[C]
	for _, acc := range accounts {
		colls, filter, paths, err := chosen(acc)
		if err != nil {
			return nil, nil, err
		}
		visible := make(map[string]bool)
		for _, path := range paths {
			visible[CanonicalCollectionPath(path)] = true
		}
		for _, coll := range colls {
			coll.Visible = !filter || visible[coll.Path]
			if only != nil {
				coll.Visible = only[coll.Path]
				coll.Only = len(only) == 1 && coll.Visible
			}
			infos = append(infos, coll)
			if coll.Visible {
				sites = append(sites, Site[C]{Client: acc.Client, Collection: coll})
			}
		}
	}
	return infos, sites, nil
}

// Ref is one object an action is about, with the account that owns it
// and that account's client.
type Ref[C any] struct {
	Client  C
	Account string
	Path    string
}
