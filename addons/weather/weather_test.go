package weather

import (
	"archive/zip"
	"bytes"
	"math"
	"os"
	"testing"
	"time"
)

// testdata/MOSMIX_L_2026100515_10384.kml is DWD's MOSMIX_L_LATEST_10384.kmz of 2026-10-05, the
// 15 UTC run for Berlin-Tempelhof, cut to six steps and seven of its elements.
func TestAForecastReadsAsDWDIssuedIt(t *testing.T) {
	kml, err := os.ReadFile("testdata/MOSMIX_L_2026100515_10384.kml")
	if err != nil {
		t.Fatal(err)
	}
	var kmz bytes.Buffer
	z := zip.NewWriter(&kmz)
	w, err := z.Create("MOSMIX_L_2026100515_10384.kml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(kml); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}

	f, err := parseKMZ(kmz.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Steps) != 6 || f.Station != "BERLIN-TEMPELHOF" || f.Lat != 52.47 || f.Lon != 13.4 {
		t.Fatalf("read %d steps at %q (%v, %v)", len(f.Steps), f.Station, f.Lat, f.Lon)
	}
	h, ok := f.At(time.Date(2026, 10, 5, 16, 30, 0, 0, time.UTC))
	want := Hour{At: time.Date(2026, 10, 5, 17, 0, 0, 0, time.UTC), Kelvin: 291.65, WW: 1, Cover: 56, Wind: 2.06, Precip: 1}
	if !ok || h != want {
		t.Fatalf("the hour in progress at 16:30 is %+v, want %+v", h, want)
	}
	if c, _ := condition(h.WW, h.Cover); c.Key != "weather.partlycloudy" {
		t.Errorf("ww 1 under 56%% cloud reads as %q", c.Key)
	}
	// Berlin's sunset on 2026-10-05 is 16:46 UTC.
	if !daylight(f.Steps[0], f.Lat, f.Lon) || daylight(f.Steps[1], f.Lat, f.Lon) {
		t.Error("16:00 UTC is not day or 17:00 UTC is not night in Berlin")
	}
	if _, ok := f.At(f.Steps[5].Add(time.Minute)); ok {
		t.Error("a forecast all in the past still has an hour to show")
	}
}

// testdata/mosmix_stationskatalog.cfg is lines of DWD's MOSMIX station catalogue as of 2026-10-05.
func TestTheNearestStationIsTheOneInTown(t *testing.T) {
	f, err := os.Open("testdata/mosmix_stationskatalog.cfg")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	stations, err := parseStations(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(stations) != 12 {
		t.Fatalf("read %d stations, want 12", len(stations))
	}
	for _, c := range []struct {
		place    string
		lat, lon float64
		id       string
		km       float64
	}{
		{"Alexanderplatz, Berlin", 52.5219, 13.4132, "10389", 0.6},
		{"Marienplatz, Munich", 48.1374, 11.5755, "10865", 4.5},
		{"Azadi Tower, Tehran", 35.6997, 51.3380, "40754", 2.1},
		{"Central Park, New York", 40.7829, -73.9654, "72503", 5.8},
		{"Jan Mayen", 70.9333, -8.6667, "01001", 0},
	} {
		st, km := nearest(stations, c.lat, c.lon)
		if st.ID != c.id || math.Abs(km-c.km) > 0.2 {
			t.Errorf("nearest %s is %s %s at %.1f km, want %s at %.1f km", c.place, st.ID, st.Name, km, c.id, c.km)
		}
	}
}
