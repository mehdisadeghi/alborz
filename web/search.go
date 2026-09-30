package web

import (
	"bytes"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/search"
)

// Searcher is a section's part in the search across sections.
type Searcher struct {
	// Kind names the section; its heading is the locale key <Kind>.search.
	Kind string
	// Answers are the keys the section reads; a query naming another is not asked of it.
	Answers []string
	// Rows is the partial that draws Found.Rows.
	Rows string
	// Search finds the first limit rows the query and the view find, view being a star or empty.
	Search func(ctx *Context, q search.Query, view string, limit int) (Found, error)
}

// Found is what a section found: its first rows, how many there are in all, the section's own list
// of them, and the accounts whose servers did not answer. The rows are drawn with the page's globals.
type Found struct {
	Rows  RenderData
	Total int
	All   string
	Down  []string
}

// searchLimit is how many rows each section shows: enough to spot what was meant, the rest are on
// the section's own list.
const searchLimit = 5

// SearchBlock is one section on the search page.
type SearchBlock struct {
	Kind, Label string
	// Unasked are the query's keys the section cannot read, so it was not searched.
	Unasked []string
	Rows    template.HTML
	Total   int
	All     string
}

type searchRenderData struct {
	BaseRenderData
	Query, View string
	Filters     []Filter
	FilterRows  []FilterRow
	Blocks      []SearchBlock
}

// SearchURL is a section's own list, the route's, narrowed by a query and a view in the request's account.
func (ctx *Context) SearchURL(route string, q search.Query, view string) string {
	params := url.Values{}
	if len(q) > 0 {
		params.Set("query", q.String())
	}
	if view != "" {
		params.Set("view", view)
	}
	return ctx.AccountPath(ctx.Path(route) + "?" + Query(params))
}

// SearchRoutes serve the search across sections.
func SearchRoutes(g *Group) {
	g.Group("", "core").GET("search", "/search", handleSearch)
}

func handleSearch(ctx *Context) error {
	text := strings.TrimSpace(ctx.QueryParam("query"))
	view := ctx.QueryParam("view")
	if view != "" && view != alborz.ViewStarred && !slices.Contains(alborz.FlagColors[:], view) {
		return NewError(http.StatusBadRequest, "no such view")
	}
	data := &searchRenderData{
		BaseRenderData: *NewBaseRenderData(ctx).WithTitle(ctx.T("search.title")),
		Query:          text,
		View:           view,
		FilterRows:     ViewRows(ctx, view, false, ctx.WithoutParam("view")),
	}
	if f, ok := ctx.FilterOn("query", ctx.T("filter.search")); ok {
		data.Filters = append(data.Filters, f)
	}
	if f, ok := ctx.FilterOn("view", ctx.T("filter.color")); ok {
		f.Value = ctx.T("color." + view)
		if view == alborz.ViewStarred {
			f.Value = ctx.T("mailbox.starred")
		}
		data.Filters = append(data.Filters, f)
	}
	if text != "" || view != "" {
		var err error
		if data.Blocks, err = ctx.searchSections(search.Parse(text), view, data.GlobalData); err != nil {
			return err
		}
	}
	return ctx.Render(http.StatusOK, "search", data)
}

// searchSections asks every section that reads the query's keys, all at once, and draws what each found.
func (ctx *Context) searchSections(q search.Query, view string, global GlobalRenderData) ([]SearchBlock, error) {
	searchers := ctx.Server.Searchers
	blocks := make([]SearchBlock, len(searchers))
	found := make([]Found, len(searchers))
	errs := make([]error, len(searchers))
	// What the context reads lazily is read here once, so the sections share it only to read.
	ctx.QueryParams()
	ctx.Logins()
	var wg sync.WaitGroup
	for i, s := range searchers {
		blocks[i] = SearchBlock{Kind: s.Kind, Label: ctx.T(s.Kind + ".search")}
		for _, key := range q.Keys() {
			if !slices.Contains(s.Answers, key) {
				blocks[i].Unasked = append(blocks[i].Unasked, key)
			}
		}
		if len(blocks[i].Unasked) > 0 {
			continue
		}
		own := *ctx
		wg.Go(func() { found[i], errs[i] = s.Search(&own, q, view, searchLimit) })
	}
	wg.Wait()
	var down []string
	for i := range blocks {
		if errs[i] != nil {
			return nil, errs[i]
		}
		if found[i].Rows == nil {
			continue
		}
		down = append(down, found[i].Down...)
		*found[i].Rows.Global() = global
		var b bytes.Buffer
		if err := ctx.Server.renderer().partials.ExecuteTemplate(&b, searchers[i].Rows, found[i].Rows); err != nil {
			return nil, err
		}
		blocks[i].Rows = template.HTML(b.String())
		blocks[i].Total, blocks[i].All = found[i].Total, found[i].All
	}
	slices.Sort(down)
	ctx.Unreachable(slices.Compact(down))
	return blocks, nil
}
