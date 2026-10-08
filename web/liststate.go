package web

import (
	"net/http"
	"net/url"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/locale"
	"git.mehdix.org/alborz/search"
)

// ListState is the request state shared by searchable and sortable lists.
// Query is kept verbatim for display and links.
type ListState struct {
	Query, View, Sort, Direction string
	PageNumber, PerPageValue     string
	Path, Source, ReturnTo       string
	Params                       url.Values
}

// ReadListState projects common list parameters once, before a section applies
// its own view validation, sort columns, page size, and row projection.
func ReadListState(ctx *Context) ListState {
	state := ListStateFrom(ctx.QueryParams())
	state.Path = ctx.Request().URL.Path
	state.Source = ctx.Request().URL.RequestURI()
	state.ReturnTo = ctx.From()
	return state
}

// ListStateFrom projects common list parameters from an address such as a form's return URL.
func ListStateFrom(q url.Values) ListState {
	query := q.Get("query")
	return ListState{
		Query: query,
		View:  q.Get("view"), Sort: q.Get("sort"), Direction: q.Get("dir"),
		PageNumber: q.Get("page"), PerPageValue: q.Get("ipp"), Params: q.Clone(),
	}
}

// ParsedQuery parses the shared search syntax without changing the displayed query.
func (s ListState) ParsedQuery() search.Query {
	return search.Parse(strings.TrimSpace(s.Query))
}

// Page reads pagination from this list address using the reader's page-size setting.
func (s ListState) Page(ctx *Context) (alborz.Page, error) {
	p := alborz.Page{Size: s.PageSize(ctx)}
	if s.PageNumber != "" {
		n, err := locale.ReadInt(s.PageNumber)
		if err != nil || n < 0 {
			return p, NewError(http.StatusBadRequest, "invalid page index")
		}
		p.Number = n
	}
	return p, nil
}

// PageSize resolves this address's page size or the reader's configured default.
func (s ListState) PageSize(ctx *Context) int { return perPageValue(ctx, s.PerPageValue) }

// Keep returns only the named list parameters, retaining repeated values.
func (s ListState) Keep(keys ...string) url.Values { return KeepParams(s.Params, keys...) }

// Rows adds the source address that rows use for pagination and return links.
func (s ListState) Rows(path string, params url.Values) url.Values {
	from := s.ReturnTo
	if s.Path == path {
		from = s.Source
	}
	if from == "" {
		return params
	}
	q := params.Clone()
	q.Set("from", from)
	return q
}

// KeepParams takes the named parameters, and only those, from a query.
func KeepParams(from url.Values, keys ...string) url.Values {
	kept := url.Values{}
	for _, key := range keys {
		for _, value := range from[key] {
			if value != "" {
				kept.Add(key, value)
			}
		}
	}
	return kept
}
