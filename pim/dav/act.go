package dav

import (
	"context"
	"net/http"
	"net/url"
	"slices"

	"git.mehdix.org/alborz"
	"github.com/labstack/echo/v4"
)

// Selection is the object the route's path names, or the rows a list had checked.
func Selection[C any](ctx *alborz.Context, client func(context.Context, *alborz.Login) (C, error)) ([]Ref[C], error) {
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
	c, err := client(ctx.Request().Context(), ctx.Login)
	if err != nil {
		return nil, err
	}
	return []Ref[C]{{c, ctx.Login.Username(), path}}, nil
}

// Action is one thing a route does to the selected objects.
type Action[C any] struct {
	Client func(context.Context, *alborz.Login) (C, error)
	// Do acts on one object; an error is the server's refusal, said on the page, and ends the run.
	Do func(ctx *alborz.Context, ref Ref[C]) error
	// List is where the action lands when the form names no page.
	List string
	// Done words the result; Quiet where the action shows where the reader already is.
	Done func(ctx *alborz.Context, done []Ref[C], next string) alborz.Notice
	// Piece answers a request for a piece of the page; nil lands as a whole page.
	Piece func(ctx *alborz.Context, ref Ref[C], next string) error
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
			ctx.Notify(Refused(ctx, err))
			return ctx.Redirect(http.StatusFound, next)
		}
		done++
	}
	// A boosted form, the notice's Undo, asks for a page, not the row.
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

// Quiet is Done for an action whose result shows where the reader already is.
func Quiet[C any](ctx *alborz.Context, _ []Ref[C], _ string) alborz.Notice {
	ctx.Quiet()
	return alborz.Notice{}
}

// Delete removes the object.
func Delete[C files](ctx *alborz.Context, ref Ref[C]) error {
	return ref.Client.RemoveAll(ctx.Request().Context(), ref.Path)
}

// Refused carries a server's refusal back to the page a button returns to.
func Refused(ctx *alborz.Context, err error) alborz.Notice {
	return alborz.Notice{Kind: alborz.NoticeFailed, Text: ctx.SaveRefusal(err)}
}

// StarRenderData is one object's star.
type StarRenderData struct {
	alborz.BaseRenderData
	Action    string
	Current   string
	Next      string
	Label     string
	MenuLabel string
}

// Star marks the selection with the form's colour; a refused write leaves the star as the server holds it.
func Star[C any](ctx *alborz.Context, client func(context.Context, *alborz.Login) (C, error), list string, mark func(ctx *alborz.Context, ref Ref[C], name string) (was string, err error)) error {
	name := ctx.FormValue("color")
	if name != "" && !slices.Contains(alborz.FlagColors[:], name) {
		return echo.NewHTTPError(http.StatusBadRequest, "no such colour")
	}
	var was string
	return Run(ctx, Action[C]{
		Client: client,
		List:   list,
		Done:   Quiet[C],
		Do: func(ctx *alborz.Context, ref Ref[C]) (err error) {
			was, err = mark(ctx, ref, name)
			return err
		},
		Piece: func(ctx *alborz.Context, ref Ref[C], next string) error {
			action := ctx.AccountPath(ObjectURL(list+"/", ref.Path, "", nil) + "/color")
			// Changing a star the list is narrowed to takes the row out of the list.
			if alborz.StarLeaves(next, name) {
				ctx.Notify(alborz.StarNotice(ctx, name, action, url.Values{"color": {was}, "next": {next}}))
				return ctx.Relocate(next)
			}
			label := ctx.T("mailbox.flagcolor")
			if name != "" {
				label = ctx.T("mailbox.flagnone")
			}
			return ctx.Render(http.StatusOK, "card-star", &StarRenderData{
				BaseRenderData: *alborz.NewBaseRenderData(ctx),
				Action:         action,
				Current:        name,
				Next:           next,
				Label:          label,
				MenuLabel:      ctx.T("mailbox.flagcolor"),
			})
		},
	})
}
