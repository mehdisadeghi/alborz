package web

import (
	"time"

	"git.mehdix.org/alborz/pim/calendar"
	"git.mehdix.org/alborz/web"
)

// readQuickEvent fills the normal event form, keeping one validation and write path.
func readQuickEvent(ctx *web.Context) error {
	form, err := ctx.FormParams()
	if err != nil {
		return err
	}
	event, err := calendar.ParseQuickEvent(form.Get("summary"), time.Now().In(web.UserLocation(ctx)), calendar.QuickWords{
		Today: ctx.T("calendar.quick.today"), Tomorrow: ctx.T("calendar.quick.tomorrow"), At: ctx.T("calendar.quick.at"),
	})
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
