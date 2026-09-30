package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"git.mehdix.org/alborz"
)

// Error is a request answered with Code; Message says why.
type Error struct {
	Code    int
	Message string
}

func (e *Error) Error() string { return e.Message }

// NewError refuses a request with code, saying why; with no why, the status text.
func NewError(code int, why ...any) *Error {
	if len(why) == 0 {
		return &Error{Code: code, Message: http.StatusText(code)}
	}
	return &Error{Code: code, Message: fmt.Sprint(why...)}
}

// status is the answer err calls for.
func status(err error) int {
	var refused *Error
	switch {
	case errors.As(err, &refused):
		return refused.Code
	case errors.Is(err, alborz.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, alborz.ErrUnreadable):
		return http.StatusBadGateway
	case errors.Is(err, alborz.ErrIMAPQueueFull):
		return http.StatusServiceUnavailable
	case errors.Is(err, alborz.ErrSuperseded):
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

// fail answers a handler's error with a page: what was not found, which server did not answer, or the status.
func (s *Server) fail(ctx *Context, err error) {
	// A reader who has gone reads no answer, and going is no failure.
	if errors.Is(err, context.Canceled) && errors.Is(ctx.Request().Context().Err(), context.Canceled) {
		return
	}
	code := status(err)
	if errors.Is(err, alborz.ErrSuperseded) {
		if err := renderInfo(ctx, code, ctx.T("search.superseded")); err == nil {
			return
		}
	}
	if code == http.StatusNotFound {
		message := err.Error()
		var missing *alborz.NotFoundError
		if errors.As(err, &missing) {
			message = ctx.Tf(missing.Key, missing.Args...)
		}
		if err := renderInfo(ctx, code, message); err == nil {
			return
		}
	}

	if errors.Is(err, alborz.ErrUnreadable) {
		s.Logger().Printf("%s %s: %v", ctx.Request().Method, ctx.Request().URL.Path, err)
		if err := renderInfo(ctx, code, ctx.T("unreadable")); err == nil {
			return
		}
	}

	// An upstream that did not answer is not this program failing, so it gets its own page rather than a 500.
	var upstream alborz.UpstreamError
	if errors.As(err, &upstream) {
		if err := renderUpstreamDown(ctx, upstream); err == nil {
			return
		}
	}

	data := struct {
		BaseRenderData
		Code   int
		Err    error
		Status string
	}{*NewBaseRenderData(ctx), code, err, http.StatusText(code)}
	if err := ctx.Render(code, "error", &data); err != nil {
		s.Logger().Printf("rendering the error page: %v", err)
	}
	s.Logger().Printf("%s %s: %v", ctx.Request().Method, ctx.Request().URL.Path, err)
}
