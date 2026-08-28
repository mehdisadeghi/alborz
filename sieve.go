package alborz

import (
	"crypto/tls"
	"fmt"
	"net"
	"time"

	"go.guido-berhoerster.org/managesieve"
)

// SieveScript is a script stored on the ManageSieve server.
type SieveScript struct {
	Name   string
	Active bool
}

// SieveClient is the subset of the ManageSieve protocol used by alborz. It
// hides the underlying library so that it can be replaced.
type SieveClient interface {
	ListScripts() ([]SieveScript, error)
	GetScript(name string) (string, error)
	// PutScript stores or replaces a script and returns server warnings.
	// The server validates the script and rejects invalid ones.
	PutScript(name, content string) (warnings string, err error)
	// ActivateScript makes the named script the only active one. An
	// empty name deactivates all scripts.
	ActivateScript(name string) error
	DeleteScript(name string) error
	Logout() error
	Close() error
}

// DialSieveFunc connects to the domain's upstream ManageSieve server and
// authenticates with the given credentials.
type DialSieveFunc func(domain, username, password string) (SieveClient, error)

func (s *Server) parseSieveUpstream(domain string) error {
	d := s.domains[domain]

	u, err := s.Upstream(domain, "sieve", "sieve+insecure")
	if _, ok := err.(*NoUpstreamError); ok {
		return nil
	} else if err != nil {
		return fmt.Errorf("domain %q: %v", domain, err)
	}
	if u.Scheme == "" {
		u, err = discoverSieve(u.Host)
		if err != nil {
			s.e.Logger.Printf("Domain %q: failed to discover ManageSieve server: %v", domain, err)
			return nil
		}
		if u == nil {
			// sieve is optional
			return nil
		}
	}

	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(host, "4190")
	}

	d.sieve.host = host
	d.sieve.insecure = u.Scheme == "sieve+insecure"

	s.e.Logger.Printf("Domain %q: configured upstream ManageSieve server: %v", domain, host)
	return nil
}

// SieveEnabled reports whether an upstream ManageSieve server is configured
// for the domain.
func (s *Server) SieveEnabled(domain string) bool {
	d, ok := s.upstreamsFor(domain)
	return ok && d.sieve.host != ""
}

func (s *Server) dialSieve(domain, username, password string) (SieveClient, error) {
	d, _ := s.upstreamsFor(domain)
	if d.sieve.host == "" {
		return nil, fmt.Errorf("ManageSieve is disabled for domain %q", domain)
	}

	host, _, _ := net.SplitHostPort(d.sieve.host)
	// One deadline spans greeting, STARTTLS and authentication; a wedged
	// server fails the request instead of hanging it. Cleared on success.
	conn, err := net.DialTimeout("tcp", d.sieve.host, upstreamTimeout)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to ManageSieve server: %v", err)
	}
	conn.SetDeadline(time.Now().Add(upstreamTimeout))
	c, err := managesieve.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to connect to ManageSieve server: %v", err)
	}

	if !d.sieve.insecure {
		if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
			c.Close()
			return nil, fmt.Errorf("STARTTLS failed: %v", err)
		}
	}

	if err := c.Authenticate(managesieve.PlainAuth("", username, password, host)); err != nil {
		c.Close()
		return nil, AuthError{err}
	}
	conn.SetDeadline(time.Time{})

	return &sieveClient{c}, nil
}

// sieveClient adapts go.guido-berhoerster.org/managesieve to SieveClient.
type sieveClient struct {
	c *managesieve.Client
}

func (sc *sieveClient) ListScripts() ([]SieveScript, error) {
	names, active, err := sc.c.ListScripts()
	if err != nil {
		return nil, err
	}
	scripts := make([]SieveScript, len(names))
	for i, name := range names {
		scripts[i] = SieveScript{Name: name, Active: name == active}
	}
	return scripts, nil
}

func (sc *sieveClient) GetScript(name string) (string, error) {
	return sc.c.GetScript(name)
}

func (sc *sieveClient) PutScript(name, content string) (string, error) {
	return sc.c.PutScript(name, content)
}

func (sc *sieveClient) ActivateScript(name string) error {
	return sc.c.ActivateScript(name)
}

func (sc *sieveClient) DeleteScript(name string) error {
	return sc.c.DeleteScript(name)
}

func (sc *sieveClient) Logout() error {
	return sc.c.Logout()
}

func (sc *sieveClient) Close() error {
	return sc.c.Close()
}
