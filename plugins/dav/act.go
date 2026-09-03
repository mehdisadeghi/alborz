package dav

import (
	"net/http"

	"git.mehdix.org/alborz"
)

// Selection is what a route acts on: the one object its own path names,
// in the URL's account, or the rows a list had checked. Acting on one
// object is acting on a selection of one.
func Selection[C any](ctx *alborz.Context, client func(*alborz.Session) (C, error)) ([]Ref[C], error) {
	if ctx.Param("path") == "" {
		params, err := ctx.FormParams()
		if err != nil {
			return nil, err
		}
		return Selected(ctx, params["paths"], client)
	}
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
	// Done words what became of the objects acted on; nil says nothing.
	Done func(ctx *alborz.Context, done []Ref[C], next string) alborz.Notice
}

// Run does the action to the request's selection and lands.
func Run[C any](ctx *alborz.Context, a Action[C]) error {
	refs, err := Selection(ctx, a.Client)
	if err != nil {
		return err
	}
	next := ctx.NextOr(ctx.AccountPath(a.List))
	done := 0
	for _, ref := range refs {
		if err := a.Do(ctx, ref); err != nil {
			return err
		}
		done++
	}
	if a.Done != nil && done > 0 {
		ctx.Session.Notify(a.Done(ctx, refs[:done], next))
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
