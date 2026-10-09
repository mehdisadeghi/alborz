//go:build !nomotd && !noaddons

package app

import (
	"fmt"

	motdlocales "git.mehdix.org/alborz/addons/motd/locales"
	motdweb "git.mehdix.org/alborz/addons/motd/web"
	"git.mehdix.org/alborz/internal/locale"
	"git.mehdix.org/alborz/web"
)

func init() {
	if err := locale.RegisterFS(motdlocales.FS); err != nil {
		panic(err)
	}
	addonRegistrations = append(addonRegistrations, func(g *web.Group, options Options) error {
		var dir string
		if value, ok := options.Addons["motd"]; ok {
			dir = value.(string)
		}
		if dir == "" {
			return nil
		}
		if err := motdweb.Mount(g.Group("", "motd"), dir); err != nil {
			return fmt.Errorf("-motd: %w", err)
		}
		return nil
	})
}
