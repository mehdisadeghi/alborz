package weather

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Forecast is one station's MOSMIX run: hourly steps and, per element, a value for each.
type Forecast struct {
	Station string
	Lat     float64
	Lon     float64
	Steps   []time.Time
	values  map[string][]float64
}

// The elements shown, by DWD's MetElementDefinition.xml.
const (
	elemKelvin = "TTT"  // temperature 2 m above surface, K
	elemWW     = "ww"   // significant weather
	elemCover  = "Neff" // effective cloud cover, %
	elemWind   = "FF"   // wind speed, m/s
	elemPrecip = "wwP"  // probability of precipitation within the last hour, %
)

var shown = []string{elemKelvin, elemWW, elemCover, elemWind, elemPrecip}

// maxKML bounds the unpacked forecast: a station's 240 hours of every element is about 350 KB.
const maxKML = 4 << 20

// parseKMZ reads a MOSMIX_L single-station KMZ, a zip holding one KML.
func parseKMZ(b []byte) (Forecast, error) {
	z, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return Forecast{}, err
	}
	if len(z.File) != 1 {
		return Forecast{}, fmt.Errorf("forecast holds %d files, not one", len(z.File))
	}
	f, err := z.File[0].Open()
	if err != nil {
		return Forecast{}, err
	}
	defer f.Close()
	kml, err := io.ReadAll(io.LimitReader(f, maxKML+1))
	if err != nil {
		return Forecast{}, err
	}
	if len(kml) > maxKML {
		return Forecast{}, fmt.Errorf("forecast is over %d bytes", maxKML)
	}
	return parseKML(kml)
}

type kmlDocument struct {
	Steps     []string `xml:"Document>ExtendedData>ProductDefinition>ForecastTimeSteps>TimeStep"`
	Placemark struct {
		Description string `xml:"description"`
		Forecasts   []struct {
			Element string `xml:"elementName,attr"`
			Value   string `xml:"value"`
		} `xml:"ExtendedData>Forecast"`
		Coordinates string `xml:"Point>coordinates"`
	} `xml:"Document>Placemark"`
}

// missing is DWD's DefaultUndefSign.
const missing = "-"

func parseKML(b []byte) (Forecast, error) {
	var doc kmlDocument
	d := xml.NewDecoder(bytes.NewReader(b))
	d.CharsetReader = latin1
	if err := d.Decode(&doc); err != nil {
		return Forecast{}, err
	}
	f := Forecast{Station: doc.Placemark.Description, values: map[string][]float64{}}
	for _, s := range doc.Steps {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return Forecast{}, err
		}
		f.Steps = append(f.Steps, t)
	}
	if len(f.Steps) == 0 {
		return Forecast{}, fmt.Errorf("forecast has no time steps")
	}
	lonLat := strings.Split(doc.Placemark.Coordinates, ",")
	if len(lonLat) < 2 {
		return Forecast{}, fmt.Errorf("forecast station has no coordinates")
	}
	var err1, err2 error
	f.Lon, err1 = strconv.ParseFloat(lonLat[0], 64)
	f.Lat, err2 = strconv.ParseFloat(lonLat[1], 64)
	if err1 != nil || err2 != nil {
		return Forecast{}, fmt.Errorf("forecast station coordinates %q", doc.Placemark.Coordinates)
	}
	for _, e := range doc.Placemark.Forecasts {
		if !slices.Contains(shown, e.Element) {
			continue
		}
		fields := strings.Fields(e.Value)
		if len(fields) != len(f.Steps) {
			return Forecast{}, fmt.Errorf("forecast %s has %d values for %d steps", e.Element, len(fields), len(f.Steps))
		}
		vs := make([]float64, len(fields))
		for i, v := range fields {
			if v == missing {
				vs[i] = math.NaN()
				continue
			}
			n, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return Forecast{}, fmt.Errorf("forecast %s: %v", e.Element, err)
			}
			vs[i] = n
		}
		f.values[e.Element] = vs
	}
	return f, nil
}

// latin1 reads the ISO-8859-1 MOSMIX is written in: each byte is the code point of the same number.
func latin1(charset string, r io.Reader) (io.Reader, error) {
	if !strings.EqualFold(charset, "ISO-8859-1") {
		return nil, fmt.Errorf("forecast in %s", charset)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var out strings.Builder
	for _, c := range b {
		out.WriteRune(rune(c))
	}
	return strings.NewReader(out.String()), nil
}

// Hour is the forecast for one step; a value DWD left out is NaN.
type Hour struct {
	At                              time.Time
	Kelvin, WW, Cover, Wind, Precip float64
}

// At is the step of the hour in progress: the first not before now. A forecast all in the past has none.
func (f Forecast) At(now time.Time) (Hour, bool) {
	for i, t := range f.Steps {
		if t.Before(now) {
			continue
		}
		v := func(e string) float64 {
			if vs, ok := f.values[e]; ok {
				return vs[i]
			}
			return math.NaN()
		}
		return Hour{At: t, Kelvin: v(elemKelvin), WW: v(elemWW), Cover: v(elemCover), Wind: v(elemWind), Precip: v(elemPrecip)}, true
	}
	return Hour{}, false
}
