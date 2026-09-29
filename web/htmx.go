package web

// Partial reports whether htmx asked for a piece of the page rather than the
// whole; a history restore asks for the whole (ADR 13).
func (ctx *Context) Partial() bool {
	h := ctx.Request().Header
	return h.Get("HX-Request") == "true" && h.Get("HX-History-Restore-Request") != "true"
}

// PartialFor reports whether htmx asked for the piece swapped into id.
func (ctx *Context) PartialFor(id string) bool {
	return ctx.Partial() && ctx.Request().Header.Get("HX-Target") == id
}
