package alborz

import (
	"context"
	"errors"
	"net"
	"net/url"
)

// Why a server an account named was refused; the page says it in the
// reader's language.
var (
	ErrServiceNotURL   = errors.New("not a URL")
	ErrServiceNotHTTPS = errors.New("not an https URL")
	ErrServiceNoHost   = errors.New("host not found")
	ErrServicePrivate  = errors.New("host is on a private network")
)

// Services are the calendar and contacts servers an account names for
// itself, over its domain's, and what those servers know it as. They
// live in the account's store, so they follow it to any browser.
type Services struct {
	// CalDAV and CardDAV are the servers' URLs; empty is the domain's.
	CalDAV, CardDAV string
	// Username is what the servers know the account as; empty is the
	// address.
	Username string
	// Default is the place a new calendar or address book goes, one of
	// the account's DAV sources or Alborz (ADR 28); empty is the first.
	Default string `json:",omitempty"`
}

const servicesKey = "services"

// Services are the account's own.
func (l *Login) Services(ctx context.Context) (Services, error) {
	var v Services
	if err := l.store.Get(ctx, servicesKey, &v); err != nil && err != ErrNoStoreEntry {
		return Services{}, err
	}
	return v, nil
}

// SetServices keeps them. The URLs are checked by the caller, which
// knows what the deployment allows; see Server.CheckServiceURL.
func (l *Login) SetServices(ctx context.Context, v Services) error {
	return l.store.Put(ctx, servicesKey, &v)
}

// CheckServiceURL says whether an account may name this server. A URL
// typed into a form makes alborz connect from where it runs, so without
// the operator's word it must be HTTPS and must not resolve to an
// address of the operator's own network: loopback, private, link-local.
// Otherwise any account could read what only the host can reach.
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
