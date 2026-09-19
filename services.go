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
// itself, and what those servers know it as. A domain's servers come
// from the operator or from its SRV records; most accounts need nothing
// here. One whose domain publishes no record, or whose calendar lives
// with another host than its mail, says so, and that wins for the
// account. The password for them is the one HTTPPassword already keeps.
//
// They are kept in the account's own store - on its mail server where
// that takes METADATA, under the address otherwise - so they follow the
// account to any browser, and people who share an account share them, as
// they share the mailbox.
type Services struct {
	// CalDAV and CardDAV are the servers' URLs; empty is the domain's.
	CalDAV, CardDAV string
	// Username is what the servers know the account as; empty is the
	// address.
	Username string
}

const servicesKey = "services"

// Services are the account's own, read once per session.
func (s *Session) Services() (Services, error) {
	s.httpLocker.Lock()
	defer s.httpLocker.Unlock()
	if s.servicesLoaded {
		return s.services, nil
	}
	var v Services
	if err := s.store.Get(servicesKey, &v); err != nil && err != ErrNoStoreEntry {
		return Services{}, err
	}
	s.services, s.servicesLoaded = v, true
	return v, nil
}

// SetServices keeps them. The URLs are checked by the caller, which
// knows what the deployment allows; see Server.CheckServiceURL.
func (s *Session) SetServices(v Services) error {
	if err := s.store.Put(servicesKey, &v); err != nil {
		return err
	}
	s.httpLocker.Lock()
	s.services, s.servicesLoaded = v, true
	s.httpLocker.Unlock()
	return nil
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
