// Package web shows the moon's phase as a segment of the status bar.
package web

import (
	"fmt"
	"math"
	"time"

	"git.mehdix.org/alborz/addons/moon"
	"git.mehdix.org/alborz/web"
)

// Mount offers the phase at the end of the bar, beside the dates it belongs with.
func Mount(g *web.Group) {
	s := g.Server()
	s.Status = append(s.Status, web.Segment{Name: "moon", End: true, Show: func(ctx *web.Context) (web.Shown, error) {
		p, lit := moon.At(time.Now())
		name := ctx.T(fmt.Sprintf("moon.phase.%d", p))
		return web.Shown{
			Parts: []string{p.Emoji() + " " + name},
			Title: name + " · " + ctx.Tf("moon.lit", int(math.Round(lit*100))),
		}, nil
	}})
}
