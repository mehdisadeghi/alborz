package weather

import (
	"math"
	"slices"
	"time"
)

// Condition is what the sky does in an hour: its locale key and its emoji by day and by night.
type Condition struct {
	Key        string
	Day, Night string
}

// weatherCodes are the ww codes DWD lists as used in MOSMIX (mosmix_element_weather_xls.xlsx,
// 2021-03-15), grouped by what a reader tells apart; intensity is left to the title's figures.
var weatherCodes = []struct {
	codes []int
	Condition
}{
	{[]int{95}, Condition{"weather.thunderstorm", "⛈️", "⛈️"}},
	{[]int{56, 57}, Condition{"weather.freezingdrizzle", "🌧️", "🌧️"}},
	{[]int{66, 67}, Condition{"weather.freezingrain", "🌧️", "🌧️"}},
	{[]int{85, 86}, Condition{"weather.snowshowers", "🌨️", "🌨️"}},
	{[]int{83, 84}, Condition{"weather.sleetshowers", "🌨️", "🌨️"}},
	{[]int{80, 81, 82}, Condition{"weather.rainshowers", "🌦️", "🌧️"}},
	{[]int{71, 73, 75}, Condition{"weather.snow", "🌨️", "🌨️"}},
	{[]int{68, 69}, Condition{"weather.sleet", "🌨️", "🌨️"}},
	{[]int{51, 53, 55}, Condition{"weather.drizzle", "🌧️", "🌧️"}},
	{[]int{61, 63, 65}, Condition{"weather.rain", "🌧️", "🌧️"}},
	{[]int{45, 49}, Condition{"weather.fog", "🌫️", "🌫️"}},
}

// cloudCover is a dry hour by effective cloud cover, on the eighths of DWD's W1W2 table in the
// same file: below 1/8, up to 4.5/8, up to 6/8, at least 7/8 (the gap goes to the overcast side).
var cloudCover = []struct {
	below float64
	Condition
}{
	{100.0 / 8, Condition{"weather.clear", "☀️", "🌙"}},
	{100 * 4.6 / 8, Condition{"weather.partlycloudy", "🌤️", "🌙"}},
	{100.0 * 7 / 8, Condition{"weather.mostlycloudy", "🌥️", "☁️"}},
	{math.Inf(1), Condition{"weather.overcast", "☁️", "☁️"}},
}

// condition is the hour's: its weather where ww names one, its cloud cover where ww only says
// how the clouds develop (codes 0 to 3). Neither known is no condition.
func condition(ww, cover float64) (Condition, bool) {
	for _, w := range weatherCodes {
		if !math.IsNaN(ww) && slices.Contains(w.codes, int(ww)) {
			return w.Condition, true
		}
	}
	if math.IsNaN(cover) {
		return Condition{}, false
	}
	for _, c := range cloudCover {
		if cover < c.below {
			return c.Condition, true
		}
	}
	panic("cloud cover past every threshold")
}

// sunrise is the sun's elevation when its upper limb meets the horizon, refraction included.
const sunrise = -0.833

// daylight reports whether the sun is up at the point, by the US Naval Observatory's approximate
// solar coordinates, good to about a minute of arc within two centuries of 2000.
func daylight(t time.Time, lat, lon float64) bool {
	rad := math.Pi / 180
	d := float64(t.Unix())/86400 + 2440587.5 - 2451545.0
	g := (357.529 + 0.98560028*d) * rad
	q := 280.459 + 0.98564736*d
	l := (q + 1.915*math.Sin(g) + 0.020*math.Sin(2*g)) * rad
	e := (23.439 - 0.00000036*d) * rad
	ra := math.Atan2(math.Cos(e)*math.Sin(l), math.Cos(l))
	dec := math.Asin(math.Sin(e) * math.Sin(l))
	gmst := 18.697374558 + 24.06570982441908*d
	h := (gmst*15+lon)*rad - ra
	elev := math.Asin(math.Sin(lat*rad)*math.Sin(dec) + math.Cos(lat*rad)*math.Cos(dec)*math.Cos(h))
	return elev/rad > sunrise
}
