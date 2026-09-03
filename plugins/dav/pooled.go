package dav

import (
	"context"

	"git.mehdix.org/alborz"
)

// Account is one signed-in account's client and collections, for the
// pages that pool every account's.
type Account[C, I any] struct {
	Name        string
	Session     *alborz.Session
	Client      C
	Collections []I
}

// Group is one account's collections, for a form asking which one to
// write into.
type Group[I any] struct {
	Account     string
	Collections []I
}

// Pooled resolves every signed-in account that has the service. An
// account that fails is logged and skipped, so one flaky server does
// not take the page down; the error surfaces only when no account
// answered. own marks each collection with the account it belongs to,
// which a pooled page has to say; none is the answer when no account
// has the service at all.
func Pooled[C, I any](ctx *alborz.Context, p *Provider, load func(context.Context, *alborz.Session) (C, []I, error), own func(*I, string), none error) ([]Account[C, I], error) {
	var accounts []Account[C, I]
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
		owned := make([]I, len(infos))
		for i := range infos {
			owned[i] = infos[i]
			own(&owned[i], s.Username())
		}
		accounts = append(accounts, Account[C, I]{Name: s.Username(), Session: s, Client: c, Collections: owned})
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
func WritableGroups[C, I any](accounts []Account[C, I], keep func(I) bool) []Group[I] {
	var groups []Group[I]
	for _, acc := range accounts {
		var kept []I
		for _, c := range acc.Collections {
			if keep(c) {
				kept = append(kept, c)
			}
		}
		if len(kept) > 0 {
			groups = append(groups, Group[I]{Account: acc.Name, Collections: kept})
		}
	}
	return groups
}
