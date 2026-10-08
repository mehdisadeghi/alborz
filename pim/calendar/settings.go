package calendar

import (
	"context"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/dav"
	"github.com/emersion/go-webdav/caldav"
)

type Settings struct {
	CalendarFilter   bool
	VisibleCalendars []string
	TaskFilter       bool
	VisibleTasks     []string
	JournalFilter    bool
	VisibleJournals  []string
}

const (
	settingsKey = "caldav.settings"
	// Subscriptions have their own entry: METADATA has no compare-and-set, so a tick written from one tab would drop a subscription added in another.
	subscriptionsKey = "caldav.subscriptions"
)

// Choose takes the ticked collections of those the form showed into the list chosen picks; one the form did not show keeps its choice.
func Choose(chosen func(*Settings) (*bool, *[]string)) func(context.Context, *alborz.Login, []string, []string, []string) error {
	return func(ctx context.Context, l *alborz.Login, all, shown, ticked []string) error {
		return pim.UpdateSettings(ctx, l, settingsKey, func(s *Settings) {
			filter, visible := chosen(s)
			pim.Choose(filter, visible, all, shown, ticked)
		})
	}
}

// A collection made after the choice was not chosen to be hidden.
func Show(ctx context.Context, l *alborz.Login, path string) error {
	return pim.UpdateSettings(ctx, l, settingsKey, func(s *Settings) {
		pim.Show(s.CalendarFilter, &s.VisibleCalendars, path)
		pim.Show(s.TaskFilter, &s.VisibleTasks, path)
		pim.Show(s.JournalFilter, &s.VisibleJournals, path)
	})
}

func EventVisibility(s *Settings) (*bool, *[]string) { return &s.CalendarFilter, &s.VisibleCalendars }

func TaskVisibility(s *Settings) (*bool, *[]string) { return &s.TaskFilter, &s.VisibleTasks }

func JournalVisibility(s *Settings) (*bool, *[]string) { return &s.JournalFilter, &s.VisibleJournals }

func Visible(ctx context.Context, accounts []pim.Account[*caldav.Client], scope string, only map[string]bool, kind func([]string) bool, chosen func(*Settings) (*bool, *[]string)) ([]dav.Collection, []pim.Site[*caldav.Client], error) {
	return pim.Visible(accounts, scope, only, func(acc pim.Account[*caldav.Client]) ([]dav.Collection, bool, []string, error) {
		settings, err := pim.LoadSettings[Settings](ctx, acc.Login.Store(), settingsKey, "CalDAV")
		if err != nil {
			return nil, false, nil, err
		}
		var cals []dav.Collection
		for _, cal := range acc.Collections {
			if kind(cal.Components) {
				cals = append(cals, cal)
			}
		}
		filter, paths := chosen(settings)
		return cals, *filter, *paths, nil
	})
}
