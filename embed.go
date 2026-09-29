package alborz

import "embed"

// Templates is the pages' tree as built in; -debug reads it from disk instead.
//
//go:embed templates
var Templates embed.FS

//go:embed prologue.txt
var prologue string

// Prologue is the banner printed once the port is bound.
var Prologue = prologue
