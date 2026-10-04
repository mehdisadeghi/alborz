package web

import (
	"context"
	"net/http"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/dav"
	"git.mehdix.org/alborz/web"
)

// Made lands a create on the new object's list with a banner naming it; account is the form's chosen owner.
func Made(ctx *web.Context, sentence, name, object, list, account string) error {
	if account != "" {
		q := "?account=" + web.QueryValue(account)
		object, list = object+q, list+q
	} else {
		object, list = ctx.AccountPath(object), ctx.AccountPath(list)
	}
	// A quick add's line is already where it lands; there is no form to go back to.
	again := ctx.Request().URL.RequestURI()
	if ctx.FormValue("quick") != "" {
		again = ""
	}
	ctx.Made(sentence, name, object, again)
	return ctx.Redirect(http.StatusFound, ctx.NextOr(list))
}

// Saved lands a new object on its list and an edited one back on the object.
func Saved(ctx *web.Context, made bool, sentence, name, object, list, account string) error {
	if made {
		return Made(ctx, sentence, name, object, list, account)
	}
	// The object's own page is what was edited.
	ctx.Quiet()
	object = ctx.AccountPath(object)
	if from := ctx.From(); from != "" {
		sep := "?"
		if strings.Contains(object, "?") {
			sep = "&"
		}
		object += sep + "from=" + web.QueryValue(from)
	}
	return ctx.Redirect(http.StatusFound, object)
}

// Destination turns a create form's "account|path" into that account's client and path.
func Destination[C any](ctx *web.Context, value string, open func(context.Context, *alborz.Login) (C, []dav.Collection, error), keep func(dav.Collection) bool) (pim.Ref[C], error) {
	account, path, ok := strings.Cut(value, "|")
	login := ctx.LoginFor(account)
	if !ok || login == nil {
		return pim.Ref[C]{}, pim.ErrNoDestination
	}
	c, colls, err := open(ctx.Request().Context(), login)
	if err != nil {
		return pim.Ref[C]{}, err
	}
	if coll := dav.At(colls, path); coll == nil || !keep(*coll) {
		return pim.Ref[C]{}, pim.ErrNoDestination
	}
	return pim.Ref[C]{Client: c, Account: account, Path: path}, nil
}
