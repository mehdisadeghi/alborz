package dav

import (
	"cmp"
	"context"
	"net/http"

	"git.mehdix.org/alborz"
)

// Services are the calendar and contacts servers an account names for itself.
type Services struct {
	// CalDAV and CardDAV are the servers' URLs; empty is the domain's.
	CalDAV, CardDAV string
	// Username is what the servers know the account as; empty is the address.
	Username string
	// Default is where a new calendar or address book goes (ADR 28); empty is the first.
	Default string `json:",omitempty"`
}

func (s Services) usernameFor(account string) string { return cmp.Or(s.Username, account) }

const ServicesKey = "services"

// LoadServices returns the account's own servers.
func LoadServices(ctx context.Context, s alborz.Store) (Services, error) {
	var v Services
	if err := s.Get(ctx, ServicesKey, &v); err != nil && err != alborz.ErrNoStoreEntry {
		return Services{}, err
	}
	return v, nil
}

// setHTTPBasicAuth sets the login's credentials for the servers it names on req.
func setHTTPBasicAuth(login *alborz.Login, req *http.Request) error {
	password, err := login.HTTPPassword(req.Context())
	if err != nil {
		return err
	}
	services, err := LoadServices(req.Context(), login.Store())
	if err != nil {
		return err
	}
	req.SetBasicAuth(services.usernameFor(login.Username()), password)
	return nil
}
