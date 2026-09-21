package alborz

import "embed"

// embeddedTheme is the alborz theme built into the binary. A file with the
// same name in the theme directory on disk overrides its embedded
// counterpart, so a theme only carries the files it changes.
//
//go:embed all:themes/alborz
var embeddedTheme embed.FS

// prologue is the ASCII art printed to stderr once the port is bound.
//
//go:embed prologue.txt
var prologue string

// Prologue names the running program, drawn large, for whoever starts
// it to see at a glance.
var Prologue = prologue
