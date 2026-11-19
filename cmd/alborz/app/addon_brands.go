//go:build !nobrands && !noaddons

package app

import (
	"git.mehdix.org/alborz/addons/brands"
	brandslocales "git.mehdix.org/alborz/addons/brands/locales"
	"git.mehdix.org/alborz/internal/locale"
	"git.mehdix.org/alborz/mail"
)

func init() {
	if err := locale.RegisterFS(brandslocales.FS); err != nil {
		panic(err)
	}
	mail.AddCheck(brands.Check)
}
