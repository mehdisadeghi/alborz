package alborzbase

import (
	"sync"

	"git.mehdix.org/alborz"
)

type plugin struct {
	alborz.Router
	viewers         []Viewer
	listings        *listingCache
	bodies          *bodyCache
	accountSidebars *alborz.Memo[sidebar]
	unifiedFolders  *alborz.Memo[map[string]string]
	correspondents  *alborz.Memo[[]string]
	senderBooks     *alborz.Memo[*senderBook]
	authServGuesses *alborz.Memo[string]
	wkd             *wkdCache
	watchers        sync.Map
}

func newPlugin(o *alborz.Options) *plugin {
	bodies := newBodyCache(o.CacheFor(bodyKeepFor))
	sidebars := alborz.NewBackgroundMemo[sidebar](o.CacheFor(listingFreshFor))
	p := &plugin{
		listings:        newListingCache(o.CacheFor(listingKeepFor), sidebars, bodies),
		bodies:          bodies,
		accountSidebars: sidebars,
		unifiedFolders:  alborz.NewMemo[map[string]string](o.CacheFor(unifiedFoldersTTL)),
		// Indexes, not cached answers: they stay with caching off.
		correspondents:  alborz.NewMemo[[]string](correspondentTTL),
		senderBooks:     alborz.NewMemo[*senderBook](senderBookTTL),
		authServGuesses: alborz.NewMemo[string](authSampleTTL),
		wkd:             newWKDCache(o.CacheFor(wkdKeepFor)),
	}
	return p
}

// Mount serves mail, showing a message part with the first of viewers that takes it.
func Mount(s *alborz.Server, viewers ...Viewer) {
	p := newPlugin(s.Options)
	p.Router, p.viewers = s.Router(), viewers
	p.registerRoutes()
	// A restored account skips the login handler, so it is followed here too.
	s.OnAccountReady = append(s.OnAccountReady, p.watchAccount, p.warmAccount)
	s.OnAccountGone = append(s.OnAccountGone, p.forgetAccount)
}
