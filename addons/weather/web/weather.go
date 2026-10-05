// Package web shows the weather at the reader's place as a segment of the status bar, and the
// page that chooses the place.
package web

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/addons/weather"
	"git.mehdix.org/alborz/web"
)

// kmh is km/h in one m/s, the unit DWD gives wind in.
const kmh = 3.6

// celsius is 0 °C in kelvin, the unit DWD gives temperature in.
const celsius = 273.15

// Mount offers the weather at the start of the bar and the page that sets the place.
func Mount(g *web.Group, w *weather.Service) {
	g.GET("place", "/weather", func(ctx *web.Context) error { return handlePlace(ctx, w) })
	g.POST("place", "/weather", handleChoose)
	s := g.Server()
	s.Status = append(s.Status, web.Segment{Name: "weather", Page: "weather.place", Show: func(ctx *web.Context) (web.Shown, error) {
		return show(ctx, w), nil
	}})
}

// show never fails the bar: a source that did not answer is a dash until the next refresh.
func show(ctx *web.Context, w *weather.Service) web.Shown {
	link := ctx.URL("weather.place")
	place := ctx.Profile().Place
	if place.Name == "" {
		return web.Shown{Parts: []string{ctx.T("weather.choose")}, Link: link}
	}
	r, err := w.Now(place.Lat, place.Lon, time.Now())
	switch {
	case errors.Is(err, weather.ErrNoStation):
		return web.Shown{Parts: []string{ctx.T("weather.nostation")}, Link: link}
	case err != nil:
		ctx.Logger().Printf("weather: %v", err)
		return web.Shown{}
	}
	h := r.Hour
	temp := ctx.Tf("weather.celsius", int(math.Round(h.Kelvin-celsius)))
	title := []string{place.Name}
	if r.Condition.Key != "" {
		glyph := r.Condition.Night
		if r.Day {
			glyph = r.Condition.Day
		}
		temp = glyph + " " + temp
		title = append(title, ctx.T(r.Condition.Key))
	}
	if !math.IsNaN(h.Wind) {
		title = append(title, ctx.Tf("weather.wind", int(math.Round(h.Wind*kmh))))
	}
	if !math.IsNaN(h.Precip) {
		title = append(title, ctx.Tf("weather.precip", int(math.Round(h.Precip))))
	}
	title = append(title,
		ctx.Tf("weather.at", ctx.Dates().Clock(h.At)),
		// Isolated, as a title cannot hold a bdi: a Latin name ending in a dot would turn in a Persian line.
		ctx.T("weather.station")+" \u2068"+r.Station.Name+"\u2069 · "+ctx.Tf("weather.km", int(math.Round(r.Km))),
		ctx.T("weather.source"))
	return web.Shown{Parts: []string{temp}, Title: strings.Join(title, "\n"), Link: link}
}

// Choice is a place and the station its weather would come from.
type Choice struct {
	alborz.Place
	Station string
	Km      int
	// Far is a place with no station near enough.
	Far bool
}

// PlaceRenderData is the place in force and the places a search found.
type PlaceRenderData struct {
	web.BaseRenderData
	Rail     map[string][]web.RailRow
	Query    string
	Searched bool
	Found    []Choice
	Current  *Choice
	// FarKm is the distance past which a station is not offered.
	FarKm int
	// Places is the server a search asks and its usage policy; empty searches the stations.
	Places, Policy string
}

func handlePlace(ctx *web.Context, w *weather.Service) error {
	data := &PlaceRenderData{
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("weather.status")),
		Rail:           web.SettingsRail(ctx),
		Query:          strings.TrimSpace(ctx.QueryParam("query")),
		FarKm:          weather.MaxStationKm,
	}
	data.Places, data.Policy = w.Places()
	choice := func(p alborz.Place) (Choice, error) {
		st, km, err := w.Near(p.Lat, p.Lon)
		if err != nil && !errors.Is(err, weather.ErrNoStation) {
			return Choice{}, err
		}
		return Choice{Place: p, Station: st.Name, Km: int(math.Round(km)), Far: err != nil}, nil
	}
	refused := func(err error, key string) error {
		ctx.Logger().Printf("weather: %v", err)
		data.Refused(ctx.T(key))
		return ctx.Render(http.StatusOK, "weather/place", data)
	}
	if p := ctx.Profile().Place; p.Name != "" {
		c, err := choice(p)
		if err != nil {
			return refused(err, "weather.unanswered")
		}
		data.Current = &c
	}
	if data.Query != "" {
		data.Searched = true
		found, err := w.Search(ctx.Request().Context(), data.Query, ctx.PageLanguage())
		if err != nil {
			return refused(err, "weather.searchfailed")
		}
		for _, p := range found {
			c, err := choice(p)
			if err != nil {
				return refused(err, "weather.unanswered")
			}
			data.Found = append(data.Found, c)
		}
	}
	return ctx.Render(http.StatusOK, "weather/place", data)
}

// handleChoose keeps the place chosen, "lat,lon,name" as the row's button sends it; empty forgets it.
func handleChoose(ctx *web.Context) error {
	place := alborz.Place{}
	notice := "weather.forgotten"
	if sent := ctx.FormValue("place"); sent != "" {
		f := strings.SplitN(sent, ",", 3)
		if len(f) != 3 || strings.TrimSpace(f[2]) == "" {
			return web.NewError(http.StatusBadRequest, "no place named")
		}
		var err1, err2 error
		place.Name = strings.TrimSpace(f[2])
		place.Lat, err1 = strconv.ParseFloat(f[0], 64)
		place.Lon, err2 = strconv.ParseFloat(f[1], 64)
		if err1 != nil || err2 != nil || math.Abs(place.Lat) > 90 || math.Abs(place.Lon) > 180 {
			return web.NewError(http.StatusBadRequest, "no place at those coordinates")
		}
		notice = "weather.chosen"
	}
	if err := ctx.UpdateProfile(func(p *alborz.Profile) { p.Place = place }); err != nil {
		ctx.Notify(web.Notice{Kind: web.NoticeFailed, Text: ctx.SaveRefusal(err)})
	} else {
		ctx.PutNotice(ctx.T(notice))
	}
	return ctx.Redirect(http.StatusFound, ctx.Path("weather.place"))
}
