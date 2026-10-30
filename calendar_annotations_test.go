package alborz_test

import (
	"encoding/json"
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
	annotationURL := ""
	web.Extend(a.Server, func(_ *web.Context, data *calweb.CalendarRenderData) error {
		calls++
		dates = append([]time.Time(nil), data.Dates...)
		query := url.Values{
			"start": {dates[0].Format(time.DateOnly)},
			"end":   {dates[len(dates)-1].AddDate(0, 0, 1).Format(time.DateOnly)},
			"units": {"metric"},
		}
		annotationURL = "/calendar/annotations?" + query.Encode()
		data.DayAnnotations = append(data.DayAnnotations, calweb.DayAnnotation{Name: "example", URL: annotationURL})
		return nil
	})
	doc := swipeDocument(t, c.Page(t, "calendar.month", url.Values{"month": {"2026-09"}}))
	if calls != 1 || len(dates) < 28 {
		t.Fatalf("month extension calls = %d, visible dates = %d", calls, len(dates))
	}
	loaders := swipeElements(doc, "hx-get", annotationURL)
	if len(loaders) != 1 {
		t.Fatalf("month has %d annotation loaders, want 1", len(loaders))
	}
	loader := loaders[0]
	if _, hidden := swipeFindAttribute(loader, "hidden"); !hidden || swipeAttribute(loader, "hx-trigger") != "load, every 3600s" || swipeAttribute(loader, "hx-swap") != "none" {
		t.Fatal("annotation loader must refresh in the background without replacing the month")
	}
	var headers map[string]string
	if err := json.Unmarshal([]byte(swipeAttribute(loader, "hx-headers")), &headers); err != nil || headers["Alborz-Unasked"] != "1" {
		t.Fatalf("annotation loader headers = %v (%v)", headers, err)
	}
	labels := swipeElements(doc, "class", "day-label")
	holders := swipeElements(doc, "class", "day-annotations")
	if len(labels) != len(dates) {
		t.Fatalf("month has %d day labels for %d visible dates", len(labels), len(dates))
	}
	if len(holders) != len(dates) {
		t.Fatalf("month has %d annotation holders for %d visible dates", len(holders), len(dates))
	}
	for i, date := range dates {
		id := "calendar-day-example-" + date.Format(time.DateOnly)
		if holders[i].Parent != labels[i].Parent || holders[i].Parent.Data != "h3" {
			t.Error("annotations must share the day heading with the date label")
		}
		if len(swipeElements(labels[i], "id", id)) != 0 {
			t.Error("annotations must leave the date label's column unchanged")
		}
		spans := swipeElements(holders[i], "id", id)
		if len(spans) != 1 || spans[0].Data != "span" || spans[0].FirstChild != nil {
			t.Errorf("day %s has no unique empty span inside its annotation holder", date.Format(time.DateOnly))
		}
	}
	list := swipeDocument(t, c.Page(t, "calendar.month", url.Values{"month": {"2026-09"}, "view": {"list"}}))
	if calls != 1 || len(swipeElements(list, "hx-get", annotationURL)) != 0 || len(swipeElements(list, "class", "day-annotation")) != 0 {
		t.Fatal("agenda invoked the month extension or rendered its annotations")
	}
}
