package calendar

import (
	"context"
	"fmt"

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

func loadSettings(ctx context.Context, store alborz.Store) (*Settings, error) {
	settings := &Settings{}
	if err := store.Get(ctx, settingsKey, settings); err != nil && err != alborz.ErrNoStoreEntry {
		return nil, err
	}
	return settings, nil
}

// Choose takes the ticked collections of those the form showed into the list chosen picks; one the form did not show keeps its choice.
func Choose(chosen func(*Settings) (*bool, *[]string)) func(context.Context, *alborz.Login, []string, []string, []string) error {
	return func(ctx context.Context, l *alborz.Login, all, shown, ticked []string) error {
		s := &Settings{}
		return l.Update(ctx, settingsKey, s, func() {
			filter, visible := chosen(s)
			pim.Choose(filter, visible, all, shown, ticked)
		})
	}
}

// A collection made after the choice was not chosen to be hidden.
func Show(ctx context.Context, l *alborz.Login, path string) error {
	s := &Settings{}
	return l.Update(ctx, settingsKey, s, func() {
		if s.CalendarFilter {
			s.VisibleCalendars = append(s.VisibleCalendars, path)
		}
		if s.TaskFilter {
			s.VisibleTasks = append(s.VisibleTasks, path)
		}
		if s.JournalFilter {
			s.VisibleJournals = append(s.VisibleJournals, path)
		}
	})
}

func EventVisibility(s *Settings) (*bool, *[]string) { return &s.CalendarFilter, &s.VisibleCalendars }

func TaskVisibility(s *Settings) (*bool, *[]string) { return &s.TaskFilter, &s.VisibleTasks }

func JournalVisibility(s *Settings) (*bool, *[]string) { return &s.JournalFilter, &s.VisibleJournals }

func Visible(ctx context.Context, accounts []pim.Account[*caldav.Client], scope string, only map[string]bool, kind func([]string) bool, chosen func(*Settings) (*bool, *[]string)) ([]dav.Collection, []pim.Site[*caldav.Client], error) {
	return pim.Visible(accounts, scope, only, func(acc pim.Account[*caldav.Client]) ([]dav.Collection, bool, []string, error) {
		settings, err := loadSettings(ctx, acc.Login.Store())
		if err != nil {
			return nil, false, nil, fmt.Errorf("failed to load CalDAV settings: %w", err)
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
