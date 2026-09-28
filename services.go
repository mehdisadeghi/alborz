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
