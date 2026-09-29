// Package locales holds the interface's strings, one file per language.
package locales

import "embed"

// FS holds every language's file.
//
//go:embed *.toml
var FS embed.FS
