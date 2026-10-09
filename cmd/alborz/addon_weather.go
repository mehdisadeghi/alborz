//go:build !noweather && !noaddons

package main

import (
	"flag"

	"git.mehdix.org/alborz/addons/weather"
	"git.mehdix.org/alborz/cmd/alborz/app"
)

func init() {
	addonFlags = append(addonFlags, func(fs *flag.FlagSet, options *app.Options) func() {
		sources := weather.Sources{}
		fs.StringVar(&sources.Stations, "weather-stations", weather.DefaultStations,
			"DWD's MOSMIX station catalogue, for the weather segment")
		fs.StringVar(&sources.Forecasts, "weather-forecasts", weather.DefaultForecasts,
			"directory of DWD's MOSMIX_L single-station forecasts, for the weather segment")
		fs.StringVar(&sources.Places, "weather-places", "",
			"Nominatim server finding the weather segment's places by name; unset searches DWD's station catalogue")
		fs.StringVar(&sources.PlacesPolicy, "weather-places-policy", "",
			"usage policy of the -weather-places server, linked above its search; unset links none")
		return func() { options.Addons["weather"] = sources }
	})
}
