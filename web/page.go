package web

import "net/http"

// PageData is the common page frame. R remains feature-specific so templates
// get one consistent base while Go keeps each rail's type and fields checked.
type PageData[R any] struct {
	BaseRenderData
	Rail R
}

// NewPageData builds the shared frame and its feature rail in one expression.
func NewPageData[R any](ctx *Context, title string, rail R) *PageData[R] {
	base := NewBaseRenderData(ctx)
	base.GlobalData.HasRail = true
	if title != "" {
		base.WithTitle(title)
	}
	return &PageData[R]{BaseRenderData: *base, Rail: rail}
}

// NewSettingsPageData builds the shared settings frame and rail.
func NewSettingsPageData(ctx *Context, title string) *PageData[map[string][]RailRow] {
	return NewPageData(ctx, title, SettingsRail(ctx))
}

// WithRefused adds a form refusal while retaining the page frame for a literal.
func (p *PageData[R]) WithRefused(text string) *PageData[R] {
	p.Refused(text)
	return p
}

// RenderRefused puts a validation refusal on a form's existing page shell.
// All form pages use the same status and notice contract for invalid input.
func RenderRefused(ctx *Context, template string, data interface {
	Refused(string) *BaseRenderData
}, text string) error {
	data.Refused(text)
	return ctx.Render(http.StatusUnprocessableEntity, template, data)
}

// WithItem marks the frame as an object page while retaining its rail type.
func (p *PageData[R]) WithItem() *PageData[R] {
	p.BaseRenderData.WithItem()
	return p
}
