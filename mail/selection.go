package mail

import (
	"net/http"
	"net/url"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-imap/v2"
)

// A bulk form's "everything" means the whole list its next URL names, asked again.

func wholeView(form url.Values) (query, view string, whole bool) {
	if form.Get("everything") == "" {
		return "", "", false
	}
	next, err := url.Parse(form.Get("next"))
	// A conversation is decided by THREAD; asking it again here would duplicate that page.
	if err != nil || next.Query().Get("thread") != "" {
		return "", "", false
	}
	view = next.Query().Get("view")
	if !KnownView(view) {
		view = ""
	}
	return next.Query().Get("query"), view, true
}

func selection(ctx *web.Context, mboxName string, form url.Values) ([]imap.UID, error) {
	query, view, whole := wholeView(form)
	if !whole {
		uids, err := ParseUIDs(form["uids"])
		if err != nil {
			return nil, web.NewError(http.StatusBadRequest, err)
		}
		return uids, nil
	}
	return FolderView(ctx.Request().Context(), ctx.Login, mboxName, query, view)
}

func folderSelection(ctx *web.Context) (mboxName string, form url.Values, uids []imap.UID, err error) {
	if mboxName, err = mailboxRef(ctx); err != nil {
		return "", nil, nil, err
	}
	if form, err = ctx.FormParams(); err != nil {
		return "", nil, nil, web.NewError(http.StatusBadRequest, err)
	}
	uids, err = selection(ctx, mboxName, form)
	return mboxName, form, uids, err
}

func (p *plugin) mergedSelection(ctx *web.Context, role string, form url.Values) ([]Ref, error) {
	query, view, whole := wholeView(form)
	if !whole {
		refs, err := ParseRefs(form["refs"])
		if err != nil {
			return nil, web.NewError(http.StatusBadRequest, err)
		}
		return refs, nil
	}
	settings, err := LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return nil, err
	}
	next, err := url.Parse(form.Get("next"))
	if err != nil {
		return nil, web.NewError(http.StatusBadRequest, err)
	}
	route, _, _ := ctx.Server.Match(next)
	spanning := route == "mail.search"
	logins := ctx.Logins()
	if account := next.Query().Get("account"); account != "" {
		s := ctx.LoginFor(account)
		if s == nil {
			return nil, web.NewError(http.StatusBadRequest, "no such account")
		}
		logins = []*alborz.Login{s}
	}
	var refs []Ref
	for _, s := range logins {
		found, err := p.ViewRefs(ctx.Request().Context(), s, role, spanning, settings, query, view)
		if err != nil {
			return nil, err
		}
		refs = append(refs, found...)
	}
	return refs, nil
}
