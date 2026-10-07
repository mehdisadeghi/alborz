package contacts

import (
	"context"
	"fmt"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/dav"
	"github.com/emersion/go-webdav/carddav"
)

type Settings struct {
	AddressBookFilter   bool
	VisibleAddressBooks []string
}

const settingsKey = "carddav.settings"

// A book made after the choice was not chosen to be hidden.
func Show(ctx context.Context, l *alborz.Login, path string) error {
	s := &Settings{}
	return l.Update(ctx, settingsKey, s, func() error {
		if s.AddressBookFilter {
			s.VisibleAddressBooks = append(s.VisibleAddressBooks, path)
		}
		return nil
	})
}

// Choose takes the ticked books of those the form showed; one the form did not show keeps its choice.
func Choose(ctx context.Context, l *alborz.Login, all, shown, ticked []string) error {
	s := &Settings{}
	return l.Update(ctx, settingsKey, s, func() error {
		pim.Choose(&s.AddressBookFilter, &s.VisibleAddressBooks, all, shown, ticked)
		return nil
	})
}

func Visible(ctx context.Context, accounts []pim.Account[*carddav.Client], scope string, only map[string]bool) ([]dav.Collection, []pim.Site[*carddav.Client], error) {
	return pim.Visible(accounts, scope, only, func(acc pim.Account[*carddav.Client]) ([]dav.Collection, bool, []string, error) {
		settings := &Settings{}
		if err := acc.Login.Store().Get(ctx, settingsKey, settings); err != nil && err != alborz.ErrNoStoreEntry {
			return nil, false, nil, fmt.Errorf("failed to load CardDAV settings: %w", err)
		}
		return acc.Collections, settings.AddressBookFilter, settings.VisibleAddressBooks, nil
	})
}
