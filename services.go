package alborz

import (
	"context"
	"errors"
	"net"
	"net/url"
)

// Reasons a server an account named was refused.
var (
	ErrServiceNotURL   = errors.New("not a URL")
	ErrServiceNotHTTPS = errors.New("not an https URL")
	ErrServiceNoHost   = errors.New("host not found")
	ErrServicePrivate  = errors.New("host is on a private network")
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

const servicesKey = "services"

// Services returns the account's own servers.
func (l *Login) Services(ctx context.Context) (Services, error) {
	var v Services
	if err := l.store.Get(ctx, servicesKey, &v); err != nil && err != ErrNoStoreEntry {
		return Services{}, err
	}
	return v, nil
}

// SetServices stores v; the caller checks the URLs with Server.CheckServiceURL.
func (l *Login) SetServices(ctx context.Context, v Services) error {
	return l.store.Put(ctx, servicesKey, &v)
}

// CheckServiceURL says whether an account may name this server. Unless the
// operator allows it, it must be HTTPS and not resolve to the operator's own network.
func (s *Server) CheckServiceURL(ctx context.Context, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return ErrServiceNotURL
	}
	if s.Options.PrivateServices {
		return nil
	}
	if u.Scheme != "https" {
		return ErrServiceNotHTTPS
	}
	ctx, cancel := context.WithTimeout(ctx, RoundTripTimeout)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", u.Hostname())
	if err != nil {
		return ErrServiceNoHost
	}
	for _, ip := range addrs {
		if !publicAddr(ip) {
			return ErrServicePrivate
		}
	}
	return nil
}
