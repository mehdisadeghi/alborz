// Package locales holds the motd add-on translations.
package locales

import "embed"

// FS contains one translation file per supported language.
//
//go:embed *.toml
var FS embed.FS
