package web

import (
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim/store"
)

// kindOf names what a collection is: calendar, tasks, journal or addressbook.
func kindOf(c store.Collection) string {
	switch {
	case c.Kind == store.AddressBook:
		return "addressbook"
	case len(c.Components) == 1 && c.Components[0] == "VTODO":
		return "tasks"
	case len(c.Components) == 1 && c.Components[0] == "VJOURNAL":
		return "journal"
	}
	return "calendar"
}

// deviceChoices lists the collections kept here that an account sees, its own and those shared with it, for
// a device password limited to some of them (ADR 109).
func deviceChoices(st *store.Store) func(string) ([]alborz.DeviceChoice, error) {
	return func(account string) ([]alborz.DeviceChoice, error) {
		var out []alborz.DeviceChoice
		for _, kind := range []store.Kind{store.Calendar, store.AddressBook} {
			seen, err := st.Visible(account, kind, time.Now())
			if err != nil {
				return nil, err
			}
			for _, c := range seen {
				choice := alborz.DeviceChoice{
					Kind: kindOf(c.Collection), Ref: st.LimitName(c.Collection),
					Path: store.PathOf(account, c.Collection), ID: c.ID, Name: c.Name, Subscribed: c.Feed != nil,
				}
				if !strings.EqualFold(c.Owner, account) {
					choice.SharedBy = c.Owner
				}
				out = append(out, choice)
			}
		}
		return out, nil
	}
}

// limitOf names a device password's collections to the store.
func limitOf(scope []alborz.DeviceCollection) []string {
	names := make([]string, len(scope))
	for i, c := range scope {
		names[i] = c.Ref
	}
	return names
}
