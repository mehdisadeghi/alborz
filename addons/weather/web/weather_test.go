package web_test

import (
	"archive/zip"
	"bytes"
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

// dwdAndNominatim serves the real fixtures, the forecast's hours moved to start at the next one, and
// counts the place searches asked.
func dwdAndNominatim(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	stations, err := os.ReadFile("../testdata/mosmix_stationskatalog.cfg")
	if err != nil {
		t.Fatal(err)
	}
	kml, err := os.ReadFile("../testdata/MOSMIX_L_2026100515_10384.kml")
	if err != nil {
		t.Fatal(err)
	}
	next := time.Now().UTC().Truncate(time.Hour)
	kml = regexp.MustCompile(`<dwd:TimeStep>[^<]*</dwd:TimeStep>`).ReplaceAllFunc(kml, func([]byte) []byte {
		next = next.Add(time.Hour)
		return []byte("<dwd:TimeStep>" + next.Format(time.RFC3339) + "</dwd:TimeStep>")
	})
	var kmz bytes.Buffer
	z := zip.NewWriter(&kmz)
	w, err := z.Create("MOSMIX_L.kml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(kml); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stations.cfg", func(w http.ResponseWriter, _ *http.Request) { w.Write(stations) })
	mux.HandleFunc("GET /mosmix/{id}/kml/{file}", func(w http.ResponseWriter, _ *http.Request) { w.Write(kmz.Bytes()) })
	var searched atomic.Int32
	mux.HandleFunc("GET /search", func(w http.ResponseWriter, r *http.Request) {
		searched.Add(1)
		if r.URL.Query().Get("q") != "Berlin" || !strings.HasPrefix(r.UserAgent(), "Alborz/") {
			t.Errorf("searched %q as %q", r.URL.Query().Get("q"), r.UserAgent())
		}
		w.Write([]byte(`[{"lat":"52.5170365","lon":"13.3888599","display_name":"Berlin, Deutschland"}]`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &searched
}

func TestTheWeatherShowsForThePlaceChosen(t *testing.T) {
	srv, searched := dwdAndNominatim(t)
	a := Alborz(t, Weather(weather.Sources{Stations: srv.URL + "/stations.cfg", Forecasts: srv.URL + "/mosmix/"}))
	c := a.SignIn(t, User)
	if resp := c.Post(t, "settings.profile", url.Values{"status": {"weather"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("turning the weather on answered %s", resp.Status)
	}
	if page := c.Page(t, "mail.mailbox", "INBOX"); !strings.Contains(page, `id="status-weather-`) {
		t.Fatal("the status bar has no place for the weather")
	}
	if shown := c.Page(t, "core.status", "weather"); !strings.Contains(shown, "Choose a place") || !strings.Contains(shown, `href="`+a.Path("weather.place")+`"`) {
		t.Fatalf("a reader with no place is not sent to choose one:\n%s", shown)
	}

	if page := c.Page(t, "settings.profile"); !strings.Contains(page, `href="`+a.Path("weather.place")+`"`) {
		t.Fatal("the settings rail does not lead to the weather")
	}
	found := c.Page(t, "weather.place", url.Values{"query": {"Berlin"}})
	for _, want := range []string{"BERLIN-TEMPELHOF", "BERLIN-BRANDENBURG", "BERLIN-ALEX."} {
		if !strings.Contains(found, want) {
			t.Fatalf("the search does not show %q:\n%s", want, found)
		}
	}
	if strings.Contains(found, "OpenStreetMap") || searched.Load() != 0 {
		t.Fatalf("a deployment with no place server asked one %d times", searched.Load())
	}
	if resp := c.Post(t, "weather.place", url.Values{"place": {"52.5167,13.4167,BERLIN-ALEX."}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("choosing Berlin answered %s", resp.Status)
	}

	shown := c.Page(t, "core.status", "weather")
	if !strings.Contains(shown, "°C") {
		t.Fatalf("the segment shows no temperature:\n%s", shown)
	}
	// The first hour's TTT is 293.95 K and Neff 55 % under ww 1.
	for _, want := range []string{"21 °C", "Partly cloudy", "BERLIN-ALEX.", "Source: Deutscher Wetterdienst"} {
		if !strings.Contains(shown, want) {
			t.Errorf("the segment does not show %q:\n%s", want, shown)
		}
	}
}

func TestAPlaceServerIsNamedAndAskedOnlyWhenConfigured(t *testing.T) {
	srv, searched := dwdAndNominatim(t)
	policy := srv.URL + "/policy"
	a := Alborz(t, Weather(weather.Sources{Stations: srv.URL + "/stations.cfg", Forecasts: srv.URL + "/mosmix/", Places: srv.URL + "/", PlacesPolicy: policy}))
	c := a.SignIn(t, User)
	if page := c.Page(t, "weather.place"); !strings.Contains(page, srv.URL) || !strings.Contains(page, `href="`+policy+`"`) || searched.Load() != 0 {
		t.Fatalf("the page does not name the place server and its policy before a search:\n%s", page)
	}
	found := c.Page(t, "weather.place", url.Values{"query": {"Berlin"}})
	for _, want := range []string{"Berlin, Deutschland", "BERLIN-ALEX.", "OpenStreetMap"} {
		if !strings.Contains(found, want) {
			t.Fatalf("the search does not show %q:\n%s", want, found)
		}
	}
	if searched.Load() != 1 {
		t.Fatalf("the place server was asked %d times", searched.Load())
	}
}
