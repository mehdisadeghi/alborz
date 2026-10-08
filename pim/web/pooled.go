package web

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/dav"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-webdav"
)

// Discovery across an account may take several exchanges; bound the whole load.
const accountLoadBudget = 30 * time.Second

// Pooled resolves every signed-in account with the service; a failing account is logged and skipped.
func Pooled[C any](ctx *web.Context, p *dav.Provider, load func(context.Context, *alborz.Login) (C, []dav.Collection, error), none error) ([]pim.Account[C], error) {
	var accounts []pim.Account[C]
	var lastErr error
	var down, refused []string
	// answered counts servers that refused other than over the password.
	answered := map[string]int{}
	fail := func(username string, err error) {
		if code, ok := webdav.HTTPErrorCode(err); ok {
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
	results := dav.Across(ctx.Request().Context(), ctx.Logins(), (*alborz.Login).Username, func(reqCtx context.Context, s *alborz.Login) (pim.Account[C], error) {
		reqCtx, cancel := context.WithTimeout(reqCtx, accountLoadBudget)
		defer cancel()
		c, infos, err := load(reqCtx, s)
		return pim.Account[C]{Client: c, Collections: infos}, err
	})
	for _, result := range results {
		s, err := result.Site, result.Err
		c, infos := result.Value.Client, result.Value.Collections
		// An account with nothing of the kind keeps its place, empty, so the first one can be made.
		if errors.Is(err, none) {
			accounts = append(accounts, pim.Account[C]{Name: s.Username(), Login: s, Client: c})
			continue
		}
		// A refused password keeps the account's place, empty, and tells the reader.
		var no *dav.RefusedError
		if errors.As(err, &no) {
			accounts = append(accounts, pim.Account[C]{Name: s.Username(), Login: s, Client: c})
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
		accounts = append(accounts, pim.Account[C]{Name: s.Username(), Login: s, Client: c, Collections: owned})
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
		key, action := "notice.davanswered", &web.NoticeAction{Label: ctx.T("settings.account"), Path: web.AccountURL(ctx.Path("mail.account"), username)}
		// A 403 stays a permission answer, with the way to the password beside it.
		if code == http.StatusForbidden {
			key, action = "notice.davforbidden", &web.NoticeAction{Label: ctx.T("servers.dav"), Path: web.AccountURL(ctx.Path("pim.server"), username)}
		}
		ctx.Notify(web.Notice{Kind: web.NoticeWarning,
			Text:    fmt.Sprintf(ctx.T(key), username, fmt.Sprintf("%d %s", code, http.StatusText(code))),
			Actions: []*web.NoticeAction{action}})
	}
	// A refusal is the one the reader can end, so it wins when both happened.
	if len(refused) > 0 {
		ctx.Notify(web.Notice{Kind: web.NoticeWarning,
			Text:    ctx.Tf("notice.davrefused", len(refused), strings.Join(refused, ", ")),
			Actions: []*web.NoticeAction{{Label: ctx.T("servers.dav"), Path: web.AccountURL(ctx.Path("pim.server"), refused[0])}}})
	}
	return accounts, nil
}

// Only is the set a URL asks to see through field, overriding stored visibility for that request.
func Only(ctx *web.Context, field string) map[string]bool {
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

// HandleChoose keeps which of the collections the form showed the reader ticked, and lands on list, a route, no longer narrowed.
func HandleChoose(p *dav.Provider, list, field string, keep func(ctx context.Context, l *alborz.Login, all, shown, ticked []string) error) func(*web.Context) error {
	return func(ctx *web.Context) error {
		params, err := ctx.FormParams()
		if err != nil {
			return err
		}
		to := Widened(ctx.NextOr(ctx.Path(list)), field)
		colls, err := p.Collections(ctx.Request().Context(), ctx.Login)
		if err == nil {
			all := make([]string, len(colls))
			for i, c := range colls {
				all[i] = c.Path
			}
			err = keep(ctx.Request().Context(), ctx.Login, all, params["shown"], params[field])
		}
		if err != nil {
			return ctx.NotifyRedirect(Refused(ctx, err), to)
		}
		// The list comes back holding what was chosen.
		return ctx.QuietRedirect(to)
	}
}

// Selected resolves checked "account|path" rows to their accounts' clients; a selection can span accounts.
func Selected[C any](ctx *web.Context, refs []string, client func(context.Context, *alborz.Login) (C, error)) ([]pim.Ref[C], error) {
	clients := map[string]C{}
	out := make([]pim.Ref[C], 0, len(refs))
	for _, ref := range refs {
		account, objPath, ok := strings.Cut(ref, "|")
		if !ok {
			return nil, web.NewError(http.StatusBadRequest, "unqualified selection")
		}
		c, ok := clients[account]
		if !ok {
			login := ctx.LoginFor(account)
			if login == nil {
				return nil, web.NewError(http.StatusBadRequest, "not signed in to that account")
			}
			var err error
			if c, err = client(ctx.Request().Context(), login); err != nil {
				return nil, err
			}
			clients[account] = c
		}
		out = append(out, pim.Ref[C]{Client: c, Account: account, Path: objPath})
	}
	return out, nil
}

// WarnUnread logs what a page left out and names the collections it leaves incomplete.
func WarnUnread(ctx *web.Context, unread []pim.Unread) {
	var names, marked []string
	for _, u := range unread {
		ctx.Logger().Printf("%s: %v", u.Collection, u.Err)
		if !slices.Contains(names, u.Collection) {
			names = append(names, u.Collection)
			marked = append(marked, web.NoticeName(u.Collection))
		}
	}
	if len(names) == 0 {
		return
	}
	ctx.Notify(web.Notice{Kind: web.NoticeWarning,
		Text:   ctx.Tf("notice.unread", len(names), strings.Join(names, ", ")),
		Markup: template.HTML(ctx.Tf("notice.unread", len(names), strings.Join(marked, ", ")))})
}

// ProjectResults turns answers from a collection query into page rows while
// keeping collection and object parse failures in the same unread stream.
// The callback may return an object-level unread, and its final result says
// whether the projected value belongs in the list.
func ProjectResults[C, O, T any](results []dav.Result[pim.Site[C], []O], project func(*O, dav.Collection) (T, *pim.Unread, bool)) ([]T, []pim.Unread) {
	var rows []T
	var unread []pim.Unread
	for _, result := range results {
		if result.Err != nil {
			unread = append(unread, pim.Unread{Collection: result.Site.Collection.Name, Err: result.Err})
			continue
		}
		for i := range result.Value {
			row, failed, include := project(&result.Value[i], result.Site.Collection)
			if failed != nil {
				unread = append(unread, *failed)
			}
			if include {
				rows = append(rows, row)
			}
		}
	}
	return rows, unread
}

// ListParts applies the common list-page boundary: report incomplete sources,
// paginate the rows, and load the section's invitations before rendering.
func ListParts[T any](ctx *web.Context, unread []pim.Unread, rows []T, invitations func() (*RailInvitations, error)) ([]T, web.Pager, *RailInvitations, error) {
	WarnUnread(ctx, unread)
	shown, pager, err := web.Paginate(ctx, rows)
	if err != nil {
		return nil, web.Pager{}, nil, err
	}
	offers, err := invitations()
	return shown, pager, offers, err
}

// SearchRows applies the common cross-section search result boundary: report incomplete sources,
// retain the total, and keep only the rows shown in the cross-section result.
func SearchRows[T any](ctx *web.Context, rows []T, unread []pim.Unread, limit int) ([]T, int) {
	WarnUnread(ctx, unread)
	total := len(rows)
	return rows[:min(limit, total)], total
}
