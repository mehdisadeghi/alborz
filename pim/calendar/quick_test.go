package calendar

import (
	"testing"
	"time"
)

func TestParseQuickEvent(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 23, 10, 0, 0, 0, loc)
	words := QuickWords{Today: "heute", Tomorrow: "morgen", At: "um"}
	for _, tc := range []struct {
		input, title, start, end string
		allDay                   bool
	}{
		{"Lunch tomorrow 12:30", "Lunch", "2026-10-24T12:30", "2026-10-24T13:30", false},
		{"2026-11-03 09:00-10:30 Design review", "Design review", "2026-11-03T09:00", "2026-11-03T10:30", false},
		{"Review morgen um 14:00", "Review", "2026-10-24T14:00", "2026-10-24T15:00", false},
		{"Release 03.11.2026", "Release", "2026-11-03T00:00", "2026-11-04T00:00", true},
		{"Release 2026-10-25", "Release", "2026-10-25T00:00", "2026-10-26T00:00", true},
		{"جلسه ۲۰۲۶-۱۱-۰۳ ۱۴:۰۰", "جلسه", "2026-11-03T14:00", "2026-11-03T15:00", false},
		{"Review today at 2pm", "Review", "2026-10-23T14:00", "2026-10-23T15:00", false},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := ParseQuickEvent(tc.input, now, words)
			if err != nil {
				t.Fatal(err)
			}
			if got.Summary != tc.title || got.Start.Format("2006-01-02T15:04") != tc.start || got.End.Format("2006-01-02T15:04") != tc.end || got.AllDay != tc.allDay {
				t.Fatalf("parsed %#v", got)
			}
		})
	}
	for _, input := range []string{"", "Lunch", "Lunch 03/04/2026", "Lunch 2026-02-30", "Lunch tomorrow 9", "Lunch 2026-02-30 tomorrow", "tomorrow 12:00", "Lunch tomorrow 25:00", "Lunch tomorrow at noon", "Lunch tomorrow 12:00-11:00", "Lunch today tomorrow", "Lunch 2026-03-29 02:30", "Lunch 2026-10-25 02:30"} {
		if got, err := ParseQuickEvent(input, now, words); err == nil {
			t.Errorf("accepted %q as %#v", input, got)
		}
	}
}

func TestQuickEventTranslatedRelativeDates(t *testing.T) {
	now := time.Date(2026, 12, 31, 23, 30, 0, 0, time.UTC)
	for _, tc := range []struct {
		input string
		words QuickWords
	}{
		{"Lunch tomorrow 12:30", QuickWords{Today: "today", Tomorrow: "tomorrow", At: "at"}},
		{"Essen morgen um 12:30", QuickWords{Today: "heute", Tomorrow: "morgen", At: "um"}},
		{"Comida mañana a las 12:30", QuickWords{Today: "hoy", Tomorrow: "mañana", At: "a las"}},
		{"ناهار فردا ۱۲:۳۰", QuickWords{Today: "امروز", Tomorrow: "فردا", At: "ساعت"}},
	} {
		got, err := ParseQuickEvent(tc.input, now, tc.words)
		if err != nil || got.Start.Format("2006-01-02T15:04") != "2027-01-01T12:30" {
			t.Errorf("%q = %#v, %v", tc.input, got, err)
		}
	}
}

func TestQuickEventRejectsNonHourlyClockChanges(t *testing.T) {
	loc, err := time.LoadLocation("Australia/Lord_Howe")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 4, 1, 12, 0, 0, 0, loc)
	for _, input := range []string{
		"Review 2026-04-05 01:45",
		"Review 2026-04-05 01:00-01:45",
		"Review 2026-04-05 00:45",
		"Review 2026-10-04 02:15",
	} {
		if got, err := ParseQuickEvent(input, now, QuickWords{}); err == nil {
			t.Errorf("accepted ambiguous/nonexistent clock in %q as %#v", input, got)
		}
	}
	for _, input := range []string{"Review 2026-04-05 02:00", "Review 2026-10-04 02:30"} {
		if _, err := ParseQuickEvent(input, now, QuickWords{}); err != nil {
			t.Errorf("valid clock %q: %v", input, err)
		}
	}
}
