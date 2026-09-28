package alborz

import (
	"net/url"
	"strconv"
)

// Filter is one narrowing in force on a listing, with the page without it.
type Filter struct {
	Label string
	Value string
	Href  string
}

// FilterRow is one entry of a list's filter menu; a row in force links to
// the page without it.
type FilterRow struct {
	Label   string
	Href    string
	Current bool
	// Star is the colour a row names.
	Star string
}

// WithoutParam returns this page with one query parameter and the page number dropped.
func (ctx *Context) WithoutParam(name string) string {
	return ctx.WithParam(name, "")
}

// WithParam returns this page with one query parameter set, or dropped when
// value is empty, and the page number dropped.
func (ctx *Context) WithParam(name, value string) string {
	u := *ctx.Request().URL
	q := u.Query()
	q.Del(name)
	if value != "" {
		q.Set(name, value)
	}
	q.Del("page")
	u.RawQuery = Query(q)
	u.Path = ctx.Request().URL.Path
	if u.RawQuery == "" {
		return u.Path
	}
	return u.Path + "?" + u.RawQuery
}

// FilterOn returns the filter for a set parameter, or false.
func (ctx *Context) FilterOn(name, label string) (Filter, bool) {
	value := ctx.QueryParam(name)
	if value == "" {
		return Filter{}, false
	}
	return Filter{Label: label, Value: value, Href: ctx.WithoutParam(name)}, true
}

// PageHref returns this page at page n; the first page carries no number.
func (ctx *Context) PageHref(n int) string {
	u := *ctx.Request().URL
	q := u.Query()
	q.Del("page")
	if n > 0 {
		q.Set("page", strconv.Itoa(n))
	}
	u.RawQuery = Query(q)
	if u.RawQuery == "" {
		return u.Path
	}
	return u.Path + "?" + u.RawQuery
}

// QueryValues returns the request's query.
func (ctx *Context) QueryValues() url.Values {
	return ctx.Request().URL.Query()
}
