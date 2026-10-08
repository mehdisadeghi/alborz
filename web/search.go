package web

import (
	"bytes"
	"cmp"
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
	// Held is Search answered from what alborz holds, where Search asks a server; nil where it asks none.
	// Rows nil says the query asks what is not held.
	Held func(ctx *Context, q search.Query, view string, limit int) (Found, error)
}

// Found is what a section found: its first rows, how many there are in all, the section's own list
// of them, and the accounts whose servers did not answer. The rows are drawn with the page's globals.
type Found struct {
	Rows  RenderData
	Total int
	All   string
	Down  []string
	// Unasked are the query's keys with a value the section cannot read, so it searched nothing.
	Unasked []string
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
	// Remote says the section's search asks a server, and Held that these rows are what alborz holds
	// while it does.
	Remote, Held bool
	// Waiting says the section has not been asked yet.
	Waiting bool
}

// searchDialog is the header's search, which asks for its blocks one section at a time.
const searchDialog = "search-dialog"

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
	return ctx.AccountPath(QueryPath(ctx.Path(route), params))
}

// SearchRoutes serve the search across sections.
func SearchRoutes(g *Group) {
	g.Group("", "core").GET("search", "/search", handleSearch)
}

func handleSearch(ctx *Context) error {
	state := ReadListState(ctx)
	text, view := strings.TrimSpace(state.Query), state.View
	if !ValidStarView(view) {
		return NewError(http.StatusBadRequest, "no such view")
	}
	searchers := ctx.searchers()
	if section := ctx.QueryParam("section"); section != "" {
		searchers = slices.DeleteFunc(searchers, func(s Searcher) bool { return s.Kind != section })
		if len(searchers) == 0 {
			return NewError(http.StatusBadRequest, "no such section")
		}
	}
	held := ctx.QueryParam("held") != ""
	if held && (len(searchers) != 1 || searchers[0].Held == nil) {
		return NewError(http.StatusBadRequest, "only a section that asks a server holds rows of its own")
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
		if data.Blocks, err = ctx.searchSections(searchers, state.ParsedQuery(), view, held, data.GlobalData); err != nil {
			return err
		}
	}
	if ctx.PartialFor(searchDialog) {
		return ctx.Render(http.StatusOK, "search-answer", data)
	}
	return ctx.Render(http.StatusOK, "search", data)
}

// searchers are the sections in the order they are shown: those that ask a server last, so that what
// they bring moves nothing under it.
func (ctx *Context) searchers() []Searcher {
	return slices.SortedStableFunc(slices.Values(ctx.Server.Searchers), func(a, b Searcher) int {
		return cmp.Compare(remote(a), remote(b))
	})
}

func remote(s Searcher) int {
	if s.Held != nil {
		return 1
	}
	return 0
}

func (ctx *Context) waitingBlocks() []SearchBlock {
	var blocks []SearchBlock
	for _, s := range ctx.searchers() {
		blocks = append(blocks, SearchBlock{Kind: s.Kind, Label: ctx.T(s.Kind + ".search"), Remote: s.Held != nil, Waiting: true})
	}
	return blocks
}

// searchSections asks the sections that read the query's keys, all at once, and draws what each found;
// held asks them for what alborz holds.
func (ctx *Context) searchSections(searchers []Searcher, q search.Query, view string, held bool, global GlobalRenderData) ([]SearchBlock, error) {
	blocks := make([]SearchBlock, len(searchers))
	found := make([]Found, len(searchers))
	errs := make([]error, len(searchers))
	// What the context reads lazily is read here once, so the sections share it only to read.
	ctx.QueryParams()
	ctx.Logins()
	var wg sync.WaitGroup
	for i, s := range searchers {
		blocks[i] = SearchBlock{Kind: s.Kind, Label: ctx.T(s.Kind + ".search"), Remote: s.Held != nil, Held: held}
		for _, key := range q.Keys() {
			if !slices.Contains(s.Answers, key) {
				blocks[i].Unasked = append(blocks[i].Unasked, key)
			}
		}
		if len(blocks[i].Unasked) > 0 {
			continue
		}
		own, ask := *ctx, s.Search
		if held {
			ask = s.Held
		}
		wg.Go(func() { found[i], errs[i] = ask(&own, q, view, searchLimit) })
	}
	wg.Wait()
	var down []string
	for i := range blocks {
		if errs[i] != nil {
			return nil, errs[i]
		}
		blocks[i].Unasked = append(blocks[i].Unasked, found[i].Unasked...)
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
