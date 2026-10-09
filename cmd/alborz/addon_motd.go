//go:build !nomotd && !noaddons

package main

import (
	"flag"

	"git.mehdix.org/alborz/cmd/alborz/app"
)

func init() {
	addonFlags = append(addonFlags, func(fs *flag.FlagSet, options *app.Options) func() {
		var dir string
		fs.StringVar(&dir, "motd", "",
			"directory of message-of-the-day collections, in the format of addons/motd/example.toml, offered as a status bar segment; unset offers none")
		return func() { options.Addons["motd"] = dir }
	})
}
