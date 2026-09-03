package alborzcaldav

import (
	"testing"
	"time"

	"github.com/emersion/go-ical"
)

// zoned is a calendar holding one event whose start names a zone the
// object does not define.
func zoned(tzid string) *ical.Calendar {
	cal := ical.NewCalendar()
	event := ical.NewEvent()
	start := ical.NewProp(ical.PropDateTimeStart)
	start.Params.Set(ical.PropTimezoneID, tzid)
	start.Value = "20260601T100000"
	event.Props.Set(start)
	cal.Children = append(cal.Children, event.Component)
	return cal
}

func observances(t *testing.T, cal *ical.Calendar) map[string]map[string]string {
	t.Helper()
	out := map[string]map[string]string{}
	for _, child := range cal.Children {
		if child.Name != ical.CompTimezone {
			continue
		}
		for _, obs := range child.Children {
			props := map[string]string{}
			for name, list := range obs.Props {
				props[name] = list[0].Value
			}
			out[obs.Name] = props
		}
	}
	return out
}

func TestEnsureTimezonesWritesTheRuleNotTheDate(t *testing.T) {
	around := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	cal := zoned("Europe/Berlin")
	ensureTimezones(cal, around)
	obs := observances(t, cal)
	daylight, standard := obs[ical.CompTimezoneDaylight], obs[ical.CompTimezoneStandard]
	if daylight == nil || standard == nil {
		t.Fatalf("Berlin needs both observances, got %v", obs)
	}
	if daylight["RRULE"] != "FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU" || standard["RRULE"] != "FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU" {
		t.Errorf("rules: daylight %q, standard %q", daylight["RRULE"], standard["RRULE"])
	}
	if daylight["TZOFFSETFROM"] != "+0100" || daylight["TZOFFSETTO"] != "+0200" {
		t.Errorf("daylight offsets: %s to %s", daylight["TZOFFSETFROM"], daylight["TZOFFSETTO"])
	}

	// The southern hemisphere moves the other way round the year.
	cal = zoned("Australia/Sydney")
	ensureTimezones(cal, around)
	obs = observances(t, cal)
	if obs[ical.CompTimezoneDaylight]["RRULE"] != "FREQ=YEARLY;BYMONTH=10;BYDAY=1SU" ||
		obs[ical.CompTimezoneStandard]["RRULE"] != "FREQ=YEARLY;BYMONTH=4;BYDAY=1SU" {
		t.Errorf("Sydney rules: %v", obs)
	}

	// A zone that stopped moving gets one standing observance and no rule.
	cal = zoned("Asia/Tehran")
	ensureTimezones(cal, around)
	obs = observances(t, cal)
	if len(obs) != 1 || obs[ical.CompTimezoneStandard] == nil || obs[ical.CompTimezoneStandard]["RRULE"] != "" {
		t.Errorf("Tehran: %v", obs)
	}
}

func TestEnsureTimezonesLeavesWhatIsThere(t *testing.T) {
	around := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	cal := zoned("Europe/Berlin")
	own := ical.NewComponent(ical.CompTimezone)
	own.Props.SetText(ical.PropTimezoneID, "Europe/Berlin")
	cal.Children = append(cal.Children, own)
	ensureTimezones(cal, around)
	zones := 0
	for _, child := range cal.Children {
		if child.Name == ical.CompTimezone {
			zones++
		}
	}
	if zones != 1 {
		t.Errorf("a definition the object carried was replaced or doubled: %d zones", zones)
	}

	cal = zoned("Mars/Olympus")
	ensureTimezones(cal, around)
	if len(cal.Children) != 1 {
		t.Errorf("a zone this build does not know was invented")
	}
}
