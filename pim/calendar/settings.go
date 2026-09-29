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

func choose(set func(*Settings, []string)) func(context.Context, alborz.Store, []string) error {
	return func(ctx context.Context, store alborz.Store, paths []string) error {
		settings, err := loadSettings(ctx, store)
		if err != nil {
			return err
		}
		set(settings, paths)
		return store.Put(ctx, settingsKey, settings)
	}
}

// A collection made after the choice was not chosen to be hidden.
func show(ctx context.Context, store alborz.Store, path string) error {
	settings, err := loadSettings(ctx, store)
	if err != nil {
		return err
	}
	if !settings.CalendarFilter && !settings.TaskFilter {
		return nil
	}
	if settings.CalendarFilter {
		settings.VisibleCalendars = append(settings.VisibleCalendars, path)
	}
	if settings.TaskFilter {
		settings.VisibleTasks = append(settings.VisibleTasks, path)
	}
	return store.Put(ctx, settingsKey, settings)
}

func eventVisibility(s *Settings) (bool, []string) { return s.CalendarFilter, s.VisibleCalendars }

func taskVisibility(s *Settings) (bool, []string) { return s.TaskFilter, s.VisibleTasks }

func visibleCalendars(ctx context.Context, accounts []pim.Account[*caldav.Client], scope string, only map[string]bool, kind func([]string) bool, chosen func(*Settings) (filter bool, paths []string)) ([]dav.Collection, []pim.Site[*caldav.Client], error) {
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
		return cals, filter, paths, nil
	})
}
