package dav

import (
	"context"
	"sync"
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
