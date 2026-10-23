package calendar

import (
	"strings"
	"testing"
)

func TestReminderSummaryIsBoundedPlainText(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"", ""},
		{"  Budget\n\treview  ", "Budget review"},
		{"Meet\u202eing\x00 at <HQ>", "Meeting at <HQ>"},
		{"برنامه‌ریزی 👩‍💻", "برنامه‌ریزی 👩‍💻"},
		{strings.Repeat("界", 200), strings.Repeat("界", 179) + "…"},
		{strings.Repeat("a", 180), strings.Repeat("a", 180)},
	} {
		if got := reminderSummary(tc.text); got != tc.want {
			t.Errorf("summary %q, want %q", got, tc.want)
		}
	}
}
