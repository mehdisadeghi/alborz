package pim

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
	"git.mehdix.org/alborz/pim/dav"
	"git.mehdix.org/alborz/settings"
	"github.com/labstack/echo/v4"
)

// Discovery across an account may take several exchanges; bound the whole load.
const accountLoadBudget = 30 * time.Second

// Account is one signed-in account's client and collections.
type Account[C any] struct {
	Name        string
	Login       *alborz.Login
	Client      C
	Collections []dav.Collection
}

// Pooled resolves every signed-in account with the service; a failing account is logged and skipped.
func Pooled[C any](ctx *alborz.Context, p *dav.Provider, load func(context.Context, *alborz.Login) (C, []dav.Collection, error), none error) ([]Account[C], error) {
	var accounts []Account[C]
	var lastErr error
	var down, refused []string
	// answered counts servers that refused other than over the password.
	answered := map[string]int{}
	fail := func(username string, err error) {
		if code := dav.Answered(err); code != 0 {
			answered[username] = code
			return
		}
		if !slices.Contains(down, username) {
			down = append(down, username)
		}
	}
	// A refusal stands until the reader changes something, so it is said once per browser.
	refuse := func(username string) {
		if ctx.Session().Once("davrefused:" + p.Kind.Name + ":" + username) {
			refused = append(refused, username)
		}
	}
	results := dav.Each(ctx.Request().Context(), ctx.Logins(), func(reqCtx context.Context, s *alborz.Login) (Account[C], error) {
		reqCtx, cancel := context.WithTimeout(reqCtx, accountLoadBudget)
		defer cancel()
		c, infos, err := load(reqCtx, s)
		return Account[C]{Client: c, Collections: infos}, err
	})
	for _, result := range results {
		s, err := result.Site, result.Err
		c, infos := result.Value.Client, result.Value.Collections
		// An account with nothing of the kind keeps its place, empty, so the first one can be made.
		if errors.Is(err, none) {
			accounts = append(accounts, Account[C]{Name: s.Username(), Login: s, Client: c})
			continue
		}
		// A refused password keeps the account's place, empty, and tells the reader.
		var no *dav.RefusedError
		if errors.As(err, &no) {
			accounts = append(accounts, Account[C]{Name: s.Username(), Login: s, Client: c})
			refuse(s.Username())
			continue
		}
		if err != nil {
			lastErr = err
			fail(s.Username(), err)
			ctx.Logger().Printf("%s: skipping %q in the pooled view: %v", p.Kind.Name, s.Username(), err)
			continue
		}
		owned := slices.Clone(infos)
		for i := range owned {
			owned[i].Account = s.Username()
		}
		accounts = append(accounts, Account[C]{Name: s.Username(), Login: s, Client: c, Collections: owned})
		for _, trouble := range p.Troubles(s.Username()) {
			ctx.Logger().Printf("%s: %q in the pooled view without a source: %v", p.Kind.Name, s.Username(), trouble)
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
		key, action := "notice.davanswered", &alborz.NoticeAction{Label: ctx.T("settings.account"), Path: "/settings/account?account=" + alborz.QueryValue(username)}
		// A 403 stays a permission answer, with the way to the password beside it.
		if code == http.StatusForbidden {
			key, action = "notice.davforbidden", &alborz.NoticeAction{Label: ctx.T("servers.dav"), Path: "/settings/servers/" + settings.ServerDAV + "?account=" + alborz.QueryValue(username)}
		}
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeWarning,
			Text:    fmt.Sprintf(ctx.T(key), username, fmt.Sprintf("%d %s", code, http.StatusText(code))),
			Actions: []*alborz.NoticeAction{action}})
	}
	// A refusal is the one the reader can end, so it wins when both happened.
	if len(refused) > 0 {
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeWarning,
			Text:    ctx.Tf("notice.davrefused", len(refused), strings.Join(refused, ", ")),
			Actions: []*alborz.NoticeAction{{Label: ctx.T("servers.dav"), Path: "/settings/servers/" + settings.ServerDAV + "?account=" + alborz.QueryValue(refused[0])}}})
	}
	return accounts, nil
}

// WritableGroups keeps, per account, the collections keep accepts.
func WritableGroups[C any](accounts []Account[C], keep func(dav.Collection) bool) []dav.Group {
	var groups []dav.Group
	for _, acc := range accounts {
		var kept []dav.Collection
		for _, c := range acc.Collections {
			if keep(c) {
				kept = append(kept, c)
			}
		}
		if len(kept) > 0 {
			groups = append(groups, dav.Group{Account: acc.Name, Collections: kept})
		}
	}
	return groups
}

// Site is one collection to query, with its account's client.
type Site[C any] struct {
	Client     C
	Collection dav.Collection
}

// Only is the set a URL asks to see through field, overriding stored visibility for that request.
func Only(ctx *alborz.Context, field string) map[string]bool {
	values := ctx.QueryParams()[field]
	if len(values) == 0 {
		return nil
	}
	only := make(map[string]bool, len(values))
	for _, v := range values {
		only[dav.CanonicalCollectionPath(v)] = true
	}
	return only
}

// Visible returns every collection for the rail and the visible ones in scope as sites; feeds are never queried.
func Visible[C any](accounts []Account[C], scope string, only map[string]bool, chosen func(Account[C]) (colls []dav.Collection, filter bool, paths []string, err error)) ([]dav.Collection, []Site[C], error) {
	var infos []dav.Collection
	var sites []Site[C]
	for _, acc := range accounts {
		colls, filter, paths, err := chosen(acc)
		if err != nil {
			return nil, nil, err
		}
		visible := make(map[string]bool)
		for _, path := range paths {
			visible[dav.CanonicalCollectionPath(path)] = true
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

// HandleChoose keeps which collections of list the reader ticked and lands on the list, no longer narrowed.
func HandleChoose(list, field string, keep func(ctx context.Context, store alborz.Store, paths []string) error) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		params, err := ctx.FormParams()
		if err != nil {
			return err
		}
		if err := keep(ctx.Request().Context(), ctx.Login.Store(), params[field]); err != nil {
			ctx.Notify(Refused(ctx, err))
		} else {
			// The list comes back holding what was chosen.
			ctx.Quiet()
		}
		return ctx.Redirect(http.StatusFound, Widened(ctx.NextOr(list), field))
	}
}

// Ref is one object an action is about, with its account and that account's client.
type Ref[C any] struct {
	Client  C
	Account string
	Path    string
}

// Selected resolves checked "account|path" rows to their accounts' clients; a selection can span accounts.
func Selected[C any](ctx *alborz.Context, refs []string, client func(context.Context, *alborz.Login) (C, error)) ([]Ref[C], error) {
	clients := map[string]C{}
	out := make([]Ref[C], 0, len(refs))
	for _, ref := range refs {
		account, objPath, ok := strings.Cut(ref, "|")
		if !ok {
			return nil, echo.NewHTTPError(http.StatusBadRequest, "unqualified selection")
		}
		c, ok := clients[account]
		if !ok {
			login := ctx.LoginFor(account)
			if login == nil {
				return nil, echo.NewHTTPError(http.StatusBadRequest, "not signed in to that account")
			}
			var err error
			if c, err = client(ctx.Request().Context(), login); err != nil {
				return nil, err
			}
			clients[account] = c
		}
		out = append(out, Ref[C]{c, account, objPath})
	}
	return out, nil
}
