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
		{"Lunch tomorrow at noon", "Lunch", "2026-10-24T12:00", "2026-10-24T13:00", false},
		{"Call Alex at 2 pm tomorrow", "Call Alex", "2026-10-24T14:00", "2026-10-24T15:00", false},
		{"Review next Friday at 9", "Review", "2026-10-30T09:00", "2026-10-30T10:00", false},
		{"Dentist on November 3 at 14:30", "Dentist", "2026-11-03T14:30", "2026-11-03T15:30", false},
		{"Review in 2 days at 10am", "Review", "2026-10-25T10:00", "2026-10-25T11:00", false},
		{"Call tomorrow from 2 to 3pm", "Call", "2026-10-24T14:00", "2026-10-24T15:00", false},
		{"Review Friday at 14:00 for 90 minutes", "Review", "2026-10-30T14:00", "2026-10-30T15:30", false},
		{"Review for 2 hours tomorrow at 10", "Review", "2026-10-24T10:00", "2026-10-24T12:00", false},
		{"Lunch at 9", "Lunch", "2026-10-24T09:00", "2026-10-24T10:00", false},
		{"Lunch at 14", "Lunch", "2026-10-23T14:00", "2026-10-23T15:00", false},
		{"Release November 3", "Release", "2026-11-03T00:00", "2026-11-04T00:00", true},
		{"Release January 3", "Release", "2027-01-03T00:00", "2027-01-04T00:00", true},
		{"Review October 23 at 9", "Review", "2027-10-23T09:00", "2027-10-23T10:00", false},
		{"Leap day February 29", "Leap day", "2028-02-29T00:00", "2028-03-01T00:00", true},
		{"Call tomorrow 2-3pm", "Call", "2026-10-24T14:00", "2026-10-24T15:00", false},
		{"Call tomorrow from 2:30 to 3pm", "Call", "2026-10-24T14:30", "2026-10-24T15:00", false},
		{"Daily standup tomorrow at 9", "Daily standup", "2026-10-24T09:00", "2026-10-24T10:00", false},
		{"Lunch for Alex tomorrow at noon", "Lunch for Alex", "2026-10-24T12:00", "2026-10-24T13:00", false},
		{"Review Fri at 14:00", "Review", "2026-10-30T14:00", "2026-10-30T15:00", false},
		{"Review Nov 3rd at noon", "Review", "2026-11-03T12:00", "2026-11-03T13:00", false},
		{"Lunch tomorrow, at noon", "Lunch", "2026-10-24T12:00", "2026-10-24T13:00", false},
		{"Meet Friday, at 2pm.", "Meet", "2026-10-30T14:00", "2026-10-30T15:00", false},
		{"Dentist on Nov 3rd, at noon", "Dentist", "2026-11-03T12:00", "2026-11-03T13:00", false},
		{"Dentist on 3 Nov at 2 p.m.", "Dentist", "2026-11-03T14:00", "2026-11-03T15:00", false},
		{"Call Alex, tomorrow at noon", "Call Alex,", "2026-10-24T12:00", "2026-10-24T13:00", false},
		{"Tomorrow at 14:00 طرح Project Ω", "طرح Project Ω", "2026-10-24T14:00", "2026-10-24T15:00", false},
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
	for _, input := range []string{"", "Lunch", "Lunch 03/04/2026", "Lunch 2026-02-30", "Lunch tomorrow 9", "Lunch 2026-02-30 tomorrow", "tomorrow 12:00", "Lunch tomorrow 25:00", "Lunch tomorrow 12:00-11:00", "Lunch today tomorrow", "Lunch 2026-03-29 02:30", "Lunch 2026-10-25 02:30", "Lunch tomorrow every week", "Lunch tomorrow at 9 at 10", "Lunch November 31 at noon", "Lunch tomorrow from 3 to 2pm", "Lunch tomorrow 2-3pm for 90 minutes", "Lunch tomorrow for 90 minutes", "Lunch tomorrow at", "Lunch tomorrow in 2 days", "Lunch tomorrow at 13pm"} {
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

func TestQuickEventRejectsRecognizedUnsupportedTiming(t *testing.T) {
	now := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	for _, input := range []string{
		"Lunch tomorrow afternoon", "Lunch in the afternoon tomorrow",
		"Lunch tomorrow morning", "Lunch tomorrow for two hours",
		"Lunch tomorrow at 10am PST", "Lunch in three days at noon",
		"Lunch in 2 weeks at noon", "Lunch tomorrow every Friday",
	} {
		if got, err := ParseQuickEvent(input, now, QuickWords{}); err == nil {
			t.Errorf("unsupported timing %q became %#v", input, got)
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
