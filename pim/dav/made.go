package dav

import (
	"context"
	"errors"
	"net/http"
	"path"
	"strings"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-webdav"
)

// Made lands a create on the new object's list with a banner naming it; account is the form's chosen owner.
func Made(ctx *alborz.Context, sentence, name, object, list, account string) error {
	if account != "" {
		q := "?account=" + alborz.QueryValue(account)
		object, list = object+q, list+q
	} else {
		object, list = ctx.AccountPath(object), ctx.AccountPath(list)
	}
	ctx.Made(sentence, name, object, ctx.Request().URL.RequestURI())
	return ctx.Redirect(http.StatusFound, ctx.NextOr(list))
}

// Saved lands a new object on its list and an edited one back on the object.
func Saved(ctx *alborz.Context, made bool, sentence, name, object, list, account string) error {
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
		object += sep + "from=" + alborz.QueryValue(from)
	}
	return ctx.Redirect(http.StatusFound, object)
}

// ErrNoDestination is a create form naming a collection the account lacks or that refuses the object.
var ErrNoDestination = errors.New("dav: no such collection to write into")

// Destination turns a create form's "account|path" into that account's client and path.
func Destination[C any](ctx *alborz.Context, value string, open func(context.Context, *alborz.Login) (C, []Collection, error), keep func(Collection) bool) (Ref[C], error) {
	account, path, ok := strings.Cut(value, "|")
	login := ctx.LoginFor(account)
	if !ok || login == nil {
		return Ref[C]{}, ErrNoDestination
	}
	c, colls, err := open(ctx.Request().Context(), login)
	if err != nil {
		return Ref[C]{}, err
	}
	if coll := At(colls, path); coll == nil || !keep(*coll) {
		return Ref[C]{}, ErrNoDestination
	}
	return Ref[C]{c, account, path}, nil
}

// Target is where a form's object is written: a new name only while free, a held one only while unchanged.
func Target(collection, name, held, etag string) (at string, ifMatch, ifNoneMatch webdav.ConditionalMatch) {
	if held == "" {
		return path.Join(collection, name), "", IfNew
	}
	return held, IfUnchanged(etag), ""
}
