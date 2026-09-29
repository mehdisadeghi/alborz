package pim

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

// ListParams are the request parameters that decide a list's objects and order.
func ListParams(ctx *web.Context, keys ...string) url.Values {
	return Keep(ctx.QueryParams(), keys...)
}

// ListParamsIn reads them from a form's return URL, the only place a write's fragment has them.
func ListParamsIn(next string, keys ...string) url.Values {
	u, err := url.Parse(next)
	if err != nil {
		return url.Values{}
	}
	return Keep(u.Query(), keys...)
}

// Widened drops next's "only" narrowing through field, since ticking a collection supersedes it.
func Widened(next, field string) string {
	u, err := url.Parse(next)
	if err != nil {
		return next
	}
	q := u.Query()
	q.Del(field)
	return ListURL(u.Path, q)
}

// ListURL is the list those parameters name.
func ListURL(path string, params url.Values) string {
	if len(params) == 0 {
		return path
	}
	return path + "?" + web.Query(params)
}

// Keep takes the named parameters, and only those, from a query.
func Keep(from url.Values, keys ...string) url.Values {
	kept := url.Values{}
	for _, key := range keys {
		for _, v := range from[key] {
			if v != "" {
				kept.Add(key, v)
			}
		}
	}
	return kept
}

// RowParams are a row's list parameters with the list its page returns to.
func RowParams(ctx *web.Context, path string, params url.Values) url.Values {
	from := ctx.From()
	if ctx.Request().URL.Path == path {
		from = ctx.Request().URL.RequestURI()
	}
	if from == "" {
		return params
	}
	q := url.Values{}
	for key, values := range params {
		q[key] = values
	}
	q.Set("from", from)
	return q
}

// ObjectURL is an object's page, with the list's parameters and, in a pooled list, its account.
func ObjectURL(base, path, account string, params url.Values) string {
	q := url.Values{}
	for key, values := range params {
		q[key] = values
	}
	if account != "" {
		q.Set("account", account)
	}
	href := base + url.PathEscape(path)
	if len(q) > 0 {
		href += "?" + web.Query(q)
	}
	return href
}

// Labels name the collection a pooled row lies in and the list narrowed to it through field.
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
		return list + "?" + web.Query(q)
	}
	return collection, href
}
