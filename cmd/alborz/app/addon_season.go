//go:build !noseason && !noaddons

package app

import (
	seasonlocales "git.mehdix.org/alborz/addons/season/locales"
	seasonweb "git.mehdix.org/alborz/addons/season/web"
	"git.mehdix.org/alborz/internal/locale"
	"git.mehdix.org/alborz/web"
)

func init() {
	if err := locale.RegisterFS(seasonlocales.FS); err != nil {
		panic(err)
	}
	addonRegistrations = append(addonRegistrations, func(g *web.Group, _ Options) error {
		seasonweb.Mount(g.Group("/addons", "season"))
		return nil
	})
}
