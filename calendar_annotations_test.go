package alborz_test

import (
	"net/url"
	"testing"
	"time"

	. "git.mehdix.org/alborz/internal/alborztest"
	calweb "git.mehdix.org/alborz/pim/calendar/web"
	"git.mehdix.org/alborz/web"
)

func TestCalendarMonthDayAnnotations(t *testing.T) {
	a, c := SignedIn(t, User)
	var dates []time.Time
	calls := 0
	web.Extend(a.Server, func(_ *web.Context, data *calweb.CalendarRenderData) error {
		calls++
		dates = append([]time.Time(nil), data.Dates...)
		data.DayAnnotations = append(data.DayAnnotations, calweb.DayAnnotation{Name: "example", URL: "/calendar/annotations"})
		return nil
	})
	c.Page(t, "calendar.month", url.Values{"month": {"2026-09"}})
	if calls != 1 || len(dates) < 28 {
		t.Fatalf("month extension calls = %d, visible dates = %d", calls, len(dates))
	}
	c.Page(t, "calendar.month", url.Values{"month": {"2026-09"}, "view": {"list"}})
	if calls != 1 {
		t.Fatal("agenda invoked the month extension")
	}
}
