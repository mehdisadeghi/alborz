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
func show(ctx context.Context, store alborz.Store, path string) error {
	settings := &Settings{}
	if err := store.Get(ctx, settingsKey, settings); err != nil && err != alborz.ErrNoStoreEntry {
		return err
	}
	if !settings.AddressBookFilter {
		return nil
	}
	settings.VisibleAddressBooks = append(settings.VisibleAddressBooks, path)
	return store.Put(ctx, settingsKey, settings)
}

func choose(ctx context.Context, store alborz.Store, paths []string) error {
	settings := &Settings{}
	if err := store.Get(ctx, settingsKey, settings); err != nil && err != alborz.ErrNoStoreEntry {
		return err
	}
	settings.AddressBookFilter, settings.VisibleAddressBooks = true, paths
	return store.Put(ctx, settingsKey, settings)
}

func visibleBooks(ctx context.Context, accounts []pim.Account[*carddav.Client], scope string, only map[string]bool) ([]dav.Collection, []pim.Site[*carddav.Client], error) {
	return pim.Visible(accounts, scope, only, func(acc pim.Account[*carddav.Client]) ([]dav.Collection, bool, []string, error) {
		settings := &Settings{}
		if err := acc.Login.Store().Get(ctx, settingsKey, settings); err != nil && err != alborz.ErrNoStoreEntry {
			return nil, false, nil, fmt.Errorf("failed to load CardDAV settings: %w", err)
		}
		return acc.Collections, settings.AddressBookFilter, settings.VisibleAddressBooks, nil
	})
}
