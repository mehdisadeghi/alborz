package web

import (
	"context"
	"net/http"
	"slices"
	"time"

	"git.mehdix.org/alborz/internal/calsys"
)

// Segment is one part of the status bar, off until the reader turns it on. Its Name owns its
// route, its value in the profile and its locale keys: "<name>.status" labels it in the settings.
type Segment struct {
	Name string
	// End puts the segment at the bar's inline end, where it stays however the others grow.
	End bool
	// Show says what the segment holds now, in the request's account or every one merged.
	Show func(*Context) (Shown, error)
}

// Shown is a segment as the bar draws it: runs of text, each isolated from the next, a title
// a hover shows, and where it leads. No runs draw a dash.
type Shown struct {
	Parts []string
	Title string
	Link  string
}

// statusWait bounds a segment: a line in the bar is not worth holding a request for, and one
// that did not answer shows a dash until the next refresh.
const statusWait = 2 * time.Second

// statusEvery is how often an open page asks its segments again; a new day shows within it.
const statusEvery = time.Minute

// StatusRoutes serves the status bar's segments, and adds the core's own: today's date in each
// calendar alborz counts in, a choice of its own.
func StatusRoutes(g *Group) {
	g.Group("", "core").GET("status", "/status/{name}", handleStatus)
	for _, cal := range calsys.Systems() {
		g.s.Status = append(g.s.Status, Segment{Name: dateSegment(cal), End: true, Show: func(ctx *Context) (Shown, error) {
			return showDate(ctx, cal)
		}})
	}
}

func dateSegment(cal calsys.System) string { return "date-" + cal.Name() }

// statusOn are the segments the reader turned on, in the order they were mounted, split by the
// end of the bar they sit at. The date in the reader's own calendar is the end's first.
func (ctx *Context) statusOn() (start, end []string) {
	on := ctx.Profile().Display.Status
	for _, s := range ctx.Server.Status {
		switch {
		case !slices.Contains(on, s.Name):
		case s.End:
			end = append(end, s.Name)
		default:
			start = append(start, s.Name)
		}
	}
	if i := slices.Index(end, dateSegment(ctx.Dates().Calendar())); i > 0 {
		own := end[i]
		end = slices.Insert(slices.Delete(end, i, i+1), 0, own)
	}
	return start, end
}

type statusRenderData struct {
	BaseRenderData
	Shown
}

func handleStatus(ctx *Context) error {
	i := slices.IndexFunc(ctx.Server.Status, func(s Segment) bool { return s.Name == ctx.Param("name") })
	if i < 0 {
		return NewError(http.StatusNotFound)
	}
	segment := ctx.Server.Status[i]
	bounded, cancel := context.WithTimeout(ctx.Request().Context(), statusWait)
	defer cancel()
	ctx.SetRequest(ctx.Request().WithContext(bounded))
	// Built before Show runs: a segment past its time goes on alone, and the page must not share ctx with it.
	data := &statusRenderData{BaseRenderData: *NewBaseRenderData(ctx)}
	type answer struct {
		shown Shown
		err   error
	}
	answered := make(chan answer, 1)
	go func() {
		shown, err := segment.Show(ctx)
		answered <- answer{shown, err}
	}()
	select {
	case a := <-answered:
		if a.err != nil {
			return a.err
		}
		data.Shown = a.shown
	case <-bounded.Done():
		ctx.Logger().Printf("status: %s did not answer in %s", segment.Name, statusWait)
	}
	return ctx.Render(http.StatusOK, "status-segment", data)
}

// showDate is today in cal; the first date in the bar carries the weekday for all of them.
func showDate(ctx *Context, cal calsys.System) (Shown, error) {
	r := ctx.Dates()
	now := r.In(time.Now())
	// A date a calendar cannot count is left out, never guessed.
	if calsys.For(cal, now) != cal {
		return Shown{}, nil
	}
	shown := Shown{Link: ctx.URL("calendar.day")}
	if _, end := ctx.statusOn(); len(end) > 0 && end[0] == dateSegment(cal) {
		shown.Parts = append(shown.Parts, r.Weekday(now.Weekday()))
	}
	shown.Parts = append(shown.Parts, r.DateIn(cal, now))
	return shown, nil
}
