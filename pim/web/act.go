package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/dav"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-webdav"
)

// Selection is the object the route's path names, or the rows a list had checked.
func Selection[C any](ctx *web.Context, client func(context.Context, *alborz.Login) (C, error)) ([]pim.Ref[C], error) {
	if ctx.Param("path") == "" {
		params, err := ctx.FormParams()
		if err != nil {
			return nil, err
		}
		return Selected(ctx, params["paths"], client)
	}
	path := ctx.Param("path")
	c, err := client(ctx.Request().Context(), ctx.Login)
	if err != nil {
		return nil, err
	}
	return []pim.Ref[C]{{Client: c, Account: ctx.Login.Username(), Path: path}}, nil
}

// Action is one thing a route does to the selected objects.
type Action[C any] struct {
	Client func(context.Context, *alborz.Login) (C, error)
	// Do acts on one object; an error is the server's refusal, said on the page, and ends the run.
	Do func(ctx *web.Context, ref pim.Ref[C]) error
	// List is the route the action lands on when the form names no page.
	List string
	// Done words the result; Quiet where the action shows where the reader already is.
	Done func(ctx *web.Context, done []pim.Ref[C], next string) web.Notice
	// Piece answers a request for a piece of the page; nil lands as a whole page.
	Piece func(ctx *web.Context, ref pim.Ref[C], next string) error
}

// Run does the action to the request's selection and lands.
func Run[C any](ctx *web.Context, a Action[C]) error {
	refs, err := Selection(ctx, a.Client)
	if err != nil {
		return err
	}
	next := ctx.NextOr(ctx.URL(a.List))
	done := 0
	for _, ref := range refs {
		if err := a.Do(ctx, ref); err != nil {
			ctx.Notify(Refused(ctx, err))
			return ctx.FoundRedirect(next)
		}
		done++
	}
	// The notice's Undo asks for the list, not the row.
	if a.Piece != nil && ctx.Fragment() && !ctx.PartialFor("main") && len(refs) == 1 {
		return a.Piece(ctx, refs[0], next)
	}
	switch {
	case ctx.FormValue("undo") != "":
		ctx.Notify(web.Notice{Kind: web.NoticeDone, Text: ctx.T("notice.undone")})
	case a.Done != nil && done > 0:
		ctx.Notify(a.Done(ctx, refs[:done], next))
	}
	return ctx.FoundRedirect(next)
}

// Handler is Run for an action that is the same whatever the request.
func Handler[C any](a Action[C]) func(*web.Context) error {
	return func(ctx *web.Context) error { return Run(ctx, a) }
}

// NoticeHandler runs an action and reports how many objects it changed.
func NoticeHandler[C any](client func(context.Context, *alborz.Login) (C, error), list, key string, do func(*web.Context, pim.Ref[C]) error) func(*web.Context) error {
	return Handler(Action[C]{Client: client, Do: do, List: list, Done: func(ctx *web.Context, done []pim.Ref[C], _ string) web.Notice {
		return web.Notice{Kind: web.NoticeDone, Text: ctx.Tf(key, len(done))}
	}})
}

// Note appends the submitted note to each selected object; an empty note is a quiet no-op.
func Note[C any](ctx *web.Context, client func(context.Context, *alborz.Login) (C, error), list string,
	change func(*web.Context, pim.Ref[C], string) error) error {
	note := strings.TrimSpace(ctx.FormValue("note"))
	return Run(ctx, Action[C]{Client: client, List: list, Done: Quiet[C], Do: func(ctx *web.Context, ref pim.Ref[C]) error {
		if note == "" {
			return nil
		}
		return change(ctx, ref, note)
	}})
}

// Quiet is Done for an action whose result shows where the reader already is.
func Quiet[C any](ctx *web.Context, _ []pim.Ref[C], _ string) web.Notice {
	ctx.Quiet()
	return web.Notice{}
}

// Delete removes the object.
func Delete[C files](ctx *web.Context, ref pim.Ref[C]) error {
	return ref.Client.RemoveAll(ctx.Request().Context(), ref.Path)
}

// Refused carries a server's refusal back to the page a button returns to.
func Refused(ctx *web.Context, err error) web.Notice {
	return web.Notice{Kind: web.NoticeFailed, Text: Refusal(ctx, err)}
}

// Unsaved is Refused for an editor's save; one refused for a form drawn before the object changed
// elsewhere (RFC 7232 4.2) links the form afresh, since reloading the answer would post it again.
func Unsaved(ctx *web.Context, err error) web.Notice {
	if code, _ := webdav.HTTPErrorCode(err); code == http.StatusPreconditionFailed {
		return web.Notice{Kind: web.NoticeFailed, Text: ctx.T("form.changedelsewhere"),
			Actions: []*web.NoticeAction{{Label: ctx.T("common.reload"), Path: ctx.Request().URL.RequestURI()}}}
	}
	return Refused(ctx, err)
}

// Drawn is the ETag the edit form was drawn with, sent as If-Match so a save over another's is refused
// (RFC 7232 3.1, RFC 4791 5.3.4, RFC 6352 6.3.2.3); a form that names none edits held, the version read now.
func Drawn(ctx *web.Context, held string) (string, error) {
	params, err := ctx.FormParams()
	if err != nil {
		return "", err
	}
	if v, ok := params["etag"]; ok {
		return v[0], nil
	}
	return held, nil
}

// Refusal is a refused write in the reader's words; one alborz makes itself says why, any other quotes the server.
func Refusal(ctx *web.Context, err error) string {
	if errors.Is(err, dav.ErrScaleUnlisted) {
		return ctx.T("form.repeatscale")
	}
	return ctx.SaveRefusal(err)
}

// StarRenderData is one object's star.
type StarRenderData struct {
	web.BaseRenderData
	Action    string
	Current   string
	Next      string
	Label     string
	MenuLabel string
}

// Star marks the selection with the form's colour; a refused write leaves the star as the server holds it.
// list and object are the routes of the list and of one object.
func Star[C any](ctx *web.Context, client func(context.Context, *alborz.Login) (C, error), list, object string, mark func(ctx *web.Context, ref pim.Ref[C], name string) (was string, err error)) error {
	name := ctx.FormValue("color")
	if name != "" && !slices.Contains(alborz.FlagColors[:], name) {
		return web.NewError(http.StatusBadRequest, "no such colour")
	}
	var was string
	return Run(ctx, Action[C]{
		Client: client,
		List:   list,
		Done:   Quiet[C],
		Do: func(ctx *web.Context, ref pim.Ref[C]) (err error) {
			was, err = mark(ctx, ref, name)
			return err
		},
		Piece: func(ctx *web.Context, ref pim.Ref[C], next string) error {
			action := ctx.URL(object+".color", ref.Path)
			// Changing a star the list is narrowed to takes the row out of the list.
			if web.StarLeaves(next, name) {
				ctx.Notify(web.StarNotice(ctx, name, action, url.Values{"color": {was}, "next": {next}}))
				return ctx.Redraw(next)
			}
			label := ctx.T("mailbox.flagcolor")
			if name != "" {
				label = ctx.T("mailbox.flagnone")
			}
			return ctx.Render(http.StatusOK, "card-star", &StarRenderData{
				BaseRenderData: *web.NewBaseRenderData(ctx),
				Action:         action,
				Current:        name,
				Next:           next,
				Label:          label,
				MenuLabel:      ctx.T("mailbox.flagcolor"),
			})
		},
	})
}
