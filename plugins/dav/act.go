package dav

import (
	"fmt"
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
	// Do acts on one object. An error is the server's no, and ends the
	// run: it is said on the page, never answered as a failure of ours.
	Do func(ctx *alborz.Context, ref Ref[C]) error
	// List is where the action lands when the form names no page.
	List string
	// Done words what became of the objects acted on; nil says nothing.
	Done func(ctx *alborz.Context, done []Ref[C], next string) alborz.Notice
	// Piece answers a page that asked for a piece of itself about the
	// one object it named; nil lands as any other request does.
	Piece func(ctx *alborz.Context, ref Ref[C], next string) error
}

// Run does the action to the request's selection and lands. A refusal
// is said on the page, never swallowed: it ends the run and lands whole,
// so the notice renders whether the request asked for a page or a piece.
func Run[C any](ctx *alborz.Context, a Action[C]) error {
	refs, err := Selection(ctx, a.Client)
	if err != nil {
		return err
	}
	next := ctx.NextOr(ctx.AccountPath(a.List))
	done := 0
	for _, ref := range refs {
		if err := a.Do(ctx, ref); err != nil {
			ctx.Notify(Refused(ctx, err))
			return ctx.Redirect(http.StatusFound, next)
		}
		done++
	}
	// A boosted form - the notice's Undo - asks for a page, not the row
	// a row's own control swaps.
	if a.Piece != nil && ctx.Partial() && ctx.Request().Header.Get("HX-Boosted") != "true" && len(refs) == 1 {
		return a.Piece(ctx, refs[0], next)
	}
	if a.Done != nil && done > 0 {
		ctx.Notify(a.Done(ctx, refs[:done], next))
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

// Refused carries a server's no back to the page a button returns to;
// there is no form to show it on.
func Refused(ctx *alborz.Context, err error) alborz.Notice {
	return alborz.Notice{Kind: alborz.NoticeFailed, Text: fmt.Sprintf(ctx.T("form.saverefused"), err)}
}
