package dav

import (
	"context"
	"sync"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail"
)

// MaxConcurrency bounds collections asked at once: serially costs a round trip each, all at once may be refused.
const MaxConcurrency = 4

// Result is one site's answer to a query run over many.
type Result[S, R any] struct {
	Site  S
	Value R
	Err   error
}

// Each runs query against every site, MaxConcurrency at a time, answering in site order.
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

// Pager is one page of a list held whole, since a DAV query cannot ask for rows after the first n.
type Pager struct {
	From, To, Total int
	Prev, Next      string

	Per     int
	Options []int
	IppBase string
}

// Paginate returns the requested page of items and the pager for the rest.
func Paginate[T any](ctx *alborz.Context, items []T) ([]T, Pager) {
	per := mail.PerPage(ctx)
	page, err := alborz.ReadInt(ctx.QueryParam("page"))
	if err != nil || page < 0 {
		page = 0
	}
	// A page past the end is the last one: the list may have shrunk since the link was drawn.
	if last := max(0, (len(items)-1)/per); page > last {
		page = last
	}
	from, to := page*per, min((page+1)*per, len(items))
	q := ctx.Request().URL.Query()
	q.Del("ipp")
	q.Del("page")
	base := ""
	if len(q) > 0 {
		base = "&" + alborz.Query(q)
	}
	pager := Pager{From: from + 1, To: to, Total: len(items),
		Per: per, Options: mail.PerPageChoices(ctx), IppBase: base}
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
