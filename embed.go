package alborz

import "embed"

//go:embed templates
var embedded embed.FS

//go:embed prologue.txt
var prologue string

// Prologue is the banner printed once the port is bound.
var Prologue = prologue
