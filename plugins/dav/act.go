package dav

import (
	"net/http"

	"git.mehdix.org/alborz"
)

// Selection is what a route acts on: the one object its own path names,
// in the URL's account.
func Selection[C any](ctx *alborz.Context, client func(*alborz.Session) (C, error)) ([]Ref[C], error) {
	path, err := ParseObjectPath(ctx.Param("path"))
	if err != nil {
		return nil, err
	}
	c, err := client(ctx.Session)
	if err != nil {
		return nil, err
	}
	return []Ref[C]{{c, ctx.Session.Username(), path}}, nil
}

// Action is one thing a route does to the objects selected: whose
// client, what to each, in which words, and where to land.
type Action[C any] struct {
	Client func(*alborz.Session) (C, error)
	// Do acts on one object. An error ends the run.
	Do func(ctx *alborz.Context, ref Ref[C]) error
	// List is where the action lands when the form names no page.
	List string
}

// Run does the action to the request's selection and lands.
func Run[C any](ctx *alborz.Context, a Action[C]) error {
	refs, err := Selection(ctx, a.Client)
	if err != nil {
		return err
	}
	next := ctx.NextOr(ctx.AccountPath(a.List))
	for _, ref := range refs {
		if err := a.Do(ctx, ref); err != nil {
			return err
		}
	}
	return ctx.Redirect(http.StatusFound, next)
}

// Handler is Run for an action that is the same whatever the request.
func Handler[C any](a Action[C]) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error { return Run(ctx, a) }
}

// Delete removes the object.
func Delete[C files](ctx *alborz.Context, ref Ref[C]) error {
	return ref.Client.RemoveAll(ctx.Request().Context(), ref.Path)
}
