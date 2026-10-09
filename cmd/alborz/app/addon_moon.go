//go:build !nomoon && !noaddons

package app

import (
	moonlocales "git.mehdix.org/alborz/addons/moon/locales"
	moonweb "git.mehdix.org/alborz/addons/moon/web"
	"git.mehdix.org/alborz/internal/locale"
	"git.mehdix.org/alborz/web"
)

func init() {
	if err := locale.RegisterFS(moonlocales.FS); err != nil {
		panic(err)
	}
	addonRegistrations = append(addonRegistrations, func(g *web.Group, _ Options) error {
		moonweb.Mount(g.Group("", "moon"))
		return nil
	})
}
