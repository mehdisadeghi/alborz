package alborz

import (
	"net/url"
)

// Filter is one narrowing in force on a listing: what is narrowed, the
// value it was narrowed to, and the same page without it. A list that
// is short for a reason says the reason, which is what a reader coming
// back to a filtered page needs and what no rail can tell them.
type Filter struct {
	Label string
	Value string
	Href  string
}

// FilterRow is one entry of a list's filter menu: a narrowing the list
// offers, the page with it, and whether it is in force. A row in force
// links to the page without it, so the same row is the way out.
type FilterRow struct {
	Label   string
	Href    string
	Current bool
	// Star is the colour a row names, drawn as its glyph.
	Star string
}

// WithoutParam is this page again with one query parameter dropped. The
// page number goes with it: a page count is about a listing, and the
// listing is not the same one once a filter is gone.
func (ctx *Context) WithoutParam(name string) string {
	return ctx.WithParam(name, "")
}

// WithParam is this page again with one query parameter set, or dropped
// when the value is empty; the page number goes either way.
func (ctx *Context) WithParam(name, value string) string {
	u := *ctx.Request().URL
	q := u.Query()
	q.Del(name)
	if value != "" {
		q.Set(name, value)
	}
	q.Del("page")
	u.RawQuery = AddressQuery(q)
	u.Path = ctx.Request().URL.Path
	if u.RawQuery == "" {
		return u.Path
	}
	return u.Path + "?" + u.RawQuery
}

// FilterOn returns the filter for a parameter that is set, and false
// when it is not, so a handler names its own filters and nothing else
// has to know which parameters a page understands.
func (ctx *Context) FilterOn(name, label string) (Filter, bool) {
	value := ctx.QueryParam(name)
	if value == "" {
		return Filter{}, false
	}
	return Filter{Label: label, Value: value, Href: ctx.WithoutParam(name)}, true
}

// QueryValues is the request's query, for a handler building a filter
// whose value is not the parameter as typed.
func (ctx *Context) QueryValues() url.Values {
	return ctx.Request().URL.Query()
}
