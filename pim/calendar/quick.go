package calendar

import (
	"errors"
	"strings"
	"time"

	"git.mehdix.org/alborz/internal/locale"
)

// QuickWords supplies the reader's translated relative-date words; English also works.
type QuickWords struct{ Today, Tomorrow, At string }

type QuickEvent struct {
	Summary    string
	Start, End time.Time
	AllDay     bool
}

var ErrQuickEvent = errors.New("event needs a title and an unambiguous date and time")

// ParseQuickEvent accepts a date followed by an optional time or time range, anywhere
// in a title. Numeric dates are Gregorian ISO or dotted day-month-year. Date-only
// events last one day; timed events default to one hour in now's location.
func ParseQuickEvent(text string, now time.Time, words QuickWords) (QuickEvent, error) {
	tokens := strings.Fields(text)
	first, last := -1, -1
	var start time.Time
	for i, token := range tokens {
		value := strings.ToLower(locale.Latin(token))
		var day time.Time
		switch {
		case strings.EqualFold(value, "today"), strings.EqualFold(value, words.Today):
			day = now
		case strings.EqualFold(value, "tomorrow"), strings.EqualFold(value, words.Tomorrow):
			day = now.AddDate(0, 0, 1)
		default:
			for _, layout := range []string{"2006-01-02", "02.01.2006"} {
				if parsed, err := time.ParseInLocation(layout, value, now.Location()); err == nil {
					day = parsed
					break
				}
			}
		}
		if day.IsZero() {
			if value != "" && value[0] >= '0' && value[0] <= '9' && (strings.Count(value, "-") == 2 || strings.Count(value, ".") == 2) {
				return QuickEvent{}, ErrQuickEvent
			}
			continue
		}
		if first != -1 {
			return QuickEvent{}, ErrQuickEvent
		}
		first, last = i, i+1
		start = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, now.Location())
	}
	if first == -1 {
		return QuickEvent{}, ErrQuickEvent
	}
	result := QuickEvent{Start: start, End: start.AddDate(0, 0, 1), AllDay: true}
	clock := last
	for _, word := range []string{words.At, "at"} {
		phrase := strings.Fields(word)
		end := clock + len(phrase)
		if len(phrase) > 0 && end <= len(tokens) && strings.EqualFold(strings.Join(tokens[clock:end], " "), strings.Join(phrase, " ")) {
			clock = end
			if clock == len(tokens) {
				return QuickEvent{}, ErrQuickEvent
			}
			break
		}
	}
	if clock < len(tokens) {
		value := strings.ToLower(locale.Latin(tokens[clock]))
		if clock != last || strings.Contains(value, ":") || strings.HasSuffix(value, "am") || strings.HasSuffix(value, "pm") || (value[0] >= '0' && value[0] <= '9') {
			from, to, ranged := strings.Cut(strings.ReplaceAll(value, "–", "-"), "-")
			var err error
			result.Start, err = quickClock(start, from)
			if err != nil {
				return QuickEvent{}, err
			}
			result.End = result.Start.Add(time.Hour)
			if ranged {
				result.End, err = quickClock(start, to)
				if err != nil || !result.End.After(result.Start) {
					return QuickEvent{}, ErrQuickEvent
				}
			} else if _, err := quickClock(result.End, result.End.Format("15:04")); err != nil {
				return QuickEvent{}, err
			}
			result.AllDay, last = false, clock+1
		}
	}
	title := append(append([]string{}, tokens[:first]...), tokens[last:]...)
	result.Summary = strings.TrimSpace(strings.Join(title, " "))
	if result.Summary == "" {
		return QuickEvent{}, ErrQuickEvent
	}
	return result, nil
}

func quickClock(day time.Time, value string) (time.Time, error) {
	for _, layout := range []string{"15:04", "3:04pm", "3pm"} {
		clock, err := time.Parse(layout, value)
		if err != nil {
			continue
		}
		at := time.Date(day.Year(), day.Month(), day.Day(), clock.Hour(), clock.Minute(), 0, 0, day.Location())
		// A gap or repeated clock time needs the full form; never guess an instant.
		if at.Hour() != clock.Hour() || at.Minute() != clock.Minute() {
			return time.Time{}, ErrQuickEvent
		}
		// Compare the adjacent zones' actual offsets: clock changes need not be one hour.
		begin, end := at.ZoneBounds()
		if !begin.IsZero() {
			begin = begin.Add(-time.Nanosecond)
		}
		_, offset := at.Zone()
		for _, adjacent := range []time.Time{begin, end} {
			if adjacent.IsZero() {
				continue
			}
			_, otherOffset := adjacent.Zone()
			if offset == otherOffset {
				continue
			}
			other := at.Add(time.Duration(offset-otherOffset) * time.Second)
			if other.Year() == at.Year() && other.YearDay() == at.YearDay() && other.Hour() == at.Hour() && other.Minute() == at.Minute() && other.Second() == at.Second() {
				return time.Time{}, ErrQuickEvent
			}
		}
		return at, nil
	}
	return time.Time{}, ErrQuickEvent
}
