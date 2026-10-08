package pim

import (
	"context"
	"fmt"

	"git.mehdix.org/alborz"
)

func LoadSettings[T any](ctx context.Context, store alborz.Store, key, name string) (*T, error) {
	settings := new(T)
	if err := store.Get(ctx, key, settings); err != nil && err != alborz.ErrNoStoreEntry {
		return nil, fmt.Errorf("failed to load %s settings: %w", name, err)
	}
	return settings, nil
}

// UpdateSettings loads a typed settings record and applies one atomic edit.
func UpdateSettings[T any](ctx context.Context, login *alborz.Login, key string, edit func(*T)) error {
	settings := new(T)
	return login.Update(ctx, key, settings, func() error {
		edit(settings)
		return nil
	})
}

// Show adds a collection to a filtered settings list when its filter is on.
func Show(filter bool, visible *[]string, path string) {
	if filter {
		*visible = append(*visible, path)
	}
}
