package mail

import (
	"sync"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/memo"
)

type plugin struct {
	alborz.Router
	listings        *listingCache
	bodies          *bodyCache
	accountSidebars *memo.Memo[sidebar]
	unifiedFolders  *memo.Memo[map[string]string]
	senderBooks     *memo.Memo[*senderBook]
	authServGuesses *memo.Memo[string]
	wkd             *wkdCache
	watchers        sync.Map
}

func newPlugin(o *alborz.Options) *plugin {
	bodies := newBodyCache(o.CacheFor(bodyKeepFor))
	sidebars := memo.NewBackground[sidebar](o.CacheFor(listingFreshFor))
	p := &plugin{
		listings:        newListingCache(o.CacheFor(listingKeepFor), sidebars, bodies),
		bodies:          bodies,
		accountSidebars: sidebars,
		unifiedFolders:  memo.New[map[string]string](o.CacheFor(unifiedFoldersTTL)),
		// Indexes, not cached answers: they stay with caching off.
		senderBooks:     memo.New[*senderBook](senderBookTTL),
		authServGuesses: memo.New[string](authSampleTTL),
		wkd:             newWKDCache(o.CacheFor(wkdKeepFor)),
	}
	return p
}

// Mount serves mail.
func Mount(s *alborz.Server) {
	p := newPlugin(s.Options)
	p.Router = s.Router()
	p.registerRoutes()
	// A restored account skips the login handler, so it is followed here too.
	s.OnAccountReady = append(s.OnAccountReady, p.watchAccount, p.warmAccount)
	s.OnAccountGone = append(s.OnAccountGone, p.forgetAccount)
}
