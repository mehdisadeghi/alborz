package web

// Partial reports whether htmx asked for a piece of the page rather than the
// whole; a history restore asks for the whole (ADR 13).
func (ctx *Context) Partial() bool {
	h := ctx.Request().Header
	return h.Get("HX-Request") == "true" && h.Get("HX-History-Restore-Request") != "true"
}

// Fragment reports whether htmx asked for a piece to swap into the page, not for a boosted page.
func (ctx *Context) Fragment() bool {
	return ctx.Partial() && ctx.Request().Header.Get("HX-Boosted") != "true"
}

// Scripted reports whether the browser runs the page's script: htmx asked, or helpers.js wrote the
// zone cookie. A full page MAY then leave a lazy piece for the script to fetch (ADR 13).
func (ctx *Context) Scripted() bool {
	if ctx.Request().Header.Get("HX-Request") == "true" {
		return true
	}
	_, err := ctx.Cookie(timezoneCookieName)
	return err == nil
}

// PartialFor reports whether htmx asked for the piece swapped into id.
func (ctx *Context) PartialFor(id string) bool {
	return ctx.Partial() && ctx.Request().Header.Get("HX-Target") == id
}
