// Package web shows the message of the day as a segment of the status bar.
package web

import (
	"strings"
	"time"

	"git.mehdix.org/alborz/addons/motd"
	"git.mehdix.org/alborz/web"
)

// Mount offers the day's text from the collections in dir.
func Mount(g *web.Group, dir string) error {
	cs, err := motd.Read(dir)
	if err != nil {
		return err
	}
	s := g.Server()
	s.Status = append(s.Status, web.Segment{Name: "motd", Show: func(ctx *web.Context) (web.Shown, error) {
		return shown(cs.On(ctx.PageLanguage(), ctx.Dates().In(time.Now()))), nil
	}})
	return nil
}

// shown puts a text's lines on one, as a poem quoted in a sentence is, and keeps it whole in the title.
func shown(day motd.Day) web.Shown {
	var lines, credit []string
	for line := range strings.Lines(day.Text) {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	for _, c := range []string{day.Author, day.Title, day.Credit} {
		if c != "" {
			credit = append(credit, c)
		}
	}
	parts := []string{strings.Join(lines, " / ")}
	if day.Author != "" {
		parts = append(parts, day.Author)
	}
	return web.Shown{Parts: parts, Title: strings.TrimSpace(day.Text) + "\n" + strings.Join(credit, " · "), Link: day.CreditURL}
}
