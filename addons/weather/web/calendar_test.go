//go:build !noweather && !noaddons

package web_test

import (
	"archive/zip"
	"bytes"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.mehdix.org/alborz/addons/weather"
	. "git.mehdix.org/alborz/internal/alborztest"
)

// calendarDWD serves a whole forecast, including missing values and a gap between days.
func calendarDWD(t *testing.T, loc *time.Location) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	stations, err := os.ReadFile("../testdata/mosmix_stationskatalog.cfg")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, loc)
	steps := []time.Time{now.Add(time.Minute), today.AddDate(0, 0, 1), today.AddDate(0, 0, 3), today.AddDate(0, 0, 4), today.AddDate(0, 0, 5)}
	var times strings.Builder
	for _, at := range steps {
		fmt.Fprintf(&times, "<TimeStep>%s</TimeStep>", at.UTC().Format(time.RFC3339))
	}
	kml := fmt.Sprintf(`<kml><Document><ExtendedData><ProductDefinition><ForecastTimeSteps>%s</ForecastTimeSteps></ProductDefinition></ExtendedData><Placemark><description>BERLIN-ALEX.</description><Point><coordinates>13.4167,52.5167,0</coordinates></Point><ExtendedData><Forecast elementName="TTT"><value>293.15 294.15 295.15 - 296.15</value></Forecast><Forecast elementName="ww"><value>1 61 1 1 -</value></Forecast><Forecast elementName="Neff"><value>0 100 0 0 -</value></Forecast></ExtendedData></Placemark></Document></kml>`, times.String())
	var kmz bytes.Buffer
	z := zip.NewWriter(&kmz)
	w, err := z.Create("MOSMIX_L.kml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(kml)); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	var fetched atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stations.cfg", func(w http.ResponseWriter, _ *http.Request) { w.Write(stations) })
	mux.HandleFunc("GET /mosmix/{id}/kml/{file}", func(w http.ResponseWriter, _ *http.Request) { fetched.Add(1); w.Write(kmz.Bytes()) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &fetched
}

func TestCalendarWeatherUsesLocalDaysAndOneForecast(t *testing.T) {
	for _, zone := range []string{"Pacific/Kiritimati", "Pacific/Honolulu"} {
		t.Run(zone, func(t *testing.T) {
			loc, err := time.LoadLocation(zone)
			if err != nil {
				t.Fatal(err)
			}
			srv, fetched := calendarDWD(t, loc)
			_, c := SignedIn(t, User, Weather(weather.Sources{Stations: srv.URL + "/stations.cfg", Forecasts: srv.URL + "/mosmix/"}))
			c.Post(t, "settings.profile", url.Values{"status": {"weather"}, "timezone": {zone}})
			c.Post(t, "weather.place", url.Values{"place": {"52.5167,13.4167,Berlin"}})
			c.Page(t, "calendar.month")
			if fetched.Load() != 0 {
				t.Fatal("initial month rendering fetched weather")
			}
			now := time.Now().In(loc)
			from, to := now.AddDate(0, 0, -1), now.AddDate(0, 0, 7)
			shown := c.Page(t, "weather.calendar", url.Values{"from": {from.Format(time.DateOnly)}, "to": {to.Format(time.DateOnly)}})
			if !strings.Contains(shown, "21 °C") {
				t.Errorf("missing the forecast temperature in %s", shown)
			}
			icons := 0
			for i := -1; i <= 7; i++ {
				date := now.AddDate(0, 0, i).Format(time.DateOnly)
				start := strings.Index(shown, `id="calendar-day-weather-`+date+`"`)
				if start < 0 {
					t.Fatalf("missing day %s: %s", date, shown)
				}
				piece, _, _ := strings.Cut(shown[start:], "</span>")
				has := strings.Contains(piece, `class="weather-day"`)
				want := i == 0 || i == 1 || i == 3
				if has != want {
					t.Errorf("day %s icon=%v, want %v: %s", date, has, want, piece)
				}
				if has {
					icons++
				}
			}
			if fetched.Load() != 1 || icons != 3 {
				t.Errorf("%d requests, %d icons", fetched.Load(), icons)
			}
		})
	}
}

func TestCalendarWeatherGlyphNavigatesToTheScopedDay(t *testing.T) {
	srv, _ := calendarDWD(t, time.UTC)
	a, c := SignedIn(t, User, Weather(weather.Sources{Stations: srv.URL + "/stations.cfg", Forecasts: srv.URL + "/mosmix/"}))
	c.Post(t, "settings.profile", url.Values{"status": {"weather"}, "timezone": {"UTC"}})
	c.Post(t, "weather.place", url.Values{"place": {"52.5167,13.4167,Berlin"}})
	calendar := c.CreateCalendar(t, "Weather", "")
	scope := url.Values{"account": {User}, "cal": {User + "|" + Here + calendar}}
	month := c.Page(t, "calendar.month", scope)
	loader := regexp.MustCompile(`hx-get="([^"]*/weather/calendar\?[^"]*)"`).FindStringSubmatch(month)
	if len(loader) != 2 {
		t.Fatalf("missing weather loader: %s", month)
	}
	loaderURL, err := url.Parse(html.UnescapeString(loader[1]))
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range scope {
		if loaderURL.Query().Get(key) != want[0] {
			t.Errorf("loader lost %s scope: %s", key, loaderURL)
		}
	}
	query := loaderURL.Query()
	date := time.Now().UTC().Format(time.DateOnly)
	query.Set("from", date)
	query.Set("to", date)
	query.Set("next", "https://example.com/external")
	shown := c.Page(t, "weather.calendar", query)
	link := regexp.MustCompile(`<a class="weather-day" href="([^"]+)"`).FindStringSubmatch(shown)
	if len(link) != 2 {
		t.Fatalf("missing glyph link: %s", shown)
	}
	href, err := url.Parse(html.UnescapeString(link[1]))
	if err != nil {
		t.Fatal(err)
	}
	if href.IsAbs() || href.Host != "" || href.Path != a.Path("calendar.day") {
		t.Errorf("unexpected day link: %s", href)
	}
	if href.Query().Get("date") != date || len(href.Query()) != 3 {
		t.Errorf("unexpected day query: %s", href)
	}
	for key, want := range scope {
		if href.Query().Get(key) != want[0] {
			t.Errorf("glyph lost %s scope: %s", key, href)
		}
	}
}

func TestCalendarWeatherValidatesRangeAndRespectsOptIn(t *testing.T) {
	srv, fetched := calendarDWD(t, time.UTC)
	_, c := SignedIn(t, User, Weather(weather.Sources{Stations: srv.URL + "/stations.cfg", Forecasts: srv.URL + "/mosmix/"}))
	date := time.Now().Format(time.DateOnly)
	for _, query := range []url.Values{{}, {"from": {"no"}, "to": {date}}, {"from": {date}, "to": {"2020-01-01"}}, {"from": {"2020-01-01"}, "to": {"2021-01-01"}}} {
		if r := c.Get(t, "weather.calendar", query); r.StatusCode != http.StatusBadRequest {
			t.Errorf("range %v: %s", query, r.Status)
		}
	}
	query := url.Values{"from": {date}, "to": {date}}
	c.Post(t, "settings.profile", url.Values{"status": {"weather"}})
	c.Page(t, "weather.calendar", query)
	c.Post(t, "settings.profile", url.Values{"status": {"clock"}})
	c.Post(t, "weather.place", url.Values{"place": {"52.5167,13.4167,Berlin"}})
	c.Page(t, "weather.calendar", query)
	if fetched.Load() != 0 {
		t.Fatal("weather fetched while disabled or without a place")
	}
}

func TestCalendarWeatherSourceFailureLeavesEmptyDays(t *testing.T) {
	srv, _ := calendarDWD(t, time.UTC)
	var failed atomic.Int32
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		failed.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(broken.Close)
	_, c := SignedIn(t, User, Weather(weather.Sources{Stations: srv.URL + "/stations.cfg", Forecasts: broken.URL + "/"}))
	c.Post(t, "settings.profile", url.Values{"status": {"weather"}})
	c.Post(t, "weather.place", url.Values{"place": {"52.5167,13.4167,Berlin"}})
	c.Page(t, "calendar.month")
	if failed.Load() != 0 {
		t.Fatal("month render requested a failed forecast source")
	}
	date := time.Now().Format(time.DateOnly)
	shown := c.Page(t, "weather.calendar", url.Values{"from": {date}, "to": {time.Now().AddDate(0, 0, 3).Format(time.DateOnly)}})
	if strings.Contains(shown, `class="weather-day"`) {
		t.Fatalf("a failed source shows weather: %s", shown)
	}
	if failed.Load() != 1 {
		t.Fatalf("failed source requested %d times", failed.Load())
	}
}
