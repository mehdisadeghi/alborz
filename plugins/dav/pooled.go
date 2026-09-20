package dav

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"github.com/labstack/echo/v4"
)

// Discovery across an account may take several exchanges; bound the whole load.
const accountLoadBudget = 30 * time.Second

// Account is one signed-in account's client and collections, for the
// pages that pool every account's.
type Account[C any] struct {
	Name        string
	Session     *alborz.Session
	Client      C
	Collections []Collection
}

// Group is one account's collections, for a form asking which one to
// write into.
type Group struct {
	Account     string
	Collections []Collection
}

// Pooled resolves every signed-in account that has the service. An
// account that fails is logged and skipped, so one flaky server does
// not take the page down; the error surfaces only when no account
// answered. Each collection is marked with the account it belongs to,
// which a pooled page has to say; none is the answer when no account
// has the service at all.
func Pooled[C any](ctx *alborz.Context, p *Provider, load func(context.Context, *alborz.Session) (C, []Collection, error), none error) ([]Account[C], error) {
	var accounts []Account[C]
	var lastErr error
	var down, refused []string
	// answered are the servers that said no in some other way than to
	// the password, with what they said.
	answered := map[string]int{}
	fail := func(username string, err error) {
		if code := Answered(err); code != 0 {
			answered[username] = code
			return
		}
		if !slices.Contains(down, username) {
			down = append(down, username)
		}
	}
	// A refusal stands until the reader changes something, so it is
	// said once to a browser, not on every page of the section.
	refuse := func(username string) {
		if ctx.Visit().Once("davrefused:" + p.kind.Name + ":" + username) {
			refused = append(refused, username)
		}
	}
	var sessions []*alborz.Session
	for _, s := range ctx.Sessions() {
		if _, ok := p.URL(s); ok {
			sessions = append(sessions, s)
		}
	}
	results := Each(ctx.Request().Context(), sessions, func(reqCtx context.Context, s *alborz.Session) (Account[C], error) {
		reqCtx, cancel := context.WithTimeout(reqCtx, accountLoadBudget)
		defer cancel()
		c, infos, err := load(reqCtx, s)
		return Account[C]{Client: c, Collections: infos}, err
	})
	for _, result := range results {
		s, err := result.Site, result.Err
		c, infos := result.Value.Client, result.Value.Collections
		// An account with nothing of the kind is not one whose server
		// failed: it keeps its place with nothing in it, so a reader
		// can see the account is there and make the first one.
		if errors.Is(err, none) {
			accounts = append(accounts, Account[C]{Name: s.Username(), Session: s, Client: c})
			continue
		}
		// A refused password is an answer the reader can act on, not a
		// failed page: the account keeps its place, empty, and is told.
		var no *RefusedError
		if errors.As(err, &no) {
			accounts = append(accounts, Account[C]{Name: s.Username(), Session: s, Client: c})
			refuse(s.Username())
			continue
		}
		if err != nil {
			lastErr = err
			fail(s.Username(), err)
			ctx.Logger().Printf("%s: skipping %q in the pooled view: %v", p.kind.Name, s.Username(), err)
			continue
		}
		owned := slices.Clone(infos)
		for i := range owned {
			owned[i].Account = s.Username()
		}
		accounts = append(accounts, Account[C]{Name: s.Username(), Session: s, Client: c, Collections: owned})
		for _, trouble := range p.Troubles(s.Username()) {
			if errors.As(trouble, &no) {
				refuse(s.Username())
			} else {
				fail(s.Username(), trouble)
			}
		}
	}
	if len(accounts) == 0 {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, none
	}
	ctx.Unreachable(down)
	for _, username := range slices.Sorted(maps.Keys(answered)) {
		code := answered[username]
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeWarning,
			Text: fmt.Sprintf(ctx.T("notice.davanswered"), username, fmt.Sprintf("%d %s", code, http.StatusText(code)))})
	}
	// A refusal is the one the reader can end, so it is the one said
	// when both happened, with the way to the account's own settings.
	if len(refused) > 0 {
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeWarning,
			Text:   ctx.Tf("notice.davrefused", len(refused), strings.Join(refused, ", ")),
			Action: &alborz.NoticeAction{Label: ctx.T("settings.account"), Path: "/settings/account?account=" + alborz.AddressParam(refused[0])}})
	}
	return accounts, nil
}

// WritableGroups keeps, per account, the collections keep accepts.
func WritableGroups[C any](accounts []Account[C], keep func(Collection) bool) []Group {
	var groups []Group
	for _, acc := range accounts {
		var kept []Collection
		for _, c := range acc.Collections {
			if keep(c) {
				kept = append(kept, c)
			}
		}
		if len(kept) > 0 {
			groups = append(groups, Group{Account: acc.Name, Collections: kept})
		}
	}
	return groups
}

// Site is one collection to query, with its account's client.
type Site[C any] struct {
	Client     C
	Collection Collection
}

// Only is the set a URL asks to see through field, which stands in for
// the stored visibility for that request alone. The URL carries the
// question and stored state carries the preference, so looking at one
// collection is a link rather than a setting somebody has to put back.
func Only(ctx *alborz.Context, field string) map[string]bool {
	values := ctx.QueryParams()[field]
	if len(values) == 0 {
		return nil
	}
	only := make(map[string]bool, len(values))
	for _, v := range values {
		only[CanonicalCollectionPath(v)] = true
	}
	return only
}

// Visible marks each account's collections with the account's own
// visibility setting, or with the URL's narrowing when it names one.
// Every collection comes back for the rail with its checkbox state,
// whatever the scope: the rail is the reader's map of every account.
// Only the visible ones in scope come back as sites to query, and never
// a feed, which is read from its address and not queried on a server.
// chosen is the kind's answer for one account: the collections the list
// is about, whether the account chose among them, and which.
func Visible[C any](accounts []Account[C], scope string, only map[string]bool, chosen func(Account[C]) (colls []Collection, filter bool, paths []string, err error)) ([]Collection, []Site[C], error) {
	var infos []Collection
	var sites []Site[C]
	for _, acc := range accounts {
		colls, filter, paths, err := chosen(acc)
		if err != nil {
			return nil, nil, err
		}
		visible := make(map[string]bool)
		for _, path := range paths {
			visible[CanonicalCollectionPath(path)] = true
		}
		for _, coll := range colls {
			coll.Visible = !filter || visible[coll.Path]
			coll.Shown = coll.Visible
			if only != nil {
				coll.Shown = only[coll.Path]
				coll.Only = len(only) == 1 && coll.Shown
			}
			infos = append(infos, coll)
			if coll.Shown && coll.Address == "" && (scope == "" || acc.Name == scope) {
				sites = append(sites, Site[C]{Client: acc.Client, Collection: coll})
			}
		}
	}
	return infos, sites, nil
}

// Ref is one object an action is about, with the account that owns it
// and that account's client.
type Ref[C any] struct {
	Client  C
	Account string
	Path    string
}

// Selected resolves a pooled list's checked rows, "account|path" each,
// to the client that owns them, opened once per account: a selection
// can span accounts, and each row names its own.
func Selected[C any](ctx *alborz.Context, refs []string, client func(*alborz.Session) (C, error)) ([]Ref[C], error) {
	clients := map[string]C{}
	out := make([]Ref[C], 0, len(refs))
	for _, ref := range refs {
		account, objPath, ok := strings.Cut(ref, "|")
		if !ok {
			return nil, echo.NewHTTPError(http.StatusBadRequest, "unqualified selection")
		}
		c, ok := clients[account]
		if !ok {
			session := ctx.SessionFor(account)
			if session == nil {
				return nil, echo.NewHTTPError(http.StatusBadRequest, "not signed in to that account")
			}
			var err error
			if c, err = client(session); err != nil {
				return nil, err
			}
			clients[account] = c
		}
		out = append(out, Ref[C]{c, account, objPath})
	}
	return out, nil
}
