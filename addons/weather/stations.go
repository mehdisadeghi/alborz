package weather

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// Station is one point DWD's MOSMIX forecasts for, as its catalogue names it.
type Station struct {
	ID, Name string
	Lat, Lon float64
}

// catalogueHeader are the column names and their underline.
const catalogueHeader = 2

// parseStations reads DWD's mosmix_stationskatalog.cfg: ID, ICAO, a name that may hold spaces,
// latitude and longitude in degrees and minutes, elevation.
func parseStations(r io.Reader) ([]Station, error) {
	var out []Station
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		f := strings.Fields(sc.Text())
		if n <= catalogueHeader || len(f) == 0 {
			continue
		}
		if len(f) < 6 {
			return nil, fmt.Errorf("station catalogue line %d: %d fields", n, len(f))
		}
		lat, err := degrees(f[len(f)-3])
		if err != nil {
			return nil, fmt.Errorf("station catalogue line %d: %v", n, err)
		}
		lon, err := degrees(f[len(f)-2])
		if err != nil {
			return nil, fmt.Errorf("station catalogue line %d: %v", n, err)
		}
		out = append(out, Station{ID: f[0], Name: strings.Join(f[2:len(f)-3], " "), Lat: lat, Lon: lon})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("station catalogue lists no station")
	}
	return out, nil
}

// degrees reads the catalogue's "52.28", 52 degrees 28 minutes.
func degrees(s string) (float64, error) {
	whole, minutes, ok := strings.Cut(strings.TrimPrefix(s, "-"), ".")
	d, err1 := strconv.Atoi(whole)
	m, err2 := strconv.Atoi(minutes)
	if !ok || len(minutes) != 2 || err1 != nil || err2 != nil {
		return 0, fmt.Errorf("%q is not degrees and minutes", s)
	}
	v := float64(d) + float64(m)/60
	if strings.HasPrefix(s, "-") {
		v = -v
	}
	return v, nil
}

// earthKm is the mean radius of the IUGG ellipsoid, within half a percent of any great circle.
const earthKm = 6371.0088

// distance is the great-circle distance in km, by the haversine formula.
func distance(lat1, lon1, lat2, lon2 float64) float64 {
	rad := math.Pi / 180
	dLat, dLon := (lat2-lat1)*rad, (lon2-lon1)*rad
	a := math.Pow(math.Sin(dLat/2), 2) + math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Pow(math.Sin(dLon/2), 2)
	return 2 * earthKm * math.Asin(math.Sqrt(a))
}

// nearest is the station closest to the point and its distance in km.
func nearest(stations []Station, lat, lon float64) (Station, float64) {
	best, bestKm := stations[0], math.Inf(1)
	for _, s := range stations {
		if km := distance(lat, lon, s.Lat, s.Lon); km < bestKm {
			best, bestKm = s, km
		}
	}
	return best, bestKm
}

// catalogueLetters writes a query as the catalogue writes names: capitals, umlauts spelled out.
var catalogueLetters = strings.NewReplacer("Ä", "AE", "Ö", "OE", "Ü", "UE", "ß", "SS")

// named are the stations whose name holds the query, those it starts first, at most limit.
func named(stations []Station, query string, limit int) []Station {
	q := catalogueLetters.Replace(strings.ToUpper(strings.TrimSpace(query)))
	var starts, holds []Station
	for _, s := range stations {
		switch {
		case strings.HasPrefix(s.Name, q):
			starts = append(starts, s)
		case strings.Contains(s.Name, q):
			holds = append(holds, s)
		}
	}
	found := append(starts, holds...)
	return found[:min(len(found), limit)]
}
