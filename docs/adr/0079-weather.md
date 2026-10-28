# ADR 79: Weather add-on

Status: accepted, amended by ADR 82 (packaged under `addons/weather`)
and ADR 89 (month-grid annotations). Amends ADR 52 (`Profile.Place`)
and ADR 54 (rule 4).

## Context

Readers may want a local weather forecast in the status bar. Deutscher
Wetterdienst (DWD) publishes global point forecasts (`MOSMIX_L`) and a
station catalogue under CC BY 4.0 without an API key. Place lookup via
OpenStreetMap Nominatim is a third-party service with rate and
attribution requirements that must remain opt-in for the operator.

## Decision

### Forecast source

1. Forecasts use DWD's `MOSMIX_L` per-station KMZ file
   (`MOSMIX_L_LATEST_<id>.kmz`, hourly steps over 240 h).
2. The station is the nearest by great-circle distance in DWD's station
   catalogue (`mosmix_stationskatalog.cfg`). If no station is within
   100 km (`maxStationKm`), no forecast is shown.
3. The status bar displays the first forecast step not before the
   current time, using `TTT` (temperature, °C), `ww` (weather code),
   `Neff` (cloud cover, %), `FF` (wind, km/h) and `wwP` (precipitation
   probability, %). Dry hours (`ww` 0–3) derive sky condition from
   `Neff` thresholds (1/8, 4.6/8, 7/8) and solar elevation at the
   station.

### Place selection

4. The reader selects a place on `/weather` (linked from the settings
   rail via `web.Segment.Page`) by submitting a search form; there is no
   live autocomplete or browser geolocation. The chosen place is stored
   in `Profile.Place` (ADR 52).
5. By default, search matches station names in the cached DWD catalogue.
6. When the operator configures `-weather-places`, searches query that
   Nominatim-compatible `/search?format=jsonv2` endpoint instead,
   throttled to at most one request per second across the process, and
   the page displays the service name and `-weather-places-policy` link
   before the form.

### Caching and attribution

7. All fetches run server-side on `Outgoing.Trusted` with bounded
   timeouts, shared across accounts:
   - station catalogue: fresh 24 h, served stale while revalidated;
   - station forecast: fresh 1 h, served stale while revalidated;
   - place query per language: cached 24 h.
   Fetches run detached so the status bar's 2 s deadline (ADR 67) never
   blocks on a cold station.
8. These caches remain active under `-cache=false` to respect upstream
   service policies (exception to ADR 54 rule 4).
9. Upstream failures log a warning and show a dash in the segment or an
   alert on the settings page, never a 500.
10. Every forecast display MUST credit "Source: Deutscher Wetterdienst"
    and state the forecast hour, station name and distance; place-server
    results credit OpenStreetMap contributors (ODbL).

## Consequences

- Forecasts reflect the nearest MOSMIX station within 100 km, which may
  differ in elevation or coastal exposure from the chosen place.
- Default search without `-weather-places` matches DWD's German station
  spellings only.

## References

- ADR 52, ADR 54, ADR 67, ADR 78, ADR 82, ADR 89
- `addons/weather/`, `addons/weather/web/`, `profile.go` (`Place`)
