//go:build !noclock && !noaddons

package app

import (
	clocklocales "git.mehdix.org/alborz/addons/clock/locales"
	clockweb "git.mehdix.org/alborz/addons/clock/web"
	"git.mehdix.org/alborz/internal/locale"
	"git.mehdix.org/alborz/web"
)

func init() {
	if err := locale.RegisterFS(clocklocales.FS); err != nil {
		panic(err)
	}
	addonRegistrations = append(addonRegistrations, func(g *web.Group, _ Options) error {
		clockweb.Mount(g.Group("", "clock"))
		return nil
	})
}
