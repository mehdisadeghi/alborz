package web

import (
	"errors"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"git.mehdix.org/alborz/addons/weather"
	calendarweb "git.mehdix.org/alborz/pim/calendar/web"
	"git.mehdix.org/alborz/web"
)

func calendarWeatherOn(ctx *web.Context) bool {
	return slices.Contains(ctx.Profile().Display.Status, "weather") && ctx.Profile().Place.Name != ""
}

func extendCalendar(s *web.Server) {
	web.Extend(s, func(ctx *web.Context, data *calendarweb.CalendarRenderData) error {
		if calendarWeatherOn(ctx) && len(data.Dates) > 0 {
			scope, err := url.ParseQuery(data.Scope)
			if err != nil {
				return err
			}
			query := calendarScope(scope)
			query.Set("from", data.Dates[0].Format(time.DateOnly))
			query.Set("to", data.Dates[len(data.Dates)-1].Format(time.DateOnly))
			data.DayAnnotations = append(data.DayAnnotations, calendarweb.DayAnnotation{Name: "weather", URL: web.QueryPath(ctx.Path("weather.calendar"), query)})
		}
		return nil
	})
}

type calendarDay struct {
	Date, Glyph, Title, Href string
}

// calendarScope carries only the filters used by the calendar's own day links.
func calendarScope(query url.Values) url.Values {
	scope := url.Values{}
	for _, key := range []string{"account", "cal"} {
		if values, ok := query[key]; ok {
			scope[key] = slices.Clone(values)
		}
	}
	return scope
}

func calendarHourMatches(now, date, hour time.Time) bool {
	if date.Format(time.DateOnly) == now.Format(time.DateOnly) {
		// MOSMIX steps name the end of an hour, which can be tomorrow's midnight.
		elapsed := hour.Sub(now)
		return elapsed >= 0 && elapsed <= time.Hour
	}
	return hour.In(now.Location()).Format(time.DateOnly) == date.Format(time.DateOnly)
}

type calendarRenderData struct {
	web.BaseRenderData
	Days []calendarDay
}

func handleCalendar(ctx *web.Context, w *weather.Service) error {
	now := ctx.Dates().In(time.Now())
	loc := now.Location()
	from, err1 := time.ParseInLocation(time.DateOnly, ctx.QueryParam("from"), loc)
	to, err2 := time.ParseInLocation(time.DateOnly, ctx.QueryParam("to"), loc)
	if err1 != nil || err2 != nil || to.Before(from) || !to.Before(from.AddDate(0, 0, 62)) {
		return web.NewError(http.StatusBadRequest, "invalid calendar weather range")
	}
	data := &calendarRenderData{BaseRenderData: *web.NewBaseRenderData(ctx)}
	place := ctx.Profile().Place
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	available := calendarWeatherOn(ctx)
	for date := from; !date.After(to); date = date.AddDate(0, 0, 1) {
		day := calendarDay{Date: date.Format(time.DateOnly)}
		data.Days = append(data.Days, day)
		if !available || date.Before(today) {
			continue
		}
		at := time.Date(date.Year(), date.Month(), date.Day(), 12, 0, 0, 0, loc)
		if date.Equal(today) {
			at = now
		}
		r, err := w.Now(place.Lat, place.Lon, at)
		if errors.Is(err, weather.ErrNoHour) {
			continue
		}
		if err != nil {
			// The source is shared by every day; do not retry or log once per cell.
			if !errors.Is(err, weather.ErrNoStation) {
				ctx.Logger().Printf("weather: %v", err)
			}
			available = false
			continue
		}
		if !calendarHourMatches(now, date, r.Hour.At) || r.Condition.Key == "" || math.IsNaN(r.Hour.Kelvin) {
			continue
		}
		day.Glyph = r.Condition.Day
		if date.Equal(today) && !r.Day {
			day.Glyph = r.Condition.Night
		}
		day.Title = strings.Join([]string{ctx.T(r.Condition.Key), ctx.Tf("weather.celsius", int(math.Round(r.Hour.Kelvin-celsius))), place.Name, ctx.Tf("weather.at", ctx.Dates().Clock(r.Hour.At))}, " · ")
		query := calendarScope(ctx.QueryParams())
		query.Set("date", day.Date)
		day.Href = web.QueryPath(ctx.Path("calendar.day"), query)
		data.Days[len(data.Days)-1] = day
	}
	return ctx.Render(http.StatusOK, "weather-calendar", data)
}
