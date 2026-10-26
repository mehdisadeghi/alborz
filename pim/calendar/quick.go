package calendar

import (
	"errors"
	"regexp"
	"strconv"
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

// ParseQuickEvent extracts date, clock/range and duration independently of their
// position in the title. Uncertain timing belongs in the full event editor.
func ParseQuickEvent(text string, now time.Time, words QuickWords) (QuickEvent, error) {
	p := quickParser{original: strings.Fields(text), now: now}
	for _, token := range p.original {
		v := strings.ToLower(locale.Latin(token))
		v = strings.ReplaceAll(strings.ReplaceAll(v, "a.m.", "am"), "p.m.", "pm")
		p.tokens = append(p.tokens, strings.Trim(v, ",;.?!"))
	}
	p.used = make([]bool, len(p.tokens))
	day, dated, err := p.date(words)
	if err != nil {
		return QuickEvent{}, err
	}
	duration, err := p.duration()
	if err != nil {
		return QuickEvent{}, err
	}
	from, to, timed, err := p.clock(words)
	if err != nil || (!dated && !timed) || (duration != 0 && (!timed || to != "")) {
		return QuickEvent{}, ErrQuickEvent
	}
	if !dated {
		day = now
	}
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, now.Location())
	result := QuickEvent{Start: start, End: start.AddDate(0, 0, 1), AllDay: !timed}
	if timed {
		result.Start, err = quickClock(start, from)
		if err != nil {
			return QuickEvent{}, err
		}
		if p.yearless && !result.Start.After(now) {
			for year := day.Year() + 1; year <= day.Year()+8; year++ {
				candidate := time.Date(year, day.Month(), day.Day(), 0, 0, 0, 0, now.Location())
				if candidate.Month() != day.Month() || candidate.Day() != day.Day() {
					continue
				}
				result.Start, err = quickClock(candidate, from)
				if err != nil {
					return QuickEvent{}, err
				}
				break
			}
		}
		if !dated && !result.Start.After(now) {
			result.Start, err = quickClock(start.AddDate(0, 0, 1), from)
			if err != nil {
				return QuickEvent{}, err
			}
		}
		if duration == 0 {
			duration = time.Hour
		}
		result.End = result.Start.Add(duration)
		if to != "" {
			result.End, err = quickClock(result.Start, to)
		}
		if err != nil || !result.End.After(result.Start) {
			return QuickEvent{}, ErrQuickEvent
		}
		if _, err = quickClock(result.End, result.End.Format("15:04")); err != nil {
			return QuickEvent{}, err
		}
	}
	var title []string
	seenTiming := false
	for i, token := range p.original {
		if p.used[i] {
			seenTiming = true
			continue
		}
		// Never silently keep obvious unsupported timing as a title suffix.
		v := p.tokens[i]
		if quickDateLike.MatchString(v) || quickClockLike.MatchString(v) || quickTimingWord(v) {
			return QuickEvent{}, ErrQuickEvent
		}
		if v == "in" && i+2 < len(p.tokens) {
			switch p.tokens[i+2] {
			case "day", "days", "week", "weeks", "month", "months", "year", "years":
				return QuickEvent{}, ErrQuickEvent
			}
		}
		switch v {
		case "morning", "afternoon", "evening":
			if i > 0 || (i+1 < len(p.used) && p.used[i+1]) {
				return QuickEvent{}, ErrQuickEvent
			}
		}
		if seenTiming {
			switch v {
			case "morning", "afternoon", "evening", "daily", "weekly", "monthly", "yearly", "weekend":
				return QuickEvent{}, ErrQuickEvent
			}
		}
		if !timed && i > 0 && p.used[i-1] {
			if _, err := strconv.Atoi(v); err == nil {
				return QuickEvent{}, ErrQuickEvent
			}
		}
		title = append(title, token)
	}
	result.Summary = strings.Join(title, " ")
	if result.Summary == "" {
		return QuickEvent{}, ErrQuickEvent
	}
	return result, nil
}

type quickParser struct {
	original, tokens []string
	used             []bool
	now              time.Time
	yearless         bool
}

var quickDateLike = regexp.MustCompile(`^\d+[./-]\d+[./-]\d+$`)
var quickClockLike = regexp.MustCompile(`^\d+(:\d+|am|pm)`)
var quickClockToken = regexp.MustCompile(`^(\d{1,2})(?::(\d{2}))?(am|pm)?$`)
var quickDayToken = regexp.MustCompile(`^\d{1,2}(st|nd|rd|th)?$`)

func quickTimingWord(v string) bool {
	switch v {
	case "at", "from", "until", "every", "tonight", "yesterday", "next", "am", "pm", "utc", "gmt", "cet", "cest", "pst", "pdt", "est", "edt":
		return true
	}
	return false
}

func (p *quickParser) mark(first, last int) {
	for i := first; i < last; i++ {
		p.used[i] = true
	}
}

func (p *quickParser) date(words QuickWords) (time.Time, bool, error) {
	var found time.Time
	for i := 0; i < len(p.tokens); i++ {
		v, end := p.tokens[i], i+1
		var day time.Time
		switch {
		case v == "today" || (words.Today != "" && v == strings.ToLower(words.Today)):
			day = p.now
		case v == "tomorrow" || (words.Tomorrow != "" && v == strings.ToLower(words.Tomorrow)):
			day = p.now.AddDate(0, 0, 1)
		case v == "in" && i+2 < len(p.tokens) && (p.tokens[i+2] == "days" || p.tokens[i+2] == "day"):
			n, err := strconv.Atoi(p.tokens[i+1])
			if err != nil || n < 1 || n > 3660 {
				return day, false, ErrQuickEvent
			}
			day, end = p.now.AddDate(0, 0, n), i+3
		default:
			weekday := v
			if v == "next" && i+1 < len(p.tokens) {
				weekday, end = p.tokens[i+1], i+2
			}
			for n, name := range []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"} {
				if weekday != name && weekday != name[:3] {
					continue
				}
				days := (n - int(p.now.Weekday()) + 7) % 7
				if days == 0 {
					days = 7
				}
				day = p.now.AddDate(0, 0, days)
			}
			if day.IsZero() {
				for _, layout := range []string{"2006-01-02", "02.01.2006"} {
					if parsed, err := time.ParseInLocation(layout, v, p.now.Location()); err == nil {
						day = parsed
						break
					}
				}
			}
			if day.IsZero() {
				for month := time.January; month <= time.December; month++ {
					if i+1 >= len(p.tokens) {
						if v == strings.ToLower(month.String()) || v == strings.ToLower(month.String()[:3]) {
							return day, false, ErrQuickEvent
						}
						continue
					}
					dateToken := p.tokens[i+1]
					if v != strings.ToLower(month.String()) && v != strings.ToLower(month.String()[:3]) {
						if !quickDayToken.MatchString(v) || (p.tokens[i+1] != strings.ToLower(month.String()) && p.tokens[i+1] != strings.ToLower(month.String()[:3])) {
							continue
						}
						dateToken = v
					}
					for _, suffix := range []string{"st", "nd", "rd", "th"} {
						dateToken = strings.TrimSuffix(dateToken, suffix)
					}
					d, err := strconv.Atoi(dateToken)
					if err != nil {
						return day, false, ErrQuickEvent
					}
					y, explicit := p.now.Year(), false
					end = i + 2
					if end < len(p.tokens) && len(p.tokens[end]) == 4 {
						if parsed, err := strconv.Atoi(p.tokens[end]); err == nil {
							y, explicit, end = parsed, true, end+1
						}
					}
					today := time.Date(p.now.Year(), p.now.Month(), p.now.Day(), 0, 0, 0, 0, p.now.Location())
					for year := y; year <= y+8; year++ {
						candidate := time.Date(year, month, d, 0, 0, 0, 0, p.now.Location())
						if candidate.Month() == month && candidate.Day() == d && (explicit || !candidate.Before(today)) {
							day = candidate
							break
						}
						if explicit {
							break
						}
					}
					if day.IsZero() {
						return day, false, ErrQuickEvent
					}
					p.yearless = !explicit
					break
				}
			}
		}
		if day.IsZero() {
			continue
		}
		if !found.IsZero() {
			return day, false, ErrQuickEvent
		}
		first := i
		if i > 0 && p.tokens[i-1] == "on" {
			first--
		}
		p.mark(first, end)
		found, i = day, end-1
	}
	return found, !found.IsZero(), nil
}

func (p *quickParser) duration() (time.Duration, error) {
	var duration time.Duration
	for i, v := range p.tokens {
		if p.used[i] || v != "for" {
			continue
		}
		if i+1 < len(p.tokens) {
			if _, err := strconv.Atoi(p.tokens[i+1]); err != nil {
				for j := i + 1; j < len(p.tokens) && j <= i+3; j++ {
					switch p.tokens[j] {
					case "minute", "minutes", "min", "mins", "hour", "hours", "hr", "hrs":
						return 0, ErrQuickEvent
					}
				}
				continue
			}
		}
		if i+2 >= len(p.tokens) {
			return 0, ErrQuickEvent
		}
		n, err := strconv.Atoi(p.tokens[i+1])
		unit := time.Duration(0)
		switch p.tokens[i+2] {
		case "minute", "minutes", "min", "mins":
			unit = time.Minute
		case "hour", "hours", "hr", "hrs":
			unit = time.Hour
		}
		if err != nil || n < 1 || n > 1440 || unit == 0 || duration != 0 {
			return 0, ErrQuickEvent
		}
		duration = time.Duration(n) * unit
		if duration > 24*time.Hour {
			return 0, ErrQuickEvent
		}
		p.mark(i, i+3)
	}
	return duration, nil
}

// readClock requires a clock-shaped token unless a connector allows a bare hour.
func (p *quickParser) readClock(i int, bare bool) (string, int) {
	if i >= len(p.tokens) || p.used[i] {
		return "", i
	}
	v, end := p.tokens[i], i+1
	if v == "noon" {
		return "12:00", end
	}
	if v == "midnight" {
		return "00:00", end
	}
	if end < len(p.tokens) && !p.used[end] && (p.tokens[end] == "am" || p.tokens[end] == "pm") {
		v += p.tokens[end]
		end++
	}
	m := quickClockToken.FindStringSubmatch(v)
	if m == nil || (!bare && m[2] == "" && m[3] == "") {
		return "", i
	}
	if m[2] == "" && m[3] == "" {
		v += ":00"
	}
	return v, end
}

func (p *quickParser) clock(words QuickWords) (string, string, bool, error) {
	var from, to string
	for i := 0; i < len(p.tokens); i++ {
		if p.used[i] {
			continue
		}
		first, at, explicit := i, i, false
		for _, phrase := range []string{"at", "from", strings.ToLower(words.At)} {
			parts := strings.Fields(phrase)
			if len(parts) > 0 && i+len(parts) <= len(p.tokens) && strings.Join(p.tokens[i:i+len(parts)], " ") == phrase {
				at, explicit = i+len(parts), true
				break
			}
		}
		var a, b string
		end := at
		if at < len(p.tokens) && !p.used[at] {
			left, right, ranged := strings.Cut(strings.ReplaceAll(p.tokens[at], "–", "-"), "-")
			if ranged && quickClockToken.MatchString(left) && quickClockToken.MatchString(right) {
				a, b, end = left, right, at+1
			}
		}
		if a == "" {
			a, end = p.readClock(at, explicit)
			if a != "" && end < len(p.tokens) && !p.used[end] && (p.tokens[end] == "to" || p.tokens[end] == "-" || p.tokens[end] == "–") {
				b, end = p.readClock(end+1, true)
				if b == "" {
					return "", "", false, ErrQuickEvent
				}
			}
		}
		if a == "" {
			if explicit {
				return "", "", false, ErrQuickEvent
			}
			continue
		}
		if from != "" {
			return "", "", false, ErrQuickEvent
		}
		// A shared suffix in 2-3pm / from 2 to 3pm applies to both clocks.
		if b != "" {
			for _, suffix := range []string{"am", "pm"} {
				if strings.HasSuffix(b, suffix) && !strings.HasSuffix(a, "am") && !strings.HasSuffix(a, "pm") {
					hour, _, _ := strings.Cut(a, ":")
					if h, err := strconv.Atoi(hour); err == nil && h >= 1 && h <= 12 {
						a += suffix
					}
				}
			}
			if quickClockToken.MatchString(a) && !strings.Contains(a, ":") && !strings.HasSuffix(a, "am") && !strings.HasSuffix(a, "pm") {
				a += ":00"
			}
			if quickClockToken.MatchString(b) && !strings.Contains(b, ":") && !strings.HasSuffix(b, "am") && !strings.HasSuffix(b, "pm") {
				b += ":00"
			}
		}
		p.mark(first, end)
		from, to, i = a, b, end-1
	}
	return from, to, from != "", nil
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
