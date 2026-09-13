package web

import "slices"

// Addon is an optional part of every page, off until the reader turns it on in the settings. Its Name
// owns its routes, its value in the profile and its locale keys: "<name>.addon" labels it.
type Addon struct {
	Name string
	// Script names the route of a script every page loads while the add-on is on.
	Script string
}

// scripts are the scripts of the add-ons the reader turned on, in the order they were mounted.
func (ctx *Context) scripts() []string {
	on := ctx.Profile().Display.Addons
	var paths []string
	for _, a := range ctx.Server.Addons {
		if a.Script != "" && slices.Contains(on, a.Name) {
			paths = append(paths, ctx.Path(a.Script))
		}
	}
	return paths
}
