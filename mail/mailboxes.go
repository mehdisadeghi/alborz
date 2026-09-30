package mail

import (
	"sync"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/memo"
	"github.com/microcosm-cc/bluemonday"
)

type plugin struct {
	server          *alborz.Server
	listings        *listingCache
	bodies          *bodyCache
	accountSidebars *memo.Memo[Rail]
	unifiedFolders  *memo.Memo[map[string]string]
	senderBooks     *memo.Memo[*senderBook]
	authServGuesses *memo.Memo[string]
	wkd             *wkdCache
	// composed cleans what the editor wrote.
	composed *bluemonday.Policy
	watchers sync.Map
}

func newPlugin(s *alborz.Server) *plugin {
	o := s.Options
	bodies := newBodyCache(o.CacheFor(bodyKeepFor))
	sidebars := memo.NewBackground[Rail](o.CacheFor(listingFreshFor))
	p := &plugin{
		server:          s,
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
