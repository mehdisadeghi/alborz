package dav

import (
	"net/url"

	"git.mehdix.org/alborz"
)

// Item is one object as a list holds it: its path on the server, and
// where its own page is with the list carried along.
type Item struct {
	Path string
	URL  string
}

// Neighbours are the objects either side of the one on the page, in the
// order the list showed them. It is how a reader walks a collection
// without going back to the list, which the message page has always had
// and the calendar, the tasks and the contacts had not. A page not
// reached from a list, or an object no longer in it, gets a zero Total
// and shows nothing.
type Neighbours struct {
	PrevURL  string
	NextURL  string
	Position int
	Total    int
}

// Around places one object among the items a list holds, in that list's
// own order.
func Around(items []Item, path string) Neighbours {
	at := -1
	for i, it := range items {
		if it.Path == path {
			at = i
			break
		}
	}
	if at < 0 {
		return Neighbours{}
	}
	n := Neighbours{Position: at + 1, Total: len(items)}
	if at > 0 {
		n.PrevURL = items[at-1].URL
	}
	if at+1 < len(items) {
		n.NextURL = items[at+1].URL
	}
	return n
}

// ListParams are the parameters of the request that decide which
// objects a list holds and in what order. An object's page is opened
// with them, and so are the links to its neighbours, so that walking a
// list stays inside the list the reader was looking at.
func ListParams(ctx *alborz.Context, keys ...string) url.Values {
	return Keep(ctx.QueryParams(), keys...)
}

// ListParamsIn are the same, read from the URL a form returns to. A
// fragment answering a write has the list's own address only there.
func ListParamsIn(next string, keys ...string) url.Values {
	u, err := url.Parse(next)
	if err != nil {
		return url.Values{}
	}
	return Keep(u.Query(), keys...)
}

// Widened is next without the narrowing to single collections that
// field names: ticking a collection says which ones the reader wants,
// so the page they return to is no longer the one "only" narrowed.
func Widened(next, field string) string {
	u, err := url.Parse(next)
	if err != nil {
		return next
	}
	q := u.Query()
	q.Del(field)
	return ListURL(u.Path, q)
}

// ListURL is the list those parameters name, for an object's page to
// return to once the object is gone or a new one is made.
func ListURL(path string, params url.Values) string {
	if len(params) == 0 {
		return path
	}
	return path + "?" + alborz.AddressQuery(params)
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

// ObjectURL is where one object's page is: the list's own parameters,
// and the account holding the object where the list pools several.
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
		href += "?" + q.Encode()
	}
	return href
}

// Labels are what a pooled list's row says about the collection holding
// its object: the collection itself, the zero one when none holds it,
// and the list narrowed to it through field, in the scope the row
// belongs to. shape is what else the narrowed list's address says.
func Labels(ctx *alborz.Context, colls []Collection, list, field string, shape url.Values) (collection func(account, path string) Collection, href func(account, path string) string) {
	collection = func(account, path string) Collection {
		if c := Holding(colls, account, path); c != nil {
			return *c
		}
		return Collection{}
	}
	href = func(account, path string) string {
		c := Holding(colls, account, path)
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
		return list + "?" + q.Encode()
	}
	return collection, href
}
