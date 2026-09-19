package dav

import (
	"context"
	"sync"

	"git.mehdix.org/alborz"
	alborzbase "git.mehdix.org/alborz/plugins/base"
)

// MaxConcurrency is how many collections are asked at once. A page
// asks one query per collection: one after another would cost a round
// trip each, and all at once is a burst a shared server may refuse.
const MaxConcurrency = 4

// Result is one site's answer to a query run over many.
type Result[S, R any] struct {
	Site  S
	Value R
	Err   error
}

// Each runs query against every site, MaxConcurrency at a time, and
// returns the answers in site order.
func Each[S, R any](ctx context.Context, sites []S, query func(context.Context, S) (R, error)) []Result[S, R] {
	results := make([]Result[S, R], len(sites))
	var wg sync.WaitGroup
	jobs := make(chan int)
	for range min(MaxConcurrency, len(sites)) {
		wg.Go(func() {
			for i := range jobs {
				if err := ctx.Err(); err != nil {
					results[i] = Result[S, R]{Site: sites[i], Err: err}
					continue
				}
				v, err := query(ctx, sites[i])
				results[i] = Result[S, R]{Site: sites[i], Value: v, Err: err}
			}
		})
	}
	for i := range sites {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return results
}

// Pager is one page of a list that alborz holds whole: a DAV query has
// no way to ask for the rows after the first n, so the list is read,
// narrowed and sorted as it always was, and only a page of it is drawn.
// Prev and Next are empty where there is no page that way.
type Pager struct {
	From, To, Total int
	Prev, Next      string
	// Per is the page size in force and Options the sizes the list's
	// menu offers; IppBase is the rest of the address a size keeps.
	Per     int
	Options []int
	IppBase string
}

// Paginate is the page of items the request asks for, as many as the
// reader reads by, and the pager that walks the rest.
func Paginate[T any](ctx *alborz.Context, items []T) ([]T, Pager) {
	per := alborzbase.PerPage(ctx)
	page, err := alborz.ReadInt(ctx.QueryParam("page"))
	if err != nil || page < 0 {
		page = 0
	}
	// A page past the end is the last one: the list got shorter since
	// the link was drawn, and an empty page says nothing true.
	if last := max(0, (len(items)-1)/per); page > last {
		page = last
	}
	from, to := page*per, min((page+1)*per, len(items))
	// Another size starts from the first page, and keeps the rest of
	// what the list was asked.
	q := ctx.Request().URL.Query()
	q.Del("ipp")
	q.Del("page")
	base := ""
	if len(q) > 0 {
		base = "&" + alborz.AddressQuery(q)
	}
	pager := Pager{From: from + 1, To: to, Total: len(items),
		Per: per, Options: alborzbase.PerPageChoices(ctx), IppBase: base}
	if len(items) == 0 {
		pager.From = 0
	}
	if page > 0 {
		pager.Prev = ctx.PageHref(page - 1)
	}
	if to < len(items) {
		pager.Next = ctx.PageHref(page + 1)
	}
	return items[from:to], pager
}
