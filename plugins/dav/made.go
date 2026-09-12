package dav

import (
	"net/http"

	"git.mehdix.org/alborz"
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
