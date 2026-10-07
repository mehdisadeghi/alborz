package pim

import (
	"net/http"
	"slices"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim/dav"
	"github.com/emersion/go-webdav"
)

// Account is one signed-in account's client and collections.
type Account[C any] struct {
	Name        string
	Login       *alborz.Login
	Client      C
	Collections []dav.Collection
}

// WritableGroups keeps, per account, the collections keep accepts.
func WritableGroups[C any](accounts []Account[C], keep func(dav.Collection) bool) []dav.Group {
	var groups []dav.Group
	for _, acc := range accounts {
		var kept []dav.Collection
		for _, c := range acc.Collections {
			if keep(c) {
				kept = append(kept, c)
			}
		}
		if len(kept) > 0 {
			groups = append(groups, dav.Group{Account: acc.Name, Collections: kept})
		}
	}
	return groups
}

// Site is one collection to query, with its account's client.
type Site[C any] struct {
	Client     C
	Collection dav.Collection
}

// Account is the site's account, whose turn it takes.
func (s Site[C]) Account() string { return s.Collection.Account }

// Unread is what a page could not read of a collection: all of it, or one object.
type Unread struct {
	Collection string
	Err        error
}

// Present leaves out the answers of listed collections their server no longer has: deleted by another
// client, they are gone, not an error, and each one's account is listed afresh on the next page.
func Present[C, R any](p *dav.Provider, results []dav.Result[Site[C], R]) []dav.Result[Site[C], R] {
	return slices.DeleteFunc(results, func(r dav.Result[Site[C], R]) bool {
		code, _ := webdav.HTTPErrorCode(r.Err)
		if code == http.StatusNotFound {
			p.Forget(r.Site.Collection.Account)
		}
		return code == http.StatusNotFound
	})
}

// Visible returns every collection for the rail and the visible ones in scope as sites.
func Visible[C any](accounts []Account[C], scope string, only map[string]bool, chosen func(Account[C]) (colls []dav.Collection, filter bool, paths []string, err error)) ([]dav.Collection, []Site[C], error) {
	var infos []dav.Collection
	var sites []Site[C]
	for _, acc := range accounts {
		colls, filter, paths, err := chosen(acc)
		if err != nil {
			return nil, nil, err
		}
		visible := make(map[string]bool)
		for _, path := range paths {
			visible[dav.CanonicalCollectionPath(path)] = true
		}
		for _, coll := range colls {
			coll.Visible = !filter || visible[coll.Path]
			coll.Shown = coll.Visible
			if only != nil {
				coll.Shown = only[coll.Path]
				coll.Only = len(only) == 1 && coll.Shown
			}
			infos = append(infos, coll)
			if coll.Shown && (scope == "" || acc.Name == scope) {
				sites = append(sites, Site[C]{Client: acc.Client, Collection: coll})
			}
		}
	}
	return infos, sites, nil
}

// Ref is one object an action is about, with its account and that account's client.
type Ref[C any] struct {
	Client  C
	Account string
	Path    string
}

// Choose ticks, in the list of the visible, those of the collections the form showed; all were visible
// before the first choice.
func Choose(filter *bool, visible *[]string, all, shown, ticked []string) {
	if !*filter {
		*filter, *visible = true, all
	}
	unticked := slices.DeleteFunc(slices.Clone(shown), func(path string) bool { return slices.Contains(ticked, path) })
	*visible = alborz.Untick(*visible, shown, unticked)
}
