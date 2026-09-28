package alborz

import "embed"

// embeddedTheme is overridden file by file by the theme directory on disk.
//
//go:embed all:themes/alborz
var embeddedTheme embed.FS

//go:embed prologue.txt
var prologue string

// Prologue is the banner printed once the port is bound.
var Prologue = prologue
