package web

import (
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
)

// AddonBundle is a feature's embedded presentation tree. It may contain
// partials/, assets/, and pages/ alongside the feature's Go code.
type AddonBundle struct {
	Name  string
	Files fs.FS
}

// RegisterAddonBundle makes a feature's embedded templates and assets available
// before the handler starts serving. Theme names must be unique across bundles.
func (s *Server) RegisterAddonBundle(name string, files fs.FS) error {
	if s.serving {
		return fmt.Errorf("add-on %q registered after serving began", name)
	}
	if name == "" || path.Base(name) != name || strings.ContainsAny(name, ".\\") {
		return fmt.Errorf("invalid add-on name %q", name)
	}
	for _, bundle := range s.bundles {
		if bundle.Name == name {
			return fmt.Errorf("add-on bundle %q registered twice", name)
		}
	}
	themes, err := readOverlayThemes(files, "addons/"+name+"/")
	if err != nil {
		return err
	}
	for _, theme := range themes {
		if s.hasTheme(theme.Name) {
			return fmt.Errorf("theme %q in add-on %q is already offered", theme.Name, name)
		}
	}
	s.bundles = append(s.bundles, AddonBundle{Name: name, Files: files})
	s.themes = append(s.themes, themes...)
	if err := s.load(); err != nil {
		s.bundles = s.bundles[:len(s.bundles)-1]
		s.themes = s.themes[:len(s.themes)-len(themes)]
		return err
	}
	return nil
}

// addonStyles returns the versioned paths of add-on styles loaded by every page.
func (s *Server) addonStyles() []string {
	var paths []string
	for _, bundle := range s.bundles {
		if _, err := fs.Stat(bundle.Files, "assets/style.css"); err == nil {
			paths = append(paths, s.AssetURL("addons/"+bundle.Name+"/style.css"))
		}
	}
	return paths
}

// Addon is an optional part of every page, off until the reader turns it on in the settings. Its Name
// owns its routes, its value in the profile and its locale keys: "<name>.addon" labels it.
type Addon struct {
	Name string
	// Script names the route of a script every page loads while the add-on is on.
	Script string
	// Settings names the route of a script the settings page loads, for what turning the add-on on asks of the browser.
	Settings string
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
