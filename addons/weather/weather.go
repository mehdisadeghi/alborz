// Package weather reads Deutscher Wetterdienst's open MOSMIX forecasts for the station nearest a
// place, and finds places by name in DWD's station catalogue or on a Nominatim server (ADR 79).
package weather

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/memo"
)

// DWD's public services, each replaceable by a flag.
const (
	DefaultStations  = "https://www.dwd.de/DE/leistungen/met_verfahren_mosmix/mosmix_stationskatalog.cfg?view=nasPublication"
	DefaultForecasts = "https://opendata.dwd.de/weather/local_forecasts/mos/MOSMIX_L/single_stations/"
)

// Sources are where the catalogue, the forecasts and the place search are asked. An empty Places
// searches the station catalogue; PlacesPolicy is where its operator states its terms.
type Sources struct {
	Stations, Forecasts, Places, PlacesPolicy string
}

const (
	// The catalogue is about 300 KB from a government web server, asked once a day.
	fetchTimeout = 15 * time.Second
	// A reader waits on a search.
	searchTimeout = 5 * time.Second

	// The catalogue changes a few times a year; it is asked daily all the same.
	stationsFresh = 24 * time.Hour
	// MOSMIX_L runs four times a day; the hour shown moves along the copy held, so an hour is soon enough.
	forecastFresh = time.Hour
	// Nominatim asks for its results to be kept; a place does not move.
	searchFresh = 24 * time.Hour

	// Nominatim's absolute maximum is one request a second, for the whole application.
	searchEvery = time.Second

	maxStations = 4 << 20
	maxKMZ      = 1 << 20
	maxSearch   = 1 << 20

	// MaxStationKm is how far a station may be before its forecast describes another place.
	MaxStationKm = 100
	// searchResults is a page's worth of namesakes to choose from.
	searchResults = 5
	// stationResults holds every station of a city: Berlin has nine.
	stationResults = 20
)

// ErrNoStation is a place with no station within MaxStationKm.
var ErrNoStation = errors.New("no forecast station nearby")

// ErrNoHour is a forecast that ends before now.
var ErrNoHour = errors.New("forecast holds no hour still to come")

// Service fetches for every reader alike: one copy per station, one search at a time.
type Service struct {
	sources Sources
	client  *http.Client
	agent   string

	stations  *memo.Memo[[]Station]
	forecasts *memo.Memo[Forecast]
	searches  *memo.Memo[[]alborz.Place]

	// searching holds the one search in flight and when the last one ended.
	searching  sync.Mutex
	lastSearch time.Time
}

// New fetches over rt, naming alborz, its version and the deployment's project page, if any.
func New(sources Sources, rt http.RoundTripper, projectURL string) *Service {
	version, _ := alborz.Build()
	agent := alborz.BrandName + "/" + version
	if projectURL != "" {
		agent += " (+" + projectURL + ")"
	}
	return &Service{
		sources:   sources,
		client:    &http.Client{Transport: rt},
		agent:     agent,
		stations:  memo.NewBackground[[]Station](stationsFresh),
		forecasts: memo.NewBackground[Forecast](forecastFresh),
		searches:  memo.New[[]alborz.Place](searchFresh),
	}
}

func (s *Service) get(ctx context.Context, u string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", s.agent)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", u, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s is over %d bytes", u, limit)
	}
	return b, nil
}

// detached bounds a fetch every reader shares, which outlives the request that started it.
func detached() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), fetchTimeout)
}

func (s *Service) catalogue() ([]Station, error) {
	return s.stations.Get("", func() ([]Station, error) {
		ctx, cancel := detached()
		defer cancel()
		b, err := s.get(ctx, s.sources.Stations, maxStations)
		if err != nil {
			return nil, err
		}
		return parseStations(bytes.NewReader(b))
	})
}

// Near is the station nearest the point and its distance in km.
func (s *Service) Near(lat, lon float64) (Station, float64, error) {
	stations, err := s.catalogue()
	if err != nil {
		return Station{}, 0, err
	}
	st, km := nearest(stations, lat, lon)
	if km > MaxStationKm {
		return st, km, ErrNoStation
	}
	return st, km, nil
}

func (s *Service) forecast(id string) (Forecast, error) {
	return s.forecasts.Get(id, func() (Forecast, error) {
		ctx, cancel := detached()
		defer cancel()
		u := s.sources.Forecasts + url.PathEscape(id) + "/kml/MOSMIX_L_LATEST_" + url.PathEscape(id) + ".kmz"
		b, err := s.get(ctx, u, maxKMZ)
		if err != nil {
			return Forecast{}, err
		}
		return parseKMZ(b)
	})
}

// Report is the weather at a place in the hour in progress.
type Report struct {
	Station Station
	Km      float64
	Hour    Hour
	// Condition is zero where DWD gave neither weather nor cloud cover.
	Condition Condition
	Day       bool
}

// Now is the report for the point at now.
func (s *Service) Now(lat, lon float64, now time.Time) (Report, error) {
	st, km, err := s.Near(lat, lon)
	if err != nil {
		return Report{}, err
	}
	f, err := s.forecast(st.ID)
	if err != nil {
		return Report{}, err
	}
	h, ok := f.At(now)
	if !ok || math.IsNaN(h.Kelvin) {
		return Report{}, ErrNoHour
	}
	c, _ := condition(h.WW, h.Cover)
	return Report{Station: st, Km: km, Hour: h, Condition: c, Day: daylight(h.At, f.Lat, f.Lon)}, nil
}

type nominatimPlace struct {
	Lat         string `json:"lat"`
	Lon         string `json:"lon"`
	DisplayName string `json:"display_name"`
}

// Places is the Nominatim server a search asks and its usage policy; empty searches the stations.
func (s *Service) Places() (server, policy string) {
	return s.sources.Places, s.sources.PlacesPolicy
}

// Search finds places by name: stations in DWD's catalogue, or on the Nominatim server named in
// lang where it knows the name in it.
func (s *Service) Search(ctx context.Context, query, lang string) ([]alborz.Place, error) {
	if s.sources.Places == "" {
		stations, err := s.catalogue()
		if err != nil {
			return nil, err
		}
		var places []alborz.Place
		for _, st := range named(stations, query, stationResults) {
			places = append(places, alborz.Place{Name: st.Name, Lat: st.Lat, Lon: st.Lon})
		}
		return places, nil
	}
	return s.searches.Get(lang+"\x00"+query, func() ([]alborz.Place, error) {
		s.searching.Lock()
		defer s.searching.Unlock()
		ctx, cancel := context.WithTimeout(ctx, searchTimeout)
		defer cancel()
		select {
		case <-time.After(time.Until(s.lastSearch.Add(searchEvery))):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		defer func() { s.lastSearch = time.Now() }()
		q := url.Values{"q": {query}, "format": {"jsonv2"}, "limit": {strconv.Itoa(searchResults)}, "accept-language": {lang}}
		b, err := s.get(ctx, s.sources.Places+"search?"+q.Encode(), maxSearch)
		if err != nil {
			return nil, err
		}
		var found []nominatimPlace
		if err := json.Unmarshal(b, &found); err != nil {
			return nil, fmt.Errorf("place search: %v", err)
		}
		places := make([]alborz.Place, 0, len(found))
		for _, f := range found {
			lat, err1 := strconv.ParseFloat(f.Lat, 64)
			lon, err2 := strconv.ParseFloat(f.Lon, 64)
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("place search: coordinates %q, %q", f.Lat, f.Lon)
			}
			places = append(places, alborz.Place{Name: f.DisplayName, Lat: lat, Lon: lon})
		}
		return places, nil
	})
}
