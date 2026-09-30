package mail

import (
	"sync"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/memo"
)

// Mailboxes is mail read and written for the accounts signed in: what it keeps of their lists and
// bodies, and the watchers that keep it in step.
type Mailboxes struct {
	server          *alborz.Server
	listings        *listingCache
	bodies          *bodyCache
	accountSidebars *memo.Memo[Rail]
	unifiedFolders  *memo.Memo[map[string]string]
	senderBooks     *memo.Memo[*senderBook]
	authServGuesses *memo.Memo[string]
	wkd             *wkdCache
	watchers        sync.Map
}

// New keeps mail for the server, for as long as its options say.
func New(s *alborz.Server) *Mailboxes {
	o := s.Options
	bodies := newBodyCache(o.CacheFor(bodyKeepFor))
	sidebars := memo.NewBackground[Rail](o.CacheFor(listingFreshFor))
	p := &Mailboxes{
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
