package web

import (
	"net/url"

	"git.mehdix.org/alborz/pim/dav"
	"git.mehdix.org/alborz/web"
)

// Item is one object as a list holds it: its server path and its page's URL.
type Item struct {
	Path string
	URL  string
}

// ItemsFor projects typed list rows into the ordered item identities used by
// object neighbours and shared list tooling.
func ItemsFor[T any](rows []T, item func(T) Item) []Item {
	items := make([]Item, len(rows))
	for i, row := range rows {
		items[i] = item(row)
	}
	return items
}

// Around places one object among a list's items, in its order.
func Around(items []Item, path string) web.Neighbours {
	at := -1
	for i, it := range items {
		if it.Path == path {
			at = i
			break
		}
	}
	if at < 0 {
		return web.Neighbours{}
	}
	n := web.Neighbours{Position: at + 1, Total: len(items)}
	if at > 0 {
		n.PrevURL = items[at-1].URL
	}
	if at+1 < len(items) {
		n.NextURL = items[at+1].URL
	}
	return n
}

// Widened drops next's "only" narrowing through field, since ticking a collection supersedes it.
func Widened(next, field string) string {
	u, err := url.Parse(next)
	if err != nil {
		return next
	}
	q := u.Query()
	q.Del(field)
	return web.QueryPath(u.Path, q)
}

// ObjectURL is an object's page, the route's, with the list's parameters and, in a pooled list, its account.
func ObjectURL(ctx *web.Context, route, path, account string, params url.Values) string {
	q := url.Values{}
	for key, values := range params {
		q[key] = values
	}
	if account != "" {
		q.Set("account", account)
	}
	href := ctx.Path(route, path)
	return web.QueryPath(href, q)
}

// Labels name the collection a pooled row lies in and the list, the route's, narrowed to it through field.
func Labels(ctx *web.Context, colls []dav.Collection, list, field string, shape url.Values) (collection func(account, path string) dav.Collection, href func(account, path string) string) {
	collection = func(account, path string) dav.Collection {
		if c := dav.Holding(colls, account, path); c != nil {
			return *c
		}
		return dav.Collection{}
	}
	href = func(account, path string) string {
		c := dav.Holding(colls, account, path)
		if c == nil {
			return ""
		}
		q := url.Values{field: {c.Path}}
		for key, values := range shape {
			q[key] = values
		}
		if account == "" {
			account = ctx.URLAccount()
		}
		if account != "" {
			q.Set("account", account)
		}
		return web.QueryPath(ctx.Path(list), q)
	}
	return collection, href
}
