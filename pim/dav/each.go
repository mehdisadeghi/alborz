package dav

import (
	"context"
	"sync"
)

// MaxConcurrency bounds one account's collections asked at once: serially costs a round trip each, all at once may be refused.
const MaxConcurrency = 4

// Result is one site's answer to a query run over many.
type Result[S, R any] struct {
	Site  S
	Value R
	Err   error
}

// Each runs query against every site of one account, MaxConcurrency at a time, answering in site order.
func Each[S, R any](ctx context.Context, sites []S, query func(context.Context, S) (R, error)) []Result[S, R] {
	return Across(ctx, sites, func(S) string { return "" }, query)
}

// Across is Each over the sites of many accounts: each account's MaxConcurrency at a time, the accounts together.
func Across[S, R any](ctx context.Context, sites []S, account func(S) string, query func(context.Context, S) (R, error)) []Result[S, R] {
	results := make([]Result[S, R], len(sites))
	turns := map[string]chan struct{}{}
	var wg sync.WaitGroup
	for i, site := range sites {
		turn := turns[account(site)]
		if turn == nil {
			turn = make(chan struct{}, MaxConcurrency)
			turns[account(site)] = turn
		}
		wg.Go(func() {
			turn <- struct{}{}
			defer func() { <-turn }()
			if err := ctx.Err(); err != nil {
				results[i] = Result[S, R]{Site: site, Err: err}
				return
			}
			v, err := query(ctx, site)
			results[i] = Result[S, R]{Site: site, Value: v, Err: err}
		})
	}
	wg.Wait()
	return results
}
