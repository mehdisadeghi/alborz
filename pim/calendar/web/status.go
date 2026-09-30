package web

import (
	"errors"
	"time"

	"git.mehdix.org/alborz/pim/calendar"
	"git.mehdix.org/alborz/web"
)

// showToday says what today holds: none, the one event by name, or a count and the next one yet to start.
func (p *plugin) showToday(ctx *web.Context) (web.Shown, error) {
	dv, err := p.dayView(ctx)
	if errors.Is(err, calendar.ErrNoCalendar) {
		return web.Shown{}, nil
	}
	if err != nil {
		return web.Shown{}, err
	}
	shown := web.Shown{Link: ctx.URL("calendar.day")}
	n := len(dv.events)
	if n == 0 {
		shown.Parts = []string{ctx.T("calendar.status.none")}
		return shown, nil
	}
	shown.Parts = []string{ctx.Tf("calendar.status.today", n)}
	var named *calendar.Occurrence
	if n == 1 {
		named = &dv.events[0]
	}
	now := time.Now()
	for i, oc := range dv.events {
		if oc.AllDay() || !oc.Start.After(now) {
			continue
		}
		shown.Parts = append(shown.Parts, ctx.Tf("calendar.status.next", n, ctx.Dates().Clock(oc.Start)))
		named = &dv.events[i]
		break
	}
	if named != nil && named.Summary() != "" {
		shown.Parts = append(shown.Parts, named.Summary())
	}
	return shown, nil
}
