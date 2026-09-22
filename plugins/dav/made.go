package dav

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"path"
	"strings"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-webdav"
)

// Made ends a create: the reader lands on the list the new thing now
// belongs to, with a banner naming it - a link to the thing itself -
// and the offer to make another from the form as it stood. Editing an
// object goes back to the object, which is where the reader was; only
// making one lands here.
//
// account names the owner where the form chose one, since a create in
// the merged view can land in any of them.
func Made(ctx *alborz.Context, sentence, name, object, list, account string) error {
	if account != "" {
		q := "?account=" + alborz.AddressParam(account)
		object, list = object+q, list+q
	} else {
		object, list = ctx.AccountPath(object), ctx.AccountPath(list)
	}
	ctx.Made(sentence, name, object, ctx.Request().URL.RequestURI())
	return ctx.Redirect(http.StatusFound, ctx.NextOr(list))
}

// Saved lands a form that wrote an object: a new one on its list, with
// the banner naming it; an edited one back on the object, which is
// where the reader was.
func Saved(ctx *alborz.Context, made bool, sentence, name, object, list, account string) error {
	if made {
		return Made(ctx, sentence, name, object, list, account)
	}
	object = ctx.AccountPath(object)
	if from := ctx.From(); from != "" {
		sep := "?"
		if strings.Contains(object, "?") {
			sep = "&"
		}
		object += sep + "from=" + url.QueryEscape(from)
	}
	return ctx.Redirect(http.StatusFound, object)
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

// Target is where a form's object is written and on what condition: a
// new one at a name of its own in the collection, and only while
// nothing is there; a held one where it is, and only while it is the
// object that was read.
func Target(collection, name, held, etag string) (at string, ifMatch, ifNoneMatch webdav.ConditionalMatch) {
	if held == "" {
		return path.Join(collection, name), "", IfNew
	}
	return held, IfUnchanged(etag), ""
}
