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
	sem := make(chan struct{}, MaxConcurrency)
	for i, site := range sites {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			v, err := query(ctx, site)
			results[i] = Result[S, R]{Site: site, Value: v, Err: err}
		}()
	}
	wg.Wait()
	return results
}
