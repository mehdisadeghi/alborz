package web

import (
	"net/http"
	"net/url"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-imap/v2"
)

func formParams(ctx *web.Context) (url.Values, error) {
	params, err := ctx.FormParams()
	if err != nil {
		return nil, web.NewError(http.StatusBadRequest, err)
	}
	return params, nil
}

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
	if !mail.KnownView(view) {
		view = ""
	}
	return next.Query().Get("query"), view, true
}

func selection(ctx *web.Context, mboxName string, form url.Values) ([]imap.UID, error) {
	query, view, whole := wholeView(form)
	if !whole {
		uids, err := mail.ParseUIDs(form["uids"])
		if err != nil {
			return nil, web.NewError(http.StatusBadRequest, err)
		}
		return uids, nil
	}
	return mail.FolderView(ctx.Request().Context(), ctx.Login, mboxName, query, view)
}

func selectedMessages(ctx *web.Context) (mbox string, form url.Values, uids []imap.UID, refs []mail.Ref, back string, err error) {
	mbox = mailboxRef(ctx)
	if form, err = formParams(ctx); err != nil {
		return
	}
	if uids, err = selection(ctx, mbox, form); err != nil {
		return
	}
	back = ctx.NextOr(ctx.URL("mail.mailbox", mbox))
	if len(uids) == 0 {
		err = nothingSelected(ctx, back)
		return
	}
	refs = mail.FolderRefs(ctx.Login.Username(), mbox, uids)
	return
}

func (p *plugin) mergedSelection(ctx *web.Context, role string, form url.Values) ([]mail.Ref, error) {
	query, view, whole := wholeView(form)
	if !whole {
		refs, err := mail.ParseRefs(form["refs"])
		if err != nil {
			return nil, web.NewError(http.StatusBadRequest, err)
		}
		return refs, nil
	}
	settings, err := mail.LoadSettings(ctx.Request().Context(), ctx.Login.Store())
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
	} else if !spanning {
		if logins, err = merging(ctx, logins, role); err != nil {
			return nil, err
		}
	}
	var refs []mail.Ref
	for _, s := range logins {
		found, err := p.ViewRefs(ctx.Request().Context(), s, role, spanning, settings, query, view)
		if err != nil {
			return nil, err
		}
		refs = append(refs, found...)
	}
	return refs, nil
}
