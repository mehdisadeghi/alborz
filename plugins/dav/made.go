package dav

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"git.mehdix.org/alborz"
)

// Saved lands a form that wrote an object on the object, in the account
// that holds it.
func Saved(ctx *alborz.Context, object, account string) error {
	if account != "" {
		return ctx.Redirect(http.StatusFound, object+"?account="+alborz.AddressParam(account))
	}
	return ctx.Redirect(http.StatusFound, ctx.AccountPath(object))
}

// ErrNoDestination is a create form naming a collection the account
// does not have, or one that will not take what is being made. The
// handler decides what to tell the form.
var ErrNoDestination = errors.New("dav: no such collection to write into")

// Destination turns a create form's "account|path" choice into that
// account's client and the bare path, refusing a collection keep does
// not accept.
func Destination[C any](ctx *alborz.Context, value string, open func(context.Context, *alborz.Session) (C, []Collection, error), keep func(Collection) bool) (Ref[C], error) {
	account, path, ok := strings.Cut(value, "|")
	session := ctx.SessionFor(account)
	if !ok || session == nil {
		return Ref[C]{}, ErrNoDestination
	}
	c, colls, err := open(ctx.Request().Context(), session)
	if err != nil {
		return Ref[C]{}, err
	}
	if coll := At(colls, path); coll == nil || !keep(*coll) {
		return Ref[C]{}, ErrNoDestination
	}
	return Ref[C]{c, account, path}, nil
}
