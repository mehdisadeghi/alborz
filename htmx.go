package alborz

// Partial reports whether the page asked for a piece of itself rather
// than for the whole of it. htmx sets the header on every request it
// makes; a handler that has a block for the change may answer with that
// block alone, and must answer the whole page to anything else. Coming
// back through history restores a whole page, which is why that one is
// not a partial. ADR 13.
func (ctx *Context) Partial() bool {
	h := ctx.Request().Header
	return h.Get("HX-Request") == "true" && h.Get("HX-History-Restore-Request") != "true"
}

// PartialFor reports whether the page asked for one named piece of
// itself. htmx names the element it will swap into in HX-Target, so a
// control that swaps the list says so and everything else - a boosted
// link, a form, a page reached from outside - still gets the page.
func (ctx *Context) PartialFor(id string) bool {
	return ctx.Partial() && ctx.Request().Header.Get("HX-Target") == id
}
