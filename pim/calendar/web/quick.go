package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"git.mehdix.org/alborz/pim/calendar"
	"git.mehdix.org/alborz/web"
)

type quickFailureKey struct{}

type quickFailure struct{ Input, Destination, Message string }

// rejectQuickEvent redraws the originating calendar with the user's draft intact.
// Only calendar list routes may be redrawn; next is never an arbitrary handler.
func (p *plugin) rejectQuickEvent(ctx *web.Context, input, message string) error {
	at, _ := url.Parse(ctx.NextOr(ctx.Path("calendar.month")))
	if at.Path != ctx.Path("calendar.month") && at.Path != ctx.Path("calendar.day") {
		at, _ = url.Parse(ctx.Path("calendar.month"))
	}
	at.Fragment = ""
	ctx.Response().Header().Set("HX-Replace-Url", at.RequestURI())
	failure := quickFailure{input, ctx.FormValue("calendar"), message}
	r := ctx.Request().Clone(context.WithValue(ctx.Request().Context(), quickFailureKey{}, failure))
	r.URL, r.RequestURI = at, at.RequestURI()
	ctx.SetRequest(r)
	if at.Path == ctx.Path("calendar.day") {
		return p.day(ctx)
	}
	return p.month(ctx)
}

func quickPageStatus(ctx *web.Context) int {
	if _, ok := ctx.Request().Context().Value(quickFailureKey{}).(quickFailure); ok {
		return http.StatusUnprocessableEntity
	}
	return http.StatusOK
}

type quickPreviewData struct {
	web.BaseRenderData
	Summary, When string
}

// The preview and submission share the parser and the reader's timezone.
func (p *plugin) quickPreview(ctx *web.Context) error {
	d := quickPreviewData{BaseRenderData: *web.NewBaseRenderData(ctx)}
	text := strings.TrimSpace(ctx.QueryParam("summary"))
	if text != "" {
		event, err := parseQuickEvent(ctx, text)
		if err != nil {
			d.When = ctx.T("calendar.quick.invalid")
		} else {
			d.Summary = event.Summary
			d.When = ctx.Dates().Short(event.Start)
			if event.AllDay {
				d.When = ctx.T("calendar.allday") + " · " + d.When
			} else {
				d.When = ctx.Dates().Clock(event.Start) + "–" + ctx.Dates().Clock(event.End) + " · " + d.When + " · " + web.UserLocation(ctx).String()
			}
		}
	}
	return ctx.Render(http.StatusOK, "event-quick-preview", &d)
}

func parseQuickEvent(ctx *web.Context, text string) (calendar.QuickEvent, error) {
	return calendar.ParseQuickEvent(text, time.Now().In(web.UserLocation(ctx)), calendar.QuickWords{
		Today: ctx.T("calendar.quick.today"), Tomorrow: ctx.T("calendar.quick.tomorrow"), At: ctx.T("calendar.quick.at"),
	})
}

// readQuickEvent fills the normal event form, keeping one validation and write path.
func readQuickEvent(ctx *web.Context) error {
	form, err := ctx.FormParams()
	if err != nil {
		return err
	}
	event, err := parseQuickEvent(ctx, form.Get("summary"))
	if err != nil {
		return err
	}
	form.Set("summary", event.Summary)
	for name, at := range map[string]time.Time{"start": event.Start, "end": event.End} {
		form.Set(name, at.Format("2006-01-02T15:04"))
		form.Set(name+"-calendar", "gregorian")
	}
	if event.AllDay {
		form.Set("allday", "1")
		form.Set("start_date", event.Start.Format("2006-01-02"))
		form.Set("end_date", event.End.AddDate(0, 0, -1).Format("2006-01-02"))
		form.Set("start_date-calendar", "gregorian")
		form.Set("end_date-calendar", "gregorian")
	}
	return nil
}
